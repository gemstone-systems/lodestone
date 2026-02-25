package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type DIDDocument struct {
	ID      string    `json:"id"`
	Service []Service `json:"service"`
}

type Service struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ServiceEndpoint string `json:"serviceEndpoint"`
}

func main() {
	http.HandleFunc("/resolve", handleResolve)
	fmt.Println("Lodestone starting on :8080...")
	http.ListenAndServe(":8080", nil)
}

func handleResolve(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// Collect URIs from both `uri` (singular) and `uris` (plural, repeatable).
	// e.g. ?uris=at://foo&uris=at://bar or the legacy ?uri=at://foo
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

	// If only a single URI was provided via the legacy `uri` param, unwrap the
	// array and return the object directly to preserve backwards compatibility.
	w.Header().Set("Content-Type", "application/json")
	if len(uris) == 1 && query.Get("uri") != "" {
		w.Write(results[0])
		return
	}

	json.NewEncoder(w).Encode(results)
}

// resolveATURI contains the core resolution logic, extracted from handleResolve.
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

	path := strings.TrimPrefix(uri, "at://")
	parts := strings.Split(path, "/")

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

	var didDoc DIDDocument
	if err := json.NewDecoder(resp.Body).Decode(&didDoc); err != nil {
		return nil, err
	}

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

func describeRepo(pdsEndpoint, did string) ([]byte, error) {
	url := fmt.Sprintf("%s/xrpc/com.atproto.repo.describeRepo?repo=%s",
		strings.TrimSuffix(pdsEndpoint, "/"),
		url.QueryEscape(did))

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func listRecords(pdsEndpoint, did, collection string) ([]byte, error) {
	url := fmt.Sprintf("%s/xrpc/com.atproto.repo.listRecords?repo=%s&collection=%s",
		strings.TrimSuffix(pdsEndpoint, "/"),
		url.QueryEscape(did),
		url.QueryEscape(collection))

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func getRecord(pdsEndpoint, did, collection, rkey string) ([]byte, error) {
	url := fmt.Sprintf("%s/xrpc/com.atproto.repo.getRecord?repo=%s&collection=%s&rkey=%s",
		strings.TrimSuffix(pdsEndpoint, "/"),
		url.QueryEscape(did),
		url.QueryEscape(collection),
		url.QueryEscape(rkey))

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

