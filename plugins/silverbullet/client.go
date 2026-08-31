// Command topos-plugin-silverbullet: this file implements the
// hand-rolled SilverBullet /.fs HTTP client half of the plugin (see
// plugin.go for the toposv1.SourcePlugin adapter and main.go for the
// subprocess entrypoint).
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrNotFound is returned by ReadFile when SilverBullet returns 404 for the
// requested page path — distinct from a transport/5xx failure so callers
// can map it to a "not found" rather than "unavailable" outcome.
var ErrNotFound = errors.New("silverbullet: not found")

// ErrForeignHost is returned when the outbound host allowlist refuses a
// dial target or redirect destination that is neither the configured
// SilverBullet host nor a loopback address. This is the enforcement half
// of the prohibition that plugin outbound traffic MUST NOT reach any host
// other than the user's own configured source instance and the loopback
// interface (PROJECT.md Constraints).
var ErrForeignHost = errors.New("silverbullet: foreign host refused")

// FileMeta is one entry of GET /.fs. Timestamps are Unix milliseconds.
// Field names confirmed against the user's real SilverBullet v2 instance
// (Task 1 Step 0): a flat JSON array of exactly this shape, e.g.
// {"name":"Decking.md","created":1779299030000,"lastModified":1779299030000,
// "contentType":"text/markdown","size":1747,"perm":"ro"} — matching
// research assumption A1 exactly (bare array, not wrapped).
type FileMeta struct {
	Name         string `json:"name"`
	Created      int64  `json:"created"`
	LastModified int64  `json:"lastModified"`
	ContentType  string `json:"contentType"`
	Size         int64  `json:"size"`
	Perm         string `json:"perm"`
}

// Client is a thin, read-only HTTP client against a SilverBullet instance's
// /.fs space-filesystem API. Every request in this file uses GET — there is
// no PUT/DELETE code path anywhere in this client (PLUG-02: plugins never
// mutate source data stores). Every outbound connection is also
// host-pinned: see allowHost.
type Client struct {
	baseURL  string
	baseHost string // lowercased Hostname() of baseURL; empty means "loopback only" (fail closed)
	token    string
	http     *http.Client
}

// NewClient builds a Client bounded to at most 4 concurrent in-flight
// connections to the configured SilverBullet instance and a 30-second
// per-request timeout, sharing exactly one http.Client/http.Transport
// across every RPC this plugin process serves.
//
// caCertPath is an optional path to a PEM-encoded CA certificate to trust
// in addition to (by replacing, per Go's tls.Config.RootCAs semantics) the
// system trust store. This is a deviation from the plan's originally
// sketched two-argument NewClient(baseURL, token string) signature,
// discovered live against the user's real instance during Task 1 Step 0:
// the instance serves HTTPS behind a self-signed certificate the system
// trust store does not contain, so a client built with Go's default TLS
// verification cannot connect at all. Pass "" for the default (system
// trust store only) behavior a non-self-signed deployment would need.
//
// Every outbound connection this client makes — including a redirect hop
// served by the SilverBullet instance itself — is checked against
// allowHost before any bytes leave the process. If baseURL fails to parse
// or has no hostname, baseHost is left empty so allowHost permits loopback
// only: a malformed base_url must never widen the allowlist.
func NewClient(baseURL, token, caCertPath string) *Client {
	var baseHost string
	if u, err := url.Parse(baseURL); err == nil {
		baseHost = strings.ToLower(u.Hostname())
	}

	c := &Client{
		// TrimRight (not TrimSuffix) so any number of trailing slashes in a
		// misconfigured base_url — e.g. a copy-pasted "https://host/" — is
		// normalized away. An un-normalized trailing slash turns every
		// "{base}/.fs" request into "{base}//.fs", which this instance's
		// live server (confirmed Task 1 Step 0) answers with the SPA HTML
		// shell (200 text/html) instead of JSON — a silent, hard-to-debug
		// failure mode rather than a clean error.
		baseURL:  strings.TrimRight(baseURL, "/"),
		baseHost: baseHost,
		token:    token,
	}

	tlsConfig := &tls.Config{}
	if caCertPath != "" {
		if pemBytes, err := os.ReadFile(caCertPath); err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(pemBytes) {
				tlsConfig.RootCAs = pool
			}
			// An unparsable PEM file falls through to the system trust
			// store (tlsConfig.RootCAs stays nil) rather than panicking
			// NewClient — verification will simply fail at request time
			// and surface through the existing Unavailable/Health error
			// path, which is the correct place for a config-fixable
			// problem to be diagnosed from.
		}
		// A missing/unreadable ca_cert path is treated the same way: fall
		// back to the system trust store rather than crash plugin startup.
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		MaxConnsPerHost: 4,
		TLSClientConfig: tlsConfig,
		// DialContext is the backstop: it refuses to open a connection to
		// a foreign host regardless of which code path in this file (now
		// or in the future) tried to reach it, catching anything
		// CheckRedirect below did not — a request never built from a
		// redirect at all, for instance.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}
			if err := c.allowHost(host); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}

	c.http = &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		// CheckRedirect is the common case: it stops a cross-host redirect
		// served by the SilverBullet instance (a compromised, spoofed, or
		// misconfigured source could otherwise steer this client at an
		// arbitrary host) before a connection to it is even opened.
		// Installing a custom CheckRedirect replaces Go's built-in
		// 10-redirect cap, so that cap is re-implemented here
		// deliberately.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("silverbullet: stopped after %d redirects", len(via))
			}
			return c.allowHost(req.URL.Hostname())
		},
	}
	return c
}

// allowHost is the outbound host allowlist predicate. It strips any port
// and IPv6 brackets from host, lowercases it, and permits the value when
// it equals this client's configured SilverBullet hostname, when
// net.ParseIP reports a loopback address, or when it is the literal
// "localhost"; otherwise it returns an error wrapping ErrForeignHost and
// naming the refused host.
//
// Port is deliberately outside the comparison: the configured host is the
// user's own SilverBullet instance, and a reverse proxy in front of it may
// legitimately move between ports on that same host.
func (c *Client) allowHost(hostport string) error {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))

	if host != "" && c.baseHost != "" && host == c.baseHost {
		return nil
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrForeignHost, host)
}

// ListFiles fetches the full space listing via a single GET /.fs — no
// pagination, no server-side filter (confirmed Task 1 Step 0 against the
// real instance: a flat JSON array of ~270 FileMeta entries, no "next"
// field, no query parameters accepted).
//
// The X-Sync-Mode: true header is required by this SilverBullet v2
// instance (confirmed live) — without it, /.fs answers with a 307 redirect
// to the SPA shell rather than the JSON listing. A response whose
// Content-Type is text/html is therefore always treated as an error here,
// never decoded as page data: it means the request was answered by the SPA
// shell, not the /.fs API, and silently parsing HTML as JSON would fail
// opaquely deep inside json.Decode instead of with a clear diagnostic.
func (c *Client) ListFiles(ctx context.Context) ([]FileMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/.fs", nil)
	if err != nil {
		return nil, fmt.Errorf("silverbullet: build request: %w", err)
	}
	c.setCommonHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		// Never log the request object or its headers — the Authorization
		// header carries the bearer token.
		return nil, fmt.Errorf("silverbullet: request /.fs: %w", err)
	}
	defer resp.Body.Close()

	if isHTMLResponse(resp) {
		return nil, fmt.Errorf("silverbullet: /.fs returned the SPA HTML shell (status %d) instead of JSON — check X-Sync-Mode header handling", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("silverbullet: unexpected status %d from /.fs", resp.StatusCode)
	}

	var files []FileMeta
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("silverbullet: decode /.fs response: %w", err)
	}
	return files, nil
}

// ReadFile fetches one page's raw bytes via GET /.fs/{path}, path-escaped
// per segment so a space-relative path containing spaces or other
// reserved characters (e.g. "projects/House move.md") round-trips
// correctly. Maps HTTP 404 to ErrNotFound. Like ListFiles, a text/html
// response is always an error, never silently returned as if it were page
// content.
func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/.fs/"+escapePathSegments(path), nil)
	if err != nil {
		return nil, fmt.Errorf("silverbullet: build request: %w", err)
	}
	c.setCommonHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("silverbullet: request /.fs/%s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if isHTMLResponse(resp) {
		return nil, fmt.Errorf("silverbullet: /.fs/%s returned the SPA HTML shell (status %d) instead of file content — check X-Sync-Mode header handling", path, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("silverbullet: unexpected status %d from /.fs/%s", resp.StatusCode, path)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("silverbullet: read body from /.fs/%s: %w", path, err)
	}
	return data, nil
}

// setCommonHeaders applies the two headers every /.fs request needs: the
// bearer token (sent regardless of whether this instance currently
// enforces it — SB_AUTH_TOKEN is still the documented, correct auth
// mechanism per the project's stack decision) and X-Sync-Mode, without
// which this SilverBullet v2 instance answers with the SPA shell instead
// of API data (confirmed live, Task 1 Step 0).
func (c *Client) setCommonHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Sync-Mode", "true")
}

// isHTMLResponse reports whether resp's Content-Type indicates the SPA
// HTML shell rather than a /.fs API response.
func isHTMLResponse(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "text/html")
}

// escapePathSegments percent-escapes each "/"-separated segment of path
// independently, so a literal "/" in the space-relative path (the intended
// directory separator) is preserved while spaces and other
// reserved/non-ASCII characters within a segment are escaped.
func escapePathSegments(path string) string {
	segments := strings.Split(path, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}
