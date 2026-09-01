package main

import (
	"context"
	"fmt"

	"github.com/davison/topos-plugins/searchkit"
	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Search (M2-R2, davison/topos#50) — sdk.ContentSearcher for WhatsApp:
// exactly Match's membership (the `groups`/`contacts` keywords over this
// plugin's own message store), then the query and required terms against
// each message body within those chats. The hit is the chat-day digest
// the stream shows, with a bounded snippet of the first matching message.
func (p *SourcePlugin) Search(_ context.Context, req *toposv1.SearchRequest) (*toposv1.SearchResponse, error) {
	if err := searchkit.RequireMembership(req); err != nil {
		return nil, err
	}
	state := p.healthState()
	if !state.Healthy() {
		return nil, status.Errorf(codes.Unavailable, "whatsapp: %s", p.currentMessage())
	}
	terms := searchkit.Terms(req.GetQuery())
	if len(terms) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	required := searchkit.Required(req)
	groupKeywords := req.GetMatchFields()["groups"].GetValues()
	contactKeywords := req.GetMatchFields()["contacts"].GetValues()
	if len(groupKeywords) == 0 && len(contactKeywords) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	chats, err := p.store.Chats()
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "whatsapp: %v", err)
	}
	matched := eligibleChats(chats, groupKeywords, contactKeywords)
	if len(matched) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	chatJIDs := make([]string, 0, len(matched))
	names := make(map[string]string, len(matched))
	isGroups := make(map[string]bool, len(matched))
	for _, c := range matched {
		chatJIDs = append(chatJIDs, c.ChatJID)
		if c.IsGroup {
			names[c.ChatJID] = c.Name
		} else {
			names[c.ChatJID] = c.ContactName
		}
		isGroups[c.ChatJID] = c.IsGroup
	}
	msgs, err := p.store.MessagesForChats(chatJIDs)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "whatsapp: %v", err)
	}
	type dayKey struct{ chat, day string }
	firstMatch := map[dayKey]string{}
	for _, m := range msgs {
		if m.Body == "" || !searchkit.ContainsAll(m.Body, terms) || !searchkit.ContainsAll(m.Body, required) {
			continue
		}
		k := dayKey{m.ChatJID, localDayKey(m.SentAtUnixMs)}
		if _, seen := firstMatch[k]; !seen {
			firstMatch[k] = m.Body
		}
	}
	var hits []*toposv1.SearchHit
	for _, d := range buildDigests(msgs, names) {
		body, ok := firstMatch[dayKey{d.ChatJID, d.Day}]
		if !ok {
			continue
		}
		hits = append(hits, &toposv1.SearchHit{Item: p.toItem(d, isGroups[d.ChatJID]), Snippet: searchkit.Snippet(body, terms), MatchedIn: toposv1.MatchedIn_MATCHED_IN_BODY})
	}
	hits, truncated := searchkit.Limit(hits, req)
	fmt.Fprintf(p.logOut, "%s: search: %d matched chat(s), %d digest hit(s)\n", pluginName, len(matched), len(hits))
	return &toposv1.SearchResponse{Hits: hits, Truncated: truncated, Note: "searched message bodies within the member chats"}, nil
}
