package main

import (
	"context"
	"testing"
	"time"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	imapclient "github.com/emersion/go-imap/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newSearchTestPlugin(t *testing.T) *SourcePlugin {
	t.Helper()
	serverAddr := newTestIMAPServer(t)
	plugin, err := NewSourcePlugin("imap://bridge.invalid:143", "username", "password", "", "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}
	plugin.client.dial = func(timeout time.Duration) (*imapclient.Client, error) {
		conn, err := imapclient.Dial(serverAddr)
		if err != nil {
			return nil, err
		}
		conn.Timeout = timeout
		return conn, nil
	}
	return plugin
}

func searchReq(folders []string, query string, required ...string) *toposv1.SearchRequest {
	return &toposv1.SearchRequest{
		Query:         query,
		MatchFields:   map[string]*toposv1.StringList{"folders": {Values: folders}},
		RequiredTerms: required,
	}
}

// Search runs IMAP SEARCH TEXT inside the member mailboxes only: the
// gamma message is found by a term in its subject (TITLE), the alpha
// message by a body-only term (BODY), and a term that lives in a
// non-member mailbox finds nothing.
func TestSearch_WithinMemberMailboxes(t *testing.T) {
	p := newSearchTestPlugin(t)
	ctx := context.Background()

	resp, err := p.Search(ctx, searchReq([]string{"GammaTeam"}, "gamma"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.GetHits()) != 1 {
		t.Fatalf("got %d hits, want 1: %v", len(resp.GetHits()), resp.GetHits())
	}
	hit := resp.GetHits()[0]
	if hit.GetItem().GetSourceId() != encodeSourceID(gammaMessageID) {
		t.Fatalf("hit source_id = %q, want gamma's", hit.GetItem().GetSourceId())
	}
	if hit.GetMatchedIn() != toposv1.MatchedIn_MATCHED_IN_TITLE {
		t.Fatalf("matched_in = %v, want TITLE", hit.GetMatchedIn())
	}

	// The same term, membership elsewhere: nothing.
	resp, err = p.Search(ctx, searchReq([]string{"AlphaTeam"}, "gamma"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.GetHits()) != 0 {
		t.Fatalf("non-member mailbox leaked %d hit(s)", len(resp.GetHits()))
	}
}

// A message seen in two member mailboxes is one hit carrying both labels,
// as Match dedups it (Pattern 2).
func TestSearch_DedupsAcrossMemberMailboxes(t *testing.T) {
	p := newSearchTestPlugin(t)
	resp, err := p.Search(context.Background(), searchReq([]string{"AlphaTeam", "BetaTeam"}, "label"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.GetHits()) != 1 {
		t.Fatalf("got %d hits, want 1 (dedup across two member mailboxes)", len(resp.GetHits()))
	}
	if got := resp.GetHits()[0].GetItem().GetLabels(); len(got) != 2 {
		t.Fatalf("labels = %v, want both mailbox leaves", got)
	}
}

func TestSearch_RequiredTermsNarrow(t *testing.T) {
	p := newSearchTestPlugin(t)
	resp, err := p.Search(context.Background(), searchReq([]string{"GammaTeam"}, "message", "no-such-term-anywhere"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.GetHits()) != 0 {
		t.Fatalf("required term not honoured: %d hit(s)", len(resp.GetHits()))
	}
}

func TestSearch_RefusesEmptyMembership(t *testing.T) {
	p := newSearchTestPlugin(t)
	_, err := p.Search(context.Background(), &toposv1.SearchRequest{Query: "gamma"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty match_fields: got %v, want InvalidArgument", err)
	}
}
