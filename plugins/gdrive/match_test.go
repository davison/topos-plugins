// Package main's match_test.go pins RPC-05's full value-algorithm and
// Match-filter semantics (Task 1), every required-Item-field / skip-and-log
// guarantee (Task 2), and the full-item-set-on-every-call gate proven at
// the SourcePlugin.Match RPC boundary against a fake, multi-level Drive
// folder tree (Task 3). Follows this repository's own idiom:
// Test<Thing>_<BehaviorInPlainEnglish> names, plain t.Errorf/t.Fatalf
// assertions, no assertion library.
package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// --- Task 1: ancestorChainValues / anyEqualFold / matchItems filter semantics ---

// nestedFileNode builds a valid driveNode — every Task 1 test that needs
// a file (not a folder) node uses this so the file's own field shape is
// never what the test is actually exercising.
func nestedFileNode(name, parentID string) *driveNode {
	return &driveNode{
		Name:         name,
		MimeType:     "application/pdf",
		ParentID:     parentID,
		ModifiedTime: "2026-08-17T00:00:00Z",
		WebViewLink:  "https://drive.google.com/file/d/x/view",
	}
}

func nestedFolderNode(name, parentID string) *driveNode {
	return &driveNode{Name: name, MimeType: folderMimeType, ParentID: parentID}
}

// TestAncestorChainValues_ThreeLevelNestedItemProducesExactOrderedValues
// pins GAP-09's worked example exactly: root "Team Docs", item at
// Team Docs/Reports/2026/q1.pdf, values ["Team Docs", "Reports",
// "Reports/2026"], in that exact order.
func TestAncestorChainValues_ThreeLevelNestedItemProducesExactOrderedValues(t *testing.T) {
	tree := map[string]*driveNode{
		"reports": nestedFolderNode("Reports", "root-1"),
		"y2026":   nestedFolderNode("2026", "reports"),
	}
	got, reachable := ancestorChainValues(tree, "root-1", "Team Docs", "y2026")
	if !reachable {
		t.Error("reachable = false, want true (chain terminates at the configured root)")
	}
	want := []string{"Team Docs", "Reports", "Reports/2026"}
	assertStringSliceEqual(t, got, want)
}

// TestAncestorChainValues_ItemDirectlyInRootYieldsOnlyRootName proves an
// item sitting directly in the configured root has exactly one value —
// the root's own name alone.
func TestAncestorChainValues_ItemDirectlyInRootYieldsOnlyRootName(t *testing.T) {
	tree := map[string]*driveNode{}
	got, reachable := ancestorChainValues(tree, "root-1", "Team Docs", "root-1")
	if !reachable {
		t.Error("reachable = false, want true (parent IS the configured root)")
	}
	want := []string{"Team Docs"}
	assertStringSliceEqual(t, got, want)
}

// TestAncestorChainValues_CyclicParentChainTerminatesAndReportsUnreachable
// proves the bounded upward walk terminates rather than hangs against a
// malformed, self-referential tree — and that the verdict for a chain
// that never reaches root is unreachable, matching
// TestReachesRoot_CyclicChainTerminatesAndReturnsFalse for the identical
// tree shape. (The pre-03-04 assertion required the root's own name for
// this chain — that assertion encoded CR-01 and is deliberately
// inverted.)
func TestAncestorChainValues_CyclicParentChainTerminatesAndReportsUnreachable(t *testing.T) {
	tree := map[string]*driveNode{
		"a": {Name: "A", ParentID: "b"},
		"b": {Name: "B", ParentID: "a"},
	}
	done := make(chan bool, 1)
	go func() {
		_, reachable := ancestorChainValues(tree, "root", "Team Docs", "a")
		done <- reachable
	}()
	select {
	case reachable := <-done:
		if reachable {
			t.Error("ancestorChainValues over a cyclic chain reported reachable = true, want false (never reaches root)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ancestorChainValues over a cyclic parent chain did not terminate")
	}
}

// TestAncestorChainValues_UnreachableChainShapesReturnFalseAndNoValues is
// the shape table for the default-deny contract: every way an upward walk
// can fail to reach the configured root returns an unreachable verdict
// and an empty value slice — no value, not even the root's own name, is
// ever emitted for an unresolvable chain.
func TestAncestorChainValues_UnreachableChainShapesReturnFalseAndNoValues(t *testing.T) {
	cases := []struct {
		name     string
		tree     map[string]*driveNode
		parentID string
	}{
		{
			name:     "ancestor missing from the tree",
			tree:     map[string]*driveNode{"orphan-parent": {Name: "Orphan", ParentID: "gone"}},
			parentID: "orphan-parent",
		},
		{
			name:     "empty parent id",
			tree:     map[string]*driveNode{},
			parentID: "",
		},
		{
			name:     "self-parented entry",
			tree:     map[string]*driveNode{"self": {Name: "Self", ParentID: "self"}},
			parentID: "self",
		},
		{
			name: "two-node cycle",
			tree: map[string]*driveNode{
				"a": {Name: "A", ParentID: "b"},
				"b": {Name: "B", ParentID: "a"},
			},
			parentID: "a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values, reachable := ancestorChainValues(tc.tree, "root", "Team Docs", tc.parentID)
			if reachable {
				t.Errorf("%s: reachable = true, want false", tc.name)
			}
			if len(values) != 0 {
				t.Errorf("%s: values = %v, want empty", tc.name, values)
			}
		})
	}
}

func assertStringSliceEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q (full: got %v, want %v)", i, got[i], want[i], got, want)
		}
	}
}

// buildMatchRequest is the small helper every Task 1/2 matchItems test
// uses to build a MatchRequest carrying exactly one "folders" entry.
func buildMatchRequest(values ...string) *toposv1.MatchRequest {
	return &toposv1.MatchRequest{
		MatchFields: map[string]*toposv1.StringList{
			"folders": {Values: values},
		},
	}
}

// TestMatchItems_RootNameMatchesEveryNonFolderItemInNestedFixture proves
// "everything synced by this instance" is expressed by the root's own
// name matching every non-folder item in a nested fixture — folders
// themselves never counted.
func TestMatchItems_RootNameMatchesEveryNonFolderItemInNestedFixture(t *testing.T) {
	st := &syncState{
		RootID:   "root-1",
		RootName: "Team Docs",
		Tree: map[string]*driveNode{
			"top":     nestedFileNode("top.pdf", "root-1"),
			"reports": nestedFolderNode("Reports", "root-1"),
			"q1":      nestedFileNode("q1.pdf", "reports"),
			"y2026":   nestedFolderNode("2026", "reports"),
			"q1-2026": nestedFileNode("q1-2026.pdf", "y2026"),
			"archive": nestedFolderNode("Archive", "root-1"),
			"old":     nestedFileNode("old.pdf", "archive"),
		},
	}
	items := matchItems(st, buildMatchRequest("Team Docs"))
	if len(items) != 4 {
		t.Fatalf("len(items) = %d, want 4 (every non-folder node, none of the 3 folders)", len(items))
	}
}

// TestMatchItems_ValueDoesNotMatchSiblingSharingNamePrefix proves a
// configured value ("Reports") never matches a sibling folder whose name
// merely shares that prefix ("Reports Archive") — comparison is an exact
// literal, never a prefix or substring match.
func TestMatchItems_ValueDoesNotMatchSiblingSharingNamePrefix(t *testing.T) {
	st := &syncState{
		RootID:   "root-1",
		RootName: "Team Docs",
		Tree: map[string]*driveNode{
			"reports":         nestedFolderNode("Reports", "root-1"),
			"q1":              nestedFileNode("q1.pdf", "reports"),
			"reports-archive": nestedFolderNode("Reports Archive", "root-1"),
			"old":             nestedFileNode("old.pdf", "reports-archive"),
		},
	}
	items := matchItems(st, buildMatchRequest("Reports"))
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].GetSourceId() != "q1" {
		t.Errorf("SourceId = %q, want %q (the item under Reports Archive must not match)", items[0].GetSourceId(), "q1")
	}
}

// TestMatchItems_CaseInsensitiveBothDirections proves exact,
// case-insensitive matching in both directions: a lowercase configured
// value against a capitalized folder name, and vice versa.
func TestMatchItems_CaseInsensitiveBothDirections(t *testing.T) {
	t.Run("LowercaseValueAgainstCapitalizedFolder", func(t *testing.T) {
		st := &syncState{
			RootID:   "root-1",
			RootName: "Team Docs",
			Tree: map[string]*driveNode{
				"reports": nestedFolderNode("Reports", "root-1"),
				"q1":      nestedFileNode("q1.pdf", "reports"),
			},
		}
		items := matchItems(st, buildMatchRequest("reports"))
		if len(items) != 1 {
			t.Fatalf("len(items) = %d, want 1", len(items))
		}
	})
	t.Run("CapitalizedValueAgainstLowercaseFolder", func(t *testing.T) {
		st := &syncState{
			RootID:   "root-1",
			RootName: "Team Docs",
			Tree: map[string]*driveNode{
				"reports": nestedFolderNode("reports", "root-1"),
				"q1":      nestedFileNode("q1.pdf", "reports"),
			},
		}
		items := matchItems(st, buildMatchRequest("Reports"))
		if len(items) != 1 {
			t.Fatalf("len(items) = %d, want 1", len(items))
		}
	})
}

// TestMatchItems_PrecomposedAccentDoesNotMatchCombiningSequence pins the
// no-Unicode-normalization rule: a folder named with a precomposed
// accented character (U+00E9) is not matched by a configured value
// spelled with the base letter plus a combining accent (e + U+0301) —
// strings.EqualFold performs simple per-code-point case folding only.
func TestMatchItems_PrecomposedAccentDoesNotMatchCombiningSequence(t *testing.T) {
	precomposed := "Caf\u00e9" // precomposed é (single code point, U+00E9)
	combining := "Cafe\u0301"  // "Cafe" + combining acute accent (U+0301)
	st := &syncState{
		RootID:   "root-1",
		RootName: "Team Docs",
		Tree: map[string]*driveNode{
			"cafe": nestedFolderNode(precomposed, "root-1"),
			"menu": nestedFileNode("menu.pdf", "cafe"),
		},
	}
	items := matchItems(st, buildMatchRequest(combining))
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0 (no Unicode normalization — precomposed and combining spellings must not match)", len(items))
	}
}

// TestMatchItems_ItemMatchingTwoSuppliedValuesReturnedExactlyOnce proves
// an item whose values match more than one supplied "folders" value
// appears exactly once in the returned set, never duplicated.
func TestMatchItems_ItemMatchingTwoSuppliedValuesReturnedExactlyOnce(t *testing.T) {
	st := &syncState{
		RootID:   "root-1",
		RootName: "Team Docs",
		Tree: map[string]*driveNode{
			"reports": nestedFolderNode("Reports", "root-1"),
			"y2026":   nestedFolderNode("2026", "reports"),
			"q1":      nestedFileNode("q1.pdf", "y2026"),
		},
	}
	items := matchItems(st, buildMatchRequest("Reports", "Reports/2026"))
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1 (matching two configured values must not duplicate the item)", len(items))
	}
}

// TestMatchItems_AbsentEmptyOrAllEmptyValuesMatchNothing covers the three
// "matches nothing, never everything" shapes contract Match rule 3
// requires: an absent "folders" key, a present key with a zero-length
// value list, and a value list of only empty strings. matchItems has no
// error return of its own — a nil error is implicit in its signature, so
// "zero items and a nil error" reduces to "zero items, no panic" here.
func TestMatchItems_AbsentEmptyOrAllEmptyValuesMatchNothing(t *testing.T) {
	st := &syncState{
		RootID:   "root-1",
		RootName: "Team Docs",
		Tree: map[string]*driveNode{
			"top": nestedFileNode("top.pdf", "root-1"),
		},
	}
	t.Run("AbsentFoldersKey", func(t *testing.T) {
		req := &toposv1.MatchRequest{MatchFields: map[string]*toposv1.StringList{}}
		if items := matchItems(st, req); len(items) != 0 {
			t.Errorf("len(items) = %d, want 0", len(items))
		}
	})
	t.Run("EmptyValueList", func(t *testing.T) {
		if items := matchItems(st, buildMatchRequest()); len(items) != 0 {
			t.Errorf("len(items) = %d, want 0", len(items))
		}
	})
	t.Run("ListOfOnlyEmptyStrings", func(t *testing.T) {
		if items := matchItems(st, buildMatchRequest("", "")); len(items) != 0 {
			t.Errorf("len(items) = %d, want 0", len(items))
		}
	})
	t.Run("NilRequest", func(t *testing.T) {
		if items := matchItems(st, nil); len(items) != 0 {
			t.Errorf("len(items) = %d, want 0 (a nil *MatchRequest must never panic or wildcard-match)", len(items))
		}
	})
}

// TestMatchItems_UndeclaredKeyIgnoredNotError proves a match_fields key
// this plugin did not declare (anything other than "folders") is treated
// as absent, never inspected, never an error — matchItems behaves
// identically whether or not the undeclared key is present.
func TestMatchItems_UndeclaredKeyIgnoredNotError(t *testing.T) {
	st := &syncState{
		RootID:   "root-1",
		RootName: "Team Docs",
		Tree: map[string]*driveNode{
			"top": nestedFileNode("top.pdf", "root-1"),
		},
	}
	withoutExtra := matchItems(st, buildMatchRequest("Team Docs"))

	reqWithExtra := buildMatchRequest("Team Docs")
	reqWithExtra.MatchFields["tags"] = &toposv1.StringList{Values: []string{"whatever-this-plugin-never-declared"}}
	withExtra := matchItems(st, reqWithExtra)

	if len(withoutExtra) != 1 || len(withExtra) != 1 {
		t.Fatalf("len(withoutExtra) = %d, len(withExtra) = %d, want 1 and 1", len(withoutExtra), len(withExtra))
	}
	if withoutExtra[0].GetSourceId() != withExtra[0].GetSourceId() {
		t.Errorf("an undeclared match_fields key changed matchItems' result: %q vs %q", withoutExtra[0].GetSourceId(), withExtra[0].GetSourceId())
	}
}

// --- Task 2: every required Item field, refuse-to-emit rules, ordering ---

// TestItemFor_FullyPopulatesEveryRequiredFieldAndLeavesPhase4FieldsEmpty
// asserts every field itemFor is responsible for on a valid node, and
// that the fields Phase 4 (or no phase yet) owns are left at their zero
// value: Preview, GroupId, GroupLabel empty; SecondaryTimestampUnix 0;
// HasThumbnail false.
func TestItemFor_FullyPopulatesEveryRequiredFieldAndLeavesPhase4FieldsEmpty(t *testing.T) {
	node := &driveNode{
		Name:         "q1.pdf",
		MimeType:     "application/pdf",
		ParentID:     "root-1",
		ModifiedTime: "2026-08-17T12:30:00Z",
		WebViewLink:  "https://drive.google.com/file/d/file-1/view",
	}
	it, err := itemFor("file-1", node, []string{"Team Docs"}, "root-1")
	if err != nil {
		t.Fatalf("itemFor: %v", err)
	}
	if it.GetSourceId() != "file-1" {
		t.Errorf("SourceId = %q, want %q", it.GetSourceId(), "file-1")
	}
	if it.GetSourceType() != sourceType {
		t.Errorf("SourceType = %q, want %q (must match Describe's own source_type)", it.GetSourceType(), sourceType)
	}
	if it.GetTitle() != "q1.pdf" {
		t.Errorf("Title = %q, want %q", it.GetTitle(), "q1.pdf")
	}
	if it.GetTimestampUnix() == 0 {
		t.Error("TimestampUnix = 0, want a non-zero parsed Unix timestamp")
	}
	if it.GetFidelity() != toposv1.LinkFidelity_LINK_FIDELITY_EXACT {
		t.Errorf("Fidelity = %v, want LINK_FIDELITY_EXACT", it.GetFidelity())
	}
	if it.GetDeepLink() != node.WebViewLink {
		t.Errorf("DeepLink = %q, want %q", it.GetDeepLink(), node.WebViewLink)
	}
	if len(it.GetLabels()) != 1 || it.GetLabels()[0] != "Team Docs" {
		t.Errorf("Labels = %v, want [\"Team Docs\"]", it.GetLabels())
	}
	if it.GetPreview() != "" {
		t.Errorf("Preview = %q, want empty (Phase 4 owns previews)", it.GetPreview())
	}
	if it.GetGroupId() != "" {
		t.Errorf("GroupId = %q, want empty", it.GetGroupId())
	}
	if it.GetGroupLabel() != "" {
		t.Errorf("GroupLabel = %q, want empty", it.GetGroupLabel())
	}
	if it.GetSecondaryTimestampUnix() != 0 {
		t.Errorf("SecondaryTimestampUnix = %d, want 0", it.GetSecondaryTimestampUnix())
	}
	if it.GetHasThumbnail() {
		t.Error("HasThumbnail = true, want false")
	}
}

// TestItemFor_ProvenanceHasExactlyTheFiveDocumentedKeys asserts the
// provenance map carries exactly the five plugin-owned keys the contract
// documents, with source_type equal to what Describe reports.
func TestItemFor_ProvenanceHasExactlyTheFiveDocumentedKeys(t *testing.T) {
	node := &driveNode{
		Name:         "q1.pdf",
		ModifiedTime: "2026-08-17T12:30:00Z",
		WebViewLink:  "https://drive.google.com/file/d/file-1/view",
	}
	it, err := itemFor("file-1", node, nil, "root-1")
	if err != nil {
		t.Fatalf("itemFor: %v", err)
	}
	prov := it.GetProvenance()
	if len(prov) != 5 {
		t.Fatalf("len(Provenance) = %d, want 5 (%+v)", len(prov), prov)
	}
	for _, key := range []string{"source_type", "source_system", "source_id", "plugin", "contract_version"} {
		if _, ok := prov[key]; !ok {
			t.Errorf("Provenance missing key %q", key)
		}
	}
	wantSourceType := (&SourcePlugin{}).describeSourceType()
	if prov["source_type"] != wantSourceType {
		t.Errorf(`Provenance["source_type"] = %q, want %q (must equal what Describe reports)`, prov["source_type"], wantSourceType)
	}
}

// describeSourceType returns the exact source_type Describe reports —
// used only by this test file to assert Provenance's own source_type
// stays in lockstep with Describe without hardcoding the literal twice.
func (p *SourcePlugin) describeSourceType() string {
	resp, _ := p.Describe(context.Background(), nil)
	return resp.GetSourceType()
}

// TestMatchItems_FolderNodeNeverAppearsAmongReturnedItems proves GAP-10's
// resolution directly at the Item-emission boundary: a folder node in the
// tree is never emitted, even when it would otherwise satisfy the
// supplied match value.
func TestMatchItems_FolderNodeNeverAppearsAmongReturnedItems(t *testing.T) {
	st := &syncState{
		RootID:   "root-1",
		RootName: "Team Docs",
		Tree: map[string]*driveNode{
			"reports": nestedFolderNode("Reports", "root-1"),
		},
	}
	items := matchItems(st, buildMatchRequest("Team Docs", "Reports"))
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0 (a folder must never be emitted as an Item)", len(items))
	}
}

// TestItemFor_EmptyWebViewLinkIsSkipped proves the refuse-to-emit rule
// for an empty deep link.
func TestItemFor_EmptyWebViewLinkIsSkipped(t *testing.T) {
	node := &driveNode{Name: "q1.pdf", ModifiedTime: "2026-08-17T00:00:00Z", WebViewLink: ""}
	if it, err := itemFor("file-1", node, nil, "root-1"); err == nil {
		t.Errorf("itemFor with an empty WebViewLink: got item %+v, err nil, want a non-nil error", it)
	}
}

// TestItemFor_NonHTTPSchemeWebViewLinkIsSkipped proves the refuse-to-emit
// rule for a link that parses but does not carry an http/https scheme.
func TestItemFor_NonHTTPSchemeWebViewLinkIsSkipped(t *testing.T) {
	node := &driveNode{Name: "q1.pdf", ModifiedTime: "2026-08-17T00:00:00Z", WebViewLink: "ftp://example.com/q1.pdf"}
	if it, err := itemFor("file-1", node, nil, "root-1"); err == nil {
		t.Errorf("itemFor with a non-http(s) WebViewLink: got item %+v, err nil, want a non-nil error", it)
	}
}

// TestItemFor_UnparseableModifiedTimeIsSkipped proves the refuse-to-emit
// rule for a modifiedTime that fails to parse as RFC 3339.
func TestItemFor_UnparseableModifiedTimeIsSkipped(t *testing.T) {
	node := &driveNode{Name: "q1.pdf", ModifiedTime: "not-a-timestamp", WebViewLink: "https://drive.google.com/file/d/file-1/view"}
	if it, err := itemFor("file-1", node, nil, "root-1"); err == nil {
		t.Errorf("itemFor with an unparseable ModifiedTime: got item %+v, err nil, want a non-nil error", it)
	}
}

// TestMatchItems_SkippedNodeDoesNotSuppressItsSiblings proves that one
// node itemFor refuses (an empty WebViewLink) does not prevent its
// sibling, which is otherwise valid, from being emitted.
func TestMatchItems_SkippedNodeDoesNotSuppressItsSiblings(t *testing.T) {
	st := &syncState{
		RootID:   "root-1",
		RootName: "Team Docs",
		Tree: map[string]*driveNode{
			"broken": {Name: "broken.pdf", MimeType: "application/pdf", ParentID: "root-1", ModifiedTime: "2026-08-17T00:00:00Z", WebViewLink: ""},
			"ok":     nestedFileNode("ok.pdf", "root-1"),
		},
	}
	items := matchItems(st, buildMatchRequest("Team Docs"))
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1 (the broken sibling must be skipped, not suppress the valid one)", len(items))
	}
	if items[0].GetSourceId() != "ok" {
		t.Errorf("SourceId = %q, want %q", items[0].GetSourceId(), "ok")
	}
}

// TestMatchItems_ReturnedItemsOrderedBySourceIdAscendingAcrossTwoCalls
// proves the deterministic ordering guarantee: items are sorted by
// SourceId ascending, byte-identically across two consecutive calls
// against the same unchanged state.
func TestMatchItems_ReturnedItemsOrderedBySourceIdAscendingAcrossTwoCalls(t *testing.T) {
	st := &syncState{
		RootID:   "root-1",
		RootName: "Team Docs",
		Tree: map[string]*driveNode{
			"zeta":  nestedFileNode("zeta.pdf", "root-1"),
			"alpha": nestedFileNode("alpha.pdf", "root-1"),
			"mu":    nestedFileNode("mu.pdf", "root-1"),
		},
	}
	req := buildMatchRequest("Team Docs")
	first := matchItems(st, req)
	second := matchItems(st, req)

	wantOrder := []string{"alpha", "mu", "zeta"}
	for _, got := range [][]*toposv1.Item{first, second} {
		if len(got) != len(wantOrder) {
			t.Fatalf("len(items) = %d, want %d", len(got), len(wantOrder))
		}
		for i, id := range wantOrder {
			if got[i].GetSourceId() != id {
				t.Errorf("index %d: SourceId = %q, want %q", i, got[i].GetSourceId(), id)
			}
		}
	}
}

// --- Task 3: the full-item-set-on-every-call gate, at the Match RPC boundary ---

// nestedDriveFixture is a small, multi-level fixture folder tree served
// by the fake Drive service: a root holding one direct file plus a
// Reports subtree (itself two levels deep) and a sibling Archive
// subtree — enough shape to exercise mid-depth scoping and the
// root-matches-everything count in one fixture. Reuses
// drivefake_test.go's newFakeDriveService/newDriveRecorder and
// syncengine_test.go's writeDriveJSON/parentFromQuery/pluginWithFakeDrive/
// sourceConfigJSON/seedValidToken/newDeltaOnlyHandler directly — same
// package, no duplication.
type nestedDriveFixture struct {
	rootID, rootName string
	// children maps a parent folder id (rootID included) to the files.list
	// rows served for that parent.
	children map[string][]*drive.File
}

func nestedFixtureFile(id, name string) *drive.File {
	return &drive.File{
		Id:           id,
		Name:         name,
		MimeType:     "application/pdf",
		ModifiedTime: "2026-08-17T00:00:00Z",
		WebViewLink:  "https://drive.google.com/file/d/" + id + "/view",
	}
}

func nestedFixtureFolder(id, name string) *drive.File {
	return &drive.File{Id: id, Name: name, MimeType: folderMimeType}
}

// newNestedDriveFixture builds the standard multi-level tree every Task 3
// test in this file shares: root -> {top.pdf, Reports/, Archive/},
// Reports -> {q1.pdf, 2026/}, 2026 -> {q1-2026.pdf}, Archive -> {old.pdf}.
// Four non-folder files total.
func newNestedDriveFixture(rootID, rootName string) nestedDriveFixture {
	return nestedDriveFixture{
		rootID:   rootID,
		rootName: rootName,
		children: map[string][]*drive.File{
			rootID: {
				nestedFixtureFile("top-1", "top.pdf"),
				nestedFixtureFolder("reports-1", "Reports"),
				nestedFixtureFolder("archive-1", "Archive"),
			},
			"reports-1": {
				nestedFixtureFile("q1-1", "q1.pdf"),
				nestedFixtureFolder("y2026-1", "2026"),
			},
			"y2026-1": {
				nestedFixtureFile("q1-2026-1", "q1-2026.pdf"),
			},
			"archive-1": {
				nestedFixtureFile("old-1", "old.pdf"),
			},
		},
	}
}

// newNestedFixtureHandler serves the same four Drive REST endpoints
// newSingleFileFixtureHandler (syncengine_test.go) does, but reads
// children from a full nestedDriveFixture instead of a single file —
// letting Task 3's tests exercise a real multi-level walk end to end.
func newNestedFixtureHandler(t *testing.T, fx nestedDriveFixture) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/changes/startPageToken":
			writeDriveJSON(t, w, &drive.StartPageToken{StartPageToken: "start-token-1"})
		case r.URL.Path == "/changes":
			writeDriveJSON(t, w, &drive.ChangeList{NewStartPageToken: "start-token-1"})
		case r.URL.Path == "/files/"+fx.rootID:
			writeDriveJSON(t, w, &drive.File{Id: fx.rootID, Name: fx.rootName, MimeType: folderMimeType})
		case r.URL.Path == "/files":
			parent := parentFromQuery(r.URL.Query().Get("q"))
			writeDriveJSON(t, w, &drive.FileList{Files: fx.children[parent]})
		default:
			http.NotFound(w, r)
		}
	}
}

// itemSignature is a deterministic, comparable summary of every field
// this plan's Item construction populates — used to assert two Match
// responses are identical item-for-item and field-for-field without
// relying on reflect.DeepEqual over a generated proto message's
// unexported internal state.
func itemSignature(it *toposv1.Item) string {
	return fmt.Sprintf("%s|%s|%s|%d|%v|%s|%v|%v",
		it.GetSourceId(), it.GetSourceType(), it.GetTitle(), it.GetTimestampUnix(),
		it.GetFidelity(), it.GetDeepLink(), it.GetLabels(), it.GetProvenance())
}

// dataFilePath joins isolatedDir's own resolved data directory
// (dataDirName, the same constant token.go/syncstate.go both use) with
// name — a small local helper so this file's tests don't repeat
// filepath.Join(isolatedDir, dataDirName, name) at every call site.
func dataFilePath(isolatedDir, name string) string {
	return filepath.Join(isolatedDir, dataDirName, name)
}

// TestMatch_TwoConsecutiveCallsAgainstUnchangedStateReturnIdenticalItemSets
// is SYNC-04's own core guarantee turned into a standing gate: the second
// call is not a delta and is not empty — it returns the identical item
// set, item for item and field for field.
func TestMatch_TwoConsecutiveCallsAgainstUnchangedStateReturnIdenticalItemSets(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := newNestedDriveFixture("root-consistent", "Team Docs")
	svc := newFakeDriveService(t, newNestedFixtureHandler(t, fx))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	req := buildMatchRequest(fx.rootName)
	first, err := p.Match(context.Background(), req)
	if err != nil {
		t.Fatalf("first Match: %v", err)
	}
	second, err := p.Match(context.Background(), req)
	if err != nil {
		t.Fatalf("second Match: %v", err)
	}

	if len(first.GetItems()) == 0 {
		t.Fatal("first Match returned zero items, want the fixture's non-folder file count")
	}
	if len(first.GetItems()) != len(second.GetItems()) {
		t.Fatalf("len(first) = %d, len(second) = %d, want equal", len(first.GetItems()), len(second.GetItems()))
	}
	for i := range first.GetItems() {
		if got, want := itemSignature(second.GetItems()[i]), itemSignature(first.GetItems()[i]); got != want {
			t.Errorf("index %d: second call's item differs from the first:\n  first:  %s\n  second: %s", i, want, got)
		}
	}
}

// TestMatch_SingleFileChangeStillReturnsFullCurrentItemSet is
// 03-RESEARCH.md Pitfall 8's own named failure mode turned into a gate: a
// Match following a sync in which exactly one file changed must still
// return the FULL current item set, never just the changed file.
func TestMatch_SingleFileChangeStillReturnsFullCurrentItemSet(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	const rootID = "root-single-change"
	statePath := dataFilePath(isolatedDir, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Team Docs",
		ChangeToken: "token-1",
		Tree: map[string]*driveNode{
			"file-1": nestedFileNode("one.pdf", rootID),
			"file-2": nestedFileNode("two.pdf", rootID),
			"file-3": nestedFileNode("three.pdf", rootID),
		},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	// Exactly one file changed (renamed) since the last sync.
	resp := &drive.ChangeList{
		Changes: []*drive.Change{
			{FileId: "file-2", File: &drive.File{
				Name: "two-renamed.pdf", MimeType: "application/pdf", Parents: []string{rootID},
				ModifiedTime: "2026-08-17T01:00:00Z", WebViewLink: "https://drive.google.com/file/d/file-2/view",
			}},
		},
		NewStartPageToken: "token-2",
	}
	svc := newFakeDriveService(t, newDeltaOnlyHandler(t, resp))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	got, err := p.Match(context.Background(), buildMatchRequest("Team Docs"))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(got.GetItems()) != 3 {
		t.Fatalf("len(Items) = %d, want 3 (the full current set, not just the one changed file)", len(got.GetItems()))
	}
}

// TestMatch_EmptyPersistedTreeReturnsZeroItemsAndNilError proves a Match
// against an empty persisted tree returns zero items and a nil error,
// never an error.
func TestMatch_EmptyPersistedTreeReturnsZeroItemsAndNilError(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	const rootID = "root-empty-tree"
	statePath := dataFilePath(isolatedDir, syncStateFileName)
	seed := &syncState{RootID: rootID, RootName: "Empty Root", ChangeToken: "token-1", Tree: map[string]*driveNode{}}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	svc := newFakeDriveService(t, newDeltaOnlyHandler(t, &drive.ChangeList{NewStartPageToken: "token-2"}))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	got, err := p.Match(context.Background(), buildMatchRequest("Empty Root"))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(got.GetItems()) != 0 {
		t.Errorf("len(Items) = %d, want 0", len(got.GetItems()))
	}
}

// TestMatch_MidDepthValueScopesToThatSubtreeOnly proves a supplied value
// matching a mid-depth subfolder ("Reports") returns every item beneath
// it at every depth (the direct child q1.pdf AND the nested
// q1-2026.pdf), and no item outside it (top.pdf, old.pdf).
func TestMatch_MidDepthValueScopesToThatSubtreeOnly(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := newNestedDriveFixture("root-middepth", "Team Docs")
	svc := newFakeDriveService(t, newNestedFixtureHandler(t, fx))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	got, err := p.Match(context.Background(), buildMatchRequest("Reports"))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	items := got.GetItems()
	if len(items) != 2 {
		t.Fatalf("len(Items) = %d, want 2 (q1.pdf and q1-2026.pdf, both under Reports)", len(items))
	}
	for _, it := range items {
		if it.GetSourceId() != "q1-1" && it.GetSourceId() != "q1-2026-1" {
			t.Errorf("unexpected item outside the Reports subtree: SourceId = %q", it.GetSourceId())
		}
	}
}

// TestMatch_RootNameCountEqualsFixtureNonFolderFileCount proves the
// root's own name returns every non-folder item in the fixture, with a
// count equal to the fixture's own non-folder file count (4) — an
// assertion that cannot pass vacuously.
func TestMatch_RootNameCountEqualsFixtureNonFolderFileCount(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	fx := newNestedDriveFixture("root-count", "Team Docs")
	svc := newFakeDriveService(t, newNestedFixtureHandler(t, fx))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, fx.rootID), svc)

	got, err := p.Match(context.Background(), buildMatchRequest(fx.rootName))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	const wantNonFolderFileCount = 4 // top.pdf, q1.pdf, q1-2026.pdf, old.pdf
	if len(got.GetItems()) != wantNonFolderFileCount {
		t.Errorf("len(Items) = %d, want %d (the fixture's own non-folder file count)", len(got.GetItems()), wantNonFolderFileCount)
	}
}

// TestMatch_AgainstExistingPersistedStateIssuesNoFilesListRequest proves
// SYNC-04's materialization reads the persisted tree, never re-walks
// Drive: a Match against already-existing persisted state makes zero
// files.list requests.
func TestMatch_AgainstExistingPersistedStateIssuesNoFilesListRequest(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	const rootID = "root-no-refetch"
	statePath := dataFilePath(isolatedDir, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Team Docs",
		ChangeToken: "token-1",
		Tree:        map[string]*driveNode{"file-1": nestedFileNode("one.pdf", rootID)},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	recorder := newDriveRecorder(newDeltaOnlyHandler(t, &drive.ChangeList{NewStartPageToken: "token-2"}))
	svc := newFakeDriveService(t, recorder.ServeHTTP)
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	if _, err := p.Match(context.Background(), buildMatchRequest("Team Docs")); err != nil {
		t.Fatalf("Match: %v", err)
	}
	if got := recorder.count("/files"); got != 0 {
		t.Errorf("files.list call count = %d, want 0 (Match against existing persisted state must read the tree, never re-walk Drive)", got)
	}
}

// --- Plan 03-04: default-deny ancestor chain (CR-01 / SYNC-03 gap closure) ---

// TestMatch_ChildOfDeletedIntermediateFolderIsExcludedFromTheItemSet is the
// CR-01 reproduction, end to end through the Match RPC: root "Team Docs"
// holds folder "Confidential" holding file report.pdf; the folder is
// trashed in Drive, and its change is the ONLY entry in the polled batch —
// Drive never emits a change for the resident descendant. The orphaned
// report.pdf must not be returned, and in particular must never match the
// configured root's own name (the GAP-09 matches-everything literal).
func TestMatch_ChildOfDeletedIntermediateFolderIsExcludedFromTheItemSet(t *testing.T) {
	isolatedDir := t.TempDir()
	seedValidToken(t, dataFilePath(isolatedDir, tokenFileName))

	const rootID = "root-cascade"
	statePath := dataFilePath(isolatedDir, syncStateFileName)
	seed := &syncState{
		RootID:      rootID,
		RootName:    "Team Docs",
		ChangeToken: "token-1",
		Tree: map[string]*driveNode{
			"confidential-1": nestedFolderNode("Confidential", rootID),
			"report-1":       nestedFileNode("report.pdf", "confidential-1"),
		},
	}
	if err := saveSyncState(statePath, seed); err != nil {
		t.Fatalf("seed saveSyncState: %v", err)
	}

	resp := &drive.ChangeList{
		Changes: []*drive.Change{
			{FileId: "confidential-1", File: &drive.File{Trashed: true}},
		},
		NewStartPageToken: "token-2",
	}
	svc := newFakeDriveService(t, newDeltaOnlyHandler(t, resp))
	p := pluginWithFakeDrive(t, isolatedDir, sourceConfigJSON(t, rootID), svc)

	got, err := p.Match(context.Background(), buildMatchRequest("Team Docs"))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	for _, it := range got.GetItems() {
		if it.GetSourceId() == "report-1" {
			t.Error("Match returned report-1 — the orphaned descendant of the trashed Confidential folder is still handed to the host")
		}
	}
	if n := len(got.GetItems()); n != 0 {
		t.Errorf("len(items) = %d, want 0 (the only file's containing folder was trashed in the polled batch)", n)
	}
}

// TestMatchItems_NodeWhoseAncestorIsMissingFromTheTreeIsExcluded proves the
// same default-deny property at the matchItems level, independent of any
// state-maintenance fix: a node whose ParentID is absent from the tree has
// an unresolvable chain and must be excluded, never reported as matching
// the configured root's own name.
func TestMatchItems_NodeWhoseAncestorIsMissingFromTheTreeIsExcluded(t *testing.T) {
	st := &syncState{
		RootID:   "root-1",
		RootName: "Team Docs",
		Tree: map[string]*driveNode{
			"report-1": nestedFileNode("report.pdf", "confidential-1"), // confidential-1 deliberately absent
		},
	}
	items := matchItems(st, buildMatchRequest("Team Docs"))
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0 (report-1's ancestor is missing from the tree — unresolvable chain must be out of scope)", len(items))
	}
}

// TestReachabilityVerdictsAgree_AncestorChainValuesMatchesReachesRoot is
// the gate against this plan's root cause: CR-01 existed because this
// repository had TWO ancestor-walking functions — match.go's
// ancestorChainValues and changepoll.go's reachesRoot — whose semantics
// were never asserted against each other, and the permissive one was the
// one Match actually consulted. This table calls BOTH functions on the
// same tree for every chain shape and asserts their verdicts are equal,
// so the two walkers can never again silently disagree on what an
// unresolvable chain means. Do not delete this test as redundant with the
// per-function tests — pinning the two functions to EACH OTHER is its
// entire purpose.
func TestReachabilityVerdictsAgree_AncestorChainValuesMatchesReachesRoot(t *testing.T) {
	const rootID = "root"
	cases := []struct {
		name     string
		tree     map[string]*driveNode
		parentID string
	}{
		{
			name:     "parent is root",
			tree:     map[string]*driveNode{},
			parentID: rootID,
		},
		{
			name:     "two-level chain reaching root",
			tree:     map[string]*driveNode{"sub-a": {Name: "A", ParentID: rootID}},
			parentID: "sub-a",
		},
		{
			name: "multi-level chain reaching root",
			tree: map[string]*driveNode{
				"sub-a": {Name: "A", ParentID: rootID},
				"sub-b": {Name: "B", ParentID: "sub-a"},
			},
			parentID: "sub-b",
		},
		{
			name:     "ancestor absent from the tree",
			tree:     map[string]*driveNode{},
			parentID: "unknown-parent",
		},
		{
			name:     "empty parent id",
			tree:     map[string]*driveNode{},
			parentID: "",
		},
		{
			name:     "self-parented entry",
			tree:     map[string]*driveNode{"self": {Name: "Self", ParentID: "self"}},
			parentID: "self",
		},
		{
			name: "two-node cycle",
			tree: map[string]*driveNode{
				"a": {Name: "A", ParentID: "b"},
				"b": {Name: "B", ParentID: "a"},
			},
			parentID: "a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, chainVerdict := ancestorChainValues(tc.tree, rootID, "Team Docs", tc.parentID)
			rootVerdict := reachesRoot(tc.tree, tc.parentID, rootID)
			if chainVerdict != rootVerdict {
				t.Errorf("%s: ancestorChainValues reachable = %v, reachesRoot = %v — the two ancestor walkers disagree on reachability", tc.name, chainVerdict, rootVerdict)
			}
		})
	}
}
