// Package searchkit is what a topos plugin needs to implement the optional
// Search RPC honestly (docs/plugin-contract.md "Search", M2-R2 of
// davison/topos#40): the membership refusal every plugin must make, the
// query and required-term matching every plugin shares, bounded snippets
// that never leak a body, and the limit/truncated bookkeeping. Each plugin
// still searches ITS OWN content its own way and decides membership by
// ITS OWN Match rule; this package only removes the seven-way duplication
// of the parts that are the same for everyone.
package searchkit

import (
	"sort"
	"strings"
	"unicode/utf8"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrNoMembership is the InvalidArgument every plugin returns for an empty
// or absent match_fields: a search without membership input would be the
// whole source, which is refused — never searched (kernel/correlate's own
// rule; docs/plugin-contract.md "Search").
var ErrNoMembership = status.Error(codes.InvalidArgument, "search: match_fields is empty — a search without membership input would be the whole source, which is refused")

// RequireMembership returns ErrNoMembership unless req carries at least one
// match field with at least one value.
func RequireMembership(req *toposv1.SearchRequest) error {
	for _, v := range req.GetMatchFields() {
		if len(v.GetValues()) > 0 {
			return nil
		}
	}
	return ErrNoMembership
}

// Terms splits a query into lowercase terms of two or more characters —
// the kernel's own rule for its index search, so a source's answer and
// the index's agree on what a query means.
func Terms(query string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(query)) {
		if utf8.RuneCountInString(f) >= 2 {
			out = append(out, f)
		}
	}
	return out
}

// Required lowercases and trims req.required_terms, dropping empties.
func Required(req *toposv1.SearchRequest) []string {
	var out []string
	for _, t := range req.GetRequiredTerms() {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ContainsAll reports whether every term occurs in s (case-insensitive
// substring). Empty terms means true.
func ContainsAll(s string, terms []string) bool {
	l := strings.ToLower(s)
	for _, t := range terms {
		if !strings.Contains(l, t) {
			return false
		}
	}
	return true
}

// Matches is the AND every plugin applies: every query term AND every
// required term occurs somewhere in the item's searchable text — title,
// preview, labels and body together.
func Matches(title, preview, body string, labels []string, terms, required []string) bool {
	all := title + "\n" + preview + "\n" + strings.Join(labels, "\n") + "\n" + body
	return ContainsAll(all, terms) && ContainsAll(all, required)
}

// MatchedIn says where the query terms matched, in the contract's
// precedence: all in the title → TITLE; else all in the body → BODY; else
// all in the labels → LABELS; else TITLE (a match spread across title and
// preview is the item's own summary).
func MatchedIn(title, body string, labels []string, terms []string) toposv1.MatchedIn {
	switch {
	case ContainsAll(title, terms):
		return toposv1.MatchedIn_MATCHED_IN_TITLE
	case body != "" && ContainsAll(body, terms):
		return toposv1.MatchedIn_MATCHED_IN_BODY
	case ContainsAll(strings.Join(labels, "\n"), terms):
		return toposv1.MatchedIn_MATCHED_IN_LABELS
	}
	return toposv1.MatchedIn_MATCHED_IN_TITLE
}

// SnippetWindow bounds a snippet: this many runes each side of the first
// matching term — hundreds of characters at most, never a body.
const SnippetWindow = 60

// Snippet returns a bounded window of body around the first occurrence of
// any term (case-insensitive), or the body's head; "" for an empty body.
// Cuts are on rune boundaries; "…" marks a cut end.
func Snippet(body string, terms []string) string {
	if body == "" {
		return ""
	}
	runes := []rune(body)
	lower := strings.ToLower(body)
	at := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 {
			at = utf8.RuneCountInString(lower[:i])
			break
		}
	}
	if at < 0 {
		at = 0
	}
	start, end := at-SnippetWindow, at+SnippetWindow
	if start < 0 {
		start = 0
	}
	if end > len(runes) {
		end = len(runes)
	}
	s := string(runes[start:end])
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s += "…"
	}
	return strings.TrimSpace(s)
}

// Limit applies req.limit: at most limit hits (0 = all), reporting
// whether any were cut.
func Limit(hits []*toposv1.SearchHit, req *toposv1.SearchRequest) ([]*toposv1.SearchHit, bool) {
	limit := int(req.GetLimit())
	if limit > 0 && len(hits) > limit {
		return hits[:limit], true
	}
	if hits == nil {
		hits = []*toposv1.SearchHit{}
	}
	return hits, false
}

// SortHitsByTimestamp orders hits newest first, then by source id — a
// stable order for sources whose own listing order is arbitrary.
func SortHitsByTimestamp(hits []*toposv1.SearchHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i].GetItem(), hits[j].GetItem()
		if a.GetTimestampUnix() != b.GetTimestampUnix() {
			return a.GetTimestampUnix() > b.GetTimestampUnix()
		}
		return a.GetSourceId() < b.GetSourceId()
	})
}
