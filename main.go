package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// -- Cache setup -------------------------------------------------------------

type cacheEntry struct {
	data      []byte
	expiresAt time.Time
}

func (e cacheEntry) expired() bool {
	return time.Now().After(e.expiresAt)
}

const (
	didCacheSize  = 4096
	xrpcCacheSize = 8192

	didTTL          = 12 * time.Hour
	describeRepoTTL = 30 * time.Minute
	getRecordTTL    = 2 * time.Minute
	// listRecords is intentionally not cached — it goes stale too easily.
)

var (
	// didCache stores resolved DID documents, keyed by DID string.
	didCache *lru.Cache[string, cacheEntry]
	// xrpcCache stores PDS XRPC responses, keyed by the full request URL.
	xrpcCache *lru.Cache[string, cacheEntry]
)

func initCaches() error {
	var err error
	didCache, err = lru.New[string, cacheEntry](didCacheSize)
	if err != nil {
		return fmt.Errorf("failed to create DID cache: %w", err)
	}
	xrpcCache, err = lru.New[string, cacheEntry](xrpcCacheSize)
	if err != nil {
		return fmt.Errorf("failed to create XRPC cache: %w", err)
	}
	return nil
}

func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next(w, r)
	}
}

// -- Types -------------------------------------------------------------------

type DIDDocument struct {
	ID      string    `json:"id"`
	Service []Service `json:"service"`
}

type Service struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ServiceEndpoint string `json:"serviceEndpoint"`
}

// -- Entry point -------------------------------------------------------------

func main() {
	if err := initCaches(); err != nil {
		panic(err)
	}

	http.HandleFunc("/resolve", withCORS(handleResolve))
	fmt.Println("Lodestone starting on :8080...")
	http.ListenAndServe(":8080", nil)
}

// -- Handler -----------------------------------------------------------------

func handleResolve(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	uris := query["uris"]
	if singular := query.Get("uri"); singular != "" {
		uris = append(uris, singular)
	}

	if len(uris) == 0 {
		http.Error(w, "missing uri or uris parameter", http.StatusBadRequest)
		return
	}

	results := make([]json.RawMessage, len(uris))
	var wg sync.WaitGroup

	for i, atURI := range uris {
		wg.Add(1)
		go func(idx int, uri string) {
			defer wg.Done()
			data, err := resolveATURI(uri)
			if err != nil {
				results[idx] = json.RawMessage(`{}`)
				return
			}
			results[idx] = data
		}(i, atURI)
	}

	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	if len(uris) == 1 && query.Get("uri") != "" {
		w.Write(results[0])
		return
	}

	json.NewEncoder(w).Encode(results)
}

// -- Core resolution ---------------------------------------------------------

func resolveATURI(atURI string) ([]byte, error) {
	authority, collection, rkey, err := parseATURI(atURI)
	if err != nil {
		return nil, fmt.Errorf("invalid AT-URI: %w", err)
	}

	did := authority
	if !strings.HasPrefix(authority, "did:") {
		did, err = resolveHandle(authority)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve handle: %w", err)
		}
	}

	didDoc, err := resolveDID(did)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve DID: %w", err)
	}

	pdsEndpoint := extractPDSEndpoint(didDoc)
	if pdsEndpoint == "" {
		return nil, fmt.Errorf("no PDS endpoint found in DID document")
	}

	if collection == "" {
		return describeRepo(pdsEndpoint, did)
	} else if rkey == "" {
		return listRecords(pdsEndpoint, did, collection)
	}
	return getRecord(pdsEndpoint, did, collection, rkey)
}

func parseATURI(uri string) (authority, collection, rkey string, err error) {
	if !strings.HasPrefix(uri, "at://") {
		return "", "", "", fmt.Errorf("URI must start with at://")
	}

	parts := strings.Split(strings.TrimPrefix(uri, "at://"), "/")
	if len(parts) < 1 {
		return "", "", "", fmt.Errorf("missing authority")
	}

	authority = parts[0]
	if len(parts) > 1 {
		collection = parts[1]
	}
	if len(parts) > 2 {
		rkey = parts[2]
	}

	return authority, collection, rkey, nil
}

// -- DID / handle resolution (cached) ----------------------------------------

func resolveHandle(handle string) (string, error) {
	resp, err := http.Get(fmt.Sprintf("https://%s/.well-known/atproto-did", handle))
	if err == nil && resp.StatusCode == 200 {
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err == nil {
			return strings.TrimSpace(string(body)), nil
		}
	}
	return "", fmt.Errorf("could not resolve handle")
}

func resolveDID(did string) (*DIDDocument, error) {
	if entry, ok := didCache.Get(did); ok && !entry.expired() {
		var doc DIDDocument
		if err := json.Unmarshal(entry.data, &doc); err == nil {
			return &doc, nil
		}
	}

	var didURL string
	if strings.HasPrefix(did, "did:plc:") {
		didURL = fmt.Sprintf("https://plc.directory/%s", did)
	} else if strings.HasPrefix(did, "did:web:") {
		domain := strings.TrimPrefix(did, "did:web:")
		didURL = fmt.Sprintf("https://%s/.well-known/did.json", domain)
	} else {
		return nil, fmt.Errorf("unsupported DID method")
	}

	resp, err := http.Get(didURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DID resolution failed with status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var didDoc DIDDocument
	if err := json.Unmarshal(raw, &didDoc); err != nil {
		return nil, err
	}

	didCache.Add(did, cacheEntry{data: raw, expiresAt: time.Now().Add(didTTL)})

	return &didDoc, nil
}

func extractPDSEndpoint(didDoc *DIDDocument) string {
	for _, service := range didDoc.Service {
		if service.Type == "AtprotoPersonalDataServer" ||
			strings.HasSuffix(service.ID, "#atproto_pds") {
			return service.ServiceEndpoint
		}
	}
	return ""
}

// -- XRPC calls (selectively cached) -----------------------------------------

func cachedGet(cacheKey string, ttl time.Duration) ([]byte, bool) {
	if ttl == 0 {
		return nil, false
	}
	if entry, ok := xrpcCache.Get(cacheKey); ok && !entry.expired() {
		return entry.data, true
	}
	return nil, false
}

func cacheSet(cacheKey string, data []byte, ttl time.Duration) {
	if ttl == 0 {
		return
	}
	xrpcCache.Add(cacheKey, cacheEntry{data: data, expiresAt: time.Now().Add(ttl)})
}

func fetchAndCache(requestURL, cacheKey string, ttl time.Duration) ([]byte, error) {
	if data, hit := cachedGet(cacheKey, ttl); hit {
		return data, nil
	}

	resp, err := http.Get(requestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	cacheSet(cacheKey, data, ttl)
	return data, nil
}

func describeRepo(pdsEndpoint, did string) ([]byte, error) {
	u := fmt.Sprintf("%s/xrpc/com.atproto.repo.describeRepo?repo=%s",
		strings.TrimSuffix(pdsEndpoint, "/"),
		url.QueryEscape(did))
	return fetchAndCache(u, u, describeRepoTTL)
}

func listRecords(pdsEndpoint, did, collection string) ([]byte, error) {
	u := fmt.Sprintf("%s/xrpc/com.atproto.repo.listRecords?repo=%s&collection=%s",
		strings.TrimSuffix(pdsEndpoint, "/"),
		url.QueryEscape(did),
		url.QueryEscape(collection))
	return fetchAndCache(u, u, 0) // ttl=0 skips cache entirely
}

func getRecord(pdsEndpoint, did, collection, rkey string) ([]byte, error) {
	u := fmt.Sprintf("%s/xrpc/com.atproto.repo.getRecord?repo=%s&collection=%s&rkey=%s",
		strings.TrimSuffix(pdsEndpoint, "/"),
		url.QueryEscape(did),
		url.QueryEscape(collection),
		url.QueryEscape(rkey))
	return fetchAndCache(u, u, getRecordTTL)
}
