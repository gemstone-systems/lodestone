package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	atURI := r.URL.Query().Get("uri")
	if atURI == "" {
		http.Error(w, "missing uri parameter", http.StatusBadRequest)
		return
	}

	// Parse AT-URI
	authority, collection, rkey, err := parseATURI(atURI)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid AT-URI: %v", err), http.StatusBadRequest)
		return
	}

	// Resolve authority to DID if needed
	did := authority
	if !strings.HasPrefix(authority, "did:") {
		// It's a handle, resolve to DID
		resolvedDID, err := resolveHandle(authority)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to resolve handle: %v", err), http.StatusInternalServerError)
			return
		}
		did = resolvedDID
	}

	// Get DID document
	didDoc, err := resolveDID(did)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to resolve DID: %v", err), http.StatusInternalServerError)
		return
	}

	// Extract PDS endpoint
	pdsEndpoint := extractPDSEndpoint(didDoc)
	if pdsEndpoint == "" {
		http.Error(w, "no PDS endpoint found in DID document", http.StatusInternalServerError)
		return
	}

	// Make appropriate XRPC call based on URI components
	var response []byte
	if collection == "" {
		// Just authority - describeRepo
		response, err = describeRepo(pdsEndpoint, did)
	} else if rkey == "" {
		// Authority + collection - listRecords
		response, err = listRecords(pdsEndpoint, did, collection)
	} else {
		// All three - getRecord
		response, err = getRecord(pdsEndpoint, did, collection, rkey)
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("XRPC call failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Return response as-is
	w.Header().Set("Content-Type", "application/json")
	w.Write(response)
}

func parseATURI(uri string) (authority, collection, rkey string, err error) {
	// Remove at:// prefix
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
	// Try DNS TXT record first
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
		// Query plc.directory
		didURL = fmt.Sprintf("https://plc.directory/%s", did)
	} else if strings.HasPrefix(did, "did:web:") {
		// Extract domain and construct .well-known URL
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
