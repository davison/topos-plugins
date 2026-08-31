package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// --- Fetch (D-04, 12-02-PLAN.md Task 3): per-preview-kind dispatch ---
// Written before fetch.go, against a temp corpus directory.

func writeFixture(t *testing.T, root, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), body, 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

func fetchFull(t *testing.T, p *SourcePlugin, sourceID string) *toposv1.FetchResponse {
	t.Helper()
	resp, err := p.Fetch(t.Context(), &toposv1.FetchRequest{
		SourceId: sourceID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch(%q): %v", sourceID, err)
	}
	return resp
}

func TestFetch_PDFFetchesAvailableWithBytesAndMime(t *testing.T) {
	root := t.TempDir()
	body := []byte("%PDF-1.4 fixture body")
	writeFixture(t, root, "invoice.pdf", body)
	p := NewSourcePlugin(root, nil, false)

	resp := fetchFull(t, p, "invoice.pdf")
	if !resp.GetAvailable() {
		t.Fatal("expected available true")
	}
	if resp.GetMimeType() != "application/pdf" {
		t.Errorf("expected mime application/pdf, got %q", resp.GetMimeType())
	}
	if resp.GetSizeBytes() != int64(len(body)) {
		t.Errorf("expected size %d, got %d", len(body), resp.GetSizeBytes())
	}
	if string(resp.GetData()) != string(body) {
		t.Errorf("expected data %q, got %q", body, resp.GetData())
	}
}

func TestFetch_PNGFetchesAvailableWithImageMime(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "photo.png", []byte("fake png bytes"))
	p := NewSourcePlugin(root, nil, false)

	resp := fetchFull(t, p, "photo.png")
	if !resp.GetAvailable() {
		t.Fatal("expected available true")
	}
	if resp.GetMimeType() != "image/png" {
		t.Errorf("expected mime image/png, got %q", resp.GetMimeType())
	}
}

func TestFetch_MarkdownFetchesAsRenderedHTMLWithMarkdownShape(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "notes.md", []byte("# Title\n\nbody\n"))
	p := NewSourcePlugin(root, nil, false)

	resp := fetchFull(t, p, "notes.md")
	if !resp.GetAvailable() {
		t.Fatal("expected available true")
	}
	if resp.GetMimeType() != "text/html" {
		t.Errorf("expected mime text/html, got %q", resp.GetMimeType())
	}
	if resp.GetContentShape() != toposv1.ContentShape_CONTENT_SHAPE_MARKDOWN_HTML {
		t.Errorf("expected CONTENT_SHAPE_MARKDOWN_HTML, got %v", resp.GetContentShape())
	}
	if !strings.Contains(string(resp.GetData()), "<h1") {
		t.Errorf("expected rendered HTML bytes, got %q", resp.GetData())
	}
}

func TestFetch_PlainTextFetchesWithTextPopulated(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "notes.txt", []byte("hello plain text"))
	p := NewSourcePlugin(root, nil, false)

	resp := fetchFull(t, p, "notes.txt")
	if !resp.GetAvailable() {
		t.Fatal("expected available true")
	}
	if resp.GetMimeType() != "text/plain" {
		t.Errorf("expected mime text/plain, got %q", resp.GetMimeType())
	}
	if resp.GetText() != "hello plain text" {
		t.Errorf("expected text %q, got %q", "hello plain text", resp.GetText())
	}
}

func TestFetch_PlainTextLongerThanBoundIsHonestlyTruncated(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("a", maxPlainTextSize+100)
	writeFixture(t, root, "big.txt", []byte(body))
	p := NewSourcePlugin(root, nil, false)

	resp := fetchFull(t, p, "big.txt")
	if !resp.GetAvailable() {
		t.Fatal("expected available true")
	}
	if !strings.HasSuffix(resp.GetText(), plainTextTruncationNotice) {
		t.Fatalf("expected the truncation notice as the text's final content, got suffix %q",
			resp.GetText()[max(0, len(resp.GetText())-80):])
	}
}

func TestFetch_DocxFetchesUnavailableWithNoMimeOrBytes(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "report.docx", []byte("not really an office doc"))
	p := NewSourcePlugin(root, nil, false)

	resp := fetchFull(t, p, "report.docx")
	if resp.GetAvailable() {
		t.Fatal("expected available false for a .docx file")
	}
	if resp.GetUnavailableReason() == "" {
		t.Error("expected a named unavailable reason")
	}
	if resp.GetMimeType() != "" {
		t.Errorf("expected no mime type, got %q", resp.GetMimeType())
	}
	if len(resp.GetData()) != 0 {
		t.Errorf("expected no bytes, got %d", len(resp.GetData()))
	}
}

func TestFetch_SVGFetchesUnavailableWithNamedReason(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "diagram.svg", []byte("<svg></svg>"))
	p := NewSourcePlugin(root, nil, false)

	resp := fetchFull(t, p, "diagram.svg")
	if resp.GetAvailable() {
		t.Fatal("expected available false for a .svg file")
	}
	if resp.GetUnavailableReason() == "" {
		t.Error("expected a named unavailable reason")
	}
}

func TestFetch_ThumbnailAlwaysUnavailableForEveryKind(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "invoice.pdf", []byte("%PDF-1.4"))
	writeFixture(t, root, "notes.md", []byte("# hi"))
	writeFixture(t, root, "notes.txt", []byte("hi"))
	writeFixture(t, root, "report.docx", []byte("doc"))
	p := NewSourcePlugin(root, nil, false)

	for _, sourceID := range []string{"invoice.pdf", "notes.md", "notes.txt", "report.docx"} {
		resp, err := p.Fetch(t.Context(), &toposv1.FetchRequest{
			SourceId: sourceID,
			Variant:  toposv1.ContentVariant_CONTENT_VARIANT_THUMBNAIL,
		})
		if err != nil {
			t.Fatalf("Fetch THUMBNAIL(%q): %v", sourceID, err)
		}
		if resp.GetAvailable() {
			t.Errorf("%s: expected THUMBNAIL to be unavailable", sourceID)
		}
	}
}

func TestFetch_MissingFileIsNotFoundGRPCError(t *testing.T) {
	root := t.TempDir()
	p := NewSourcePlugin(root, nil, false)

	_, err := p.Fetch(t.Context(), &toposv1.FetchRequest{
		SourceId: "does-not-exist.pdf",
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected codes.NotFound, got %v", err)
	}
}

func TestFetch_OversizeFileIsUnavailableWithSizeReasonAndBytesNeverRead(t *testing.T) {
	root := t.TempDir()
	// A sparse file reporting a size over the cap without actually
	// allocating maxByteRenditionSize+1 bytes on disk — proves the cap
	// check happens before any read, not after a full read.
	f, err := os.Create(filepath.Join(root, "huge.pdf"))
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if err := f.Truncate(maxByteRenditionSize + 1); err != nil {
		t.Fatalf("truncate fixture: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	p := NewSourcePlugin(root, nil, false)

	resp := fetchFull(t, p, "huge.pdf")
	if resp.GetAvailable() {
		t.Fatal("expected available false for an oversize file")
	}
	if resp.GetUnavailableReason() == "" {
		t.Error("expected a named reason citing the size limit")
	}
	if len(resp.GetData()) != 0 {
		t.Error("expected no bytes to have been read for an oversize file")
	}
}

// TestFetch_SymlinkSwappedAfterIndexingIsRefusedBeforeAnyBytesAreServed
// proves CR-02 is closed at the byte-serving site: a file indexed as
// legitimate and then swapped on disk for a symlink pointing outside the
// configured root is refused by Fetch, and none of the outside target's
// bytes ever appear in the outcome — the gap being closed is a disclosure,
// so the assertion checks for the absence of the secret bytes, not merely
// the error code.
func TestFetch_SymlinkSwappedAfterIndexingIsRefusedBeforeAnyBytesAreServed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secretBody := []byte("this must never be served")
	writeFixture(t, outside, "secret.txt", secretBody)
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "notes.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	p := NewSourcePlugin(root, nil, false)

	resp, err := p.Fetch(t.Context(), &toposv1.FetchRequest{
		SourceId: "notes.md",
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatal("expected an error for a post-index symlink swap outside the configured root")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected codes.InvalidArgument, got %v", err)
	}
	if resp != nil {
		t.Fatalf("expected a nil response, got %+v", resp)
	}
	if strings.Contains(err.Error(), string(secretBody)) {
		t.Fatalf("expected the secret body to appear nowhere in the outcome, got error %q", err.Error())
	}
}

// TestFetch_IncludeGlobAdmittedUnknownExtensionIsMetadataOnlyNotNotFound
// proves the gap 12-VERIFICATION.md recorded is closed: a file admitted to
// scope only because include_glob widened past the default extension
// allowlist previews honestly as metadata-only, on BOTH CONTENT_VARIANT_FULL
// (the kernel's detail route) and CONTENT_VARIANT_PREVIEW (the kernel's
// content route) — the reported false 404 appeared on both.
func TestFetch_IncludeGlobAdmittedUnknownExtensionIsMetadataOnlyNotNotFound(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "archive.zip", []byte("not a document"))
	p := NewSourcePlugin(root, map[string]string{"include_glob": "**/*.zip"}, false)

	full := fetchFull(t, p, "archive.zip")
	if full.GetAvailable() {
		t.Fatal("expected available false for an unrecognized extension admitted only by include_glob")
	}
	if full.GetUnavailableReason() != metadataOnlyReason {
		t.Errorf("expected unavailable_reason %q, got %q", metadataOnlyReason, full.GetUnavailableReason())
	}
	if full.GetMimeType() != "" {
		t.Errorf("expected no mime type, got %q", full.GetMimeType())
	}
	if len(full.GetData()) != 0 {
		t.Errorf("expected no bytes, got %d", len(full.GetData()))
	}

	preview, err := p.Fetch(t.Context(), &toposv1.FetchRequest{
		SourceId: "archive.zip",
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW,
	})
	if err != nil {
		t.Fatalf("Fetch PREVIEW: %v", err)
	}
	if preview.GetAvailable() {
		t.Fatal("expected available false for PREVIEW too")
	}
	if preview.GetUnavailableReason() != metadataOnlyReason {
		t.Errorf("expected unavailable_reason %q, got %q", metadataOnlyReason, preview.GetUnavailableReason())
	}
}

// TestFetch_ExcludedByGlobIsStillNotFound proves the honesty fix never
// widens what is served: a file matching exclude_glob is outside this
// instance's scope and stays codes.NotFound even though it is on disk.
func TestFetch_ExcludedByGlobIsStillNotFound(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "invoice.pdf", []byte("%PDF-1.4"))
	p := NewSourcePlugin(root, map[string]string{"exclude_glob": "**/*.pdf"}, false)

	_, err := p.Fetch(t.Context(), &toposv1.FetchRequest{
		SourceId: "invoice.pdf",
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatal("expected an error for a file excluded by exclude_glob")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected codes.NotFound, got %v", err)
	}
}

// TestFetch_UnknownExtensionWithNoIncludeGlobIsStillNotFound proves today's
// behavior for a genuinely out-of-scope extension is unchanged: with no
// include_glob at all, an unrecognized extension stays codes.NotFound,
// matching TestMatch_ExtensionOutsideDefaultAllowlistIsIgnored's verdict.
func TestFetch_UnknownExtensionWithNoIncludeGlobIsStillNotFound(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "archive.zip", []byte("not a document"))
	p := NewSourcePlugin(root, nil, false)

	_, err := p.Fetch(t.Context(), &toposv1.FetchRequest{
		SourceId: "archive.zip",
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatal("expected an error for an unrecognized extension with no include_glob")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected codes.NotFound, got %v", err)
	}
}

// TestFetch_MalformedGlobPatternSurfacesTheOffendingPattern proves a
// malformed operator glob names itself in the error rather than being
// silently swallowed, and fails with codes.Unavailable — the same class of
// answer Match already gives for the identical pattern.
func TestFetch_MalformedGlobPatternSurfacesTheOffendingPattern(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "invoice.pdf", []byte("%PDF-1.4"))
	p := NewSourcePlugin(root, map[string]string{"include_glob": "[unterminated"}, false)

	_, err := p.Fetch(t.Context(), &toposv1.FetchRequest{
		SourceId: "invoice.pdf",
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatal("expected an error for a malformed include_glob pattern")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected codes.Unavailable, got %v", err)
	}
	if !strings.Contains(st.Message(), "[unterminated") {
		t.Errorf("expected the offending pattern to appear in the error message, got %q", st.Message())
	}
}

// TestFetch_SymlinkedRootStillServesAnInRootFile proves a plugin rooted at a
// symlinked directory (the common ~/Documents -> ~/dotfiles/Documents
// dotfile-manager pattern, WR-01) still fetches successfully — reading
// through the resolved real path (12-07-PLAN.md Task 2, WR-02) is a valid
// I/O target and does not break this legitimate pattern.
func TestFetch_SymlinkedRootStillServesAnInRootFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "notes.md"), []byte("# hi\n\nbody\n"), 0o644); err != nil {
		t.Fatalf("write notes.md: %v", err)
	}
	linkRoot := filepath.Join(tmp, "linkroot")
	if err := os.Symlink(real, linkRoot); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	p := NewSourcePlugin(linkRoot, nil, false)

	resp := fetchFull(t, p, "notes.md")
	if !resp.GetAvailable() {
		t.Fatal("expected available true for a file under a symlinked root")
	}
	if resp.GetContentShape() != toposv1.ContentShape_CONTENT_SHAPE_MARKDOWN_HTML {
		t.Errorf("expected CONTENT_SHAPE_MARKDOWN_HTML, got %v", resp.GetContentShape())
	}
	if !strings.Contains(string(resp.GetData()), "<h1") {
		t.Errorf("expected rendered HTML bytes, got %q", resp.GetData())
	}
}

func TestFetch_SourceIDEscapingTheRootIsRefusedBeforeAnyFileIsOpened(t *testing.T) {
	root := t.TempDir()
	p := NewSourcePlugin(root, nil, false)

	_, err := p.Fetch(t.Context(), &toposv1.FetchRequest{
		SourceId: "../../etc/passwd",
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatal("expected an error for a source_id escaping the configured root")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected codes.InvalidArgument, got %v", err)
	}
}
