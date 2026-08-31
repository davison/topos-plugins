// Package main's changepoll.go polls Google Drive's account-wide
// changes.list feed from a stored page token, hand-rolling the pagination
// loop ChangesListCall has no Pages() helper for (unlike FilesListCall),
// classifies a stale (410 Gone) page token, and filters the unscoped
// change stream default-deny against this plugin's own locally cached
// folder-membership tree — the only access-control boundary standing
// between the operator's entire Drive and what this plugin ever hands the
// host (T-03-07, T-03-08). syncengine.go's ensureSynced wires the whole
// delta path in.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

// pollChanges drains every page of changes.list starting from pageToken,
// returning the concatenated changes in the exact order Drive returned
// them (page-then-index) and the newStartPageToken to persist for next
// time. ChangesListCall has no Pages() convenience helper (confirmed by
// its absence on the generated client, 03-RESEARCH.md Pitfall 5), so this
// loop is hand-written: NewStartPageToken is recorded whenever it is
// non-empty (Drive sends it only on the final page — the two token fields
// must never be confused, one advances the loop, the other is what gets
// persisted) and the loop terminates when NextPageToken is empty. On any
// error, pollChanges returns the wrapped error with a nil change slice and
// an empty token — never a partial batch presented as complete.
//
// Each page's own changes.list call is individually wrapped in withRetry
// (the fourth of RES-01's four sanctioned call sites) — one wrap per page
// request, inside this loop, rather than around the whole drain — so a
// transient failure on, say, page three retries only page three rather
// than restarting the entire drain from page one. isStalePageToken below
// stays completely untouched by this: 410 is outside withRetry's retryable
// set, so it still arrives at the classifier on the first attempt exactly
// as it does today, precisely what that function's own doc comment already
// anticipated.
func pollChanges(ctx context.Context, svc *drive.Service, pageToken string) ([]*drive.Change, string, error) {
	var changes []*drive.Change
	var newToken string

	token := pageToken
	for {
		var resp *drive.ChangeList
		err := withRetry(ctx, func(ctx context.Context) error {
			r, err := svc.Changes.List(token).
				Fields("nextPageToken, newStartPageToken, changes(fileId, removed, file(id, name, mimeType, parents, modifiedTime, webViewLink, trashed, size))").
				IncludeRemoved(true).
				PageSize(1000).
				Context(ctx).
				Do()
			if err != nil {
				return err
			}
			resp = r
			return nil
		})
		if err != nil {
			return nil, "", fmt.Errorf("poll changes: %w", err)
		}

		changes = append(changes, resp.Changes...)
		if resp.NewStartPageToken != "" {
			newToken = resp.NewStartPageToken
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	return changes, newToken, nil
}

// isStalePageToken reports whether err is Drive's documented 410 Gone
// response for a page token whose change history is no longer available
// (03-RESEARCH.md Pitfall 4) — the one signal that means the stored token
// and tree can no longer be trusted and a full resync is required. This is
// the only place an HTTP status is inspected; kept as a small pure
// function so Phase 5's RES-01 retry decorator can wrap the caller without
// touching it.
func isStalePageToken(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == http.StatusGone
}

// reachesRoot walks parentID upward through tree (the plugin's own locally
// cached membership state, never a live Drive call) until it reaches
// rootID, returning true, or runs out of known ancestors, returning
// false. This is default-deny by construction (T-03-07): an empty
// parentID, an ancestor absent from tree, or an exhausted chain are all
// treated as NOT reachable — a file is never assumed in scope, it must be
// provably reachable. The walk is bounded by the tree's own entry count
// so a cyclic or self-parented entry can never hang (T-03-09).
func reachesRoot(tree map[string]*driveNode, parentID, rootID string) bool {
	limit := len(tree) + 1
	for i := 0; parentID != ""; i++ {
		if parentID == rootID {
			return true
		}
		if i >= limit {
			return false
		}
		node, ok := tree[parentID]
		if !ok {
			return false
		}
		parentID = node.ParentID
	}
	return false
}

// applyChange applies one Drive change to tree, returning whether the
// change was resolvable. A Removed change, a change whose File is nil, or
// a change whose File.Trashed is true all delete the entry and report
// resolved — this covers explicit deletion, trashing, and loss of access
// alike. A file with an empty Parents slice can never be a descendant of
// the configured root, so it too deletes any existing entry and reports
// resolved. When the (guarded, len(...) > 0) first parent is present in
// tree or IS rootID, reachesRoot decides whether the node is in scope: a
// reachable parent upserts the node with its current Name, MimeType,
// ParentID, ModifiedTime, and WebViewLink and reports resolved; an
// unreachable-but-KNOWN parent (present in tree, chain just doesn't reach
// root) deletes any existing entry and reports resolved — that parent
// chain is definitively out of scope, not merely undetermined. Only a
// parent entirely ABSENT from tree is genuinely undetermined: applyChange
// changes nothing and reports NOT resolved, the deferral signal
// applyChanges' fixpoint loop acts on, rather than reaching for a
// files.get call this plugin's API surface (COVERAGE.md) does not
// integrate this phase.
func applyChange(tree map[string]*driveNode, rootID string, ch *drive.Change) bool {
	id := ch.FileId

	if ch.Removed || ch.File == nil || ch.File.Trashed {
		delete(tree, id)
		return true
	}

	f := ch.File
	var parentID string
	if len(f.Parents) > 0 {
		parentID = f.Parents[0]
	}
	if parentID == "" {
		delete(tree, id)
		return true
	}

	if parentID != rootID {
		if _, ok := tree[parentID]; !ok {
			return false // parent unknown: undetermined, deferred to fixpoint
		}
	}

	if !reachesRoot(tree, parentID, rootID) {
		delete(tree, id) // moved out of scope (or was never in scope)
		return true
	}

	tree[id] = &driveNode{
		Name:         f.Name,
		MimeType:     f.MimeType,
		ParentID:     parentID,
		ModifiedTime: f.ModifiedTime,
		WebViewLink:  f.WebViewLink,
		Size:         f.Size,
	}
	return true
}

// applyChanges applies changes to tree (applyChangesToFixpoint), then
// runs pruneUnreachable over the settled result. The ordering is
// load-bearing: pruning before or during the fixpoint would delete a
// node whose newly created parent folder arrives later in the same batch
// (03-RESEARCH.md Pitfall 3) — the prune runs only against the settled
// tree. A non-zero prune is logged as a count only — no id, no name —
// per the standing OPS-03 log discipline.
func applyChanges(tree map[string]*driveNode, rootID string, changes []*drive.Change) {
	applyChangesToFixpoint(tree, rootID, changes)
	if n := pruneUnreachable(tree, rootID); n > 0 {
		log.Printf("gdrive: sync: pruned %d entries whose ancestor chain no longer reaches the configured root", n)
	}
}

// pruneUnreachable removes every tree entry whose ancestor chain no
// longer resolves to rootID, returning the number removed. This is the
// compensating pass for Drive's changes.list feed never emitting a
// change event for the descendants of a folder that was itself trashed,
// deleted, or moved out of the watched subtree — without it, those
// descendants would sit orphaned in the tree (and in syncstate.json)
// indefinitely (CR-01 / T-03-20).
//
// Reachability is decided by reachesRoot itself — never a second upward
// walk, since two walkers with drifting semantics are the exact defect
// class plan 03-04 exists to close. The pass is two-phase so the outcome
// is order-independent by construction: first collect every orphaned id
// against the unmutated tree (a broken chain is broken at its topmost
// missing ancestor, so every transitive descendant fails the same walk),
// then delete.
func pruneUnreachable(tree map[string]*driveNode, rootID string) int {
	var orphaned []string
	for id, node := range tree {
		if !reachesRoot(tree, node.ParentID, rootID) {
			orphaned = append(orphaned, id)
		}
	}
	for _, id := range orphaned {
		delete(tree, id)
	}
	return len(orphaned)
}

// applyChangesToFixpoint applies changes to tree strictly in the order
// received, re-running the unresolved subset repeatedly until a full
// pass resolves nothing new (fixpoint) — this is what lets a newly
// created file whose newly created parent folder arrives later in the
// SAME batch resolve on this sync rather than silently waiting for the
// next one (03-RESEARCH.md Pitfall 3). Everything still unresolved at
// fixpoint is out of scope by default-deny: any existing entry for it is
// deleted.
func applyChangesToFixpoint(tree map[string]*driveNode, rootID string, changes []*drive.Change) {
	pending := changes
	for len(pending) > 0 {
		var next []*drive.Change
		resolvedAny := false
		for _, ch := range pending {
			if applyChange(tree, rootID, ch) {
				resolvedAny = true
				continue
			}
			next = append(next, ch)
		}
		if !resolvedAny {
			// Fixpoint reached: nothing left in `next` resolved this pass,
			// so every remaining change's parent chain never reaches root
			// through this tree. Out of scope by default-deny.
			for _, ch := range next {
				delete(tree, ch.FileId)
			}
			return
		}
		pending = next
	}
}
