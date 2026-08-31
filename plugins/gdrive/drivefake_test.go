// Package main's drivefake_test.go provides a fake Drive REST endpoint
// (httptest-backed, no real Google credentials needed) that
// syncengine_test.go and folderwalk_test.go build fixture folder trees
// against. Extends this repository's own established httptest pattern
// (plugin_test.go's refreshTokenServer, plugin_test.go:363-388) via the
// generated Drive client's own documented WithHTTPClient/WithEndpoint
// options.
package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// newFakeDriveService builds a *drive.Service whose every request is
// served by handler, over a local httptest.Server — no real Google
// credentials or network access is used.
func newFakeDriveService(t *testing.T, handler http.HandlerFunc) *drive.Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	svc, err := drive.NewService(t.Context(),
		option.WithHTTPClient(srv.Client()),
		option.WithEndpoint(srv.URL),
	)
	if err != nil {
		t.Fatalf("drive.NewService: %v", err)
	}
	return svc
}

// driveRecorder wraps an http.HandlerFunc, counting requests per URL path
// and capturing every request's query parameters, so tests can assert
// both "how many times was files.list called" (the second-Match-issues-
// zero-further-requests proof) and "which query parameters were ever
// sent" (the no-Shared-Drive-parameter proof).
type driveRecorder struct {
	handler http.HandlerFunc

	mu      sync.Mutex
	calls   map[string]int
	queries []map[string][]string
}

func newDriveRecorder(handler http.HandlerFunc) *driveRecorder {
	return &driveRecorder{handler: handler, calls: map[string]int{}}
}

func (r *driveRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.calls[req.URL.Path]++
	r.queries = append(r.queries, map[string][]string(req.URL.Query()))
	r.mu.Unlock()
	r.handler(w, req)
}

// count returns how many requests this recorder has seen at path.
func (r *driveRecorder) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[path]
}

// sawQueryParam reports whether any recorded request carried a query
// parameter named name, regardless of its value.
func (r *driveRecorder) sawQueryParam(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.queries {
		if _, ok := q[name]; ok {
			return true
		}
	}
	return false
}
