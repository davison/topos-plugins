package main

import (
	"path/filepath"
	"strings"
)

// previewKind is the closed set of preview shapes D-04 (12-CONTEXT.md)
// describes: raw bytes+mime for PDFs and inline-renderable images,
// kernel-rendered markdown, plain text, and metadata-plus-deep-link-only
// for everything else the default allowlist still wants in the stream
// (office formats, and image formats the kernel cannot render inline).
type previewKind int

const (
	// previewKindBytes is the raw-bytes-plus-mime shape (PDF, inline images).
	previewKindBytes previewKind = iota
	// previewKindMarkdown is goldmark-rendered HTML (CONTENT_SHAPE_MARKDOWN_HTML).
	previewKindMarkdown
	// previewKindPlainText is text/plain, served as text.
	previewKindPlainText
	// previewKindMetadataOnly declares no preview at all — office formats,
	// and image extensions the kernel's allowedRenditionTypes cannot serve.
	previewKindMetadataOnly
)

// classification is classify's result for one extension: which preview
// kind, and — only for previewKindBytes — the exact mime string Fetch must
// declare. Every other kind carries mime == "": previewKindMetadataOnly
// must never guess a mime type (12-02-PLAN.md Task 2 behavior), and
// previewKindMarkdown/previewKindPlainText each have one fixed mime
// (text/html, text/plain) decided by fetch.go, not by this table.
type classification struct {
	kind previewKind
	mime string
}

// extensionTable is the closed, hand-rolled extension -> classification
// map this plugin's default document-ish allowlist is built from (D-03).
// Deliberately not the stdlib "mime" package's extension-lookup helper:
// that helper's behavior varies with the host's /etc/mime.types, and this
// table must read identically on the operator's desktop, in CI and on a
// fresh install (12-RESEARCH.md Anti-Patterns, 12-02-PLAN.md Task 2
// action text).
//
// Every image extension claiming previewKindBytes here is already present
// in kernel/httpapi/item.go's allowedRenditionTypes; svg/bmp/tif/tiff/heic
// are real images the kernel cannot render inline, so they stay in the
// allowlist (present, so they appear in the stream) as metadata-only
// rather than being dropped from the allowlist entirely.
var extensionTable = map[string]classification{
	".pdf": {kind: previewKindBytes, mime: "application/pdf"},

	".png":  {kind: previewKindBytes, mime: "image/png"},
	".jpg":  {kind: previewKindBytes, mime: "image/jpeg"},
	".jpeg": {kind: previewKindBytes, mime: "image/jpeg"},
	".gif":  {kind: previewKindBytes, mime: "image/gif"},
	".webp": {kind: previewKindBytes, mime: "image/webp"},

	".md":       {kind: previewKindMarkdown},
	".markdown": {kind: previewKindMarkdown},

	".txt":  {kind: previewKindPlainText},
	".text": {kind: previewKindPlainText},
	".log":  {kind: previewKindPlainText},
	".csv":  {kind: previewKindPlainText},

	".doc":  {kind: previewKindMetadataOnly},
	".docx": {kind: previewKindMetadataOnly},
	".xls":  {kind: previewKindMetadataOnly},
	".xlsx": {kind: previewKindMetadataOnly},
	".ppt":  {kind: previewKindMetadataOnly},
	".pptx": {kind: previewKindMetadataOnly},
	".odt":  {kind: previewKindMetadataOnly},
	".ods":  {kind: previewKindMetadataOnly},
	".odp":  {kind: previewKindMetadataOnly},
	".rtf":  {kind: previewKindMetadataOnly},

	// Real images the kernel's allowedRenditionTypes cannot serve inline —
	// present so they appear in the stream, metadata-only so Fetch never
	// tries to hand the content route bytes it would refuse.
	".svg":  {kind: previewKindMetadataOnly},
	".bmp":  {kind: previewKindMetadataOnly},
	".tif":  {kind: previewKindMetadataOnly},
	".tiff": {kind: previewKindMetadataOnly},
	".heic": {kind: previewKindMetadataOnly},
}

// classify returns name's classification by its lowercased extension, and
// ok=false when the extension is outside extensionTable entirely — a
// distinct outcome from previewKindMetadataOnly: ok decides inclusion in
// the default allowlist, while the returned kind (when ok is true) decides
// preview shape. Matching is case-insensitive (REPORT.PDF == report.pdf).
func classify(name string) (classification, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	c, ok := extensionTable[ext]
	return c, ok
}
