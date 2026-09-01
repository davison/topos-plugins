package main

import (
	"context"
	"testing"
	"time"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func groupsSearchReq(groups []string, query string, required ...string) *toposv1.SearchRequest {
	return &toposv1.SearchRequest{
		Query:         query,
		MatchFields:   map[string]*toposv1.StringList{"groups": {Values: groups}},
		RequiredTerms: required,
	}
}

// Search reaches captured message bodies inside the member chats only: a
// hit is the day digest whose messages carried the terms, snippet from
// the first matching body; a non-member chat's bodies are never searched.
func TestSearch_BodiesWithinMemberChats(t *testing.T) {
	p := newTestPlugin(t)
	p.setHealthState(healthStateLinked, "")
	now := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC).UnixMilli()
	for jid, name := range map[string]string{"1@g.us": "Book Club", "2@g.us": "Other Group"} {
		if err := p.store.EnsureChat(jid, true); err != nil {
			t.Fatalf("EnsureChat: %v", err)
		}
		if err := p.store.UpsertChatName(jid, true, name, now); err != nil {
			t.Fatalf("UpsertChatName: %v", err)
		}
	}
	msgs := []messageRecord{
		{ID: "m1", ChatJID: "1@g.us", SenderJID: "a@s.whatsapp.net", SenderName: "Ann", SentAtUnixMs: now, Body: "who has the Dune paperback?"},
		{ID: "m2", ChatJID: "1@g.us", SenderJID: "b@s.whatsapp.net", SenderName: "Bo", SentAtUnixMs: now + 60_000, Body: "I do, bringing it Thursday"},
		{ID: "m3", ChatJID: "2@g.us", SenderJID: "c@s.whatsapp.net", SenderName: "Cy", SentAtUnixMs: now, Body: "dune sandwiches for the picnic"},
	}
	for _, m := range msgs {
		if err := p.store.Append(m); err != nil {
			t.Fatalf("Append %s: %v", m.ID, err)
		}
	}
	ctx := context.Background()
	resp, err := p.Search(ctx, groupsSearchReq([]string{"Book Club"}, "dune"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.GetHits()) != 1 {
		t.Fatalf("got %d hits, want the one Book Club day digest: %v", len(resp.GetHits()), resp.GetHits())
	}
	hit := resp.GetHits()[0]
	if hit.GetMatchedIn() != toposv1.MatchedIn_MATCHED_IN_BODY {
		t.Fatalf("matched_in = %v, want BODY", hit.GetMatchedIn())
	}
	if hit.GetSnippet() != "who has the Dune paperback?" {
		t.Fatalf("snippet = %q, want the matching body", hit.GetSnippet())
	}
	if hit.GetItem().GetTitle() == "" {
		t.Fatalf("hit item is not a digest: %+v", hit.GetItem())
	}

	// A required term from the other group's message narrows to nothing.
	resp, err = p.Search(ctx, groupsSearchReq([]string{"Book Club"}, "dune", "sandwiches"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.GetHits()) != 0 {
		t.Fatalf("required term not honoured / non-member leaked: %d hit(s)", len(resp.GetHits()))
	}
}

func TestSearch_RefusesEmptyMembership(t *testing.T) {
	p := newTestPlugin(t)
	p.setHealthState(healthStateLinked, "")
	_, err := p.Search(context.Background(), &toposv1.SearchRequest{Query: "dune"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty match_fields: got %v, want InvalidArgument", err)
	}
}
