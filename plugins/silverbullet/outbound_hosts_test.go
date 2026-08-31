package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestAllowHost_PredicateTable proves the outbound host allowlist directly:
// a client built against a given base URL permits that hostname (any
// letter case, any port), loopback addresses, and the literal "localhost";
// and refuses a foreign hostname, a foreign non-loopback IP literal, and an
// empty host. Every refusal must satisfy errors.Is(err, ErrForeignHost) —
// the test is on the sentinel specifically, not "any error".
func TestAllowHost_PredicateTable(t *testing.T) {
	c := NewClient("https://notes.example.lan:8443", "test-token", "")

	permit := []string{
		"notes.example.lan",
		"NOTES.EXAMPLE.LAN",
		"notes.example.lan:9000",
		"127.0.0.1",
		"127.0.0.1:8080",
		"::1",
		"[::1]:8080",
		"localhost",
		"LOCALHOST",
	}
	for _, host := range permit {
		if err := c.allowHost(host); err != nil {
			t.Errorf("allowHost(%q): expected nil, got %v", host, err)
		}
	}

	refuse := []string{
		"exfil.example.invalid",
		"203.0.113.5",
		"",
	}
	for _, host := range refuse {
		err := c.allowHost(host)
		if err == nil {
			t.Errorf("allowHost(%q): expected an error, got nil", host)
			continue
		}
		if !errors.Is(err, ErrForeignHost) {
			t.Errorf("allowHost(%q): expected errors.Is(err, ErrForeignHost), got %v", host, err)
		}
	}
}

// TestReadFile_CrossHostRedirect_Refused proves the guard fires before any
// connection to the foreign host is opened: the SilverBullet instance (or
// anything impersonating it) answers a page read with a 302 to a different
// host, and ReadFile must refuse it via the sentinel — not merely return
// "some error", which an unresolvable DNS name would also produce.
func TestReadFile_CrossHostRedirect_Refused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.fs/Decking.md" {
			w.Header().Set("Location", "http://exfil.example.invalid/.fs/Decking.md")
			w.WriteHeader(http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "")
	_, err := c.ReadFile(context.Background(), "Decking.md")
	if err == nil {
		t.Fatal("expected an error for a cross-host redirect")
	}
	if !errors.Is(err, ErrForeignHost) {
		t.Fatalf("expected errors.Is(err, ErrForeignHost), got %v", err)
	}
}

// TestReadFile_SameHostRedirect_StillFollowed proves the guard does not
// break a legitimate same-host redirect. It redirects to a distinct
// trailing-slash path rather than the same path with one extra slash
// appended, because Go's own URL reference resolution (net/url's
// resolvePath, used when following a Location header) collapses repeated
// slashes as part of the standard dot-segment-removal algorithm — a
// literal "same path plus one more slash" redirect target is normalized
// away by Go before this client's guard ever sees it, which would make
// that specific literal untestable here. What's under test is unaffected
// either way: a same-host redirect to a different path is followed and
// returns the real body.
func TestReadFile_SameHostRedirect_StillFollowed(t *testing.T) {
	const body = "# Real page\n\nafter redirect"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.fs/Decking.md":
			http.Redirect(w, r, "/.fs/Decking-canonical.md", http.StatusFound)
		case "/.fs/Decking-canonical.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "")
	got, err := c.ReadFile(context.Background(), "Decking.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != body {
		t.Fatalf("unexpected body: %q", got)
	}
}

// TestReadFile_RedirectCap_StopsLooping proves installing a custom
// CheckRedirect does not silently drop Go's own redirect-loop protection:
// a same-host endpoint that redirects to itself forever must not hang
// ReadFile forever, and the handler must not be hit more than 11 times
// (the original request plus 10 redirects).
func TestReadFile_RedirectCap_StopsLooping(t *testing.T) {
	var hits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "")
	_, err := c.ReadFile(context.Background(), "Loop.md")
	if err == nil {
		t.Fatal("expected an error from an infinite same-host redirect loop")
	}
	if got := atomic.LoadInt32(&hits); got > 11 {
		t.Fatalf("handler hit %d times, want at most 11", got)
	}
}
