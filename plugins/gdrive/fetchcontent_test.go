// Package main's fetchcontent_test.go is this plan's end-to-end proof: one
// text/plain document in a fake Drive folder, visible via Match with a
// live, bounded preview, and openable via Fetch with content downloaded
// fresh from Drive during that call. Reuses syncengine_test.go's and
// drivefake_test.go's fixtures/helpers (same package) rather than
// duplicating them. Follows this repository's own idiom:
// Test<Thing>_<BehaviorInPlainEnglish> names, plain t.Errorf/t.Fatalf
// assertions, no assertion library.
package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// contentFixtureRecorder captures every request this test's fixture
// handler saw to the one fixture file's own alt=media download endpoint —
// specifically, the Range request header value present on each (""
// meaning absent) — so a test can assert exactly which calls carried a
// Range header and which did not. drivefake_test.go's driveRecorder counts
// by URL path only, which would conflate a metadata files.get and a media
// files.get.Download() to the same "/files/{id}" path; this recorder
// distinguishes by hooking the media branch of the fixture handler
// directly, not by inspecting the request afterward.
type contentFixtureRecorder struct {
	mu          sync.Mutex
	mediaRanges []string
}

func (r *contentFixtureRecorder) recordMedia(rangeHeader string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mediaRanges = append(r.mediaRanges, rangeHeader)
}

func (r *contentFixtureRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.mediaRanges)
}

func (r *contentFixtureRecorder) rangeAt(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i < 0 || i >= len(r.mediaRanges) {
		return ""
	}
	return r.mediaRanges[i]
}

// newContentFixtureHandler serves the same four Drive REST endpoints
// syncengine_test.go's newSingleFileFixtureHandler does, plus a fifth:
// fx.fileID's own alt=media download, which returns fileBody and records
// the request's Range header (or its absence) into rec.
func newContentFixtureHandler(t *testing.T, fx driveFixture, mimeType string, fileBody []byte, rec *contentFixtureRecorder) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/changes/startPageToken":
			writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-1"})
		case r.URL.Path == "/changes":
			writeDriveJSON(t, w, &drive.ChangeList{NewStartPageToken: "start-token-1"})
		case r.URL.Path == "/files/"+fx.rootID:
			writeDriveJSON(t, w, &drive.File{Id: fx.rootID, Name: fx.rootName, MimeType: folderMimeType})
		case r.URL.Path == "/files/"+fx.fileID:
			// The only caller of this path is files.get's Download()
			// (alt=media) — rangePreview and fetchRegularFile both use
			// it, never Do() (alt=json), which this fixture never needs
			// to serve for the file itself.
			rec.recordMedia(r.Header.Get("Range"))
			w.Header().Set("Content-Type", mimeType)
			_, _ = w.Write(fileBody)
		case r.URL.Path == "/files":
			parent := parentFromQuery(r.URL.Query().Get("q"))
			var files []*drive.File
			if parent == fx.rootID && fx.fileID != "" {
				files = []*drive.File{{
					Id:           fx.fileID,
					Name:         fx.fileName,
					MimeType:     mimeType,
					ModifiedTime: "2026-08-17T00:00:00Z",
					WebViewLink:  "https://drive.google.com/file/d/" + fx.fileID + "/view",
					Size:         fx.fileSize,
				}}
			}
			writeDriveJSON(t, w, &drive.FileList{Files: files})
		default:
			http.NotFound(w, r)
		}
	}
}

// TestMatchAndFetch_TextFileEndToEnd is this plan's tracer proof: a
// text/plain document in the configured folder appears in Match with a
// live, bounded preview that is a prefix of the fixture bytes, and
// opening it via Fetch with CONTENT_VARIANT_FULL returns the whole
// fixture body, downloaded fresh from Drive during that call — the
// preview download carried a Range header, the Fetch download did not.
func TestMatchAndFetch_TextFileEndToEnd(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-1", rootName: "Team Docs", fileID: "file-1", fileName: "note.txt"}
	fileBody := []byte("Hello from a fixture text file used to prove Match's preview and Fetch's full content both come from a live Drive call.")
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "text/plain", fileBody, rec))

	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	matchResp, err := p.Match(context.Background(), buildMatchRequest(fx.rootName))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	items := matchResp.GetItems()
	if len(items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(items))
	}
	preview := items[0].GetPreview()
	if preview == "" {
		t.Fatal("Preview is empty, want a non-empty bounded preview")
	}
	if !strings.HasPrefix(string(fileBody), preview) {
		t.Errorf("Preview %q is not a prefix of the fixture bytes %q", preview, fileBody)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("media download count after Match = %d, want 1 (exactly one preview fetch)", got)
	}
	if rec.rangeAt(0) == "" {
		t.Error("the preview's media download carried no Range header, want one")
	}

	fetchResp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: fx.fileID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !fetchResp.GetAvailable() {
		t.Fatal("Fetch Available = false, want true")
	}
	if fetchResp.GetText() != string(fileBody) {
		t.Errorf("Fetch Text = %q, want the full fixture body %q", fetchResp.GetText(), fileBody)
	}
	if got := rec.count(); got != 2 {
		t.Fatalf("media download count after Fetch = %d, want 2 (one preview fetch, one Fetch download)", got)
	}
	if rangeHeader := rec.rangeAt(1); rangeHeader != "" {
		t.Errorf("Fetch's own media download carried a Range header (%q), want none", rangeHeader)
	}
}

// TestFetch_UnknownSourceIdReturnsNotFound proves a source_id absent from
// this instance's own persisted tree returns codes.NotFound — the tree is
// the only access-control boundary this plugin has (T-04-02).
func TestFetch_UnknownSourceIdReturnsNotFound(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-2", rootName: "Team Docs", fileID: "file-2", fileName: "note.txt"}
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "text/plain", []byte("body"), rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	_, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: "never-existed",
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("Fetch error = %v, want a gRPC status with code NotFound", err)
	}
}

// TestFetch_UnspecifiedVariantReturnsInvalidArgument proves
// CONTENT_VARIANT_UNSPECIFIED is rejected with codes.InvalidArgument and
// that no media download is ever issued for that call.
func TestFetch_UnspecifiedVariantReturnsInvalidArgument(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-3", rootName: "Team Docs", fileID: "file-3", fileName: "note.txt"}
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "text/plain", []byte("body"), rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	_, err := p.Fetch(context.Background(), &toposv1.FetchRequest{SourceId: fx.fileID})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("Fetch error = %v, want a gRPC status with code InvalidArgument", err)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("media download count after an unspecified-variant Fetch = %d, want 0", got)
	}
}

// TestFetch_PreviewAndThumbnailReturnUnavailableNoRendition proves both
// CONTENT_VARIANT_PREVIEW and CONTENT_VARIANT_THUMBNAIL return a normal
// available:false outcome — never a gRPC error — carrying
// unavailableNoRendition, and issue no Drive media call.
func TestFetch_PreviewAndThumbnailReturnUnavailableNoRendition(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-4", rootName: "Team Docs", fileID: "file-4", fileName: "note.txt"}
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "text/plain", []byte("body"), rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	for _, variant := range []toposv1.ContentVariant{
		toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW,
		toposv1.ContentVariant_CONTENT_VARIANT_THUMBNAIL,
	} {
		resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{SourceId: fx.fileID, Variant: variant})
		if err != nil {
			t.Fatalf("Fetch(%v): %v", variant, err)
		}
		if resp.GetAvailable() {
			t.Errorf("Fetch(%v) Available = true, want false", variant)
		}
		if resp.GetUnavailableReason() != unavailableNoRendition {
			t.Errorf("Fetch(%v) UnavailableReason = %q, want %q", variant, resp.GetUnavailableReason(), unavailableNoRendition)
		}
	}
	if got := rec.count(); got != 0 {
		t.Errorf("media download count after PREVIEW/THUMBNAIL Fetch calls = %d, want 0", got)
	}
}

// TestBuildPreview_NonTextShapedMimeTypeYieldsEmptyPreviewNoDriveCall
// proves a binary MIME type (outside GAP-15's text-shaped allowlist)
// yields an empty preview and issues zero Drive calls for that file.
func TestBuildPreview_NonTextShapedMimeTypeYieldsEmptyPreviewNoDriveCall(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-5", rootName: "Team Docs", fileID: "file-5", fileName: "photo.png"}
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "image/png", []byte{0x89, 0x50, 0x4e, 0x47}, rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	matchResp, err := p.Match(context.Background(), buildMatchRequest(fx.rootName))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	items := matchResp.GetItems()
	if len(items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(items))
	}
	if preview := items[0].GetPreview(); preview != "" {
		t.Errorf("Preview = %q, want empty for a non-text-shaped MIME type", preview)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("media download count for a non-text-shaped file = %d, want 0", got)
	}
}

// --- Task 3: Fetch's full contract surface for regular files ---

// TestFetch_FullVariantOnTextShapedFileReturnsFullTextAndFiveProvenanceKeys
// proves the behavior list's first bullet: FULL on a text-shaped regular
// file returns available:true, the whole live body as Text, the correct
// SizeBytes, and exactly the five documented provenance keys.
func TestFetch_FullVariantOnTextShapedFileReturnsFullTextAndFiveProvenanceKeys(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-full-text", rootName: "Team Docs", fileID: "file-full-text", fileName: "note.txt"}
	fileBody := []byte("the whole live body, fetched fresh from Drive during this one Fetch call.")
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "text/plain", fileBody, rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	if _, err := p.Match(context.Background(), buildMatchRequest(fx.rootName)); err != nil {
		t.Fatalf("Match: %v", err)
	}

	resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: fx.fileID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !resp.GetAvailable() {
		t.Fatal("Available = false, want true")
	}
	if resp.GetText() != string(fileBody) {
		t.Errorf("Text = %q, want the full fixture body %q", resp.GetText(), fileBody)
	}
	if got, want := resp.GetSizeBytes(), int64(len(fileBody)); got != want {
		t.Errorf("SizeBytes = %d, want %d", got, want)
	}
	wantKeys := []string{"source_type", "source_system", "source_id", "plugin", "contract_version"}
	prov := resp.GetProvenance()
	if len(prov) != len(wantKeys) {
		t.Fatalf("len(Provenance) = %d, want %d (%v)", len(prov), len(wantKeys), prov)
	}
	for _, k := range wantKeys {
		if _, ok := prov[k]; !ok {
			t.Errorf("Provenance missing key %q, got %v", k, prov)
		}
	}
}

// TestFetch_FullVariantOnBinaryFileReturnsAvailableWithEmptyTextAndRealSize
// proves a non-text-shaped regular file still returns available:true with
// the real downloaded byte count, but an empty Text — this plugin returns
// no binary rendition for a regular file (MimeType/Data stay unset).
func TestFetch_FullVariantOnBinaryFileReturnsAvailableWithEmptyTextAndRealSize(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-full-binary", rootName: "Team Docs", fileID: "file-full-binary", fileName: "photo.png"}
	fileBody := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "image/png", fileBody, rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	if _, err := p.Match(context.Background(), buildMatchRequest(fx.rootName)); err != nil {
		t.Fatalf("Match: %v", err)
	}

	resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: fx.fileID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !resp.GetAvailable() {
		t.Fatal("Available = false, want true")
	}
	if resp.GetText() != "" {
		t.Errorf("Text = %q, want empty for a non-text-shaped MIME type", resp.GetText())
	}
	if got, want := resp.GetSizeBytes(), int64(len(fileBody)); got != want {
		t.Errorf("SizeBytes = %d, want %d", got, want)
	}
}

// TestFetch_FullVariantOnZeroByteFileReturnsAvailableTrueNotFalse proves the
// empty-input edge case: available:true, empty Text, SizeBytes 0 — never
// available:false, which is reserved for CONT-03's named causes.
func TestFetch_FullVariantOnZeroByteFileReturnsAvailableTrueNotFalse(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-zero-byte", rootName: "Team Docs", fileID: "file-zero-byte", fileName: "empty.txt"}
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "text/plain", nil, rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	if _, err := p.Match(context.Background(), buildMatchRequest(fx.rootName)); err != nil {
		t.Fatalf("Match: %v", err)
	}

	resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: fx.fileID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !resp.GetAvailable() {
		t.Fatal("Available = false, want true for a zero-byte file")
	}
	if resp.GetText() != "" {
		t.Errorf("Text = %q, want empty", resp.GetText())
	}
	if resp.GetSizeBytes() != 0 {
		t.Errorf("SizeBytes = %d, want 0", resp.GetSizeBytes())
	}
}

// TestFetch_OversizedFileReturnsUnavailableTooLargeToReturnNoDownload proves
// the maxFetchBytes pre-flight cap: a node whose recorded Size exceeds the
// cap returns available:false with unavailableTooLargeToReturn and issues
// no media download at all.
func TestFetch_OversizedFileReturnsUnavailableTooLargeToReturnNoDownload(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{
		rootID: "root-oversized", rootName: "Team Docs",
		fileID: "file-oversized", fileName: "huge.txt",
		fileSize: maxFetchBytes + 1,
	}
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "text/plain", []byte("irrelevant, must never be downloaded"), rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	// Match itself would also attempt a preview fetch for this text-shaped
	// file; that is preview.go's own concern (bounded to previewRangeBytes,
	// unaffected by driveNode.Size), not this test's. Seed the tree
	// directly instead of calling Match, so this test's own recorder count
	// assertion is solely about the Fetch call under test.
	statePath := dataFilePath(isolatedDir, syncStateFileName)
	seed := &syncState{
		RootID:      fx.rootID,
		RootName:    fx.rootName,
		ChangeToken: "start-token-1",
		Tree: map[string]*driveNode{
			fx.fileID: {
				Name: fx.fileName, MimeType: "text/plain", ParentID: fx.rootID,
				ModifiedTime: "2026-08-17T00:00:00Z",
				WebViewLink:  "https://drive.google.com/file/d/" + fx.fileID + "/view",
				Size:         fx.fileSize,
			},
		},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: fx.fileID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.GetAvailable() {
		t.Error("Available = true, want false for an oversized file")
	}
	if resp.GetUnavailableReason() != unavailableTooLargeToReturn {
		t.Errorf("UnavailableReason = %q, want %q", resp.GetUnavailableReason(), unavailableTooLargeToReturn)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("media download count for an oversized file = %d, want 0 (no download should ever be attempted)", got)
	}
}

// newFailingDownloadFixtureHandler serves the same fixture shape
// newContentFixtureHandler does, except fx.fileID's own alt=media download
// always fails with HTTP 500 — proving a genuine Drive transport failure
// surfaces as codes.Unavailable, never as available:false.
func newFailingDownloadFixtureHandler(t *testing.T, fx driveFixture, mimeType string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/changes/startPageToken":
			writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-1"})
		case r.URL.Path == "/changes":
			writeDriveJSON(t, w, &drive.ChangeList{NewStartPageToken: "start-token-1"})
		case r.URL.Path == "/files/"+fx.rootID:
			writeDriveJSON(t, w, &drive.File{Id: fx.rootID, Name: fx.rootName, MimeType: folderMimeType})
		case r.URL.Path == "/files/"+fx.fileID:
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/files":
			parent := parentFromQuery(r.URL.Query().Get("q"))
			var files []*drive.File
			if parent == fx.rootID && fx.fileID != "" {
				files = []*drive.File{{
					Id:           fx.fileID,
					Name:         fx.fileName,
					MimeType:     mimeType,
					ModifiedTime: "2026-08-17T00:00:00Z",
					WebViewLink:  "https://drive.google.com/file/d/" + fx.fileID + "/view",
				}}
			}
			writeDriveJSON(t, w, &drive.FileList{Files: files})
		default:
			http.NotFound(w, r)
		}
	}
}

// TestFetch_TransportFailureDuringDownloadReturnsUnavailableStatusNeverAvailableFalse
// proves a genuine Drive transport failure during the download itself
// returns a gRPC codes.Unavailable status — never a normal
// available:false FetchResponse, which would misrepresent a real failure
// as an expected outcome.
func TestFetch_TransportFailureDuringDownloadReturnsUnavailableStatusNeverAvailableFalse(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-transport-fail", rootName: "Team Docs", fileID: "file-transport-fail", fileName: "note.txt"}
	svc := newFakeDriveService(t, newFailingDownloadFixtureHandler(t, fx, "text/plain"))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	statePath := dataFilePath(isolatedDir, syncStateFileName)
	seed := &syncState{
		RootID: fx.rootID, RootName: fx.rootName, ChangeToken: "start-token-1",
		Tree: map[string]*driveNode{
			fx.fileID: {
				Name: fx.fileName, MimeType: "text/plain", ParentID: fx.rootID,
				ModifiedTime: "2026-08-17T00:00:00Z",
				WebViewLink:  "https://drive.google.com/file/d/" + fx.fileID + "/view",
			},
		},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: fx.fileID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err == nil {
		t.Fatalf("Fetch: want a non-nil error, got a response (Available=%v)", resp.GetAvailable())
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("Fetch error = %v, want a gRPC status with code Unavailable", err)
	}
}

// TestFetch_TwoConsecutiveCallsEachIssueTheirOwnLiveDownload proves nothing
// is memoized between Fetch calls: two consecutive FULL requests for the
// same source_id each cause their own media download.
func TestFetch_TwoConsecutiveCallsEachIssueTheirOwnLiveDownload(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-two-calls", rootName: "Team Docs", fileID: "file-two-calls", fileName: "note.txt"}
	fileBody := []byte("body fetched fresh, twice, from Drive.")
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "text/plain", fileBody, rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	statePath := dataFilePath(isolatedDir, syncStateFileName)
	seed := &syncState{
		RootID: fx.rootID, RootName: fx.rootName, ChangeToken: "start-token-1",
		Tree: map[string]*driveNode{
			fx.fileID: {
				Name: fx.fileName, MimeType: "text/plain", ParentID: fx.rootID,
				ModifiedTime: "2026-08-17T00:00:00Z",
				WebViewLink:  "https://drive.google.com/file/d/" + fx.fileID + "/view",
			},
		},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	req := &toposv1.FetchRequest{SourceId: fx.fileID, Variant: toposv1.ContentVariant_CONTENT_VARIANT_FULL}
	if _, err := p.Fetch(context.Background(), req); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("media download count after the first Fetch = %d, want 1", got)
	}
	if _, err := p.Fetch(context.Background(), req); err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if got := rec.count(); got != 2 {
		t.Fatalf("media download count after the second Fetch = %d, want 2 — nothing may be cached or memoized between calls", got)
	}
}

// TestFullSyncPlusFetch_SyncStateFileNeverContainsTheFetchedDocumentBody is
// the CONT-04 behavioral gate: after a full sync and a Fetch, the persisted
// syncstate.json bytes contain no substring of the fetched document body,
// and the plugin's data directory holds no file beyond token.json and
// syncstate.json.
func TestFullSyncPlusFetch_SyncStateFileNeverContainsTheFetchedDocumentBody(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	const sentinel = "sentinel-fetched-body-must-never-reach-syncstate-json"
	fx := driveFixture{rootID: "root-sentinel", rootName: "Team Docs", fileID: "file-sentinel", fileName: "note.txt"}
	fileBody := []byte("distinctive fixture content wrapping the marker: " + sentinel)
	rec := &contentFixtureRecorder{}
	svc := newFakeDriveService(t, newContentFixtureHandler(t, fx, "text/plain", fileBody, rec))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	if _, err := p.Match(context.Background(), buildMatchRequest(fx.rootName)); err != nil {
		t.Fatalf("Match: %v", err)
	}
	fetchResp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{
		SourceId: fx.fileID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(fetchResp.GetText(), sentinel) {
		t.Fatalf("Fetch Text %q does not contain the sentinel — the test fixture itself is broken", fetchResp.GetText())
	}

	stateBytes, err := os.ReadFile(dataFilePath(isolatedDir, syncStateFileName))
	if err != nil {
		t.Fatalf("ReadFile(syncstate.json): %v", err)
	}
	if strings.Contains(string(stateBytes), sentinel) {
		t.Error("syncstate.json contains the fetched document's own sentinel content — fetched bytes leaked into the persisted sync store")
	}

	dataDir := filepath.Join(isolatedDir, dataDirName)
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dataDir, err)
	}
	for _, e := range entries {
		if e.Name() != tokenFileName && e.Name() != syncStateFileName {
			t.Errorf("unexpected file %q in the plugin data directory, want only %q and %q", e.Name(), tokenFileName, syncStateFileName)
		}
	}
}

// contentBearingFieldSubstrings is the exact forbidden-substring vocabulary
// TestDriveNodeStruct_NeverDeclaresAContentBearingField checks every
// driveNode field name and json tag against, case-insensitively: content,
// preview, rendition, body, text, or data. Widening this list to silence a
// real finding, rather than removing the offending field from driveNode,
// is exactly the erosion this scanner exists to make visible.
var contentBearingFieldSubstrings = []string{"content", "preview", "rendition", "body", "text", "data"}

// fieldNamesAndJSONTag returns every Go field name field declares, plus its
// json tag's own name portion (before any comma-separated option like
// omitempty), if present — the full identifier surface a future field
// addition to driveNode could use to smuggle in a content-bearing name.
func fieldNamesAndJSONTag(field *ast.Field) []string {
	var out []string
	for _, n := range field.Names {
		out = append(out, n.Name)
	}
	if field.Tag == nil {
		return out
	}
	unquoted, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return out
	}
	jsonTag, ok := reflect.StructTag(unquoted).Lookup("json")
	if !ok {
		return out
	}
	name := strings.Split(jsonTag, ",")[0]
	if name != "" && name != "-" {
		out = append(out, name)
	}
	return out
}

// TestDriveNodeStruct_NeverDeclaresAContentBearingField is the CONT-04
// structural gate: a go/ast source scan of syncstate.go that fails if the
// driveNode struct ever declares a field whose Go name or json tag
// contains "content", "preview", "rendition", "body", "text", or "data" —
// so a future edit cannot silently start caching document bytes in the
// sync store, even if every other test in this package still passes.
// Demonstrated fail-first by hand (temporarily adding `Preview string
// `json:"preview"“ to driveNode and observing this test fail, then
// reverting) before this test was committed — recorded in
// 04-01-SUMMARY.md, not reproduced as a permanent test fixture, since a
// live source mutation isn't something a test itself can safely automate.
func TestDriveNodeStruct_NeverDeclaresAContentBearingField(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "syncstate.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser.ParseFile(syncstate.go): %v", err)
	}

	var foundStruct bool
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "driveNode" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		foundStruct = true
		for _, field := range st.Fields.List {
			for _, name := range fieldNamesAndJSONTag(field) {
				lower := strings.ToLower(name)
				for _, bad := range contentBearingFieldSubstrings {
					if strings.Contains(lower, bad) {
						t.Errorf("driveNode field/tag %q contains the forbidden substring %q — the sync store must never gain a content/preview/rendition-bearing field", name, bad)
					}
				}
			}
		}
		return true
	})
	if !foundStruct {
		t.Fatal("driveNode struct type not found in syncstate.go — this scanner's target may have moved")
	}
}

// TestUnavailableReasons_AreDistinctFromEachOtherAndFromEveryOtherStatusString
// pins the Task 3 acceptance criterion directly: unavailableNoRendition and
// unavailableTooLargeToReturn are both non-empty, differ from each other,
// and equal none of the four Phase 5 verbatim health sentences nor any of
// the four Phase 2 status constants.
func TestUnavailableReasons_AreDistinctFromEachOtherAndFromEveryOtherStatusString(t *testing.T) {
	reasons := []struct{ name, value string }{
		{"unavailableNoRendition", unavailableNoRendition},
		{"unavailableTooLargeToReturn", unavailableTooLargeToReturn},
	}
	for _, r := range reasons {
		if r.value == "" {
			t.Errorf("%s is empty, want a non-empty named reason", r.name)
		}
	}
	if unavailableNoRendition == unavailableTooLargeToReturn {
		t.Fatal("unavailableNoRendition and unavailableTooLargeToReturn are equal, want two distinct reason strings")
	}

	otherStrings := []struct{ name, value string }{
		{"healthAuthorized", healthAuthorized},
		{"healthNoTokenFile", healthNoTokenFile},
		{"healthNoClientCredentials", healthNoClientCredentials},
		{"healthRefreshFailed", healthRefreshFailed},
	}
	for _, r := range reasons {
		for _, o := range otherStrings {
			if r.value == o.value {
				t.Errorf("%s equals %s (%q) — every status/reason string this plugin returns must be distinguishable", r.name, o.name, r.value)
			}
		}
		for _, sentence := range verbatimHealthSentences {
			if r.value == sentence {
				t.Errorf("%s equals a Phase 5 verbatim health sentence (%q)", r.name, sentence)
			}
		}
	}
}

// --- Task 3: phase close-out — mixed-folder end-to-end proof ---

// mixedFolderFixtureItem is one non-folder item in Task 3's single mixed
// fixture folder. body is the regular-file media body (files.get
// alt=media) for a non-Workspace item, or the files.export body for a
// Workspace-native item whose export succeeds; it is nil for the
// over-ceiling Doc (exportFails, never returns a body) and for the
// declined-format Drawing (never requested at all — table lookup alone
// decides that, before any Drive call).
type mixedFolderFixtureItem struct {
	id, name, mimeType string
	body               []byte
	exportFails        bool
}

// newMixedFolderFixtureHandler serves every Drive REST endpoint Task 3's
// single mixed-folder end-to-end test needs against rootID/rootName and
// items: root metadata, changes (always reports zero changes — this test
// is about preview/content classification and persistence, not delta
// application), files.list (returning every item as a direct child of
// rootID), each regular item's own alt=media download, and each
// Workspace-native item's own files.export — failing the test outright if
// a request ever reaches an id this fixture does not recognize, or the
// declined-format Drawing's own export endpoint at all.
func newMixedFolderFixtureHandler(t *testing.T, rootID, rootName string, items []mixedFolderFixtureItem) http.HandlerFunc {
	t.Helper()
	byID := make(map[string]mixedFolderFixtureItem, len(items))
	for _, it := range items {
		byID[it.id] = it
	}
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/changes/startPageToken":
			writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-mixed"})
		case r.URL.Path == "/changes":
			writeDriveJSON(t, w, &drive.ChangeList{NewStartPageToken: "start-token-mixed"})
		case r.URL.Path == "/files/"+rootID:
			writeDriveJSON(t, w, &drive.File{Id: rootID, Name: rootName, MimeType: folderMimeType})
		case r.URL.Path == "/files":
			parent := parentFromQuery(r.URL.Query().Get("q"))
			var files []*drive.File
			if parent == rootID {
				for _, it := range items {
					files = append(files, &drive.File{
						Id:           it.id,
						Name:         it.name,
						MimeType:     it.mimeType,
						ModifiedTime: "2026-08-17T00:00:00Z",
						WebViewLink:  "https://drive.google.com/file/d/" + it.id + "/view",
					})
				}
			}
			writeDriveJSON(t, w, &drive.FileList{Files: files})
		case strings.HasSuffix(r.URL.Path, "/export"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/files/"), "/export")
			it, ok := byID[id]
			if !ok {
				t.Errorf("export request for unrecognized file id %q", id)
				http.NotFound(w, r)
				return
			}
			if it.exportFails {
				http.Error(w, `{"error":{"code":403,"message":"This file is too large to be exported.","errors":[{"reason":"`+exportSizeLimitReason+`","message":"This file is too large to be exported."}]}}`, http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", it.mimeType)
			_, _ = w.Write(it.body)
		case strings.HasPrefix(r.URL.Path, "/files/"):
			id := strings.TrimPrefix(r.URL.Path, "/files/")
			it, ok := byID[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", it.mimeType)
			_, _ = w.Write(it.body)
		default:
			http.NotFound(w, r)
		}
	}
}

// TestPhase4EndToEnd_MixedFolderProvesAllFourSuccessCriteria is Task 3's
// single named end-to-end test, covering every bullet in the task's own
// <behavior> block against one mixed fixture folder: a text file, a
// binary file, a zero-byte file, a Google Doc, a Google Sheet, an
// over-ceiling Google Doc, and a Google Drawing. It proves all four Phase
// 4 roadmap success criteria at once — see the criterion-to-test mapping
// in 04-02-SUMMARY.md.
func TestPhase4EndToEnd_MixedFolderProvesAllFourSuccessCriteria(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	const (
		rootID, rootName = "root-mixed", "Mixed Folder"
		textSentinel     = "sentinel-mixed-text-body"
		binarySentinel   = "sentinel-mixed-binary-body"
		docSentinel      = "sentinel-mixed-doc-export-text"
		sheetSentinel    = "sentinel-mixed-sheet-export-csv"
	)

	fixtureItems := []mixedFolderFixtureItem{
		{id: "file-a-text", name: "a-note.txt", mimeType: "text/plain", body: []byte("regular text file body wrapping the marker: " + textSentinel)},
		{id: "file-b-binary", name: "b-photo.png", mimeType: "image/png", body: []byte("regular binary file body wrapping the marker: " + binarySentinel)},
		{id: "file-c-zero", name: "c-empty.txt", mimeType: "text/plain", body: nil},
		{id: "file-d-doc", name: "d-plan.gdoc", mimeType: "application/vnd.google-apps.document", body: []byte("exported Doc text wrapping the marker: " + docSentinel)},
		{id: "file-e-sheet", name: "e-budget.gsheet", mimeType: "application/vnd.google-apps.spreadsheet", body: []byte("exported,sheet,csv," + sheetSentinel)},
		{id: "file-f-over-ceiling", name: "f-huge.gdoc", mimeType: "application/vnd.google-apps.document", exportFails: true},
		{id: "file-g-drawing", name: "g-diagram.gdraw", mimeType: "application/vnd.google-apps.drawing"},
	}
	wantNonEmptyPreview := map[string]bool{"file-a-text": true, "file-d-doc": true, "file-e-sheet": true}

	rec := newDriveRecorder(newMixedFolderFixtureHandler(t, rootID, rootName, fixtureItems))
	svc := newFakeDriveService(t, rec.ServeHTTP)
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	// runCycle exercises one full Match-plus-Fetch-every-item pass and
	// returns every Fetch response keyed by SourceId, so the caller can
	// assert both classification behavior and (once, after both cycles)
	// the CONT-04 sentinel-persistence gate.
	runCycle := func(t *testing.T) map[string]*toposv1.FetchResponse {
		t.Helper()
		matchResp, err := p.Match(context.Background(), buildMatchRequest(rootName))
		if err != nil {
			t.Fatalf("Match: %v", err)
		}
		gotItems := matchResp.GetItems()
		if len(gotItems) != len(fixtureItems) {
			t.Fatalf("len(Items) = %d, want %d (every non-folder item)", len(gotItems), len(fixtureItems))
		}
		for i := 1; i < len(gotItems); i++ {
			if gotItems[i-1].GetSourceId() >= gotItems[i].GetSourceId() {
				t.Errorf("Items not sorted by SourceId ascending: %q then %q", gotItems[i-1].GetSourceId(), gotItems[i].GetSourceId())
			}
		}
		for _, item := range gotItems {
			preview := item.GetPreview()
			if wantNonEmptyPreview[item.GetSourceId()] {
				if preview == "" {
					t.Errorf("Item %s: Preview is empty, want non-empty", item.GetSourceId())
				}
			} else if preview != "" {
				t.Errorf("Item %s: Preview = %q, want empty", item.GetSourceId(), preview)
			}
		}

		fetchResponses := make(map[string]*toposv1.FetchResponse, len(fixtureItems))
		for _, it := range fixtureItems {
			resp, err := p.Fetch(context.Background(), &toposv1.FetchRequest{SourceId: it.id, Variant: toposv1.ContentVariant_CONTENT_VARIANT_FULL})
			if err != nil {
				t.Fatalf("Fetch(%s): %v", it.id, err)
			}
			fetchResponses[it.id] = resp
		}
		return fetchResponses
	}

	assertFetchOutcomes := func(t *testing.T, fetchResponses map[string]*toposv1.FetchResponse) {
		t.Helper()
		if resp := fetchResponses["file-a-text"]; !resp.GetAvailable() || !strings.Contains(resp.GetText(), textSentinel) {
			t.Errorf("file-a-text: Available=%v Text=%q, want available:true containing %q", resp.GetAvailable(), resp.GetText(), textSentinel)
		}
		if resp := fetchResponses["file-b-binary"]; !resp.GetAvailable() || resp.GetText() != "" {
			t.Errorf("file-b-binary: Available=%v Text=%q, want available:true with empty Text (no rendition for a regular file)", resp.GetAvailable(), resp.GetText())
		}
		if resp := fetchResponses["file-c-zero"]; !resp.GetAvailable() || resp.GetText() != "" || resp.GetSizeBytes() != 0 {
			t.Errorf("file-c-zero: Available=%v Text=%q SizeBytes=%d, want available:true, empty Text, SizeBytes 0", resp.GetAvailable(), resp.GetText(), resp.GetSizeBytes())
		}
		if resp := fetchResponses["file-d-doc"]; !resp.GetAvailable() || !strings.Contains(resp.GetText(), docSentinel) {
			t.Errorf("file-d-doc: Available=%v Text=%q, want available:true containing %q", resp.GetAvailable(), resp.GetText(), docSentinel)
		}
		if resp := fetchResponses["file-e-sheet"]; !resp.GetAvailable() || !strings.Contains(resp.GetText(), sheetSentinel) {
			t.Errorf("file-e-sheet: Available=%v Text=%q, want available:true containing %q", resp.GetAvailable(), resp.GetText(), sheetSentinel)
		}
		if resp := fetchResponses["file-f-over-ceiling"]; resp.GetAvailable() || resp.GetUnavailableReason() != unavailableExportCeiling {
			t.Errorf("file-f-over-ceiling: Available=%v UnavailableReason=%q, want available:false with %q", resp.GetAvailable(), resp.GetUnavailableReason(), unavailableExportCeiling)
		}
		if resp := fetchResponses["file-g-drawing"]; resp.GetAvailable() || resp.GetUnavailableReason() != unavailableDeclinedFormat {
			t.Errorf("file-g-drawing: Available=%v UnavailableReason=%q, want available:false with %q", resp.GetAvailable(), resp.GetUnavailableReason(), unavailableDeclinedFormat)
		}
	}

	// fetchablePaths is every Drive REST path this fixture expects to be
	// hit at least once per cycle — everything except the Drawing, whose
	// own declined-format path (below) must NEVER be hit, in either cycle.
	fetchablePaths := map[string]string{
		"file-a-text":         "/files/file-a-text",
		"file-b-binary":       "/files/file-b-binary",
		"file-c-zero":         "/files/file-c-zero",
		"file-d-doc":          "/files/file-d-doc/export",
		"file-e-sheet":        "/files/file-e-sheet/export",
		"file-f-over-ceiling": "/files/file-f-over-ceiling/export",
	}
	const declinedFormatPath = "/files/file-g-drawing/export"

	firstResponses := runCycle(t)
	assertFetchOutcomes(t, firstResponses)

	firstCounts := make(map[string]int, len(fetchablePaths))
	for id, path := range fetchablePaths {
		n := rec.count(path)
		if n == 0 {
			t.Errorf("path %s (item %s) saw 0 requests after the first cycle, want at least 1", path, id)
		}
		firstCounts[path] = n
	}
	if got := rec.count(declinedFormatPath); got != 0 {
		t.Errorf("declined-format export path %s saw %d requests after the first cycle, want 0 — a declined format must never reach Drive", declinedFormatPath, got)
	}

	// Second identical cycle: nothing may be cached or memoized between
	// Match/Fetch calls (CONT-04), so every download and export this
	// fixture serves is re-issued, doubling every fetchable path's count —
	// the declined-format Drawing's own export path stays at zero.
	secondResponses := runCycle(t)
	assertFetchOutcomes(t, secondResponses)

	for id, path := range fetchablePaths {
		want := 2 * firstCounts[path]
		if got := rec.count(path); got != want {
			t.Errorf("path %s (item %s) saw %d requests after the second cycle, want exactly %d (double the first cycle's %d — nothing may be cached)", path, id, got, want, firstCounts[path])
		}
	}
	if got := rec.count(declinedFormatPath); got != 0 {
		t.Errorf("declined-format export path %s saw %d requests after the second cycle, want 0", declinedFormatPath, got)
	}

	// CONT-04's behavioral gate, extended to the export path: the
	// persisted syncstate.json bytes must contain no substring of any
	// fixture body OR any exported text, and the plugin's data directory
	// must hold only token.json and syncstate.json.
	stateBytes, err := os.ReadFile(dataFilePath(isolatedDir, syncStateFileName))
	if err != nil {
		t.Fatalf("ReadFile(syncstate.json): %v", err)
	}
	for _, sentinel := range []string{textSentinel, binarySentinel, docSentinel, sheetSentinel} {
		if strings.Contains(string(stateBytes), sentinel) {
			t.Errorf("syncstate.json contains the sentinel %q — fixture content leaked into the persisted sync store", sentinel)
		}
	}

	dataDir := filepath.Join(isolatedDir, dataDirName)
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dataDir, err)
	}
	for _, e := range entries {
		if e.Name() != tokenFileName && e.Name() != syncStateFileName {
			t.Errorf("unexpected file %q in the plugin data directory, want only %q and %q", e.Name(), tokenFileName, syncStateFileName)
		}
	}
}

// contentBearingLogNames is the identifier vocabulary
// TestSource_NeverInterpolatesDocumentContentIntoAPrintOrLogCall treats as
// content-bearing: a fetched or exported document's own bytes/text (the
// data/text/body identifiers preview.go's, workspaceexport.go's, and
// fetchcontent.go's own io.ReadAll/FetchResponse call sites actually use),
// or a Drive node's own human-readable Name — the same discipline
// secrets_test.go's credentialBearingNames already applies to credentials
// (T-04-07), extended here to document content per this phase's own
// standing log-discipline convention (every log line in preview.go,
// workspaceexport.go, and fetchcontent.go names the file id and a fixed
// reason only). Widening this set to silence a real finding, rather than
// fixing the flagged call site, is exactly the erosion
// secrets_test.go's own credentialBearingNames comment already warns
// against, applied here to the second sensitive-value category this
// phase introduces.
var contentBearingLogNames = map[string]bool{
	"data": true, "Data": true,
	"text": true, "Text": true,
	"body": true, "Body": true,
	"Name": true,
}

// TestSource_NeverInterpolatesDocumentContentIntoAPrintOrLogCall extends
// secrets_test.go's own go/ast source-scanning idiom (nonTestGoFiles,
// printLogFamily, credentialBearingArgName — reused directly, same
// package, not duplicated) to this phase's own sensitive-value category:
// no print or log call anywhere in this package's non-test .go files may
// pass an argument whose final identifier name is in
// contentBearingLogNames. nonTestGoFiles lists every non-test .go file in
// the package directory by construction, so preview.go, workspaceexport.go,
// and fetchcontent.go are covered automatically — this test also asserts
// those three names are present, so a future rename or file removal cannot
// silently narrow this scanner's coverage.
//
// Demonstrated fail-first by hand (temporarily adding
// `log.Printf("debug: %s", data)` to workspaceexport.go's fetchWorkspaceDoc,
// observing this test fail with the expected file:line and identifier name,
// then reverting the change and confirming `go build`/`git status` were
// clean again) before this test was committed — recorded in
// 04-02-SUMMARY.md, not reproduced as a permanent test fixture, for the
// same reason TestDriveNodeStruct_NeverDeclaresAContentBearingField's own
// fail-first demonstration (04-01-SUMMARY.md) was not: a live source
// mutation isn't something a test can safely automate against its own
// package at run time.
func TestSource_NeverInterpolatesDocumentContentIntoAPrintOrLogCall(t *testing.T) {
	files := nonTestGoFiles(t)
	for _, want := range []string{"preview.go", "workspaceexport.go", "fetchcontent.go"} {
		found := false
		for _, f := range files {
			if f == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("nonTestGoFiles() = %v, want it to include %q", files, want)
		}
	}

	fset := token.NewFileSet()
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s): %v", name, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !printLogFamily(call) {
				return true
			}
			pos := fset.Position(call.Pos())
			for _, arg := range call.Args {
				if argName := credentialBearingArgName(arg); argName != "" && contentBearingLogNames[argName] {
					t.Errorf("%s:%d: print/log call passes content-bearing identifier %q as an argument", pos.Filename, pos.Line, argName)
				}
			}
			return true
		})
	}
}
