package main

import (
	"context"
	"testing"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func conversationsSearchReq(values []string, query string, required ...string) *toposv1.SearchRequest {
	return &toposv1.SearchRequest{
		Query:         query,
		MatchFields:   map[string]*toposv1.StringList{"conversations": {Values: values}},
		RequiredTerms: required,
	}
}

// Search reaches message bodies inside the member conversations only: a
// hit is the day digest whose messages carried the terms, with the first
// matching body as its snippet, and a term that only lives in a
// non-member conversation finds nothing.
func TestSearch_BodiesWithinMemberConversations(t *testing.T) {
	plugin, groupDay1, _ := newFetchTestPlugin(t)
	ctx := context.Background()

	resp, err := plugin.Search(ctx, conversationsSearchReq([]string{"House Move"}, "van"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.GetHits()) != 2 {
		t.Fatalf("got %d hits, want the two House Move day digests: %v", len(resp.GetHits()), resp.GetHits())
	}
	var sawDay1 bool
	for _, h := range resp.GetHits() {
		if h.GetMatchedIn() != toposv1.MatchedIn_MATCHED_IN_BODY {
			t.Fatalf("matched_in = %v, want BODY", h.GetMatchedIn())
		}
		if h.GetSnippet() == "" {
			t.Fatalf("hit %q carries no snippet", h.GetItem().GetSourceId())
		}
		if h.GetItem().GetSourceId() == groupDay1 {
			sawDay1 = true
			if h.GetSnippet() != "let's book the van" {
				t.Fatalf("day-1 snippet = %q, want the matching body", h.GetSnippet())
			}
		}
	}
	if !sawDay1 {
		t.Fatalf("day-1 digest %q missing from hits", groupDay1)
	}

	// Required terms narrow within the day: "booked" is only day 2.
	resp, err = plugin.Search(ctx, conversationsSearchReq([]string{"House Move"}, "van", "booked"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.GetHits()) != 1 || resp.GetHits()[0].GetItem().GetSourceId() == groupDay1 {
		t.Fatalf("required term: got %v, want only the day-2 digest", resp.GetHits())
	}

	// "3pm" lives in the private conversation, which is not a member.
	resp, err = plugin.Search(ctx, conversationsSearchReq([]string{"House Move"}, "3pm"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.GetHits()) != 0 {
		t.Fatalf("non-member conversation leaked %d hit(s)", len(resp.GetHits()))
	}
}

func TestSearch_RefusesEmptyMembership(t *testing.T) {
	plugin, _, _ := newFetchTestPlugin(t)
	_, err := plugin.Search(context.Background(), &toposv1.SearchRequest{Query: "van"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty match_fields: got %v, want InvalidArgument", err)
	}
}
