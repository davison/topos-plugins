// Package main's driveerror.go classifies a Google Drive API failure into
// this plugin's healthState enum (RPC-03/RPC-04): the two named causes a
// live Drive call can surface — the configured folder no longer being
// accessible, and Google rate-limiting or quota-exhausting this plugin.
// Mirrors the exact errors.As(err, &gerr) idiom changepoll.go's
// isStalePageToken and workspaceexport.go's classifyExportError already
// establish in this codebase (05-RESEARCH.md Pattern 1) — a third,
// differently-shaped classifier is deliberately not introduced. Inspects
// ONLY the structured HTTP status code and the structured Errors[].Reason
// tokens — NEVER the human-readable Message field or err.Error()'s
// formatted output, which carry no API stability guarantee. This is the
// only place a Reason token is compared, and driveerror_test.go's decoy
// case proves message text alone can never route a classification.
package main

import (
	"errors"
	"net/http"

	"google.golang.org/api/googleapi"
)

// Reason tokens Google's Drive API returns in a structured 403 response
// (developers.google.com/workspace/drive/api/guides/handle-errors,
// 05-RESEARCH.md Code Examples, [CITED]): the four rate/quota reasons and
// the one insufficient-permissions reason classifyDriveError matches
// against — never against Message or err.Error()'s formatted text.
const (
	reasonRateLimitExceeded        = "rateLimitExceeded"
	reasonUserRateLimitExceeded    = "userRateLimitExceeded"
	reasonDailyLimitExceeded       = "dailyLimitExceeded"
	reasonSharingRateLimitExceeded = "sharingRateLimitExceeded"

	reasonInsufficientFilePermissions = "insufficientFilePermissions"
)

// classifyDriveError classifies err — a failure from this plugin's live
// Health probe (plugin.go's probeDriveReachable) or its sync-critical Drive
// calls once 05-02's retry decorator gives up — into a healthState. errors.As
// (not a type assertion) is load-bearing: gax's invoke loop (05-02) wraps a
// Drive error in an apierror wrapper before returning it, and only
// errors.As traverses that wrapper's Unwrap chain back to the underlying
// *googleapi.Error.
//
// HTTP 404 classifies as stateFolderInaccessible immediately — a missing
// folder is unambiguous regardless of any structured Errors slice. The
// structured Errors slice is then iterated IN ITS OWN ORDER, matching each
// item's Reason field against the four rate-limit tokens
// (stateRateLimited) and the insufficient-permissions token
// (stateFolderInaccessible); when more than one entry matches, the FIRST
// match in that slice's own order wins — this precedence is pinned by a
// test in driveerror_test.go. As a final fallback, HTTP 429 with no
// matching structured reason still classifies as stateRateLimited, since
// Drive's 429 responses do not always carry a populated Errors slice. A
// nil error, a non-Drive error (no *googleapi.Error in its chain), an
// empty Errors slice with a status code outside {404, 429}, or a 403 with
// no recognised reason token all return (stateUnclassified, false) — the
// caller falls back to the healthProbeFailed internal diagnostic, never a
// verbatim sentence for a cause classifyDriveError could not actually
// confirm.
func classifyDriveError(err error) (state healthState, ok bool) {
	var gerr *googleapi.Error
	if !errors.As(err, &gerr) {
		return stateUnclassified, false
	}

	if gerr.Code == http.StatusNotFound {
		return stateFolderInaccessible, true
	}

	for _, item := range gerr.Errors {
		switch item.Reason {
		case reasonRateLimitExceeded, reasonUserRateLimitExceeded,
			reasonDailyLimitExceeded, reasonSharingRateLimitExceeded:
			return stateRateLimited, true
		case reasonInsufficientFilePermissions:
			return stateFolderInaccessible, true
		}
	}

	if gerr.Code == http.StatusTooManyRequests {
		return stateRateLimited, true
	}

	return stateUnclassified, false
}
