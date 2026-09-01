package main

import (
	"context"
	"fmt"

	"github.com/davison/topos-plugins/searchkit"
	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Search (M2-R2, davison/topos#50) — sdk.ContentSearcher for Signal:
// exactly Match's membership (the `conversations` keywords, through the
// same guarded read-only open and read set), then the query and required
// terms against each message's body within those conversations. The unit
// of a hit is what the stream shows — the conversation-day digest — so a
// day whose messages contain a match is the hit, with a bounded snippet
// of the first matching message. Nothing is written; the schema guard
// applies exactly as it does to Match.
func (p *SourcePlugin) Search(_ context.Context, req *toposv1.SearchRequest) (*toposv1.SearchResponse, error) {
	if err := searchkit.RequireMembership(req, "conversations"); err != nil {
		return nil, err
	}
	terms := searchkit.Terms(req.GetQuery())
	if len(terms) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	required := searchkit.Required(req)
	keywords := req.GetMatchFields()["conversations"].GetValues()
	if len(keywords) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	db, err := p.openGuarded()
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "signal: %v", err)
	}
	defer db.Close()
	ownAci, err := readOwnAci(db)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "signal: %v", err)
	}
	convs, err := readConversations(db, ownAci)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "signal: %v", err)
	}
	matched := eligibleConversations(convs, keywords)
	if len(matched) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	matchedByID := make(map[string]conversation, len(matched))
	convIDs := make([]string, 0, len(matched))
	names := make(map[string]string, len(matched))
	for _, c := range matched {
		matchedByID[c.ID] = c
		convIDs = append(convIDs, c.ID)
		names[c.ID] = conversationDisplayName(c)
	}
	msgs, err := readMessages(db, convIDs, buildSenderNames(convs, ownAci))
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "signal: %v", err)
	}
	// Which conversation-days contain a matching message, and the first
	// matching body for the snippet.
	type dayKey struct{ conv, day string }
	firstMatch := map[dayKey]string{}
	for _, m := range msgs {
		if m.Body == "" || !searchkit.ContainsAll(m.Body, terms) || !searchkit.ContainsAll(m.Body, required) {
			continue
		}
		k := dayKey{m.ConversationID, localDayKey(m.SentAtUnixMs)}
		if _, seen := firstMatch[k]; !seen {
			firstMatch[k] = m.Body
		}
	}
	var hits []*toposv1.SearchHit
	for _, d := range buildDigests(msgs, names) {
		body, ok := firstMatch[dayKey{d.ConversationID, d.Day}]
		if !ok {
			continue
		}
		hits = append(hits, &toposv1.SearchHit{
			Item:      p.toItem(d, matchedByID[d.ConversationID]),
			Snippet:   searchkit.Snippet(body, terms),
			MatchedIn: toposv1.MatchedIn_MATCHED_IN_BODY,
		})
	}
	hits, truncated := searchkit.Limit(hits, req)
	fmt.Fprintf(p.logOut, "%s: search: %d matched conversation(s), %d digest hit(s)\n", pluginName, len(matched), len(hits))
	return &toposv1.SearchResponse{Hits: hits, Truncated: truncated, Note: "searched message bodies within the member conversations"}, nil
}
