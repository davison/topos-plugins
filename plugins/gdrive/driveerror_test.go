// Package main's driveerror_test.go covers classifyDriveError's whole
// decision table, including the decoy case that proves message text can
// never route a classification — mirroring workspaceexport_test.go's own
// decoy test for classifyExportError (04-02).
package main

import (
	"errors"
	"net/http"
	"testing"

	"google.golang.org/api/googleapi"
)

func TestClassifyDriveError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantOK    bool
		wantState healthState
	}{
		{
			name:      "404NotFound",
			err:       &googleapi.Error{Code: http.StatusNotFound},
			wantOK:    true,
			wantState: stateFolderInaccessible,
		},
		{
			name: "403InsufficientFilePermissions",
			err: &googleapi.Error{
				Code:   http.StatusForbidden,
				Errors: []googleapi.ErrorItem{{Reason: reasonInsufficientFilePermissions}},
			},
			wantOK:    true,
			wantState: stateFolderInaccessible,
		},
		{
			name:      "429TooManyRequests",
			err:       &googleapi.Error{Code: http.StatusTooManyRequests},
			wantOK:    true,
			wantState: stateRateLimited,
		},
		{
			name: "403RateLimitExceeded",
			err: &googleapi.Error{
				Code:   http.StatusForbidden,
				Errors: []googleapi.ErrorItem{{Reason: reasonRateLimitExceeded}},
			},
			wantOK:    true,
			wantState: stateRateLimited,
		},
		{
			name: "403UserRateLimitExceeded",
			err: &googleapi.Error{
				Code:   http.StatusForbidden,
				Errors: []googleapi.ErrorItem{{Reason: reasonUserRateLimitExceeded}},
			},
			wantOK:    true,
			wantState: stateRateLimited,
		},
		{
			name: "403DailyLimitExceeded",
			err: &googleapi.Error{
				Code:   http.StatusForbidden,
				Errors: []googleapi.ErrorItem{{Reason: reasonDailyLimitExceeded}},
			},
			wantOK:    true,
			wantState: stateRateLimited,
		},
		{
			name: "403SharingRateLimitExceeded",
			err: &googleapi.Error{
				Code:   http.StatusForbidden,
				Errors: []googleapi.ErrorItem{{Reason: reasonSharingRateLimitExceeded}},
			},
			wantOK:    true,
			wantState: stateRateLimited,
		},
		{
			name:      "403EmptyErrorsSlice",
			err:       &googleapi.Error{Code: http.StatusForbidden},
			wantOK:    false,
			wantState: stateUnclassified,
		},
		{
			name: "403UnrecognisedReason",
			err: &googleapi.Error{
				Code:   http.StatusForbidden,
				Errors: []googleapi.ErrorItem{{Reason: "somethingElseEntirely"}},
			},
			wantOK:    false,
			wantState: stateUnclassified,
		},
		{
			// The decoy case: an error whose ONLY rate-limit-looking text
			// sits in the human-readable Message field, with an empty
			// structured Errors slice, must NOT classify — proving
			// classifyDriveError reads only structured fields, never
			// Message or err.Error()'s formatted text (05-RESEARCH.md
			// Pattern 1, mirroring workspaceexport_test.go's own decoy).
			name: "DecoyMessageTextNeverRoutesAClassification",
			err: &googleapi.Error{
				Code:    http.StatusForbidden,
				Message: "rateLimitExceeded: you have exceeded your rate limit, please slow down",
			},
			wantOK:    false,
			wantState: stateUnclassified,
		},
		{
			name: "MultiTokenPrecedenceFirstMatchWins",
			err: &googleapi.Error{
				Code: http.StatusForbidden,
				Errors: []googleapi.ErrorItem{
					{Reason: reasonInsufficientFilePermissions},
					{Reason: reasonRateLimitExceeded},
				},
			},
			wantOK:    true,
			wantState: stateFolderInaccessible,
		},
		{
			name: "MultiTokenPrecedenceRateLimitFirst",
			err: &googleapi.Error{
				Code: http.StatusForbidden,
				Errors: []googleapi.ErrorItem{
					{Reason: reasonRateLimitExceeded},
					{Reason: reasonInsufficientFilePermissions},
				},
			},
			wantOK:    true,
			wantState: stateRateLimited,
		},
		{
			name:      "NilError",
			err:       nil,
			wantOK:    false,
			wantState: stateUnclassified,
		},
		{
			name:      "NonDriveError",
			err:       errors.New("plain network failure"),
			wantOK:    false,
			wantState: stateUnclassified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, ok := classifyDriveError(tt.err)
			if ok != tt.wantOK {
				t.Errorf("classifyDriveError(%v) ok = %v, want %v", tt.err, ok, tt.wantOK)
			}
			if state != tt.wantState {
				t.Errorf("classifyDriveError(%v) state = %v, want %v", tt.err, state, tt.wantState)
			}
		})
	}
}
