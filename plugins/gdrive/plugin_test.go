// Package main's plugin_test.go covers every Phase 1 behavior a later
// phase could silently regress: the verbatim configuration surface,
// Describe's purity and zero-configuration success, and the three stub
// RPC contracts. Follows contract/mock/plugin_test.go's own idiom:
// Test<RPC>_<BehaviorInPlainEnglish> names, plain t.Errorf/t.Fatalf
// assertions, no assertion library.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// verbatimHealthSentences holds the four exact Phase 5 health sentences
// PRD.md:175-180 defines. Shared by every Phase 1/Phase 2 test that must
// prove its own status text never collides with one of these — this slice
// must always hold exactly 4 entries; a test adding a 5th would silently
// weaken every guard built on top of it.
var verbatimHealthSentences = []string{
	`Not authorized — run "topos-plugin-gdrive auth" in a terminal, then use this source's "Refresh now".`,
	`Authorization expired or was revoked — run "topos-plugin-gdrive auth" again, then use this source's "Refresh now".`,
	`Rate limited by Google Drive — retrying automatically. No action needed.`,
	`The configured Drive folder is no longer accessible — check the folder still exists and is shared with this account.`,
}

// TestDescribe_DeclaresTheThreeVerbatimExtrasFields is the highest-value
// test for RPC-01: it proves the declared configuration surface matches
// PRD.md:118-122 byte-for-byte. The folder_id placeholder's comparison
// below is intentionally a plain Go == against a string literal containing
// a U+2014 EM DASH — never a trimmed, case-folded, or Unicode-normalized
// comparison. A well-meaning future "cleanup" that normalizes whitespace
// or punctuation in this comparison is exactly the regression this test
// exists to catch.
func TestDescribe_DeclaresTheThreeVerbatimExtrasFields(t *testing.T) {
	p := NewSourcePlugin()
	resp, err := p.Describe(context.Background(), &toposv1.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	extras := resp.GetExtras()
	if len(extras) != 3 {
		t.Fatalf("len(Extras) = %d, want 3 (an added or removed field must fail this test)", len(extras))
	}

	tests := []struct {
		key, label, placeholder string
		required, secret        bool
	}{
		{"client_id", "OAuth Client ID", "GDRIVE_CLIENT_ID", true, true},
		{"client_secret", "OAuth Client Secret", "GDRIVE_CLIENT_SECRET", true, true},
		{"folder_id", "Drive Folder ID", "e.g. 1a2B3cD4EfGhIjKlmNoPQRstuVwxYZ — the id segment of the folder's Drive URL", true, false},
	}

	for i, want := range tests {
		got := extras[i]
		if got.GetKey() != want.key {
			t.Errorf("Extras[%d].Key = %q, want %q", i, got.GetKey(), want.key)
		}
		if got.GetLabel() != want.label {
			t.Errorf("Extras[%d].Label = %q, want %q", i, got.GetLabel(), want.label)
		}
		if got.GetRequired() != want.required {
			t.Errorf("Extras[%d].Required = %v, want %v", i, got.GetRequired(), want.required)
		}
		if got.GetSecret() != want.secret {
			t.Errorf("Extras[%d].Secret = %v, want %v", i, got.GetSecret(), want.secret)
		}
		if got.GetPlaceholder() != want.placeholder {
			t.Errorf("Extras[%d].Placeholder = %q, want %q", i, got.GetPlaceholder(), want.placeholder)
		}
	}
}

// TestDescribe_DeclaresExactlyOneMatchVocabularyEntry asserts both the
// count and the value separately: a second entry and a wrong entry are
// different regressions (PRD.md:148).
func TestDescribe_DeclaresExactlyOneMatchVocabularyEntry(t *testing.T) {
	p := NewSourcePlugin()
	resp, err := p.Describe(context.Background(), &toposv1.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	vocab := resp.GetMatchVocabulary()
	if len(vocab) != 1 {
		t.Fatalf("len(MatchVocabulary) = %d, want 1", len(vocab))
	}
	if vocab[0] != "folders" {
		t.Errorf("MatchVocabulary[0] = %q, want %q", vocab[0], "folders")
	}
}

// TestDescribe_IsAPureConstantRegardlessOfRequest is the automated form of
// the contract's idempotence requirement (contract/plugin-contract.md:
// 488-501): Describe must return the same response for a nil request and
// for an empty request. DescribeRequest (contract/plugin.proto:16) has no
// settable field, so there is no third, "populated", request shape to
// exercise beyond these two.
func TestDescribe_IsAPureConstantRegardlessOfRequest(t *testing.T) {
	p := NewSourcePlugin()

	respNil, err := p.Describe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Describe(nil): %v", err)
	}
	respEmpty, err := p.Describe(context.Background(), &toposv1.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe(&DescribeRequest{}): %v", err)
	}

	assertDescribeResponsesEqual(t, respNil, respEmpty)
}

// assertDescribeResponsesEqual compares two DescribeResponse values
// field-by-field (avoiding a direct google.golang.org/protobuf/proto
// import, per the plan's stated alternative).
func assertDescribeResponsesEqual(t *testing.T, a, b *toposv1.DescribeResponse) {
	t.Helper()
	if a.GetSourceType() != b.GetSourceType() {
		t.Errorf("SourceType differs: %q vs %q", a.GetSourceType(), b.GetSourceType())
	}
	if a.GetDisplayName() != b.GetDisplayName() {
		t.Errorf("DisplayName differs: %q vs %q", a.GetDisplayName(), b.GetDisplayName())
	}
	if a.GetContractVersion() != b.GetContractVersion() {
		t.Errorf("ContractVersion differs: %q vs %q", a.GetContractVersion(), b.GetContractVersion())
	}
	if len(a.GetMatchVocabulary()) != len(b.GetMatchVocabulary()) {
		t.Fatalf("MatchVocabulary length differs: %v vs %v", a.GetMatchVocabulary(), b.GetMatchVocabulary())
	}
	for i := range a.GetMatchVocabulary() {
		if a.GetMatchVocabulary()[i] != b.GetMatchVocabulary()[i] {
			t.Errorf("MatchVocabulary[%d] differs: %q vs %q", i, a.GetMatchVocabulary()[i], b.GetMatchVocabulary()[i])
		}
	}
	if len(a.GetExtras()) != len(b.GetExtras()) {
		t.Fatalf("Extras length differs: %d vs %d", len(a.GetExtras()), len(b.GetExtras()))
	}
	for i := range a.GetExtras() {
		ea, eb := a.GetExtras()[i], b.GetExtras()[i]
		if ea.GetKey() != eb.GetKey() || ea.GetLabel() != eb.GetLabel() || ea.GetRequired() != eb.GetRequired() ||
			ea.GetSecret() != eb.GetSecret() || ea.GetPlaceholder() != eb.GetPlaceholder() {
			t.Errorf("Extras[%d] differs: %+v vs %+v", i, ea, eb)
		}
	}
}

// TestDescribe_SucceedsWithNoCredentialsAndNoTokenFile is the automated
// half of Phase 1 success criterion 3. It blanks the two OAuth env vars
// and redirects HOME/XDG_DATA_HOME to a fresh, empty temp directory so no
// token file can exist anywhere the plugin might look, then asserts
// Describe succeeds AND that the temp directory is still empty afterward —
// the strongest automated statement available that Describe is
// side-effect-free (it created nothing on disk).
func TestDescribe_SucceedsWithNoCredentialsAndNoTokenFile(t *testing.T) {
	isolatedDir := t.TempDir()
	t.Setenv("GDRIVE_CLIENT_ID", "")
	t.Setenv("GDRIVE_CLIENT_SECRET", "")
	t.Setenv("HOME", isolatedDir)
	t.Setenv("XDG_DATA_HOME", isolatedDir)

	p := NewSourcePlugin()
	resp, err := p.Describe(context.Background(), &toposv1.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe with no credentials and no token file: %v", err)
	}
	if resp == nil {
		t.Fatal("Describe returned a nil response with no error")
	}

	entries, err := os.ReadDir(isolatedDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", isolatedDir, err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = filepath.Join(isolatedDir, e.Name())
		}
		t.Errorf("Describe created filesystem entries under HOME/XDG_DATA_HOME, want none: %v", names)
	}
}

// isolatedNoAuthPlugin builds a SourcePlugin whose getenv is confined to a
// fresh, empty temp directory with no credentials configured anywhere —
// never the production NewSourcePlugin()'s os.Getenv, which on this
// operator's own machine would read the real token file plan 02-01's live
// auth run produced. Every Match/Fetch/Health test that does not
// specifically exercise a real token/credential must use this constructor,
// never NewSourcePlugin() directly, so `go test` never touches the
// operator's real secret or attempts a live network call.
func isolatedNoAuthPlugin(t *testing.T) *SourcePlugin {
	t.Helper()
	isolatedDir := t.TempDir()
	getenv := staticGetenv(map[string]string{
		"HOME":          isolatedDir,
		"XDG_DATA_HOME": isolatedDir,
	})
	return NewSourcePluginWithEnv(getenv)
}

// TestMatch_ReturnsUnavailableBeforeAuthExists asserts the contract-honest
// answer: no token can exist before authorization, so Match must fail with
// codes.Unavailable rather than returning an empty item set that could be
// mistaken for a real "zero matches" result.
func TestMatch_ReturnsUnavailableBeforeAuthExists(t *testing.T) {
	p := isolatedNoAuthPlugin(t)
	_, err := p.Match(context.Background(), &toposv1.MatchRequest{})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("Match error = %v, want a gRPC status with code Unavailable", err)
	}
}

// TestFetch_ReturnsUnavailableBeforeAuthExists mirrors
// TestMatch_ReturnsUnavailableBeforeAuthExists for Fetch.
func TestFetch_ReturnsUnavailableBeforeAuthExists(t *testing.T) {
	p := isolatedNoAuthPlugin(t)
	_, err := p.Fetch(context.Background(), &toposv1.FetchRequest{})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("Fetch error = %v, want a gRPC status with code Unavailable", err)
	}
}

// TestHealth_ReportsUnreachableBeforeAuthExists was deliberately FLIPPED by
// Phase 5 (05-RESEARCH.md's placeholder note: Phase 1/2's own tests assert
// the OPPOSITE of what Phase 5 must deliver, and are expected to flip, not
// regressions to avoid). With no token file anywhere, Health must now
// report the never-authorized sentence verbatim — LastError no longer just
// avoids colliding with a Phase 5 sentence, it now equals one exactly.
func TestHealth_ReportsUnreachableBeforeAuthExists(t *testing.T) {
	p := isolatedNoAuthPlugin(t)
	resp, err := p.Health(context.Background(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.GetReachable() {
		t.Error("Reachable = true, want false")
	}
	if resp.GetLastError() != verbatimHealthSentences[0] {
		t.Errorf("LastError = %q, want the never-authorized sentence %q", resp.GetLastError(), verbatimHealthSentences[0])
	}
}

// TestInternalStatusConstants_DifferFromVerbatimHealthSentences pins the
// Task 2 acceptance criterion directly against every internal status
// constant itself, not just whatever text one particular RPC happens to
// return — a future edit that reintroduces a Phase 5 sentence into any one
// of these constants must fail here regardless of which RPC exercises it.
// Formerly TestPhase2StatusConstants_DifferFromPhase5VerbatimSentences
// (Phase 1/2); renamed and extended by Task 2 to also cover
// healthProbeFailed, the one internal diagnostic constant Task 2 adds.
func TestInternalStatusConstants_DifferFromVerbatimHealthSentences(t *testing.T) {
	if len(verbatimHealthSentences) != 4 {
		t.Fatalf("verbatimHealthSentences has %d entries, want exactly 4", len(verbatimHealthSentences))
	}

	internalConstants := []struct {
		name, value string
	}{
		{"healthAuthorized", healthAuthorized},
		{"healthNoTokenFile", healthNoTokenFile},
		{"healthNoClientCredentials", healthNoClientCredentials},
		{"healthRefreshFailed", healthRefreshFailed},
		{"healthProbeFailed", healthProbeFailed},
	}
	for _, c := range internalConstants {
		for _, sentence := range verbatimHealthSentences {
			if c.value == sentence {
				t.Errorf("%s equals a Phase 5 verbatim health sentence (%q)", c.name, sentence)
			}
		}
	}
}

// TestVerbatimHealthSentences_ArePairwiseDistinct is this task's must_haves
// pin that the four sentence-bearing health states produce four
// pairwise-distinct sentences.
func TestVerbatimHealthSentences_ArePairwiseDistinct(t *testing.T) {
	if len(verbatimHealthSentences) != 4 {
		t.Fatalf("verbatimHealthSentences has %d entries, want exactly 4", len(verbatimHealthSentences))
	}
	for i := 0; i < len(verbatimHealthSentences); i++ {
		for j := i + 1; j < len(verbatimHealthSentences); j++ {
			if verbatimHealthSentences[i] == verbatimHealthSentences[j] {
				t.Errorf("verbatimHealthSentences[%d] and [%d] are equal, want pairwise distinct: %q", i, j, verbatimHealthSentences[i])
			}
		}
	}
}

// TestHealthAndMatch_AgreeOnTheNeverAuthorizedSentence was deliberately
// FLIPPED and RENAMED by Phase 5 (05-RESEARCH.md's placeholder note; this
// test was TestHealthAndMatch_AgreeOnTheNoTokenFileCause through Phase 2).
// With an isolated, empty HOME/XDG_DATA_HOME and no credentials configured
// anywhere, Health's LastError and Match's gRPC status message must both
// equal the never-authorized sentence — verbatimHealthSentences[0] —
// byte-for-byte, and must equal each other.
func TestHealthAndMatch_AgreeOnTheNeverAuthorizedSentence(t *testing.T) {
	isolatedDir := t.TempDir()
	getenv := staticGetenv(map[string]string{
		"HOME":          isolatedDir,
		"XDG_DATA_HOME": isolatedDir,
	})

	healthPlugin := NewSourcePluginWithEnv(getenv)
	healthResp, err := healthPlugin.Health(context.Background(), &toposv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if healthResp.GetReachable() {
		t.Error("Reachable = true, want false")
	}
	if healthResp.GetLastError() != verbatimHealthSentences[0] {
		t.Errorf("Health LastError = %q, want %q", healthResp.GetLastError(), verbatimHealthSentences[0])
	}

	matchPlugin := NewSourcePluginWithEnv(getenv)
	_, matchErr := matchPlugin.Match(context.Background(), &toposv1.MatchRequest{})
	st, ok := status.FromError(matchErr)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("Match error = %v, want a gRPC status with code Unavailable", matchErr)
	}
	if st.Message() != verbatimHealthSentences[0] {
		t.Errorf("Match status message = %q, want %q", st.Message(), verbatimHealthSentences[0])
	}
	if st.Message() != healthResp.GetLastError() {
		t.Errorf("Match status message %q and Health LastError %q disagree, want identical", st.Message(), healthResp.GetLastError())
	}
}

// healthProbeFolderID is the fixed folder id every Task 2 probe test below
// configures — probeDriveReachable issues its single files.get against
// exactly this id.
const healthProbeFolderID = "root-1"

// authorizedHealthProbePlugin builds a SourcePlugin with a valid, unexpired
// token already seeded (so ensureTokenState reports stateHealthy and
// Health proceeds all the way to probeDriveReachable) and its Drive service
// short-circuited to svc, following syncengine_test.go's own
// pluginWithFakeDrive idiom.
func authorizedHealthProbePlugin(t *testing.T, svc *drive.Service) *SourcePlugin {
	t.Helper()
	isolatedDir := t.TempDir()
	seedValidToken(t, filepath.Join(isolatedDir, dataDirName, tokenFileName))
	return pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, healthProbeFolderID), svc)
}

// driveErrorHandler serves a minimal Drive-shaped JSON error body at code,
// optionally carrying one or more structured errors[].reason tokens — the
// exact shape google.golang.org/api/googleapi parses back into a
// *googleapi.Error with a populated Errors slice, so classifyDriveError's
// structured-field inspection has something real to classify.
func driveErrorHandler(t *testing.T, code int, reasons ...string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		type errItem struct {
			Reason string `json:"reason"`
		}
		items := make([]errItem, 0, len(reasons))
		for _, reason := range reasons {
			items = append(items, errItem{Reason: reason})
		}
		body := map[string]any{
			"error": map[string]any{
				"code":   code,
				"errors": items,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Fatalf("encode drive error body: %v", err)
		}
	}
}

// driveSuccessFileHandler answers a files.get request with a minimal,
// successful *drive.File response.
func driveSuccessFileHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		writeDriveJSON(t, w, &drive.File{Id: healthProbeFolderID})
	}
}

// TestHealth_ReflectsLiveDriveProbeOutcome is Task 2's central proof: every
// PRD-named Drive cause is actually reachable from Health, and a fully
// healthy plugin reports Reachable: true with an empty LastError — the
// red-dot behavior STATE.md flagged as expected-until-Phase-5 is gone
// (05-RESEARCH.md Pitfall 3).
func TestHealth_ReflectsLiveDriveProbeOutcome(t *testing.T) {
	tests := []struct {
		name          string
		handler       http.HandlerFunc
		wantReachable bool
		wantSentence  string // checked only when !wantReachable
	}{
		{
			name:          "404FolderInaccessible",
			handler:       driveErrorHandler(t, http.StatusNotFound),
			wantReachable: false,
			wantSentence:  sentenceFolderInaccessible,
		},
		{
			name:          "403InsufficientFilePermissions",
			handler:       driveErrorHandler(t, http.StatusForbidden, reasonInsufficientFilePermissions),
			wantReachable: false,
			wantSentence:  sentenceFolderInaccessible,
		},
		{
			name:          "429RateLimited",
			handler:       driveErrorHandler(t, http.StatusTooManyRequests),
			wantReachable: false,
			wantSentence:  sentenceRateLimited,
		},
		{
			name:          "403RateLimitExceeded",
			handler:       driveErrorHandler(t, http.StatusForbidden, reasonRateLimitExceeded),
			wantReachable: false,
			wantSentence:  sentenceRateLimited,
		},
		{
			name:          "Success",
			handler:       driveSuccessFileHandler(t),
			wantReachable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newFakeDriveService(t, tt.handler)
			p := authorizedHealthProbePlugin(t, svc)

			resp, err := p.Health(context.Background(), &toposv1.HealthRequest{})
			if err != nil {
				t.Fatalf("Health: %v", err)
			}
			if resp.GetReachable() != tt.wantReachable {
				t.Errorf("Reachable = %v, want %v", resp.GetReachable(), tt.wantReachable)
			}
			if tt.wantReachable {
				if resp.GetLastError() != "" {
					t.Errorf("LastError = %q, want empty", resp.GetLastError())
				}
				return
			}
			if resp.GetLastError() != tt.wantSentence {
				t.Errorf("LastError = %q, want %q", resp.GetLastError(), tt.wantSentence)
			}
		})
	}
}

// TestHealth_IssuesExactlyOneDriveRequestPerCall is the driveRecorder-backed
// proof that a single, healthy Health call issues exactly one Drive
// request — probeDriveReachable's single files.get, never more.
func TestHealth_IssuesExactlyOneDriveRequestPerCall(t *testing.T) {
	recorder := newDriveRecorder(driveSuccessFileHandler(t))
	svc := newFakeDriveService(t, recorder.ServeHTTP)
	p := authorizedHealthProbePlugin(t, svc)

	if _, err := p.Health(context.Background(), &toposv1.HealthRequest{}); err != nil {
		t.Fatalf("Health: %v", err)
	}

	if got := recorder.count("/files/" + healthProbeFolderID); got != 1 {
		t.Errorf("Drive request count for /files/%s = %d, want 1", healthProbeFolderID, got)
	}
}

// TestSourcePlugin_LastSyncUnix_ReturnsZeroWhenNoSyncStateFileExists is
// GAP-19's file-absent case: never having synced reports 0, not an error.
func TestSourcePlugin_LastSyncUnix_ReturnsZeroWhenNoSyncStateFileExists(t *testing.T) {
	p := isolatedNoAuthPlugin(t)
	if got := p.lastSyncUnix(); got != 0 {
		t.Errorf("lastSyncUnix() = %d, want 0", got)
	}
}

// TestSourcePlugin_LastSyncUnix_ReturnsFileModTimeWhenSyncStateFileExists
// is GAP-19's file-present case: LastSyncUnix equals the persisted
// syncstate.json file's own modification time in Unix seconds.
func TestSourcePlugin_LastSyncUnix_ReturnsFileModTimeWhenSyncStateFileExists(t *testing.T) {
	isolatedDir := t.TempDir()
	getenv := staticGetenv(map[string]string{
		"HOME":          isolatedDir,
		"XDG_DATA_HOME": isolatedDir,
	})
	path, err := syncStatePath(getenv)
	if err != nil {
		t.Fatalf("syncStatePath: %v", err)
	}
	st := &syncState{RootID: "root-1", RootName: "Team Docs", ChangeToken: "tok", Tree: map[string]*driveNode{}}
	if err := saveSyncState(path, st); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}

	p := NewSourcePluginWithEnv(getenv)
	if got, want := p.lastSyncUnix(), info.ModTime().Unix(); got != want {
		t.Errorf("lastSyncUnix() = %d, want %d", got, want)
	}
}

// TestClassifyTokenError covers classifyTokenError's whole decision table:
// the two resolution-stage file sentinels, a real *oauth2.RetrieveError
// (the shape a Google refresh-grant rejection takes — the same type
// authfailurespike_test.go's live spike inspects), and a plain local error
// that is neither (a resolution failure due to missing client credentials
// or an undecodable source config), which must stay stateUnclassified per
// this task's must_haves.
func TestClassifyTokenError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want healthState
	}{
		{"AbsentTokenFile", fmt.Errorf("token file %s: %w", "x", fs.ErrNotExist), stateNeverAuthorized},
		{"MalformedTokenFile", fmt.Errorf("token file %s: %w", "x", errTokenFileMalformed), stateNeverAuthorized},
		{
			"RefreshGrantRejected",
			&oauth2.RetrieveError{ErrorCode: "invalid_grant", ErrorDescription: "Token has been expired or revoked."},
			stateExpiredRevoked,
		},
		{"MissingClientCredentials", errors.New("client credentials not configured: extras key \"client_id\" or GDRIVE_CLIENT_ID"), stateUnclassified},
		{"UndecodableSourceConfig", errors.New(sourceConfigEnvVar + ": could not decode as JSON"), stateUnclassified},
		{"Nil", nil, stateUnclassified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyTokenError(tt.err); got != tt.want {
				t.Errorf("classifyTokenError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestHealthSentences_MatchThePRDTableByteForByte pins healthState.Sentence()
// against verbatimHealthSentences (plugin_test.go's own independent,
// test-side copy of PRD.md:175-180's table) with a plain Go == — no
// trimming, case-folding, or Unicode normalization at the comparison site,
// per this task's must_haves. stateHealthy and stateUnclassified both
// return "" — neither has a PRD-mandated sentence.
func TestHealthSentences_MatchThePRDTableByteForByte(t *testing.T) {
	if len(verbatimHealthSentences) != 4 {
		t.Fatalf("verbatimHealthSentences has %d entries, want exactly 4", len(verbatimHealthSentences))
	}

	tests := []struct {
		name  string
		state healthState
		want  string
	}{
		{"stateNeverAuthorized", stateNeverAuthorized, verbatimHealthSentences[0]},
		{"stateExpiredRevoked", stateExpiredRevoked, verbatimHealthSentences[1]},
		{"stateRateLimited", stateRateLimited, verbatimHealthSentences[2]},
		{"stateFolderInaccessible", stateFolderInaccessible, verbatimHealthSentences[3]},
	}
	for _, tt := range tests {
		if got := tt.state.Sentence(); got != tt.want {
			t.Errorf("%s.Sentence() = %q, want %q", tt.name, got, tt.want)
		}
	}

	if got := stateHealthy.Sentence(); got != "" {
		t.Errorf("stateHealthy.Sentence() = %q, want empty", got)
	}
	if got := stateUnclassified.Sentence(); got != "" {
		t.Errorf("stateUnclassified.Sentence() = %q, want empty", got)
	}
}

// TestConstructAndDescribe_CreatesZeroFilesystemEntries is the
// NewSourcePluginWithEnv-flavored companion to
// TestDescribe_SucceedsWithNoCredentialsAndNoTokenFile: it proves
// constructing the plugin through the injected-getenv constructor and
// calling Describe performs no I/O, using the same isolation shape the
// rest of this task's tests use.
func TestConstructAndDescribe_CreatesZeroFilesystemEntries(t *testing.T) {
	isolatedDir := t.TempDir()
	getenv := staticGetenv(map[string]string{
		"HOME":          isolatedDir,
		"XDG_DATA_HOME": isolatedDir,
	})

	p := NewSourcePluginWithEnv(getenv)
	resp, err := p.Describe(context.Background(), &toposv1.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if resp == nil {
		t.Fatal("Describe returned a nil response with no error")
	}

	entries, err := os.ReadDir(isolatedDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", isolatedDir, err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = filepath.Join(isolatedDir, e.Name())
		}
		t.Errorf("constructing the plugin and calling Describe created filesystem entries, want none: %v", names)
	}
}

// refreshTokenServer stands up an httptest.Server answering exactly one
// OAuth2 refresh grant with the given access/refresh token pair. When
// omitRefreshToken is true, the response's refresh_token field is omitted
// entirely — modeling Google's real behavior of not always reissuing a
// refresh token on every refresh grant.
func refreshTokenServer(t *testing.T, newAccessToken, newRefreshToken string, omitRefreshToken bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"access_token": newAccessToken,
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		if !omitRefreshToken {
			body["refresh_token"] = newRefreshToken
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Fatalf("encode refresh response: %v", err)
		}
	}))
}

// TestPersistingTokenSource_PersistsRotatedRefreshTokenOnRefresh is the
// offline refresh-persistence proof: an already-expired seed token drives a
// real refresh grant against an httptest server, and the rotated
// access/refresh token pair the server returns must be re-readable from
// disk afterward, at a file still mode 0600.
func TestPersistingTokenSource_PersistsRotatedRefreshTokenOnRefresh(t *testing.T) {
	srv := refreshTokenServer(t, "new-access-token", "rotated-refresh-token", false)
	defer srv.Close()

	isolatedDir := t.TempDir()
	path := filepath.Join(isolatedDir, "token.json")
	seed := &oauth2.Token{
		AccessToken:  "expired-access-token",
		RefreshToken: "original-refresh-token",
		Expiry:       time.Now().Add(-time.Hour), // already expired: forces a refresh
	}
	if err := saveToken(path, seed); err != nil {
		t.Fatalf("saveToken (seed): %v", err)
	}

	conf := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
	}
	ts := newPersistingTokenSource(conf.TokenSource(context.Background(), seed), path)

	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "new-access-token" {
		t.Errorf("returned AccessToken = %q, want %q", tok.AccessToken, "new-access-token")
	}

	reread, err := loadToken(path)
	if err != nil {
		t.Fatalf("loadToken (re-read after refresh): %v", err)
	}
	if reread.AccessToken != "new-access-token" {
		t.Errorf("persisted AccessToken = %q, want %q", reread.AccessToken, "new-access-token")
	}
	if reread.RefreshToken != "rotated-refresh-token" {
		t.Errorf("persisted RefreshToken = %q, want %q", reread.RefreshToken, "rotated-refresh-token")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("token file mode = %o, want 0600", mode)
	}
}

// TestPersistingTokenSource_KeepsPreviousRefreshTokenWhenRefreshOmitsOne
// covers the companion case: a refresh grant response that omits
// refresh_token entirely (Google does not always reissue one) must not
// overwrite the previously persisted refresh token with an empty string.
func TestPersistingTokenSource_KeepsPreviousRefreshTokenWhenRefreshOmitsOne(t *testing.T) {
	srv := refreshTokenServer(t, "new-access-token", "", true)
	defer srv.Close()

	isolatedDir := t.TempDir()
	path := filepath.Join(isolatedDir, "token.json")
	seed := &oauth2.Token{
		AccessToken:  "expired-access-token",
		RefreshToken: "original-refresh-token",
		Expiry:       time.Now().Add(-time.Hour),
	}
	if err := saveToken(path, seed); err != nil {
		t.Fatalf("saveToken (seed): %v", err)
	}

	conf := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
	}
	ts := newPersistingTokenSource(conf.TokenSource(context.Background(), seed), path)

	if _, err := ts.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}

	reread, err := loadToken(path)
	if err != nil {
		t.Fatalf("loadToken (re-read after refresh): %v", err)
	}
	if reread.RefreshToken != "original-refresh-token" {
		t.Errorf("persisted RefreshToken = %q, want the original %q to be carried forward", reread.RefreshToken, "original-refresh-token")
	}
	if reread.AccessToken != "new-access-token" {
		t.Errorf("persisted AccessToken = %q, want %q", reread.AccessToken, "new-access-token")
	}
}
