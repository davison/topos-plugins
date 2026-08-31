// Package main's driveclient.go is this plugin's single *drive.Service
// construction point — nothing else in this codebase builds one, and
// nothing here re-resolves credentials. newDriveService is always called
// with the exact oauth2.TokenSource SourcePlugin.tokenSource already
// resolved (plugin.go), never a second, independently-derived credential
// path (T-03-05).
package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/googleapis/gax-go/v2"
	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// newDriveService builds a *drive.Service from ts, the already-resolved
// OAuth token source. No Shared Drive parameter (supportsAllDrives,
// includeItemsFromAllDrives, driveId, corpora) is ever set on any request
// this plugin issues (folderwalk.go) — v1 targets My Drive only, the
// recorded disposition of the deferred SYNC-V2-01.
func newDriveService(ctx context.Context, ts oauth2.TokenSource) (*drive.Service, error) {
	return drive.NewService(ctx, option.WithTokenSource(ts))
}

// retryInitialDelay, retryMaxDelay, and retryMultiplier tune retryBackoff
// below — Google's own suggested starting point for a client-side retry
// loop (05-RESEARCH.md Code Examples, cross-checked against
// gax-go/v2@v2.23.0's own Backoff doc comment, which defaults to the
// identical values when left zero). Widening or narrowing any of the three
// is a local, reversible edit covered by retry_test.go's own tests — see
// this plan's <reversibility> note.
const (
	retryInitialDelay = time.Second
	retryMaxDelay     = 30 * time.Second
	retryMultiplier   = 2.0
)

// retryBackoff is withRetry's single source of every retry delay
// (RES-01): gax-go/v2's own Backoff.Pause already implements full-jitter
// exponential backoff — `1 + rand.Int63n(cur)`, capped at Max and growing
// by Multiplier each attempt (call_option.go:184-208, read directly from
// the pinned v2.23.0 source this phase's research verified) — so this
// plugin computes no delay of its own; TestRetryPath_ComputesNoDelayOfItsOwn
// (retry_test.go) is the standing AST gate proving that. gax.OnHTTPCodes
// takes Backoff BY VALUE and each Retryer keeps its own copy
// (call_option.go's own doc comment: "bo is only used for its
// parameters"), so this package-level var is safe to read concurrently —
// every withRetry call gets an independently-mutating copy, never a
// shared one. It is package-level, rather than a local literal inside
// withRetry, solely so retry_test.go's withFastRetryBackoff helper can
// substitute a millisecond-scale value for the duration of a test; tests
// that do so must not run in parallel with each other, since this is a
// single, package-level variable, never per-call state.
var retryBackoff = gax.Backoff{
	Initial:    retryInitialDelay,
	Max:        retryMaxDelay,
	Multiplier: retryMultiplier,
}

// withRetry wraps call with RES-01's shared retry-with-backoff-and-jitter
// decorator, built on gax.Invoke + gax.OnHTTPCodes(retryBackoff, 429, 500,
// 502, 503) — the exact primitive Google's own generated API clients use
// internally for this purpose (05-RESEARCH.md Don't Hand-Roll). It has
// exactly four sanctioned call sites in this codebase — startPageToken and
// rootFolderName (folderwalk.go), walkFolder's paged files.list call
// (folderwalk.go), and pollChanges' change-list call (changepoll.go) — and
// TestRetryDecorator_WrapsExactlyTheFourSyncCriticalCallSites
// (retry_test.go) is the standing source-level gate proving no fifth site
// is ever added. It is deliberately NEVER used by two other call shapes in
// this codebase: plugin.go's probeDriveReachable (Health's own live probe),
// because the contract requires Health to stay cheap and never cached
// (contract/plugin-contract.md:806-808) — a rate-limited dashboard poll
// must report the rate-limited sentence immediately, not block behind a
// backoff sequence whose Max alone is 30 seconds; and preview.go's/
// workspaceexport.go's preview-attachment paths, because a preview fetch
// already degrades silently to an empty string by design (CONT-03/CONT-04)
// and is not part of the sync-completeness guarantee RES-01/RES-02 exist to
// protect — retrying it would only slow every Match call for a per-item,
// non-fatal concern.
//
// 410 (a stale page token) is deliberately absent from the retryable set:
// it is a signal to discard the stored state and resync, not a transient
// condition, and changepoll.go's isStalePageToken depends on it arriving
// unretried. 400, 401, 403, and 404 are absent too — a permanently
// rejected credential or a deleted/inaccessible folder must fail on the
// first attempt rather than hammer Google with retries that can never
// succeed (05-RESEARCH.md's Known Threat Patterns: an unbounded retry
// loop against a permanently-failing credential is itself a
// self-inflicted denial-of-service risk against the operator's own quota).
//
// gax.Invoke has one behavior this decorator must handle explicitly, or
// RES-02 cannot work: when the retry loop is ended by the CALLER's context
// expiring (rather than by a non-retryable response), gax.Invoke returns
// ctx.Err() and discards the last observed call error — confirmed by
// reading gax-go/v2@v2.23.0's own invoke.go this phase's research session
// (the sp(ctx, d) sleep-interrupt path: "else if err = sp(ctx, d); err !=
// nil { return err }"). Without correcting for this, an exhausted retry
// sequence against a sustained rate limit would surface a bare "context
// deadline exceeded" instead of the classifiable Drive error
// classifyDriveError needs to name it "rate limited" rather than merely
// "timed out." withRetry corrects for this itself: the invoked closure
// captures each attempt's own error into lastCallErr, and when gax.Invoke's
// returned error is a context cancellation or deadline AND a distinct,
// non-context call error was actually observed, the two are joined with
// errors.Join so errors.As traversal — which is what every downstream
// classifier (isStalePageToken, classifyDriveError) relies on, never a type
// assertion — can still reach the underlying *googleapi.Error through the
// chain. gax.Invoke may also itself substitute an *apierror.APIError
// wrapper for the original error before returning it (apierror.go's own
// Unwrap method still makes it errors.As-traversable) — which is exactly
// why no caller of withRetry may ever type-assert its returned error.
func withRetry(ctx context.Context, call func(context.Context) error) error {
	var lastCallErr error
	err := gax.Invoke(ctx, func(ctx context.Context, _ gax.CallSettings) error {
		attemptErr := call(ctx)
		if attemptErr != nil {
			lastCallErr = attemptErr
		}
		return attemptErr
	}, gax.WithRetry(func() gax.Retryer {
		return gax.OnHTTPCodes(retryBackoff,
			http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
		)
	}))
	if err == nil {
		return nil
	}
	if isContextExpiry(err) && lastCallErr != nil && !isContextExpiry(lastCallErr) {
		return errors.Join(err, lastCallErr)
	}
	return err
}

// isContextExpiry reports whether err is (or wraps) exactly the context
// package's own cancellation/deadline sentinels — the two errors
// gax.Invoke's sleep-interrupt path can return in place of the last
// observed call error.
func isContextExpiry(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
