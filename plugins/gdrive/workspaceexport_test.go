// Package main's workspaceexport_test.go hardens workspaceexport.go
// against every case this plan's own <behavior>/<acceptance_criteria>
// blocks name: the locked three-entry export table, a live Google Doc
// traced end to end through both Match's preview and Fetch's full
// content, and each of the three types requesting its own correct export
// MIME type. Extended by Task 2 with CONT-03's two distinct
// named-unavailable causes. Follows this repository's own idiom:
// Test<Thing>_<BehaviorInPlainEnglish> names, plain t.Errorf/t.Fatalf
// assertions, no assertion library.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// --- Task 1: the locked export table ---

// TestWorkspaceExportMIME_LockedToExactlyThreeEntries pins the T-04-12
// exact-match test: workspaceExportMIME carries exactly the three
// PRD-scoped types, and no other.
func TestWorkspaceExportMIME_LockedToExactlyThreeEntries(t *testing.T) {
	if got := len(workspaceExportMIME); got != 3 {
		t.Fatalf("len(workspaceExportMIME) = %d, want 3 (a fourth entry needs a recorded scope change)", got)
	}
	want := map[string]string{
		"application/vnd.google-apps.document":     "text/plain",
		"application/vnd.google-apps.spreadsheet":  "text/csv",
		"application/vnd.google-apps.presentation": "text/plain",
	}
	for mimeType, wantExport := range want {
		if got := workspaceExportMIME[mimeType]; got != wantExport {
			t.Errorf("workspaceExportMIME[%q] = %q, want %q", mimeType, got, wantExport)
		}
	}
}

// --- Task 1: exportPreview direct exercises, per Workspace type ---

// exportFixtureRecorder captures every request's query parameters this
// test's fixture handler saw against a files.export endpoint, so a test
// can assert exactly which mimeType value was requested for each call.
type exportFixtureRecorder struct {
	mu      sync.Mutex
	queries []url.Values
}

func (r *exportFixtureRecorder) record(q url.Values) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, q)
}

func (r *exportFixtureRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queries)
}

func (r *exportFixtureRecorder) mimeTypeAt(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i < 0 || i >= len(r.queries) {
		return ""
	}
	return r.queries[i].Get("mimeType")
}

// newExportOnlyFixtureHandler serves exactly one endpoint — fileID's own
// files.export call — asserting the request path is the file's export
// endpoint and failing the test if the request ever carries a Range
// header (files.export does not honor partial downloads,
// 04-RESEARCH.md Pitfall 1).
func newExportOnlyFixtureHandler(t *testing.T, fileID string, body []byte) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/files/" + fileID + "/export"
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, wantPath)
			http.NotFound(w, r)
			return
		}
		if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
			t.Errorf("export request for %s carried a Range header (%q), want none", fileID, rangeHeader)
		}
		w.Header().Set("Content-Type", r.URL.Query().Get("mimeType"))
		_, _ = w.Write(body)
	}
}

// TestExportPreview_EachWorkspaceTypeRequestsItsOwnExportMIME proves a Doc,
// a Sheet, and a Slide each request the export MIME type
// workspaceExportMIME names for it, observed via the fixture's own
// recorded mimeType query value — never a Range header on any of them.
func TestExportPreview_EachWorkspaceTypeRequestsItsOwnExportMIME(t *testing.T) {
	cases := []struct {
		name, nodeMimeType, wantExportMIME string
	}{
		{"Doc", "application/vnd.google-apps.document", "text/plain"},
		{"Sheet", "application/vnd.google-apps.spreadsheet", "text/csv"},
		{"Slide", "application/vnd.google-apps.presentation", "text/plain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const fileID = "file-workspace-type"
			body := []byte("export fixture body for " + tc.name)
			rec := &exportFixtureRecorder{}
			svc := newFakeDriveService(t, func(w http.ResponseWriter, r *http.Request) {
				rec.record(r.URL.Query())
				newExportOnlyFixtureHandler(t, fileID, body)(w, r)
			})

			got := exportPreview(context.Background(), svc, fileID, tc.nodeMimeType)
			if got == "" {
				t.Fatalf("exportPreview(%s) = empty, want a non-empty preview", tc.nodeMimeType)
			}
			if !strings.HasPrefix(string(body), got) {
				t.Errorf("preview %q is not a prefix of the export body %q", got, body)
			}
			if n := rec.count(); n != 1 {
				t.Fatalf("export request count = %d, want 1", n)
			}
			if gotMIME := rec.mimeTypeAt(0); gotMIME != tc.wantExportMIME {
				t.Errorf("export request mimeType query = %q, want %q", gotMIME, tc.wantExportMIME)
			}
		})
	}
}

// TestExportPreview_DeclinedFormatYieldsEmptyPreviewNoDriveCall proves a
// Workspace MIME type absent from workspaceExportMIME (Drawing, out of
// PRD's CONT-02 scope per GAP-17) is decided by table lookup alone —
// exportPreview never issues a request for it.
func TestExportPreview_DeclinedFormatYieldsEmptyPreviewNoDriveCall(t *testing.T) {
	svc := newFakeDriveService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected Drive request for a declined format: %s", r.URL.Path)
	})
	got := exportPreview(context.Background(), svc, "file-drawing", "application/vnd.google-apps.drawing")
	if got != "" {
		t.Errorf("exportPreview(drawing) = %q, want empty", got)
	}
}

// --- Task 1: one Google Doc, end to end through Match and Fetch ---

// newWorkspaceFixtureHandler serves the Drive REST endpoints one Match
// call plus one Fetch call need against a fixture folder holding exactly
// one Workspace-native file: root metadata, changes, files.list, and
// fx.fileID's own files.export.
func newWorkspaceFixtureHandler(t *testing.T, fx driveFixture, nodeMimeType, wantExportMIME string, exportBody []byte, rec *exportFixtureRecorder) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/changes/startPageToken":
			writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-1"})
		case r.URL.Path == "/changes":
			writeDriveJSON(t, w, &drive.ChangeList{NewStartPageToken: "start-token-1"})
		case r.URL.Path == "/files/"+fx.rootID:
			writeDriveJSON(t, w, &drive.File{Id: fx.rootID, Name: fx.rootName, MimeType: folderMimeType})
		case r.URL.Path == "/files/"+fx.fileID+"/export":
			if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
				t.Errorf("export request for %s carried a Range header (%q), want none — files.export does not honor partial downloads", fx.fileID, rangeHeader)
			}
			rec.record(r.URL.Query())
			w.Header().Set("Content-Type", wantExportMIME)
			_, _ = w.Write(exportBody)
		case r.URL.Path == "/files":
			parent := parentFromQuery(r.URL.Query().Get("q"))
			var files []*drive.File
			if parent == fx.rootID && fx.fileID != "" {
				files = []*drive.File{{
					Id:           fx.fileID,
					Name:         fx.fileName,
					MimeType:     nodeMimeType,
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

// TestMatchAndFetch_GoogleDocEndToEnd is this plan's tracer proof: a
// Google Doc in the configured folder appears in Match with a live,
// bounded preview rendered by files.export, and opening it via Fetch
// returns its whole exported content, fetched fresh from Drive during
// that call.
func TestMatchAndFetch_GoogleDocEndToEnd(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-doc", rootName: "Team Docs", fileID: "file-doc", fileName: "plan.gdoc"}
	exportBody := []byte("Exported plain-text body of a Google Doc, fetched live via files.export.")
	rec := &exportFixtureRecorder{}
	svc := newFakeDriveService(t, newWorkspaceFixtureHandler(t, fx, "application/vnd.google-apps.document", "text/plain", exportBody, rec))
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
	if !strings.HasPrefix(string(exportBody), preview) {
		t.Errorf("Preview %q is not a prefix of the exported fixture text %q", preview, exportBody)
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
	if fetchResp.GetText() != string(exportBody) {
		t.Errorf("Fetch Text = %q, want the whole exported fixture text %q", fetchResp.GetText(), exportBody)
	}
	if got := rec.count(); got != 2 {
		t.Fatalf("export request count = %d, want 2 (one preview export, one Fetch export)", got)
	}
	if got := rec.mimeTypeAt(0); got != "text/plain" {
		t.Errorf("preview export mimeType query = %q, want %q", got, "text/plain")
	}
}

// --- Task 2: CONT-03's two named-unavailable causes ---

// newExportErrorFixtureHandler serves fileID's own files.export endpoint
// with a structured googleapi.Error response shaped like a real Drive
// structured error (an "errors" array carrying reason/message per item),
// so errors.As(err, &gerr) genuinely produces a populated *googleapi.Error
// — never a bare HTTP status with no structured body. rec, when non-nil,
// records the request's query values the same way exportFixtureRecorder
// already does elsewhere in this file.
func newExportErrorFixtureHandler(t *testing.T, fileID string, statusCode int, reason, message string, rec *exportFixtureRecorder) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/files/" + fileID + "/export"
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, wantPath)
			http.NotFound(w, r)
			return
		}
		if rec != nil {
			rec.record(r.URL.Query())
		}
		body, err := json.Marshal(map[string]any{
			"error": map[string]any{
				"code":    statusCode,
				"message": message,
				"errors": []map[string]string{
					{"reason": reason, "message": message},
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal export error fixture body: %v", err)
		}
		http.Error(w, string(body), statusCode)
	}
}

// newExportBodyReadFailureHandler serves fileID's own files.export
// endpoint with a 200 response whose declared Content-Length exceeds the
// bytes actually written, so the client's io.ReadAll(resp.Body) observes
// an unexpected-EOF read failure partway through — proving a body read
// that fails part-way is treated as a genuine failure, never a short Text
// presented as complete.
func newExportBodyReadFailureHandler(t *testing.T, fileID string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/files/" + fileID + "/export"
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, wantPath)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", "10000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short body, far less than the declared Content-Length above"))
	}
}

// TestClassifyExportError_TableOfCases covers classifyExportError's own
// classification surface directly: a genuine exportSizeLimitExceeded
// structured reason is the only case that reports isCeiling true; every
// other shape (a different structured reason, no structured body at all,
// a plain non-googleapi error, or nil) reports false.
func TestClassifyExportError_TableOfCases(t *testing.T) {
	makeGoogleAPIError := func(code int, reason, message string) error {
		return &googleapi.Error{
			Code:    code,
			Message: message,
			Errors:  []googleapi.ErrorItem{{Reason: reason, Message: message}},
		}
	}
	cases := []struct {
		name        string
		err         error
		wantCeiling bool
	}{
		{"exportSizeLimitExceeded reason", makeGoogleAPIError(403, exportSizeLimitReason, "This file is too large to be exported."), true},
		{"different 403 reason", makeGoogleAPIError(403, "someOtherReason", "a different structured failure"), false},
		{"500 with no structured reason", &googleapi.Error{Code: 500, Message: "internal error"}, false},
		{"nil error", nil, false},
		{"plain non-googleapi error", errors.New("unrelated transport failure"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, isCeiling := classifyExportError(tc.err)
			if isCeiling != tc.wantCeiling {
				t.Errorf("classifyExportError(%v) isCeiling = %v, want %v", tc.err, isCeiling, tc.wantCeiling)
			}
			if isCeiling && reason != unavailableExportCeiling {
				t.Errorf("classifyExportError(%v) reason = %q, want %q", tc.err, reason, unavailableExportCeiling)
			}
			if !isCeiling && reason != "" {
				t.Errorf("classifyExportError(%v) reason = %q, want empty for a non-ceiling verdict", tc.err, reason)
			}
		})
	}
}

// TestClassifyExportError_MessageTextDecoyNeverFlipsTheVerdict is this
// plan's own decoy proof: a 403 whose Message contains the too-large
// wording but whose structured Reason token differs must NOT classify as
// the ceiling case — classification is structural (Reason), never a
// string match against human-readable text.
func TestClassifyExportError_MessageTextDecoyNeverFlipsTheVerdict(t *testing.T) {
	decoy := &googleapi.Error{
		Code:    403,
		Message: "This file is too large to be exported.",
		Errors:  []googleapi.ErrorItem{{Reason: "rateLimitExceeded", Message: "This file is too large to be exported."}},
	}
	reason, isCeiling := classifyExportError(decoy)
	if isCeiling {
		t.Fatalf("classifyExportError(decoy) isCeiling = true, want false — the Message text must never override the structured Reason token")
	}
	if reason != "" {
		t.Errorf("classifyExportError(decoy) reason = %q, want empty", reason)
	}
}

// TestFetchWorkspaceDoc_DeclinedFormatReturnsAvailableFalseNoDriveCall
// proves a Workspace MIME type absent from workspaceExportMIME returns
// available:false with unavailableDeclinedFormat and issues zero export
// requests for that file id.
func TestFetchWorkspaceDoc_DeclinedFormatReturnsAvailableFalseNoDriveCall(t *testing.T) {
	rec := &exportFixtureRecorder{}
	svc := newFakeDriveService(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.URL.Query())
		t.Errorf("unexpected Drive request for a declined format: %s", r.URL.Path)
	})
	resp, err := fetchWorkspaceDoc(context.Background(), svc, "file-drawing", "application/vnd.google-apps.drawing", map[string]string{"source_id": "file-drawing"})
	if err != nil {
		t.Fatalf("fetchWorkspaceDoc: %v", err)
	}
	if resp.GetAvailable() {
		t.Error("Available = true, want false for a declined format")
	}
	if resp.GetUnavailableReason() != unavailableDeclinedFormat {
		t.Errorf("UnavailableReason = %q, want %q", resp.GetUnavailableReason(), unavailableDeclinedFormat)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("export request count = %d, want 0 — a declined format must never reach Drive", got)
	}
}

// TestFetchWorkspaceDoc_ExportCeilingReturnsAvailableFalseWithCeilingReason
// proves a files.export failure whose structured reason is
// exportSizeLimitExceeded returns available:false with
// unavailableExportCeiling — a normal, expected outcome, never a gRPC
// error.
func TestFetchWorkspaceDoc_ExportCeilingReturnsAvailableFalseWithCeilingReason(t *testing.T) {
	const fileID = "file-over-ceiling"
	svc := newFakeDriveService(t, newExportErrorFixtureHandler(t, fileID, http.StatusForbidden, exportSizeLimitReason, "This file is too large to be exported.", nil))
	resp, err := fetchWorkspaceDoc(context.Background(), svc, fileID, "application/vnd.google-apps.document", map[string]string{"source_id": fileID})
	if err != nil {
		t.Fatalf("fetchWorkspaceDoc: %v", err)
	}
	if resp.GetAvailable() {
		t.Error("Available = true, want false for an over-ceiling export")
	}
	if resp.GetUnavailableReason() != unavailableExportCeiling {
		t.Errorf("UnavailableReason = %q, want %q", resp.GetUnavailableReason(), unavailableExportCeiling)
	}
}

// TestFetchWorkspaceDoc_NonCeiling403ReturnsUnavailableStatusNotAvailableFalse
// proves a 403 whose structured reason is some OTHER token is a genuine
// failure — codes.Unavailable, never available:false.
func TestFetchWorkspaceDoc_NonCeiling403ReturnsUnavailableStatusNotAvailableFalse(t *testing.T) {
	const fileID = "file-other-403"
	svc := newFakeDriveService(t, newExportErrorFixtureHandler(t, fileID, http.StatusForbidden, "rateLimitExceeded", "rate limited", nil))
	resp, err := fetchWorkspaceDoc(context.Background(), svc, fileID, "application/vnd.google-apps.document", map[string]string{"source_id": fileID})
	if err == nil {
		t.Fatalf("fetchWorkspaceDoc: want a non-nil error, got a response (Available=%v)", resp.GetAvailable())
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("fetchWorkspaceDoc error = %v, want a gRPC status with code Unavailable", err)
	}
}

// TestFetchWorkspaceDoc_500ReturnsUnavailableStatus proves an export
// responding 500 (no structured 403 body at all) is also a genuine
// failure — codes.Unavailable, never available:false.
func TestFetchWorkspaceDoc_500ReturnsUnavailableStatus(t *testing.T) {
	const fileID = "file-500"
	svc := newFakeDriveService(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":500,"message":"internal error"}}`, http.StatusInternalServerError)
	})
	_, err := fetchWorkspaceDoc(context.Background(), svc, fileID, "application/vnd.google-apps.document", map[string]string{"source_id": fileID})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("fetchWorkspaceDoc error = %v, want a gRPC status with code Unavailable", err)
	}
}

// TestFetchWorkspaceDoc_BodyReadFailureReturnsUnavailableStatus proves a
// body read that fails part-way returns codes.Unavailable and discards
// what was read — never a short Text with Available: true.
func TestFetchWorkspaceDoc_BodyReadFailureReturnsUnavailableStatus(t *testing.T) {
	const fileID = "file-body-read-fail"
	svc := newFakeDriveService(t, newExportBodyReadFailureHandler(t, fileID))
	resp, err := fetchWorkspaceDoc(context.Background(), svc, fileID, "application/vnd.google-apps.document", map[string]string{"source_id": fileID})
	if err == nil {
		t.Fatalf("fetchWorkspaceDoc: want a non-nil error, got a response (Available=%v, Text=%q)", resp.GetAvailable(), resp.GetText())
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("fetchWorkspaceDoc error = %v, want a gRPC status with code Unavailable", err)
	}
}

// TestExportPreview_CeilingFailureYieldsEmptyPreviewNeverTheReasonString
// proves Match-time degrade-never-fail extends to the export-ceiling
// failure: exportPreview returns "" — never the FetchResponse-shaped
// reason string, which is Fetch's own vocabulary, not Item.preview's.
func TestExportPreview_CeilingFailureYieldsEmptyPreviewNeverTheReasonString(t *testing.T) {
	const fileID = "file-preview-over-ceiling"
	svc := newFakeDriveService(t, newExportErrorFixtureHandler(t, fileID, http.StatusForbidden, exportSizeLimitReason, "This file is too large to be exported.", nil))
	got := exportPreview(context.Background(), svc, fileID, "application/vnd.google-apps.document")
	if got != "" {
		t.Errorf("exportPreview(over-ceiling) = %q, want empty", got)
	}
	if got == unavailableExportCeiling {
		t.Error("exportPreview leaked the Fetch-only reason string into Item.preview")
	}
}

// TestUnavailableExportReasons_AreDistinctFromEachOtherAndFromEveryOtherStatusString
// pins the Task 2 acceptance criterion directly: unavailableExportCeiling
// and unavailableDeclinedFormat are both non-empty, differ from each
// other, and equal none of the four Phase 5 verbatim health sentences, the
// four Phase 2 status constants, or plan 04-01's own two regular-file
// unavailable reasons.
func TestUnavailableExportReasons_AreDistinctFromEachOtherAndFromEveryOtherStatusString(t *testing.T) {
	reasons := []struct{ name, value string }{
		{"unavailableExportCeiling", unavailableExportCeiling},
		{"unavailableDeclinedFormat", unavailableDeclinedFormat},
	}
	for _, r := range reasons {
		if r.value == "" {
			t.Errorf("%s is empty, want a non-empty named reason", r.name)
		}
	}
	if unavailableExportCeiling == unavailableDeclinedFormat {
		t.Fatal("unavailableExportCeiling and unavailableDeclinedFormat are equal, want two distinct reason strings")
	}

	otherStrings := []struct{ name, value string }{
		{"healthAuthorized", healthAuthorized},
		{"healthNoTokenFile", healthNoTokenFile},
		{"healthNoClientCredentials", healthNoClientCredentials},
		{"healthRefreshFailed", healthRefreshFailed},
		{"unavailableNoRendition", unavailableNoRendition},
		{"unavailableTooLargeToReturn", unavailableTooLargeToReturn},
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

// --- UAT Test 2 closure: concurrent Match with a hanging Workspace export ---

// TestMatch_ConcurrentCallsWithHangingWorkspaceExportReturnBounded closes
// Phase 4 verification's last open item (04-VERIFICATION.md
// behavior_unverified_items / 04-UAT.md Test 2): two Match calls issued
// concurrently over a fixture whose only file is a Workspace-native Doc
// with a hanging files.export must BOTH return within the per-item
// previewFetchTimeout bound — never wedged on the export — with no data
// race (run under `go test -race`) and the hung item's preview empty:
// degrade, never fail, on the export path exactly as on the
// regular-file path.
func TestMatch_ConcurrentCallsWithHangingWorkspaceExportReturnBounded(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := driveFixture{rootID: "root-hang", rootName: "Team Docs", fileID: "file-hang-doc", fileName: "plan.gdoc"}
	svc := newFakeDriveService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/changes/startPageToken":
			writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-1"})
		case r.URL.Path == "/changes":
			writeDriveJSON(t, w, &drive.ChangeList{NewStartPageToken: "start-token-1"})
		case r.URL.Path == "/files/"+fx.rootID:
			writeDriveJSON(t, w, &drive.File{Id: fx.rootID, Name: fx.rootName, MimeType: folderMimeType})
		case r.URL.Path == "/files/"+fx.fileID+"/export":
			<-r.Context().Done() // hang until the caller's previewFetchTimeout cancels the request
		case r.URL.Path == "/files":
			parent := parentFromQuery(r.URL.Query().Get("q"))
			var files []*drive.File
			if parent == fx.rootID {
				files = []*drive.File{{
					Id:           fx.fileID,
					Name:         fx.fileName,
					MimeType:     "application/vnd.google-apps.document",
					ModifiedTime: "2026-08-17T00:00:00Z",
					WebViewLink:  "https://drive.google.com/file/d/" + fx.fileID + "/view",
				}}
			}
			writeDriveJSON(t, w, &drive.FileList{Files: files})
		default:
			http.NotFound(w, r)
		}
	})
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	req := buildMatchRequest(fx.rootName)

	const calls = 2
	var wg sync.WaitGroup
	results := make([]*toposv1.MatchResponse, calls)
	errs := make([]error, calls)
	start := time.Now()
	done := make(chan struct{})
	go func() {
		for i := 0; i < calls; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i], errs[i] = p.Match(context.Background(), req)
			}(i)
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 30*time.Second {
			t.Errorf("two concurrent Match calls took %s against a hanging export, want bounded near previewFetchTimeout (%s)", elapsed, previewFetchTimeout)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent Match calls did not return within 30s — previewFetchTimeout did not bound the hanging export")
	}

	for i := 0; i < calls; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent Match call %d: %v", i, errs[i])
		}
		items := results[i].GetItems()
		if len(items) != 1 {
			t.Fatalf("concurrent Match call %d returned %d items, want 1", i, len(items))
		}
		if got := items[0].GetPreview(); got != "" {
			t.Errorf("concurrent Match call %d: preview = %q, want empty (its export hung past previewFetchTimeout)", i, got)
		}
	}
}
