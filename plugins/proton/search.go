package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/davison/topos-plugins/searchkit"
	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
	"github.com/emersion/go-imap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Search (M2-R2, davison/topos#50) — sdk.ContentSearcher for Proton Mail
// (IMAP through the Bridge): exactly Match's membership (the `folders`
// keywords against mailbox leaf names), then the server's own search —
// IMAP SEARCH TEXT, which covers headers and bodies — for every query and
// required term, within each member mailbox (read-only EXAMINE, as Match).
// Only the matching messages' envelopes are fetched, never a body, so a
// hit carries no snippet beyond its subject; matched_in is TITLE when the
// subject alone carries every term, otherwise BODY.
func (p *SourcePlugin) Search(_ context.Context, req *toposv1.SearchRequest) (*toposv1.SearchResponse, error) {
	if err := searchkit.RequireMembership(req); err != nil {
		return nil, err
	}
	terms := searchkit.Terms(req.GetQuery())
	if len(terms) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	required := searchkit.Required(req)
	keywords := req.GetMatchFields()["folders"].GetValues()
	if len(keywords) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	conn, err := p.client.connect(syncDialTimeout)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "proton: connect: %v", err)
	}
	defer conn.Logout()
	mailboxes, err := listMailboxes(conn)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "proton: list mailboxes: %v", err)
	}
	var matchedMailboxes []mailboxInfo
	for _, mbox := range mailboxes {
		leaf := leafName(mbox.name, mbox.delimiter)
		if matchesAnyKeyword(leaf, keywords) {
			matchedMailboxes = append(matchedMailboxes, mailboxInfo{name: mbox.name, leaf: leaf})
		}
	}
	if len(matchedMailboxes) == 0 {
		return &toposv1.SearchResponse{Hits: []*toposv1.SearchHit{}}, nil
	}
	criteria := imap.NewSearchCriteria()
	criteria.Text = append(append([]string{}, terms...), required...)

	byMessageID := map[string]*matched{}
	for _, mbox := range matchedMailboxes {
		mboxStatus, err := conn.Select(mbox.name, true) // readOnly=true -> IMAP EXAMINE
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "proton: examine %q: %v", mbox.name, err)
		}
		if mboxStatus.Messages == 0 {
			continue
		}
		uids, err := conn.UidSearch(criteria)
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "proton: search %q: %v", mbox.name, err)
		}
		if len(uids) == 0 {
			continue
		}
		seqset := new(imap.SeqSet)
		seqset.AddNum(uids...)
		items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchInternalDate, imap.FetchUid}
		messages := make(chan *imap.Message, 32)
		done := make(chan error, 1)
		go func() { done <- conn.UidFetch(seqset, items, messages) }()
		for msg := range messages {
			if msg == nil || msg.Envelope == nil {
				continue
			}
			id := normalizeMessageID(msg.Envelope.MessageId)
			if id == "" {
				continue
			}
			if m, ok := byMessageID[id]; ok {
				m.labels = appendUniqueLabel(m.labels, mbox.leaf)
				continue
			}
			byMessageID[id] = &matched{envelope: msg.Envelope, mailbox: mbox.name, internalDate: msg.InternalDate, labels: []string{mbox.leaf}}
		}
		if err := <-done; err != nil {
			return nil, status.Errorf(codes.Unavailable, "proton: fetch %q: %v", mbox.name, err)
		}
	}
	discovered := make(map[string]string, len(byMessageID))
	var hits []*toposv1.SearchHit
	for msgID, m := range byMessageID {
		sourceID := encodeSourceID(msgID)
		discovered[sourceID] = m.mailbox
		it := p.toItem(sourceID, m)
		where := toposv1.MatchedIn_MATCHED_IN_BODY
		if searchkit.ContainsAll(strings.ToLower(m.envelope.Subject), terms) {
			where = toposv1.MatchedIn_MATCHED_IN_TITLE
		}
		hits = append(hits, &toposv1.SearchHit{Item: it, Snippet: "", MatchedIn: where})
	}
	p.mergeMailboxCache(discovered)
	searchkit.SortHitsByTimestamp(hits)
	hits, truncated := searchkit.Limit(hits, req)
	return &toposv1.SearchResponse{Hits: hits, Truncated: truncated, Note: fmt.Sprintf("IMAP SEARCH TEXT within %d member mailbox(es); bodies were not fetched, so no snippets", len(matchedMailboxes))}, nil
}
