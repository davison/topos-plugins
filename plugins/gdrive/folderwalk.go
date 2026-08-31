// Package main's folderwalk.go performs the first-run breadth-first
// files.list walk under the configured root folder, and captures the
// changes.getStartPageToken value this plugin's later incremental sync
// (plan 03-02) polls from. Sequential, no goroutine fan-out
// (03-RESEARCH.md Open Question 1) — simpler to reason about for
// SYNC-05's kill/restart-resume guarantee and trivially safe with respect
// to Phase 5's future backoff wrapper.
package main

import (
	"context"
	"fmt"

	"google.golang.org/api/drive/v3"
)

// folderMimeType is Drive's own mimeType value for a folder object — used
// both to recurse the walk (a discovered folder is queued as a further
// parent to list) and, in match.go, to exclude folder objects from the
// Match item set (GAP-10).
const folderMimeType = "application/vnd.google-apps.folder"

// startPageToken captures the changes.list starting point via
// changes.getStartPageToken. Captured BEFORE the walk begins (GAP-11) so
// a file added to the folder during a slow first walk is redelivered by
// the very next changes.list poll rather than falling into the gap
// between "the walk observed the tree" and "the token started tracking
// changes." One of RES-01's four sanctioned withRetry call sites
// (driveclient.go) — a transient 429/5xx here retries with backoff rather
// than aborting the whole sync.
func startPageToken(ctx context.Context, svc *drive.Service) (string, error) {
	var token string
	err := withRetry(ctx, func(ctx context.Context) error {
		tok, err := svc.Changes.GetStartPageToken().Context(ctx).Do()
		if err != nil {
			return err
		}
		token = tok.StartPageToken
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("capture changes start page token: %w", err)
	}
	return token, nil
}

// rootFolderName fetches the configured root folder's own Name — the
// GAP-09 Option A value that, alone, matches everything synced by this
// instance. No other Drive call in this plugin's walk surfaces the root's
// own metadata: files.list only ever returns the root's CHILDREN, never
// the root object itself, so this one additional files.get call is the
// only way to learn the operator's own root folder name. The second of
// RES-01's four sanctioned withRetry call sites (driveclient.go).
func rootFolderName(ctx context.Context, svc *drive.Service, rootID string) (string, error) {
	var name string
	err := withRetry(ctx, func(ctx context.Context) error {
		f, err := svc.Files.Get(rootID).Fields("id, name").Context(ctx).Do()
		if err != nil {
			return err
		}
		name = f.Name
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("get root folder %s: %w", rootID, err)
	}
	return name, nil
}

// walkFolder breadth-first-traverses every subfolder under rootID,
// issuing one files.list call per folder level, and returns the full
// id-keyed tree of every file and folder discovered — draining the queue
// until it is empty, so a file nested arbitrarily deep below the
// configured root is present in the tree after one walk. Every node is
// stored under its Drive file id — never its name, which is untrusted,
// collaborator-controllable metadata (Assumption A3). Folder nodes are
// stored in the tree too (the ancestor-chain membership walk needs them)
// but carry their own MimeType so match.go can exclude them from the item
// set (GAP-10). ParentID is always the queue's own current parent id —
// this plugin's own scoping decision — never trusted from the response's
// own Parents slice. trashed = false is enforced both in the query AND by
// checking each returned file's own Trashed field explicitly, since a
// response may still report a file whose current state is trashed
// (03-RESEARCH.md Pitfall 2). A files.list failure at any folder level
// aborts the whole walk and returns the wrapped error — never a partial
// tree.
//
// The whole paged files.list call for one folder level — every page, not
// merely the per-page callback — is wrapped in withRetry (the third of
// RES-01's four sanctioned call sites), because a retried attempt replays
// the call from its first page. Without correcting for that, a 429 on a
// folder's SECOND page would replay its first page too and enqueue every
// folder that first page contained a second time, duplicating listing work
// and, on a deep tree, growing the queue without bound (05-RESEARCH.md
// Pitfall 5's idempotency note, T-05-07). walkFolder therefore accumulates
// each attempt's own discoveries into fresh per-attempt containers
// (attemptNodes/attemptChildFolders), reset at the START of every attempt,
// and merges them into the shared tree and the breadth-first queue only
// once that attempt's withRetry call returns without error — so a replayed
// page can never double-enqueue a folder or double-count a file, provably
// (TestWalkFolder_RetriedSecondPageProducesIdenticalTreeToUnfailedRun,
// retry_test.go).
func walkFolder(ctx context.Context, svc *drive.Service, rootID string) (map[string]*driveNode, error) {
	tree := map[string]*driveNode{}
	queue := []string{rootID}
	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]

		q := fmt.Sprintf("'%s' in parents and trashed = false", parentID)

		var attemptNodes map[string]*driveNode
		var attemptChildFolders []string
		err := withRetry(ctx, func(ctx context.Context) error {
			// Reset per-attempt state: a retried attempt starts this
			// folder level's listing over from its own first page, so any
			// partial results a PREVIOUS attempt collected must never
			// survive into this one.
			attemptNodes = map[string]*driveNode{}
			attemptChildFolders = nil
			return svc.Files.List().
				Q(q).
				Fields("nextPageToken, files(id, name, mimeType, parents, modifiedTime, webViewLink, trashed, size)").
				PageSize(1000).
				Context(ctx).
				Pages(ctx, func(page *drive.FileList) error {
					for _, f := range page.Files {
						if f.Trashed {
							continue
						}
						attemptNodes[f.Id] = &driveNode{
							Name:         f.Name,
							MimeType:     f.MimeType,
							ParentID:     parentID,
							ModifiedTime: f.ModifiedTime,
							WebViewLink:  f.WebViewLink,
							Size:         f.Size,
						}
						if f.MimeType == folderMimeType {
							attemptChildFolders = append(attemptChildFolders, f.Id)
						}
					}
					return nil
				})
		})
		if err != nil {
			return nil, fmt.Errorf("list children of %s: %w", parentID, err)
		}

		for id, node := range attemptNodes {
			tree[id] = node
		}
		queue = append(queue, attemptChildFolders...)
	}
	return tree, nil
}
