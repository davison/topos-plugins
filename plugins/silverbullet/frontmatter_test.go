package main

import (
	"strings"
	"testing"
)

// TestMatchesKeyword_TagCaseInsensitive proves D-03's tag match: exact,
// case-insensitive against a tag value.
func TestMatchesKeyword_TagCaseInsensitive(t *testing.T) {
	if !MatchesKeyword("projects/House move", []string{"house and home"}, "House And Home") {
		t.Fatal("expected a case-insensitive exact tag match")
	}
}

// TestMatchesKeyword_FinalPathSegment proves D-03's name match against the
// page path's final segment, case-insensitive.
func TestMatchesKeyword_FinalPathSegment(t *testing.T) {
	if !MatchesKeyword("projects/House move", nil, "house move") {
		t.Fatal("expected a case-insensitive match against the final path segment")
	}
}

// TestMatchesKeyword_NoPrefixMatch proves D-03's exclusion: no
// prefix/substring matching against the page name.
func TestMatchesKeyword_NoPrefixMatch(t *testing.T) {
	if MatchesKeyword("projects/House move", nil, "house") {
		t.Fatal("expected no match: \"house\" is a prefix of \"House move\", not an exact match")
	}
}

// TestMatchesKeyword_FullPath proves D-03's name match against the full
// space-relative path (extension already stripped by the caller).
func TestMatchesKeyword_FullPath(t *testing.T) {
	if !MatchesKeyword("projects/House move", nil, "projects/house move") {
		t.Fatal("expected a case-insensitive match against the full path")
	}
}

// TestIsPage_ExcludesUnderscorePaths proves isPage filters SilverBullet's
// own leading-underscore library/plug paths out of webspace matching.
func TestIsPage_ExcludesUnderscorePaths(t *testing.T) {
	if isPage(FileMeta{Name: "_plug/foo.md"}) {
		t.Error("expected _plug/foo.md to be excluded")
	}
	if !isPage(FileMeta{Name: "notes/a.md"}) {
		t.Error("expected notes/a.md to be included")
	}
	if isPage(FileMeta{Name: "img/a.png"}) {
		t.Error("expected img/a.png (non-markdown) to be excluded")
	}
}

// TestExtractTagsAndBody_FrontmatterAndInlineUnion proves ExtractTagsAndBody
// strips YAML frontmatter and unions its tags: values with inline #tags
// found in the remaining body.
func TestExtractTagsAndBody_FrontmatterAndInlineUnion(t *testing.T) {
	raw := []byte("---\ntags: [house and home, admin]\n---\nbody #urgent")
	body, tags := ExtractTagsAndBody(raw)

	if string(body) != "body #urgent" {
		t.Errorf("expected the frontmatter-stripped body to be %q, got %q", "body #urgent", string(body))
	}

	want := map[string]bool{"house and home": true, "admin": true, "urgent": true}
	if len(tags) != len(want) {
		t.Fatalf("expected %d tags, got %d: %v", len(want), len(tags), tags)
	}
	for _, tag := range tags {
		if !want[tag] {
			t.Errorf("unexpected tag %q", tag)
		}
	}
}

// TestExtractTagsAndBody_Table is table-driven coverage of every shape
// ExtractTagsAndBody must handle: frontmatter-only tags, inline-only tags,
// both (see TestExtractTagsAndBody_FrontmatterAndInlineUnion above),
// neither, and malformed frontmatter falling back to treating the whole
// input as body.
func TestExtractTagsAndBody_Table(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantBody string
		wantTags map[string]bool
	}{
		{
			name:     "frontmatter-only tags",
			raw:      "---\ntags: [house]\n---\nplain body, no inline tags",
			wantBody: "plain body, no inline tags",
			wantTags: map[string]bool{"house": true},
		},
		{
			name:     "inline-only tags",
			raw:      "no frontmatter here, just #house and #move",
			wantBody: "no frontmatter here, just #house and #move",
			wantTags: map[string]bool{"house": true, "move": true},
		},
		{
			name:     "neither",
			raw:      "just a plain page with no tags at all",
			wantBody: "just a plain page with no tags at all",
			wantTags: map[string]bool{},
		},
		{
			name:     "malformed frontmatter falls back to whole input as body",
			raw:      "---\ntags: [unterminated\nbody text",
			wantBody: "---\ntags: [unterminated\nbody text",
			wantTags: map[string]bool{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, tags := ExtractTagsAndBody([]byte(tc.raw))
			if string(body) != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			got := map[string]bool{}
			for _, tag := range tags {
				got[tag] = true
			}
			if len(got) != len(tc.wantTags) {
				t.Fatalf("tags = %v, want %v", tags, tc.wantTags)
			}
			for tag := range tc.wantTags {
				if !got[tag] {
					t.Errorf("expected tag %q, got tags %v", tag, tags)
				}
			}
		})
	}
}

// TestMatchesKeyword_Table consolidates the exact/case-insensitive match
// matrix (tag, final path segment, full path, and the negative prefix
// case) into one table, alongside the granular tests above.
func TestMatchesKeyword_Table(t *testing.T) {
	cases := []struct {
		name     string
		pagePath string
		tags     []string
		keyword  string
		want     bool
	}{
		{"tag exact case-insensitive", "projects/House move", []string{"house and home"}, "House And Home", true},
		{"final path segment case-insensitive", "projects/House move", nil, "house move", true},
		{"full path case-insensitive", "projects/House move", nil, "projects/house move", true},
		{"prefix of page name does not match", "projects/House move", nil, "house", false},
		{"substring of a tag does not match", "projects/House move", []string{"household"}, "house", false},
		{"no match at all", "projects/House move", []string{"garden"}, "kitchen", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesKeyword(tc.pagePath, tc.tags, tc.keyword); got != tc.want {
				t.Errorf("MatchesKeyword(%q, %v, %q) = %v, want %v", tc.pagePath, tc.tags, tc.keyword, got, tc.want)
			}
		})
	}
}

// TestSnippet_Table proves Snippet's whitespace-collapse and rune-boundary
// truncation behavior, including the exact-cap boundary and multi-byte
// runes never being split mid-character.
func TestSnippet_Table(t *testing.T) {
	t.Run("whitespace collapsed", func(t *testing.T) {
		got := Snippet([]byte("line one\n\n  line   two\ttabbed"))
		want := "line one line two tabbed"
		if got != want {
			t.Errorf("Snippet = %q, want %q", got, want)
		}
	})

	t.Run("input exactly at the rune cap is unchanged", func(t *testing.T) {
		input := strings.Repeat("a", previewRuneCap)
		got := Snippet([]byte(input))
		if got != input {
			t.Errorf("expected input exactly at the cap to be returned unchanged, got length %d, want %d", len(got), len(input))
		}
	})

	t.Run("input one rune over the cap is truncated on a rune boundary", func(t *testing.T) {
		input := strings.Repeat("a", previewRuneCap+1)
		got := Snippet([]byte(input))
		if len([]rune(got)) != previewRuneCap {
			t.Errorf("expected exactly %d runes, got %d", previewRuneCap, len([]rune(got)))
		}
	})

	t.Run("multi-byte runes are not split", func(t *testing.T) {
		// "日" (U+65E5) is a 3-byte UTF-8 rune. Build input at cap+1 runes
		// entirely of multi-byte characters and confirm the truncated
		// result is still valid UTF-8 with exactly previewRuneCap runes —
		// a byte-index truncation (rather than rune-index) would either
		// panic or produce an invalid/mid-character cut here.
		input := strings.Repeat("日", previewRuneCap+1)
		got := Snippet([]byte(input))
		runes := []rune(got)
		if len(runes) != previewRuneCap {
			t.Errorf("expected exactly %d runes, got %d", previewRuneCap, len(runes))
		}
		if !strings.HasPrefix(input, got) {
			t.Errorf("expected the truncated result to be a clean prefix of the input, got %q", got)
		}
	})
}
