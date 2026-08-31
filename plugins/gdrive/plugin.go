// Package main's plugin.go implements sdk.SourcePlugin: the four RPCs
// declared by contract/plugin.proto's SourcePlugin service. This file is
// written to be read as documentation — each RPC method's comment states
// what the contract requires of it, not merely what this particular
// implementation happens to do — mirroring contract/mock/plugin.go's own
// stated intent.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

const (
	// sourceType and displayName are this repository's own choice — none
	// of the four permitted clean-room inputs specify a value for either
	// field. Recorded as CONTRACT-GAPS.md GAP-03: these strings are
	// user-visible (display_name renders in the host's UI; source_type is
	// retained as descriptive provenance on every item this plugin later
	// emits) and must stay stable across all five phases once chosen.
	sourceType  = "gdrive"
	displayName = "Google Drive"

	// contractVersion names the contract generation this plugin was built
	// against (contract/plugin.proto:20-31).
	contractVersion = "topos.v2"
)

// matchVocabulary is the field-name vocabulary this plugin's Match RPC
// reads from MatchRequest.match_fields, declared by the plugin itself.
// PRD.md:148 locks this to exactly one entry: "folders" — match values are
// the item's resolved folder-path segments relative to the configured
// root folder.
var matchVocabulary = []string{"folders"}

// sourceSystem is this plugin's GAP-12 resolution for the "source_system"
// provenance key: the configured folder's own canonical Drive URL. Unlike
// sourceType/displayName above, this cannot be a package constant — it
// depends on the operator's own configured folder id — so it is a small
// pure function instead, computed fresh from a syncState's own RootID
// (always the configured folder_id) at Match time.
func sourceSystem(rootID string) string {
	return "https://drive.google.com/drive/folders/" + rootID
}

// Phase 2 status constants, returned by Health's LastError and (wrapped as
// a gRPC status) by Match/Fetch when a token cannot be resolved. Each is
// distinct from all four Phase 5 verbatim health sentences
// (plugin_test.go's verbatimHealthSentences) — introducing, paraphrasing,
// or approaching any of those four strings in Phase 2 code is exactly the
// regression that guard exists to catch. Declared once, here, and used by
// both Health and the error-returning RPCs so the two can never disagree.
const (
	healthAuthorized          = "authorized: a valid access token was minted from the persisted refresh token"
	healthNoTokenFile         = "not authorized: no token file found — run \"topos-plugin-gdrive auth\" first"
	healthNoClientCredentials = "not authorized: OAuth client credentials are not configured"
	healthRefreshFailed       = "not authorized: refreshing the access token failed"
)

// healthState is this plugin's internal named-state health classification
// (RPC-03/RPC-04): one constant per distinguishable cause, never a single
// generic "unhealthy" flag. Both Health and Match consult the SAME state
// via ensureTokenState/probeDriveReachable and derive their operator-visible
// text exclusively through Sentence()/unhealthyMessage below, so the two
// RPCs can never disagree about what a given failure is called.
// stateUnclassified is the zero value: a cause outside PRD.md's four named
// rows (missing client credentials, an undecodable source config, an
// unclassifiable probe failure) is unclassified, and reports an internal
// diagnostic constant — never one of the four verbatim sentences and never
// invented remedy text.
type healthState int

const (
	stateUnclassified healthState = iota
	stateHealthy
	stateNeverAuthorized
	stateExpiredRevoked
	stateRateLimited
	stateFolderInaccessible
)

// The four Phase 5 verbatim health sentences, copied byte-for-byte out of
// PRD.md:175-180's Health States table — never retyped, never paraphrased
// (PRD.md:167-170's standing prohibition against inventing remedy
// language). Each contains a U+2014 EM DASH and, in the first two, a pair
// of ASCII double quotes wrapping the auth subcommand name — copied exactly
// as PRD.md itself renders them. plugin_test.go's independently maintained
// verbatimHealthSentences slice is the byte-for-byte counterparty these
// four constants are tested against; neither copy is ever defined in terms
// of the other.
const (
	sentenceNeverAuthorized    = `Not authorized — run "topos-plugin-gdrive auth" in a terminal, then use this source's "Refresh now".`
	sentenceExpiredRevoked     = `Authorization expired or was revoked — run "topos-plugin-gdrive auth" again, then use this source's "Refresh now".`
	sentenceRateLimited        = `Rate limited by Google Drive — retrying automatically. No action needed.`
	sentenceFolderInaccessible = `The configured Drive folder is no longer accessible — check the folder still exists and is shared with this account.`
)

// Sentence returns s's exact PRD-mandated verbatim sentence, or the empty
// string for stateUnclassified and stateHealthy — neither of which
// PRD.md's Health States table names a sentence for. This is the ONLY
// place a healthState is mapped to operator-visible text; unhealthyMessage
// is the only caller.
func (s healthState) Sentence() string {
	switch s {
	case stateNeverAuthorized:
		return sentenceNeverAuthorized
	case stateExpiredRevoked:
		return sentenceExpiredRevoked
	case stateRateLimited:
		return sentenceRateLimited
	case stateFolderInaccessible:
		return sentenceFolderInaccessible
	default:
		return ""
	}
}

// unhealthyMessage returns state.Sentence() when that is non-empty, else
// fallback. This is the ONLY place Health/Match ever choose the text they
// report for an unhealthy state — every caller routes through this
// function rather than building LastError/status text directly from a
// health state or a Phase 2 constant, which is what keeps the "never
// invent new remedy language" prohibition mechanical rather than a matter
// of reviewer vigilance (05-RESEARCH.md Pitfall 1).
func unhealthyMessage(state healthState, fallback string) string {
	if s := state.Sentence(); s != "" {
		return s
	}
	return fallback
}

// classifyTokenError is the single mapping from a token-resolution OR
// token-refresh failure to a healthState — consumed by ensureTokenState
// below (both of its branches) and, directly, by plan 05-01 Task 3's
// authfailurespike_test.go, so the spike harness classifies a real
// Google refresh-grant failure with the exact same function Health/Match
// use, never a hand-rolled copy.
//
// fs.ErrNotExist or errTokenFileMalformed — the same two sentinels
// healthCauseForResolutionError already tests for — are the one
// RESOLUTION-stage cause classifyTokenError can determine from a local
// filesystem check alone, with no Google call needed, so they map to
// stateNeverAuthorized.
//
// errors.As into *oauth2.RetrieveError identifies a REFRESH-stage failure:
// Google's token endpoint answering a refresh grant with a non-2xx status
// or a populated RFC 6749 'error' field (persistingTokenSource.Token()
// returns this error type unwrapped, since it never wraps oauth2's own
// error further). GAP-20's resolution applies here: PRD.md's four-row
// table permits no finer sub-classification of "expired or revoked" than
// this single state, regardless of what Google's own response
// distinguishes between user-revoked, inactivity-expired, or
// app-deleted — so every *oauth2.RetrieveError maps to stateExpiredRevoked
// alike.
//
// Every other resolution failure (a WEBSPACES_SOURCE_CONFIG decode
// failure, missing client credentials — neither of which is ever an
// *oauth2.RetrieveError, since both are constructed locally before any
// network call is attempted) is outside the PRD's four named causes and
// maps to stateUnclassified — the caller reports the matching Phase 2
// diagnostic constant instead, via unhealthyMessage's fallback argument.
func classifyTokenError(err error) healthState {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errTokenFileMalformed) {
		return stateNeverAuthorized
	}
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		return stateExpiredRevoked
	}
	return stateUnclassified
}

// healthProbeFailed is the internal diagnostic constant for a live Drive
// probe failure that classifyDriveError could not map to one of the two
// PRD-named Drive causes (folder-inaccessible, rate-limited) — worded so it
// can never be confused with any of the four Phase 5 verbatim sentences or
// the four Phase 2 status constants above; TestInternalStatusConstants_...
// (plugin_test.go) pins that separation.
const healthProbeFailed = "unhealthy: the Drive reachability probe failed for an unclassified reason"

// healthProbeTimeout bounds probeDriveReachable's single files.get call.
// The contract requires Health to stay cheap and NEVER cached
// (contract/plugin-contract.md:806-808) — a rate-limited dashboard poll
// must report the rate-limited sentence promptly, not hang behind a long
// default HTTP timeout. This probe is deliberately never wrapped by
// 05-02's retry decorator for the identical reason.
const healthProbeTimeout = 5 * time.Second

// SourcePlugin implements sdk.SourcePlugin. NewSourcePlugin performs no
// I/O and reads no environment — the add-source trial launch constructs
// this plugin before an operator has configured anything
// (TestDescribe_SucceedsWithNoCredentialsAndNoTokenFile asserts the empty
// directory that proves it). tokenSource resolves the OAuth token source
// lazily, on first use by Health/Match/Fetch, guarded by once so repeated
// RPCs resolve it exactly once per process lifetime.
type SourcePlugin struct {
	getenv func(string) string

	once  sync.Once
	ts    oauth2.TokenSource
	tsErr error

	// syncMu guards ensureSynced's whole first-run-vs-persisted-state
	// decision (syncengine.go) so a concurrent call can never observe or
	// trigger two overlapping folder walks.
	syncMu sync.Mutex

	// driveOnce/svc/svcErr resolve this plugin's single *drive.Service
	// construction point (syncengine.go's driveService), guarded the same
	// once-per-process-lifetime way tokenSource's once/ts/tsErr trio
	// above already is.
	driveOnce sync.Once
	svc       *drive.Service
	svcErr    error
}

// NewSourcePlugin builds a SourcePlugin whose getenv is os.Getenv — the
// production constructor. It performs no I/O, reads no environment, and
// creates no directory or file.
func NewSourcePlugin() *SourcePlugin {
	return &SourcePlugin{getenv: os.Getenv}
}

// NewSourcePluginWithEnv builds a SourcePlugin with an injected getenv, for
// tests that need to isolate HOME/XDG_DATA_HOME/GDRIVE_* without mutating
// the real process environment.
func NewSourcePluginWithEnv(getenv func(string) string) *SourcePlugin {
	return &SourcePlugin{getenv: getenv}
}

// tokenSource resolves this plugin's OAuth token source: the persisted
// token file, the client credentials (extras-first, GAP-07), and the
// shared oauth2.Config newOAuthConfig builds — wrapped so that a refresh
// rotation is persisted, not discarded (token.go's persistingTokenSource).
// Resolution happens once, guarded by p.once, and every failure names the
// missing path, key, or variable and never a value.
func (p *SourcePlugin) tokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	p.once.Do(func() {
		path, err := tokenPath(p.getenv)
		if err != nil {
			p.tsErr = fmt.Errorf("resolve token path: %w", err)
			return
		}
		tok, err := loadToken(path)
		if err != nil {
			p.tsErr = err
			return
		}

		cfg, err := loadSourceConfig(p.getenv)
		if err != nil {
			p.tsErr = err
			return
		}
		clientID, clientSecret, err := cfg.clientCredentials(p.getenv)
		if err != nil {
			p.tsErr = err
			return
		}

		conf := newOAuthConfig(clientID, clientSecret, "")
		p.ts = newPersistingTokenSource(conf.TokenSource(ctx, tok), path)
	})
	return p.ts, p.tsErr
}

// Describe is called once, immediately after the handshake, before any
// other RPC — including before any source using this plugin has been
// persisted (contract/plugin-contract.md:488-501: the kernel may
// trial-launch this binary and call Describe against fields the operator
// has typed but not yet saved, sometimes before any credential value has
// even been entered). A well-behaved Describe is therefore idempotent and
// side-effect-free regardless of call site — this implementation performs
// no I/O of any kind and branches on neither its context nor its request,
// which is why Describe returns an identical response for a nil request,
// an empty request, and a populated request alike.
//
// This is also the CONTRACT-GAPS.md GAP-04 resolution: even though every
// declared ExtrasField below is required: true, Describe never validates
// that any of them are actually configured. Fail-loud-on-missing-required-
// key enforcement is deferred to Match/Health (Phase 2+), where a missing
// credential is a genuine failure rather than an expected state during an
// add-source form fill.
func (p *SourcePlugin) Describe(_ context.Context, _ *toposv1.DescribeRequest) (*toposv1.DescribeResponse, error) {
	return &toposv1.DescribeResponse{
		SourceType:      sourceType,
		DisplayName:     displayName,
		ContractVersion: contractVersion,
		// Copied (not returned by reference) so that no caller can
		// mutate the shared package-level backing array and corrupt
		// every subsequent Describe response for the process's
		// lifetime — matching the fresh-per-call construction Extras
		// already uses below.
		MatchVocabulary: append([]string(nil), matchVocabulary...),
		// Extras declares the exact three provider-specific config keys
		// this plugin expects (PRD.md:118-122), verbatim — key, label, and
		// placeholder are copied byte-for-byte out of that table, not
		// retyped, because the folder_id placeholder contains a U+2014 EM
		// DASH that a hand-typed hyphen would silently break. The secret
		// flag on client_id/client_secret is load-bearing: it routes both
		// fields through the host's own secret-handling input control.
		Extras: []*toposv1.ExtrasField{
			{
				Key:         "client_id",
				Label:       "OAuth Client ID",
				Required:    true,
				Secret:      true,
				Placeholder: "GDRIVE_CLIENT_ID",
			},
			{
				Key:         "client_secret",
				Label:       "OAuth Client Secret",
				Required:    true,
				Secret:      true,
				Placeholder: "GDRIVE_CLIENT_SECRET",
			},
			{
				Key:         "folder_id",
				Label:       "Drive Folder ID",
				Required:    true,
				Secret:      false,
				Placeholder: "e.g. 1a2B3cD4EfGhIjKlmNoPQRstuVwxYZ — the id segment of the folder's Drive URL",
			},
		},
		// Icon/IconMime deliberately left unset — both are optional
		// (contract/plugin.proto:41-70) and this plugin ships no icon
		// asset in Phase 1.
	}, nil
}

// Match is called only at sync time, never at request time
// (contract/plugin-contract.md's Match section). Resolves this plugin's
// token source (ensureTokenState — the same classifier Health itself
// consults, so a token-resolution or refresh failure names the identical
// health state Health would report for the same underlying state,
// preserving TestHealthAndMatch_AgreeOnTheNeverAuthorizedSentence), then
// the configured folder id, then ensureSynced's persisted-vs-first-run
// decision (syncengine.go), then filters the resulting tree against the
// "folders" match field (match.go), then attaches a live, bounded preview
// to every returned item (preview.go's attachPreviews, CONT-01/CONT-04) —
// fetched fresh from Drive on every Match call, never cached. Every
// failure at every stage through folder/sync resolution returns
// codes.Unavailable — real Drive traffic returning an empty item set
// would be indistinguishable from a genuine zero-match result, which is
// exactly the confusion the Phase 1 stub comment already warned against.
// A preview-attachment failure never reaches this error path: attachPreviews
// degrades individual items' Preview fields internally and always returns.
func (p *SourcePlugin) Match(ctx context.Context, req *toposv1.MatchRequest) (*toposv1.MatchResponse, error) {
	if state, msg := p.ensureTokenState(ctx); state != stateHealthy {
		return nil, status.Error(codes.Unavailable, msg)
	}

	cfg, err := loadSourceConfig(p.getenv)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	folderID, err := cfg.folderID()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}

	st, err := p.ensureSynced(ctx, folderID)
	if err != nil {
		// classifyDriveError inspects only err's structured Drive fields;
		// when it cannot classify a cause it returns (stateUnclassified,
		// false), and unhealthyMessage falls back to the existing wrapped
		// error text for a Drive failure that names no PRD-mandated cause.
		state, _ := classifyDriveError(err)
		return nil, status.Error(codes.Unavailable, unhealthyMessage(state, err.Error()))
	}

	items := matchItems(st, req)

	svc, err := p.driveService(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	attachPreviews(ctx, svc, st.Tree, items)

	return &toposv1.MatchResponse{Items: items}, nil
}

// Fetch is called only at request time — when a user or agent opens a
// specific item — never from the sync/Match path
// (contract/plugin-contract.md's Fetch section). Delegates to
// fetchcontent.go's fetchItem, which mirrors Match's own token -> config
// -> folder -> sync resolution sequence and then fetches content fresh
// from Drive on every call — no cache, no memoization, no content ever
// persisted beyond the bounded preview Match already returns (CONT-04).
func (p *SourcePlugin) Fetch(ctx context.Context, req *toposv1.FetchRequest) (*toposv1.FetchResponse, error) {
	return p.fetchItem(ctx, req)
}

// Health is a lightweight reachability probe
// (contract/plugin-contract.md's Health section). Classifies the token
// stage first (ensureTokenState — the same classifier Match's token branch
// consults): if that yields anything other than a healthy token, Health
// returns immediately with Reachable: false and unhealthyMessage of that
// state, WITHOUT ever touching Drive — a plugin with no valid token has
// nothing live to probe. Only a healthy token proceeds to
// probeDriveReachable's live, single, non-retried Drive call, so all four
// PRD-named causes are reachable from Health, not only the two
// token-resolution ones. Reachable is true, with an empty LastError, only
// when that probe itself succeeds — never assumed from a healthy token
// alone (05-RESEARCH.md Pitfall 3). LastSyncUnix is populated in every
// branch from lastSyncUnix() (GAP-19): the persisted sync-state file's own
// modification time in Unix seconds, or 0 when that file does not exist.
// Health returns a nil Go error in all cases it can reach — the contract
// forbids a gRPC error from Health itself.
func (p *SourcePlugin) Health(ctx context.Context, _ *toposv1.HealthRequest) (*toposv1.HealthResponse, error) {
	lastSync := p.lastSyncUnix()

	if state, msg := p.ensureTokenState(ctx); state != stateHealthy {
		return &toposv1.HealthResponse{
			Reachable:    false,
			LastError:    msg,
			LastSyncUnix: lastSync,
		}, nil
	}

	state, msg := p.probeDriveReachable(ctx)
	return &toposv1.HealthResponse{
		Reachable:    state == stateHealthy,
		LastError:    msg,
		LastSyncUnix: lastSync,
	}, nil
}

// probeDriveReachable issues exactly ONE live, non-retried Drive call —
// files.get on the configured folder id, requesting only Fields("id"),
// bounded by healthProbeTimeout — and classifies the outcome into a
// healthState plus its already-resolved operator-visible message (empty on
// success). Resolving the source config or folder id first is outside the
// PRD's four named causes (a config problem, not a live-reachability one),
// so a failure there reports stateUnclassified with the existing
// fail-loud-by-name error text, exactly like the failure this same
// resolution already produces elsewhere in this file. A Drive-service
// construction failure is classified the same way. On a probe failure,
// classifyDriveError determines the state; when it cannot classify a
// cause, unhealthyMessage falls back to healthProbeFailed. On success,
// returns (stateHealthy, "").
//
// Deliberately NEVER wrapped by 05-02's retry decorator: the contract
// calls Health live and uncached on every dashboard poll
// (contract/plugin-contract.md:806-808), so a rate-limited poll must
// report the rate-limited sentence immediately rather than block behind a
// backoff.
func (p *SourcePlugin) probeDriveReachable(ctx context.Context) (healthState, string) {
	cfg, err := loadSourceConfig(p.getenv)
	if err != nil {
		return stateUnclassified, err.Error()
	}
	folderID, err := cfg.folderID()
	if err != nil {
		return stateUnclassified, err.Error()
	}

	svc, err := p.driveService(ctx)
	if err != nil {
		return stateUnclassified, err.Error()
	}

	probeCtx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()

	if _, err := svc.Files.Get(folderID).Fields("id").Context(probeCtx).Do(); err != nil {
		state, _ := classifyDriveError(err)
		return state, unhealthyMessage(state, healthProbeFailed)
	}

	return stateHealthy, ""
}

// lastSyncUnix implements GAP-19's resolution for
// HealthResponse.LastSyncUnix: the persisted sync-state file's own
// modification time in Unix seconds, or 0 on any error, including the file
// not existing at all (never synced). No Drive call, no schema change to
// syncstate.json — a single filesystem stat on the already-resolved
// syncStatePath.
func (p *SourcePlugin) lastSyncUnix() int64 {
	path, err := syncStatePath(p.getenv)
	if err != nil {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().Unix()
}

// ensureTokenState is ensureTokenValid's sibling: it performs the identical
// resolve-then-mint sequence but returns both the classified healthState
// and its operator-visible message (unhealthyMessage(state, fallback),
// where fallback is the matching Phase 2 diagnostic constant) rather than
// a plain error. BOTH branches — the tokenSource resolution failure and
// the ts.Token() refresh failure — classify through classifyTokenError, so
// Health, Match, and the RPC-06 spike harness (authfailurespike_test.go)
// consume the identical mapping and can never disagree about what a given
// token failure is called. Health and Match's token-failure branches both
// call this — never ensureTokenValid — on success it returns
// (stateHealthy, healthAuthorized). Fetch keeps using ensureTokenValid's
// plain-error signature (fetchcontent.go), which is unchanged below.
func (p *SourcePlugin) ensureTokenState(ctx context.Context) (healthState, string) {
	ts, err := p.tokenSource(ctx)
	if err != nil {
		fallback := healthCauseForResolutionError(err)
		state := classifyTokenError(err)
		return state, unhealthyMessage(state, fallback)
	}
	if _, err := ts.Token(); err != nil {
		state := classifyTokenError(err)
		return state, unhealthyMessage(state, healthRefreshFailed)
	}
	return stateHealthy, healthAuthorized
}

// ensureTokenValid resolves and validates this plugin's token source,
// returning nil on success or an error carrying the matching Phase 2
// status-constant text otherwise. Shared by ensureTokenState (which
// performs the identical sequence but additionally reports the classified
// healthState) and Fetch (which proceeds to folder/Drive resolution on
// success instead) so every caller classifies the same underlying failure
// identically.
func (p *SourcePlugin) ensureTokenValid(ctx context.Context) error {
	ts, err := p.tokenSource(ctx)
	if err != nil {
		return errors.New(healthCauseForResolutionError(err))
	}
	if _, err := ts.Token(); err != nil {
		return errors.New(healthRefreshFailed)
	}
	return nil
}

// healthCauseForResolutionError classifies a tokenSource resolution error
// into one of the two resolution-stage Phase 2 status constants: a missing
// or malformed token file (fs.ErrNotExist or errTokenFileMalformed) maps to
// healthNoTokenFile; any other resolution failure (a WEBSPACES_SOURCE_CONFIG
// decode failure or a missing client credential) maps to
// healthNoClientCredentials, since token-file loading is attempted first
// inside tokenSource and always fails first when the token file itself is
// the problem.
func healthCauseForResolutionError(err error) string {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errTokenFileMalformed) {
		return healthNoTokenFile
	}
	return healthNoClientCredentials
}
