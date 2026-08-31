package main

import (
	"bytes"

	"github.com/yuin/goldmark"
)

// mdConverter is built once at package init — documented safe for
// concurrent use, and rebuilding it per call would be wasteful (goldmark.
// Markdown parses its own extension chain on construction).
//
// goldmark is left at its defaults deliberately: it does not render raw
// HTML passed through the source markdown, and it does not permit
// dangerous URL schemes (e.g. "javascript:") in link/image targets by
// itself producing them from ordinary markdown syntax. The kernel's own
// bluemonday-based sanitizer (kernel/httpapi/rendition.go, D-11) is the
// second, independent layer of defense (T-02-01) that now runs
// kernel-side rather than in this plugin — do NOT enable an "unsafe"
// HTML-passthrough extension here to "make links work"; goldmark's
// safe-by-default behavior is still this plugin's own first layer.
var mdConverter = goldmark.New()

// RenderMarkdown converts markdown to an HTML fragment via goldmark. The
// returned bytes are UNSANITIZED — D-11 moved sanitization to the kernel's
// rendition boundary (kernel/httpapi/rendition.go), which sanitizes and
// wraps every text/html rendition after this plugin returns it. This
// plugin still owns the readability decision goldmark's conversion makes
// (Phase 3's "the producing plugin decides readability" rule carries
// forward unchanged) — it no longer owns sanitization or presentation.
func RenderMarkdown(markdown []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := mdConverter.Convert(markdown, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
