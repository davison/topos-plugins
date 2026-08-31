package main

import (
	"strings"
	"testing"
)

// These tests cover RenderMarkdown's own responsibility only — converting
// markdown to an HTML fragment. Sanitization moved to the kernel's
// rendition boundary (kernel/httpapi/rendition.go, D-11); the sanitizer
// assertions that used to live in this file (script-element stripping,
// event-handler stripping, javascript-scheme link stripping) relocated
// there along with the document-wrap and theme-stylesheet assertions
// this plugin no longer owns.

func TestRenderMarkdown_HeadingAndText(t *testing.T) {
	out, err := RenderMarkdown([]byte("# Title\n\nhello"))
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "<h1") {
		t.Errorf("expected an h1 element, got: %s", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("expected the word %q to survive rendering, got: %s", "hello", got)
	}
}

// TestRenderMarkdown_OrdinaryMarkdownSurvives proves the conversion is
// non-vacuous: ordinary markdown constructs (headings, http links, lists,
// emphasis, fenced code) must render.
func TestRenderMarkdown_OrdinaryMarkdownSurvives(t *testing.T) {
	md := "# Heading\n\n" +
		"[a link](http://example.com/page)\n\n" +
		"- item one\n- item two\n\n" +
		"**bold** and *emphasis*\n\n" +
		"```\ncode block\n```\n"

	out, err := RenderMarkdown([]byte(md))
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	got := string(out)

	checks := []string{"<h1", "href=\"http://example.com/page\"", "<li", "<strong", "<em", "<pre", "code block"}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("expected rendered output to contain %q, got: %s", want, got)
		}
	}
}

// TestRenderMarkdown_RawHTMLNotPassedThrough proves goldmark's own
// safe-by-default behavior (this plugin's first, independent layer of
// defense, T-02-01) still holds now that the kernel owns the second layer:
// raw HTML embedded in markdown source is not rendered as live markup.
func TestRenderMarkdown_RawHTMLNotPassedThrough(t *testing.T) {
	out, err := RenderMarkdown([]byte("hello <script>alert(1)</script> world"))
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	got := string(out)
	if strings.Contains(strings.ToLower(got), "<script>alert") {
		t.Errorf("expected goldmark's default (non-raw-HTML) rendering to not pass through a live <script> element, got: %s", got)
	}
}

// TestRenderMarkdown_EmptyAndNilInputYieldsNoError pins the boundary
// behaviour for empty/nil input: RenderMarkdown must not error or panic.
func TestRenderMarkdown_EmptyAndNilInputYieldsNoError(t *testing.T) {
	if _, err := RenderMarkdown(nil); err != nil {
		t.Errorf("RenderMarkdown(nil): unexpected error: %v", err)
	}
	if _, err := RenderMarkdown([]byte{}); err != nil {
		t.Errorf("RenderMarkdown(empty): unexpected error: %v", err)
	}
}
