package main

import "testing"

func TestEligibleConversations_GroupNameExactCaseInsensitiveMatches(t *testing.T) {
	convs := []conversation{
		{ID: "g1", Type: "group", Name: "House Move"},
	}
	got := eligibleConversations(convs, []string{"house move"})
	if len(got) != 1 || got[0].ID != "g1" {
		t.Fatalf("expected g1 to match case-insensitively, got %+v", got)
	}
}

func TestEligibleConversations_GroupNameSubstringDoesNotMatch(t *testing.T) {
	convs := []conversation{
		{ID: "g1", Type: "group", Name: "Household"},
	}
	got := eligibleConversations(convs, []string{"house"})
	if len(got) != 0 {
		t.Fatalf("expected zero matches for a substring-only match, got %+v", got)
	}
}

func TestEligibleConversations_PrivateMatchesOnNickname(t *testing.T) {
	convs := []conversation{
		{ID: "p1", Type: "private", NicknameGivenName: "Dad", ProfileName: "George", ProfileFamilyName: "Davison"},
	}
	got := eligibleConversations(convs, []string{"Dad"})
	if len(got) != 1 || got[0].ID != "p1" {
		t.Fatalf("expected p1 to match on nickname, got %+v", got)
	}
}

func TestEligibleConversations_PrivateMatchesOnSystemContactName(t *testing.T) {
	convs := []conversation{
		{ID: "p1", Type: "private", SystemGivenName: "Luke", SystemFamilyName: "Ward", ProfileName: "Luke"},
	}
	got := eligibleConversations(convs, []string{"Luke Ward"})
	if len(got) != 1 || got[0].ID != "p1" {
		t.Fatalf("expected p1 to match on system contact name, got %+v", got)
	}
}

// TestEligibleConversations_PrivateProfileNameOnlyDoesNotMatch is the
// load-bearing D-06 case: a 1:1 conversation whose ONLY name field equal
// to a keyword is the contact's self-chosen profile name must NOT match.
func TestEligibleConversations_PrivateProfileNameOnlyDoesNotMatch(t *testing.T) {
	convs := []conversation{
		{ID: "p1", Type: "private", ProfileName: "House", ProfileFamilyName: "Move"},
	}
	got := eligibleConversations(convs, []string{"House Move"})
	if len(got) != 0 {
		t.Fatalf("expected zero matches when only the profile name matches (D-06), got %+v", got)
	}
}

// TestEligibleConversations_DerivedTitleFallsBackToProfileNameStillDoesNotMatch
// covers "a 1:1 conversation whose derived title field equals a keyword
// but whose own-name fields do not does NOT match (the derived title
// falls back to the profile name)" — conv.Name here simulates a
// materialized "title" column that (like Signal Desktop's own computed
// title getter) falls back to the profile name; candidateNames must never
// consult conv.Name for a private conversation at all.
func TestEligibleConversations_DerivedTitleFallsBackToProfileNameStillDoesNotMatch(t *testing.T) {
	convs := []conversation{
		{ID: "p1", Type: "private", Name: "House Move", ProfileName: "House", ProfileFamilyName: "Move"},
	}
	got := eligibleConversations(convs, []string{"House Move"})
	if len(got) != 0 {
		t.Fatalf("expected zero matches when only the derived title (conv.Name) matches, got %+v", got)
	}
}

func TestEligibleConversations_NoteToSelfNeverMatches(t *testing.T) {
	convs := []conversation{
		{ID: "self", Type: "private", IsNoteToSelf: true, SystemGivenName: "House", NicknameGivenName: "House"},
	}
	got := eligibleConversations(convs, []string{"House"})
	if len(got) != 0 {
		t.Fatalf("expected Note to Self to never match even when its name equals a keyword, got %+v", got)
	}
}

func TestEligibleConversations_EmptyKeywordListYieldsZeroMatches(t *testing.T) {
	convs := []conversation{
		{ID: "g1", Type: "group", Name: "House Move"},
		{ID: "p1", Type: "private", NicknameGivenName: "Dad"},
	}
	got := eligibleConversations(convs, nil)
	if len(got) != 0 {
		t.Fatalf("expected zero matches for an empty keyword list, got %+v", got)
	}
}
