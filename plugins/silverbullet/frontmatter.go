package main

import (
	"bytes"
	"regexp"
	"strings"
	"unicode"

	"github.com/adrg/frontmatter"
)

// frontmatterFields is the subset of SilverBullet's YAML frontmatter this
// plugin reads. Other frontmatter keys (title, updated, created, ...) are
// ignored — only tags feed keyword matching (D-03).
type frontmatterFields struct {
	Tags []string `yaml:"tags"`
}

// inlineTagPattern matches an inline "#tag" occurrence: a "#" immediately
// followed by an alphanumeric character (so a markdown heading like
// "# Title", which always has a space after the "#", never matches),
// anchored at the start of the body or after whitespace so "foo#bar" is
// never mistaken for a tag on "bar".
var inlineTagPattern = regexp.MustCompile(`(?:^|\s)#([[:alnum:]][[:alnum:]_-]*)`)

// ExtractTagsAndBody returns the frontmatter-stripped body and the union of
// frontmatter `tags:` values and inline #tags found in that body, as a
// de-duplicated (case-insensitively), order-stable slice of the
// first-seen spelling of each tag.
//
// Malformed frontmatter (unparsable YAML, or none present at all) falls
// back to treating the whole input as body with zero frontmatter tags —
// adrg/frontmatter.Parse returns the original content unchanged in that
// case, which is exactly the fallback this function wants; the error is
// deliberately not propagated to the caller.
func ExtractTagsAndBody(raw []byte) (body []byte, tags []string) {
	var fields frontmatterFields
	rest, err := frontmatter.Parse(bytes.NewReader(raw), &fields)
	if err != nil {
		rest = raw
		fields = frontmatterFields{}
	}
	body = rest

	seen := map[string]bool{}
	var ordered []string
	addTag := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		key := strings.ToLower(t)
		if seen[key] {
			return
		}
		seen[key] = true
		ordered = append(ordered, t)
	}

	for _, t := range fields.Tags {
		addTag(t)
	}
	for _, m := range inlineTagPattern.FindAllStringSubmatch(string(body), -1) {
		addTag(m[1])
	}

	return body, ordered
}

// MatchesKeyword implements D-03: a page matches keyword when the keyword
// case-insensitively equals any of its tags, its full space-relative path
// with ".md" already stripped, or that path's final segment — exact only,
// never a prefix or substring match.
func MatchesKeyword(pagePath string, tags []string, keyword string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, keyword) {
			return true
		}
	}
	if strings.EqualFold(pagePath, keyword) {
		return true
	}
	finalSegment := pagePath
	if idx := strings.LastIndex(pagePath, "/"); idx >= 0 {
		finalSegment = pagePath[idx+1:]
	}
	return strings.EqualFold(finalSegment, keyword)
}

// Snippet collapses whitespace runs and truncates to previewRuneCap runes
// on a rune boundary — the preview is a bounded snippet, never the full
// page body (the plan's own prohibition: full page bodies are never
// persisted to the local index).
func Snippet(body []byte) string {
	collapsed := strings.Join(strings.FieldsFunc(string(body), unicode.IsSpace), " ")
	runes := []rune(collapsed)
	if len(runes) <= previewRuneCap {
		return collapsed
	}
	return string(runes[:previewRuneCap])
}

// isPage reports whether f is a markdown page eligible for webspace
// matching: a ".md" suffix, excluding SilverBullet's own leading-underscore
// library/plug/resource paths (e.g. "_plug/foo.md", "_resources/...").
func isPage(f FileMeta) bool {
	return strings.HasSuffix(f.Name, ".md") && !strings.HasPrefix(f.Name, "_")
}
