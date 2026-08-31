// Command topos-plugin-paperless: this file implements the hand-rolled
// paperless-ngx REST client half of the plugin (see plugin.go for the
// toposv1.SourcePlugin adapter and main.go for the subprocess
// entrypoint).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned by Document when paperless-ngx returns 404 for
// the requested document id — distinct from a transport/5xx failure so
// callers can map it to a "not found" rather than "unavailable" outcome.
var ErrNotFound = errors.New("paperless: not found")

// ErrForeignHost is returned when the outbound host allowlist refuses a
// dial target or redirect destination that is neither the configured
// paperless-ngx host nor a loopback address. This is the enforcement half
// of the prohibition that plugin outbound traffic MUST NOT reach any host
// other than the user's own configured paperless-ngx instance and the
// loopback interface (PROJECT.md Constraints; 01-01-PLAN.md's third
// prohibition, closed by gap G-01-6).
var ErrForeignHost = errors.New("paperless: foreign host refused")

// RenditionResult holds one fetched rendition (preview or thumbnail).
// Available is false, with a zero Data/ContentType, when paperless-ngx
// returned 404 for the rendition — a normal outcome (e.g. a file type
// paperless cannot preview), not an error.
type RenditionResult struct {
	Available   bool
	Data        []byte
	ContentType string
}

// Client is a thin, read-only REST client against a paperless-ngx
// instance. Every request uses the GET method — there is no code path in
// this file that sends any other method (PLUG-02: plugins never mutate
// source data stores). Every outbound connection is also host-pinned: see
// allowHost.
type Client struct {
	baseURL    string
	baseHost   string // lowercased Hostname() of baseURL; empty means "loopback only" (fail closed)
	token      string
	apiVersion string
	http       *http.Client
}

// NewClient builds a Client bounded to at most 4 concurrent in-flight
// connections to paperless-ngx (SRC-04/concurrency) and a 30-second
// per-request timeout, sharing exactly one http.Client/http.Transport
// across every RPC this plugin process serves.
//
// Every outbound connection this client makes — including a redirect hop
// served by paperless-ngx itself — is checked against allowHost before any
// bytes leave the process. If baseURL fails to parse or has no hostname,
// baseHost is left empty so allowHost permits loopback only: a malformed
// base_url must never widen the allowlist.
func NewClient(baseURL, token, apiVersion string) *Client {
	var baseHost string
	if u, err := url.Parse(baseURL); err == nil {
		baseHost = strings.ToLower(u.Hostname())
	}

	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		baseHost:   baseHost,
		token:      token,
		apiVersion: apiVersion,
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		MaxConnsPerHost: 4,
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
		// CheckRedirect is the common case: it stops a cross-host
		// redirect served by paperless-ngx (a compromised, spoofed, or
		// misconfigured source could otherwise steer this client at an
		// arbitrary host) before a connection to it is even opened.
		// Installing a custom CheckRedirect replaces Go's built-in
		// 10-redirect cap, so that cap is re-implemented here
		// deliberately.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("paperless: stopped after %d redirects", len(via))
			}
			return c.allowHost(req.URL.Hostname())
		},
	}
	return c
}

// allowHost is the outbound host allowlist predicate. It strips any port
// and IPv6 brackets from host, lowercases it, and permits the value when
// it equals this client's configured paperless-ngx hostname, when
// net.ParseIP reports a loopback address, or when it is the literal
// "localhost"; otherwise it returns an error wrapping ErrForeignHost and
// naming the refused host.
//
// Port is deliberately outside the comparison: the configured host is the
// user's own paperless-ngx instance, and a reverse proxy in front of it
// may legitimately move between ports (e.g. 80 to 443) on that same host.
// The prohibition this enforces is about foreign hosts, not foreign
// ports — do not "tighten" this into a port check.
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

// Document is a paperless-ngx document as relevant to this plugin.
type Document struct {
	ID      int
	Title   string
	Content string
	Created time.Time // date-only "created" field, parsed as midnight UTC
	Added   time.Time // full-datetime "added" field
	TagIDs  []int
}

// Tag is a paperless-ngx tag.
type Tag struct {
	ID   int
	Name string
}

type tagResult struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type tagsPage struct {
	Next    *string     `json:"next"`
	Results []tagResult `json:"results"`
}

type documentResult struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Created string `json:"created"`
	Added   string `json:"added"`
	Tags    []int  `json:"tags"`
}

type documentsPage struct {
	Next    *string          `json:"next"`
	Results []documentResult `json:"results"`
}

// ResolveTagIDs resolves each keyword to zero or more tag IDs via an exact,
// case-insensitive tag-name lookup (name__iexact — D-03). It never uses a
// substring-based tag name filter, which would make the keyword "house"
// match the tag "Household".
func (c *Client) ResolveTagIDs(ctx context.Context, keywords []string) ([]int, error) {
	var ids []int
	seen := map[int]bool{}
	for _, kw := range keywords {
		q := url.Values{}
		q.Set("name__iexact", kw)
		q.Set("page_size", "100")

		var page tagsPage
		if err := c.getJSON(ctx, "/api/tags/", q, &page); err != nil {
			return nil, fmt.Errorf("paperless: resolve tag for keyword %q: %w", kw, err)
		}
		for _, t := range page.Results {
			if !seen[t.ID] {
				seen[t.ID] = true
				ids = append(ids, t.ID)
			}
		}
	}
	return ids, nil
}

// ListDocuments fetches every document tagged with any of tagIDs
// (confirmed OR semantics via Django's tags__id__in), following pagination
// to completion. Returns an empty slice, not an error, when tagIDs is
// empty.
func (c *Client) ListDocuments(ctx context.Context, tagIDs []int) ([]Document, error) {
	if len(tagIDs) == 0 {
		return nil, nil
	}

	idStrs := make([]string, len(tagIDs))
	for i, id := range tagIDs {
		idStrs[i] = strconv.Itoa(id)
	}

	q := url.Values{}
	q.Set("tags__id__in", strings.Join(idStrs, ","))
	q.Set("page_size", "100")
	q.Set("ordering", "-created")

	var docs []Document
	path := "/api/documents/"
	values := q
	for {
		var page documentsPage
		if err := c.getJSON(ctx, path, values, &page); err != nil {
			return nil, fmt.Errorf("paperless: list documents: %w", err)
		}
		for _, d := range page.Results {
			doc, err := toDocument(d)
			if err != nil {
				return nil, fmt.Errorf("paperless: parse document %d: %w", d.ID, err)
			}
			docs = append(docs, doc)
		}
		if page.Next == nil || *page.Next == "" {
			break
		}
		nextPath, nextValues, err := splitNextURL(*page.Next)
		if err != nil {
			return nil, fmt.Errorf("paperless: parse next page URL: %w", err)
		}
		path, values = nextPath, nextValues
	}
	return docs, nil
}

// AllTags fetches every tag known to paperless-ngx, paginated to
// completion, keyed by tag ID. Used to resolve a document's own tags to
// human-readable names for Item.Labels.
func (c *Client) AllTags(ctx context.Context) (map[int]Tag, error) {
	out := map[int]Tag{}
	path := "/api/tags/"
	values := url.Values{"page_size": {"100"}}
	for {
		var page tagsPage
		if err := c.getJSON(ctx, path, values, &page); err != nil {
			return nil, fmt.Errorf("paperless: list tags: %w", err)
		}
		for _, t := range page.Results {
			out[t.ID] = Tag{ID: t.ID, Name: t.Name}
		}
		if page.Next == nil || *page.Next == "" {
			break
		}
		nextPath, nextValues, err := splitNextURL(*page.Next)
		if err != nil {
			return nil, fmt.Errorf("paperless: parse next tags page URL: %w", err)
		}
		path, values = nextPath, nextValues
	}
	return out, nil
}

// Document fetches the full detail of a single document, including its
// extracted content, for live item-open (KERN-03) — never called from the
// sync/Match path. Returns ErrNotFound when paperless-ngx 404s.
func (c *Client) Document(ctx context.Context, id int) (Document, error) {
	path := fmt.Sprintf("/api/documents/%d/", id)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return Document{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Accept", "application/json; version="+c.apiVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return Document{}, fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return Document{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Document{}, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, path)
	}

	var d documentResult
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return Document{}, fmt.Errorf("decode response from %s: %w", path, err)
	}
	return toDocument(d)
}

// Preview fetches a document's inline preview rendition via
// GET {base}/api/documents/{id}/preview/. A 404 from paperless-ngx (no
// previewable rendition for this file type) yields
// RenditionResult{Available: false} with a nil error — it is a normal
// outcome, not a transport failure.
func (c *Client) Preview(ctx context.Context, id int) (RenditionResult, error) {
	return c.rendition(ctx, fmt.Sprintf("/api/documents/%d/preview/", id))
}

// Thumbnail fetches a document's thumbnail rendition via
// GET {base}/api/documents/{id}/thumb/. Same 404-is-not-an-error contract
// as Preview.
func (c *Client) Thumbnail(ctx context.Context, id int) (RenditionResult, error) {
	return c.rendition(ctx, fmt.Sprintf("/api/documents/%d/thumb/", id))
}

// rendition performs the shared GET + status/body handling behind Preview
// and Thumbnail.
func (c *Client) rendition(ctx context.Context, path string) (RenditionResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return RenditionResult{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return RenditionResult{}, fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return RenditionResult{Available: false}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return RenditionResult{}, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, path)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return RenditionResult{}, fmt.Errorf("read body from %s: %w", path, err)
	}
	return RenditionResult{Available: true, Data: data, ContentType: resp.Header.Get("Content-Type")}, nil
}

func toDocument(d documentResult) (Document, error) {
	// created is a date-only field as of paperless-ngx API v9+ (never the
	// deprecated full-datetime creation field that preceded it) — parsed
	// as midnight UTC.
	created, err := time.Parse("2006-01-02", d.Created)
	if err != nil {
		// Fall back to RFC3339 in case the server still returns a full
		// datetime; take just the date portion either way.
		if t, err2 := time.Parse(time.RFC3339, d.Created); err2 == nil {
			created = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		} else {
			return Document{}, fmt.Errorf("parse created %q: %w", d.Created, err)
		}
	}

	added, err := time.Parse(time.RFC3339, d.Added)
	if err != nil {
		added = time.Unix(0, 0).UTC()
	}

	return Document{
		ID: d.ID, Title: d.Title, Content: d.Content,
		Created: created, Added: added, TagIDs: d.Tags,
	}, nil
}

// getJSON performs a single GET request against path+query and decodes the
// JSON response body into out. Every request in this file uses GET; there
// is no PUT/POST/PATCH/DELETE code path anywhere in the plugin.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out interface{}) error {
	full := c.baseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Accept", "application/json; version="+c.apiVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		// Never log the request object or its headers — the Authorization
		// header carries the bearer token (T-01-02).
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d from %s", resp.StatusCode, path)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response from %s: %w", path, err)
	}
	return nil
}

var absoluteURLPrefix = regexp.MustCompile(`^https?://[^/]+`)

// splitNextURL turns a paperless-ngx pagination "next" URL (which may be
// absolute, including scheme and host) back into a path + query pair
// relative to this client's configured base URL, so getJSON can prefix it
// consistently.
func splitNextURL(next string) (string, url.Values, error) {
	trimmed := absoluteURLPrefix.ReplaceAllString(next, "")
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", nil, err
	}
	return u.Path, u.Query(), nil
}
