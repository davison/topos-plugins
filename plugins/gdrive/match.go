// Package main's match.go resolves each Drive item's Match-comparable
// literal values (GAP-09's resolved algorithm, Option A — the cumulative
// ancestor-chain value set) and builds the Item set Match returns: the
// full current tree, filtered against the operator's configured "folders"
// match values, exact and case-insensitive (contract Match rule 2) —
// never a delta of what changed since the last call (SYNC-04's "full item
// set, not a delta" axis is TIME, not SCOPE; filtering against
// match_fields still applies, 03-RESEARCH.md Pitfall 8). Every emitted
// Item carries the kernel's full required field set; every node that
// cannot produce a valid Item is skipped, logged by id and reason only
// (never the file's own name), rather than handed to the kernel to drop
// silently at its own sync-time boundary.
package main

import (
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// ancestorChainValues returns every literal Match value the item whose
// current parent is parentID should be comparable against, plus an
// explicit reachability verdict carrying exactly the same meaning
// changepoll.go's reachesRoot assigns it: true only when the upward walk
// actually terminated at rootID. The two functions are provably
// symmetric — TestReachabilityVerdictsAgree_AncestorChainValuesMatchesReachesRoot
// pins their verdicts to each other for every chain shape. An ancestor
// absent from the tree, an empty parent id, and a walk that exhausted its
// step bound (cyclic or self-parented chain) all return (nil, false):
// default-deny, no value is ever emitted for a chain that did not
// provably reach the configured root (T-03-19 / CR-01).
//
// On a true verdict the value set is GAP-09 Option A, unchanged: the
// configured root's own name (so a webspace configured with that literal
// matches everything synced by this instance), then each cumulative
// relative path prefix from root down to (not including) the item's own
// name. E.g. for root "Team Docs" and a file at
// Team Docs/Reports/2026/q1.pdf, returns
// ["Team Docs", "Reports", "Reports/2026"].
//
// Paths are resolved lazily here, at call time, by walking the live
// tree's ParentID chain upward — never pre-computed or cached on the
// node, so a moved subfolder's own single tree-entry update is reflected
// in every descendant's very next resolution with no separate
// propagation pass (03-RESEARCH.md Pitfall 6). The upward walk is bounded
// by the tree's own entry count, mirroring reachesRoot, so a cyclic or
// self-parented entry in a malformed tree terminates rather than hangs.
// The returned slice never contains an empty string or a duplicate value.
func ancestorChainValues(tree map[string]*driveNode, rootID, rootName, parentID string) ([]string, bool) {
	var segments []string
	id := parentID
	limit := len(tree) + 1
	for steps := 0; steps < limit && id != rootID && id != ""; steps++ {
		node, ok := tree[id]
		if !ok {
			return nil, false
		}
		segments = append([]string{node.Name}, segments...)
		id = node.ParentID
	}
	if id != rootID {
		return nil, false
	}

	seen := make(map[string]bool, len(segments)+1)
	values := make([]string, 0, len(segments)+1)
	add := func(v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		values = append(values, v)
	}
	add(rootName)
	for i := range segments {
		add(strings.Join(segments[:i+1], "/"))
	}
	return values, true
}

// nonEmptyValues filters out every empty string in values, preserving
// order — so a supplied value list of only empty strings (or one mixed
// with empty entries) never contributes a spurious match: an empty
// string can never legitimately match a folder-path value (contract
// Match rule 3: an empty value list, and therefore an effectively empty
// one, matches nothing, never everything).
func nonEmptyValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// matchItems returns every non-folder tree node whose ancestorChainValues
// match ANY of req's declared "folders" values (matchVocabulary's one
// entry), exact and case-insensitive, built as a fully-populated Item and
// sorted by SourceId ascending so two consecutive calls against unchanged
// state return byte-identical ordering. Reads only the "folders" key from
// req.GetMatchFields() — any other key present is never inspected, so it
// is treated as absent rather than an error by construction (contract
// Match rule 1). An absent "folders" key, a present-but-empty value list,
// and a value list of only empty strings each match nothing (contract
// Match rule 3) via nonEmptyValues' filtering and the early return below —
// never a wildcard. Folder objects are never emitted (GAP-10) — skipped
// before ancestorChainValues or itemFor is even called. A node whose
// ancestor chain does not provably reach the configured root (an
// unreachable verdict from ancestorChainValues) is logged by id and a
// fixed reason and skipped — never compared, never emitted (T-03-19). A
// node itemFor refuses to build a valid Item for (an empty or
// non-absolute-http(s) webViewLink, or an unparseable modifiedTime) is
// logged by id and reason and skipped, never emitted invalid — a
// sibling's own successful item is unaffected in either case.
func matchItems(st *syncState, req *toposv1.MatchRequest) []*toposv1.Item {
	values := nonEmptyValues(req.GetMatchFields()["folders"].GetValues())
	if len(values) == 0 {
		return nil
	}

	var items []*toposv1.Item
	for id, node := range st.Tree {
		if node.MimeType == folderMimeType {
			continue
		}
		labels, reachable := ancestorChainValues(st.Tree, st.RootID, st.RootName, node.ParentID)
		if !reachable {
			log.Printf("gdrive: match: skip item %s: %s", id, "parent chain does not reach the configured root")
			continue
		}
		if !anyEqualFold(labels, values) {
			continue
		}
		item, err := itemFor(id, node, labels, st.RootID)
		if err != nil {
			log.Printf("gdrive: match: skip item %s: %s", id, err)
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SourceId < items[j].SourceId })
	return items
}

// anyEqualFold reports whether any of labels exactly, case-insensitively
// equals any of values — never a substring or prefix match (contract
// Match rule 2). strings.EqualFold performs simple per-code-point Unicode
// case folding only, with no Unicode normalization: a precomposed
// (U+00E9) and a combining-sequence (e + U+0301) spelling of the same
// accented letter are never treated as equal. The documented fix for a
// spelling mismatch is the operator adding the variant string to their
// own config, never this plugin widening its comparison.
func anyEqualFold(labels, values []string) bool {
	for _, l := range labels {
		for _, v := range values {
			if strings.EqualFold(l, v) {
				return true
			}
		}
	}
	return false
}

// itemFor builds one fully-populated Item from a matched driveNode, or
// returns a non-nil error naming the reason (by id only, never the
// node's own name) when the node cannot produce an Item the kernel would
// accept: an empty or non-absolute-http(s) webViewLink, or a
// modifiedTime that fails to parse as RFC 3339. matchItems logs and
// skips on this error rather than emitting an invalid Item — the kernel
// drops an item with an empty deep_link or an unspecified fidelity
// silently at its own sync-time boundary, which would otherwise make a
// short item set indistinguishable from an intentional result.
//
// Preview is left unset here and populated afterward, in place, by
// preview.go's attachPreviews — plugin.go's Match calls attachPreviews on
// the full item set matchItems returns, after itemFor has already run, so
// a preview fetch failure can never affect which items itemFor builds or
// how matchItems orders them. Whatever attachPreviews assigns is exactly
// what the host persists in its own local index. SecondaryTimestampUnix
// stays 0, GroupId/GroupLabel stay empty (documents have no thread
// concept, which the contract explicitly models as both empty), and
// HasThumbnail stays false.
func itemFor(id string, node *driveNode, labels []string, rootID string) (*toposv1.Item, error) {
	if node.WebViewLink == "" {
		return nil, fmt.Errorf("empty webViewLink")
	}
	link, err := url.Parse(node.WebViewLink)
	if err != nil {
		return nil, fmt.Errorf("unparseable webViewLink: %w", err)
	}
	if !link.IsAbs() || (link.Scheme != "http" && link.Scheme != "https") {
		return nil, fmt.Errorf("webViewLink is not an absolute http(s) URL")
	}

	ts, err := time.Parse(time.RFC3339, node.ModifiedTime)
	if err != nil {
		return nil, fmt.Errorf("unparseable modifiedTime: %w", err)
	}

	// title falls back to a non-empty placeholder rather than the empty
	// string the contract forbids for a Required field — should never
	// happen against a real Drive response, but a defensive floor is
	// cheap and honest.
	title := node.Name
	if title == "" {
		title = "(untitled)"
	}

	return &toposv1.Item{
		SourceId:      id,
		SourceType:    sourceType,
		Title:         title,
		TimestampUnix: ts.Unix(),
		Fidelity:      toposv1.LinkFidelity_LINK_FIDELITY_EXACT,
		DeepLink:      node.WebViewLink,
		Labels:        labels,
		Provenance:    provenanceFor(id, rootID),
	}, nil
}

// provenanceFor builds the five plugin-populated provenance keys the
// contract documents ("Provenance" section) — mirroring
// contract/mock/plugin.go's own provenanceFor shape, substituting this
// plugin's own sourceType/sourceSystem/contractVersion values. The sixth
// key, synced_at_unix, is filled in by the kernel's index layer at read
// time and must never be set here.
func provenanceFor(sourceID, rootID string) map[string]string {
	return map[string]string{
		"source_type":      sourceType,
		"source_system":    sourceSystem(rootID),
		"source_id":        sourceID,
		"plugin":           "topos-plugin-gdrive",
		"contract_version": contractVersion,
	}
}
