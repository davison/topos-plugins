package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

const (
	sourceType      = "filesystem"
	displayName     = "Filesystem folder"
	contractVersion = "topos.v2"

	// iconMIME is the declared mime for iconSVG below, returned verbatim
	// from Describe (internal/audit's plugin-icon contract, mirroring every
	// other in-repo plugin).
	iconMIME = "image/svg+xml"
)

// matchVocabulary is the field-name vocabulary this plugin declares in its
// Describe response and reads from MatchRequest.match_fields — folder paths,
// mirroring the Proton plugin's own "folders" vocabulary (D-05,
// 12-CONTEXT.md).
var matchVocabulary = []string{"folders"}

// iconSVG is a Lucide folder-family glyph, stroke baked to the literal
// --muted-foreground hex (never "currentColor" — an img-loaded SVG cannot
// inherit page CSS; internal/audit/plugin_icons_test.go enforces this
// mechanically).
//
// Source-Project: @lucide/svelte (lucide-icons/lucide)
// Source-File:    dist/icons/folder.svelte
// Source-Version: @lucide/svelte v1.27.0
// Source-License: ISC
//
//go:embed assets/icon.svg
var iconSVG []byte

// SourcePlugin implements sdk.SourcePlugin over a configured local/network
// filesystem folder: Match resolves each top-level file's scope and
// preview-kind classification through scope.go/classify.go (D-03, D-04)
// instead of the 12-01 tracer's inline ".pdf" test. Subfolder recursion
// remains a later plan's work — this plan widens document scope and
// preview shapes only, still walking the configured root's top level.
type SourcePlugin struct {
	root      string
	extras    map[string]string
	recursive bool
}

// NewSourcePlugin builds a SourcePlugin rooted at root — already expanded
// (main.go's expandHome) and otherwise unvalidated; a root that does not
// exist or is not readable surfaces honestly through Health/Match, never
// silently. extras carries this instance's own config.Source.Extras
// verbatim (D-12/D-13) — may be nil, a legitimate "no scope overrides
// configured" state that newScope resolves to the default allowlist
// alone. recursive carries config.Source.Recursive verbatim
// (12-03-PLAN.md Task 1) — false means Match reads the root's own top
// level only; true means every depth (Task 2's walk.go is the consumer).
func NewSourcePlugin(root string, extras map[string]string, recursive bool) *SourcePlugin {
	return &SourcePlugin{root: root, extras: extras, recursive: recursive}
}

// includeGlobKey and excludeGlobKey are the two extras keys this plugin
// declares in Describe (D-03) and reads in Match via newScope — the exact
// strings scope.go's newScope indexes into the extras map with.
const (
	includeGlobKey = "include_glob"
	excludeGlobKey = "exclude_glob"
)

func (p *SourcePlugin) Describe(_ context.Context, _ *toposv1.DescribeRequest) (*toposv1.DescribeResponse, error) {
	return &toposv1.DescribeResponse{
		SourceType:      sourceType,
		DisplayName:     displayName,
		ContractVersion: contractVersion,
		MatchVocabulary: matchVocabulary,
		Icon:            iconSVG,
		IconMime:        iconMIME,
		// Extras (D-15, PLUG-09): declaring these two keys here is the only
		// place they need to exist — Phase 11's declared-fields editor
		// renders them generically from this response, no new UI code.
		Extras: []*toposv1.ExtrasField{
			{
				Key:         includeGlobKey,
				Label:       "Include glob (comma-separated)",
				Required:    false,
				Secret:      false,
				Placeholder: "**/*.pdf,**/*.md",
			},
			{
				Key:         excludeGlobKey,
				Label:       "Exclude glob (comma-separated)",
				Required:    false,
				Secret:      false,
				Placeholder: "**/node_modules/**",
			},
		},
	}, nil
}

// Match delegates the tree traversal to walk.go's walk (12-03-PLAN.md
// Task 2): recursion on/off, symlink and hidden-file policy, permission
// tolerance and the per-sync item cap all live there. walk returns the
// COMPLETE current candidate set — never partial — which is exactly what
// kernel/correlate's full-replace persistence requires; Match itself does
// no diffing of its own. No file body is ever read here — preview stays
// empty at Match time (Fetch re-derives classification fresh, never
// caching it from here).
//
// Match reads only its own declared "folders" field from match_fields and
// ignores every other key present in the request map (D-05): when the
// field is present, only items whose folder label appears in it are kept
// (case-insensitive exact comparison, never substring/prefix); when
// absent, every item is returned so the kernel's keywords fallback can do
// the matching.
func (p *SourcePlugin) Match(ctx context.Context, req *toposv1.MatchRequest) (*toposv1.MatchResponse, error) {
	sc := newScope(p.extras)

	results, skipped, err := walk(ctx, p.root, p.recursive, sc)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "filesystem: %v", err)
	}
	if skipped > 0 {
		// go-plugin captures this subprocess's stderr and re-emits it
		// through the kernel's own hclog pipeline (host.go's stderrTail)
		// — no dedicated plugin-side logger exists for this plugin, so
		// this is the established, already-wired channel for a
		// non-fatal notice.
		fmt.Fprintf(os.Stderr, "topos-plugin-filesystem: skipped %d unreadable subtree(s) during walk\n", skipped)
	}

	folders, hasFolders := req.GetMatchFields()["folders"]

	var items []*toposv1.Item
	for _, r := range results {
		it := p.toItem(r.sourceID, r.info)
		if hasFolders && !labelMatchesAny(it.GetLabels(), folders.GetValues()) {
			continue
		}
		items = append(items, it)
	}

	return &toposv1.MatchResponse{Items: items}, nil
}

// labelMatchesAny reports whether any of labels exactly, case-
// insensitively equals any of values — no substring/prefix matching,
// mirroring every other plugin's own match-field comparison discipline
// (D-04, docs/plugin-contract.md).
func labelMatchesAny(labels, values []string) bool {
	for _, l := range labels {
		for _, v := range values {
			if strings.EqualFold(l, v) {
				return true
			}
		}
	}
	return false
}

// toItem builds the Item for sourceID (a D-01 forward-slash relative path
// — walk.go's walk already resolved it, at any depth), given info already
// stat'd by walk. Title is the file's own base name (never the full
// relative path — a nested file's directory context lives in Labels, not
// the title); deep_link is the file:// URI the kernel rewrites at serve
// time (Task 1 checkpoint, option-a); fidelity is always EXACT. Provenance
// carries all five plugin-populated keys docs/plugin-contract.md's
// "Provenance" section documents — source_type, source_system, source_id,
// plugin and contract_version — the complete set, not an arbitrary subset
// (WR-01, 12-07-PLAN.md Task 3): source_system is p.root, the filesystem
// analog of paperless/silverbullet's p.baseURL and signal's p.configDir —
// this instance's own address. synced_at_unix is deliberately never set
// here: the kernel's index layer owns that key and overwrites it anyway.
func (p *SourcePlugin) toItem(sourceID string, info os.FileInfo) *toposv1.Item {
	full := filepath.Join(p.root, filepath.FromSlash(sourceID))
	modUnix := info.ModTime().Unix()

	return &toposv1.Item{
		SourceId:               sourceID,
		SourceType:             sourceType,
		Title:                  filepath.Base(sourceID),
		TimestampUnix:          modUnix,
		SecondaryTimestampUnix: modUnix,
		Fidelity:               toposv1.LinkFidelity_LINK_FIDELITY_EXACT,
		DeepLink:               fileDeepLink(p.root, sourceID),
		Labels:                 folderLabels(p.root, full),
		Provenance: map[string]string{
			"source_type":      sourceType,
			"source_system":    p.root,
			"source_id":        sourceID,
			"plugin":           "topos-plugin-filesystem",
			"contract_version": contractVersion,
		},
	}
}

// Fetch is implemented in fetch.go — the per-preview-kind dispatch
// (12-02-PLAN.md Task 3) that superseded this tracer's PDF-only
// fetchBytes.

// Health lists the configured root's own top level: a readable directory
// — empty or not — is reachable; a missing, unreadable, or non-directory
// path is unreachable with the OS error as last_error (12-03-PLAN.md
// Task 2, 12-CONTEXT.md Claude's Discretion; mirrors the WhatsApp/Signal
// "degrade honestly" precedent). os.ReadDir, not os.Stat, is deliberate:
// Stat alone can succeed against a directory whose own permissions deny
// entry (traversal only needs execute on the PARENT, not the target),
// so it cannot distinguish "readable" from "exists but denied" — exactly
// the T-12-16 distinction this response must never collapse. No persisted
// last-known-mtime cache exists anywhere: the freshness bound for a
// network mount is the sync interval (12-RESEARCH.md Pitfall 4).
func (p *SourcePlugin) Health(_ context.Context, _ *toposv1.HealthRequest) (*toposv1.HealthResponse, error) {
	if _, err := os.ReadDir(p.root); err != nil {
		return &toposv1.HealthResponse{Reachable: false, LastError: err.Error()}, nil
	}
	return &toposv1.HealthResponse{Reachable: true, LastSyncUnix: time.Now().Unix()}, nil
}
