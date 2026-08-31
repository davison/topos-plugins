// Package main's syncengine.go wires this plugin's whole sync decision:
// ensureSynced loads the persisted sync-state file if one exists and is
// well-formed and polls changes.list from its stored token (changepoll.go)
// to bring it up to date; an absent, malformed, or 410-stale state instead
// triggers a full first-run walk (resync, GAP-11's resolved
// token-capture-before-walk ordering). Either path's resulting tree and
// change token are persisted together in exactly ONE atomic saveSyncState
// call — the token must never advance in a write that did not also store
// the tree it corresponds to (T-03-10). driveService lazily resolves this
// plugin's single *drive.Service construction point, built from the exact
// oauth2.TokenSource SourcePlugin.tokenSource already resolves — never a
// second credential path (T-03-05).
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"google.golang.org/api/drive/v3"
)

// syncRetryDeadline bounds ensureSynced's whole resolve-and-persist
// sequence (RES-02). This bound is LOAD-BEARING, not stylistic:
// driveclient.go's retryBackoff (gax-go/v2's own Backoff) carries no
// maximum retry count, so without an outer deadline a sustained rate limit
// against any of RES-01's four sanctioned withRetry call sites would retry
// forever and a dashboard-driven sync would never return — the operator
// would see neither the completed item set nor the honest rate-limited
// sentence, only a hung call. A derived context.WithTimeout takes the
// EARLIER of the two deadlines it is built from, so a host that already
// sets its own, tighter RPC deadline on the incoming ctx keeps control —
// this constant only ever tightens an already-bounded call, never loosens
// one.
const syncRetryDeadline = 2 * time.Minute

// driveService resolves this plugin's single *drive.Service, guarded by
// sync.Once so repeated calls resolve it exactly once per process
// lifetime — mirroring tokenSource's own once-guarded shape (plugin.go).
func (p *SourcePlugin) driveService(ctx context.Context) (*drive.Service, error) {
	p.driveOnce.Do(func() {
		ts, err := p.tokenSource(ctx)
		if err != nil {
			p.svcErr = err
			return
		}
		svc, err := newDriveService(ctx, ts)
		if err != nil {
			p.svcErr = fmt.Errorf("construct drive service: %w", err)
			return
		}
		p.svc = svc
	})
	return p.svc, p.svcErr
}

// ensureSynced resolves this plugin's current sync state and persists it
// in exactly ONE atomic state-save call, made here and nowhere else in
// this file (T-03-10's standing grep-count gate over this source file).
// Holds p.syncMu for the whole operation so a
// concurrent call can never observe or interleave two overlapping syncs.
// On any error BEFORE that single save call — including a failed poll, a
// failed walk, or the save itself failing — ensureSynced returns the error
// and the previously persisted state is left completely untouched: the
// in-memory updated state is never handed to the caller unless it was
// actually written, so a failed sync's next attempt re-polls or re-derives
// the same window rather than silently skipping it.
func (p *SourcePlugin) ensureSynced(ctx context.Context, folderID string) (*syncState, error) {
	p.syncMu.Lock()
	defer p.syncMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, syncRetryDeadline)
	defer cancel()

	path, err := syncStatePath(p.getenv)
	if err != nil {
		return nil, fmt.Errorf("resolve sync state path: %w", err)
	}

	svc, err := p.driveService(ctx)
	if err != nil {
		return nil, err
	}

	fresh, err := resolveSyncState(ctx, svc, path, folderID)
	if err != nil {
		return nil, err
	}

	if err := saveSyncState(path, fresh); err != nil {
		return nil, err
	}
	return fresh, nil
}

// resolveSyncState computes this sync's resulting *syncState WITHOUT
// persisting it — ensureSynced's single saveSyncState call is what
// actually writes it. A persisted, well-formed syncstate.json is brought
// up to date via deltaSyncState's changes.list poll (zero files.list
// requests, SYNC-02); an absent or malformed one, or a poll that fails
// with a stale (410) page token, instead falls through to resync's full
// first-run walk — the SAME helper both the first-run and 410 branches
// share, rather than two independent copies of that logic.
func resolveSyncState(ctx context.Context, svc *drive.Service, path, folderID string) (*syncState, error) {
	st, err := loadSyncState(path)
	switch {
	case err == nil:
		fresh, derr := deltaSyncState(ctx, svc, st)
		if derr == nil {
			return fresh, nil
		}
		if !isStalePageToken(derr) {
			return nil, derr
		}
		// Stale page token: the stored token and tree can no longer be
		// trusted — discard both and fall through to a full resync,
		// exactly as if this were the first run.
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, errSyncStateMalformed):
		// Never synced, or synced before but now corrupt — either way, a
		// full first-run walk is the correct, honest recovery: this
		// repository's standing rule (loadToken's same distinction)
		// never continues on a partial or untrusted state.
	default:
		return nil, err
	}
	return resync(ctx, svc, folderID)
}

// resync performs a full first-run walk: capture the
// changes.getStartPageToken value BEFORE the walk begins (GAP-11), fetch
// the root's own name, and breadth-first-walk the whole tree. Returns the
// resulting *syncState WITHOUT persisting it — shared, unpersisted, by
// both resolveSyncState's absent/malformed branch and its 410-stale
// branch. A failure at any step returns the wrapped error and no partial
// state.
func resync(ctx context.Context, svc *drive.Service, folderID string) (*syncState, error) {
	token, err := startPageToken(ctx, svc)
	if err != nil {
		return nil, err
	}

	rootName, err := rootFolderName(ctx, svc, folderID)
	if err != nil {
		return nil, err
	}

	tree, err := walkFolder(ctx, svc, folderID)
	if err != nil {
		return nil, err
	}

	return &syncState{
		RootID:      folderID,
		RootName:    rootName,
		ChangeToken: token,
		Tree:        tree,
	}, nil
}

// deltaSyncState polls changes.list from st's persisted ChangeToken
// (changepoll.go's pollChanges) and applies the drained batch
// (applyChanges) to a shallow COPY of st's tree — st itself, and the
// caller's previously persisted state, are never mutated in place, so a
// caller that discards this function's result (e.g. because the
// subsequent saveSyncState call fails) has changed nothing. When the
// poll's own newToken is empty (Drive omits newStartPageToken on every
// page but the last, and a caller could in principle observe an empty
// batch with no persisted page boundary at all), the PREVIOUSLY persisted
// token is kept rather than overwritten with an empty one. Returns the
// wrapped poll error unchanged (including a 410) — resolveSyncState is
// what classifies and reacts to isStalePageToken, not this function.
//
// Known limitation (WR-02): the configured root is deliberately never a
// tree entry, so Drive's change feed cannot deliver an applicable change
// for it — the previous RootName is carried forward unchanged below, and
// a root rename in Drive is therefore not observable on the delta path
// until a full resync rebuilds the state. Refreshing the name here would
// require a files.get call, which COVERAGE.md marks OPT-OUT for Phase 3
// and flips to INTEGRATE in Phase 4; the refresh belongs there.
func deltaSyncState(ctx context.Context, svc *drive.Service, st *syncState) (*syncState, error) {
	changes, newToken, err := pollChanges(ctx, svc, st.ChangeToken)
	if err != nil {
		return nil, err
	}

	tree := make(map[string]*driveNode, len(st.Tree))
	for id, node := range st.Tree {
		tree[id] = node
	}
	applyChanges(tree, st.RootID, changes)

	token := st.ChangeToken
	if newToken != "" {
		token = newToken
	}

	return &syncState{
		RootID:      st.RootID,
		RootName:    st.RootName,
		ChangeToken: token,
		Tree:        tree,
	}, nil
}
