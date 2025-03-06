package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

// RequestKey uniquely identifies a request based on URL and method
type RequestKey struct {
	URL    string
	Method string
}

// CacheEntry represents a cached response
type CacheEntry struct {
	Response   []byte
	Headers    http.Header
	StatusCode int
	ExpiresAt  time.Time
}

// InFlightRequest represents an ongoing request
type InFlightRequest struct {
	Done  chan struct{}
	Entry *CacheEntry
	Error error
}

type CacheServer struct {
	client     *http.Client
	cache      sync.Map
	inFlight   sync.Map
	cacheTTL   time.Duration
	targetHost string
}

func NewCacheServer(cacheTTL time.Duration, targetHost string) *CacheServer {
	return &CacheServer{
		client:     &http.Client{},
		cacheTTL:   cacheTTL,
		targetHost: targetHost,
	}
}

func (cs *CacheServer) getCacheKey(r *http.Request) RequestKey {
	return RequestKey{
		URL:    r.URL.String(),
		Method: r.Method,
	}
}

func (cs *CacheServer) makeRequest(req *http.Request) (*CacheEntry, error) {
	// Create a new request to the target host
	targetURL := cs.targetHost + req.URL.Path
	if req.URL.RawQuery != "" {
		targetURL += "?" + req.URL.RawQuery
	}
	log.Printf("Calling URL: %s", targetURL)
	newReq, err := http.NewRequest(req.Method, targetURL, req.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to create target request: %w", err)
	}

	// Copy original headers
	for key, values := range req.Header {
		for _, value := range values {
			newReq.Header.Add(key, value)
		}
	}

	// Add Accept-Encoding header to handle gzip
	newReq.Header.Set("Accept-Encoding", "gzip")

	resp, err := cs.client.Do(newReq)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	var reader io.ReadCloser
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		reader, err = gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer reader.Close()
	default:
		reader = resp.Body
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Create a copy of the headers
	headers := make(http.Header)
	for k, v := range resp.Header {
		headers[k] = v
	}

	// Remove the content encoding header since we've already decoded it
	headers.Del("Content-Encoding")

	entry := &CacheEntry{
		Response:   body,
		Headers:    headers,
		StatusCode: resp.StatusCode,
		ExpiresAt:  time.Now().Add(cs.cacheTTL),
	}

	return entry, nil
}

func (cs *CacheServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := cs.getCacheKey(r)
	log.Printf("Cache key: %s", key)
	// Check cache first
	if entry, ok := cs.cache.Load(key); ok {
		cachedEntry := entry.(*CacheEntry)
		if time.Now().Before(cachedEntry.ExpiresAt) {
			// Copy all headers
			for k, v := range cachedEntry.Headers {
				w.Header()[k] = v
			}
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(cachedEntry.StatusCode)
			w.Write(cachedEntry.Response)
			return
		}
		cs.cache.Delete(key)
	}

	// Check if there's an in-flight request
	if inFlight, ok := cs.inFlight.Load(key); ok {
		req := inFlight.(*InFlightRequest)
		// Wait for the in-flight request to complete
		<-req.Done
		if req.Error != nil {
			http.Error(w, req.Error.Error(), http.StatusBadGateway)
			return
		}
		// Copy all headers
		for k, v := range req.Entry.Headers {
			w.Header()[k] = v
		}
		w.Header().Set("X-Cache", "COALESCED")
		w.WriteHeader(req.Entry.StatusCode)
		w.Write(req.Entry.Response)
		return
	}

	// Create new in-flight request
	inFlight := &InFlightRequest{
		Done: make(chan struct{}),
	}
	cs.inFlight.Store(key, inFlight)
	defer cs.inFlight.Delete(key)

	// Clone the request body if it exists
	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}

	// Make the actual request
	entry, err := cs.makeRequest(r)
	if err != nil {
		inFlight.Error = err
		close(inFlight.Done)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Store in cache
	cs.cache.Store(key, entry)
	inFlight.Entry = entry
	close(inFlight.Done)

	// Respond to the original request
	// Copy all headers
	for k, v := range entry.Headers {
		w.Header()[k] = v
	}
	w.Header().Set("X-Cache", "MISS")
	w.WriteHeader(entry.StatusCode)
	w.Write(entry.Response)
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	targetHost := os.Getenv("TARGET_SERVER_URL")

	if targetHost == "" {
		log.Fatal("TARGET_SERVER_URL environment variable is required")
	}

	cacheServer := NewCacheServer(2*time.Minute, targetHost)
	port := os.Getenv("PORT")

	server := &http.Server{
		Addr:    fmt.Sprintf(":%s", port),
		Handler: cacheServer,
	}

	log.Printf("Cache server starting on :%s, forwarding to %s", port, targetHost)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
