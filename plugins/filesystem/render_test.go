package main

import (
	"strings"
	"testing"
)

// --- RenderMarkdown (D-04): goldmark markdown -> unsanitized HTML fragment ---
// Written before render.go.

func TestRenderMarkdown_HeadingAndLinkConvertToHTMLElements(t *testing.T) {
	out, err := RenderMarkdown([]byte("# Title\n\n[a link](https://example.com)\n"))
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "<h1") {
		t.Errorf("expected an <h1> element in %q", got)
	}
	if !strings.Contains(got, `<a href="https://example.com"`) {
		t.Errorf("expected an <a href> element in %q", got)
	}
}

func TestRenderMarkdown_RawHTMLIsNotPassedThroughAsLiveMarkup(t *testing.T) {
	out, err := RenderMarkdown([]byte("before\n\n<script>alert(1)</script>\n\nafter\n"))
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "<script>alert(1)</script>") {
		t.Fatalf("expected raw HTML not to survive goldmark's safe defaults verbatim, got %q", got)
	}
}
