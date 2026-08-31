package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	imap "github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

const (
	sourceType      = "proton"
	displayName     = "Proton Mail"
	contractVersion = "topos.v2"

	// noSubjectPlaceholder is used as Title when a message's ENVELOPE
	// carries an empty Subject.
	noSubjectPlaceholder = "(no subject)"

	// iconMIME is the declared mime for iconSVG below, returned verbatim
	// from Describe (09-02-PLAN.md Task 2, 09-UI-SPEC.md Fix 10).
	iconMIME = "image/svg+xml"
)

// matchVocabulary is the field-name vocabulary this plugin declares and
// reads from MatchRequest.match_fields — proton's own native categorization
// is its IMAP mailbox/label leaf names.
var matchVocabulary = []string{"folders"}

// iconSVG is this plugin's identity icon — a Lucide Mail glyph with its
// stroke color baked to the literal --muted-foreground hex (see
// assets/icon.svg's own provenance comment). Returned verbatim from
// Describe's Icon field; the kernel caches it at that call site and
// serves it at GET /api/plugins/topos-plugin-proton/icon.
//
// Source-Project: @lucide/svelte (lucide-icons/lucide)
// Source-File:    dist/icons/mail.svelte
// Source-Version: @lucide/svelte v1.27.0
// Source-License: ISC
//
//go:embed assets/icon.svg
var iconSVG []byte

// noThumbnailReason is the fixed unavailable_reason for the THUMBNAIL
// content variant — an email has no image rendition, ever.
const noThumbnailReason = "Proton Mail messages have no thumbnail rendition"

// matched holds one Message-Id's Match-time state, built while scanning
// every keyword-matched mailbox and merged across every matching mailbox
// (03-RESEARCH.md Pattern 2): the same message can legitimately appear
// under several Labels/* mailboxes simultaneously (Proton's non-
// destructive label model), and every matched label must be preserved on
// the single resulting item rather than discarded by a later, naive
// overwrite.
type matched struct {
	envelope *imap.Envelope
	mailbox  string // the FIRST mailbox this message was found in — Fetch's mailbox-lookup cache target
	// internalDate is the message's IMAP INTERNALDATE — the primary
	// chronological sort key for the resulting item (Item.TimestampUnix),
	// distinct from the envelope's own Date header (which feeds
	// SecondaryTimestampUnix instead). Set from the FIRST mailbox this
	// message was found in, exactly like mailbox and envelope above: the
	// merge branch for an already-seen Message-Id deliberately leaves it
	// untouched.
	internalDate time.Time
	labels       []string // every matched mailbox's leaf name, deduplicated
}

// SourcePlugin implements sdk.SourcePlugin against a Proton Mail Bridge
// instance via Client.
type SourcePlugin struct {
	client         *Client
	baseURL        string
	webmailBaseURL string

	// mailboxCache maps a source_id (the Task 2 encoding of a normalized
	// Message-ID) to the mailbox name Fetch should SELECT to resolve it.
	// Entries ACCUMULATE across Match calls and are never discarded
	// wholesale (03-06 Task 1, closing 03-REVIEW.md CR-01): kernel/
	// correlate.SyncSource calls Match once per configured webspace within
	// a single sync cycle, sequentially, against the ONE long-lived plugin
	// instance kernel/pluginhost launches per source — so a per-call
	// rebuild (03-RESEARCH.md's original "Fetch-time mailbox lookup"
	// finding, whose reasoning covered only the fresh-process case) would
	// leave only the last-processed webspace's items resolvable, which is
	// the defect this comment now exists to prevent recurring. Two
	// consequences of accumulating instead:
	//   (a) growth is bounded and steady-state stable: each entry is two
	//       short strings, and a repeat sync cycle upserts the same keys
	//       rather than appending, so size tracks the number of distinct
	//       messages across all keyword-matched mailboxes, not the number
	//       of cycles that have run.
	//   (b) a stale entry for a message that has moved to a different
	//       mailbox or been deleted is harmless: fetchFull re-resolves the
	//       UID via UID SEARCH HEADER Message-Id inside the EXAMINEd
	//       mailbox, so a stale entry yields a clean NotFound rather than
	//       another message's body, and the next Match that rediscovers
	//       the message overwrites the entry with its current mailbox
	//       (last-writer-wins per key — see mergeMailboxCache).
	// A Fetch issued before the first sync completes (e.g. immediately
	// after a kernel restart) still surfaces as a clear NotFound below,
	// not a silent failure.
	mailboxMu    sync.RWMutex
	mailboxCache map[string]string

	// logOut is the plugin's log sink — os.Stderr in production, which
	// go-plugin parses and re-emits through the kernel's hclog so plugin
	// and kernel logs interleave sanely. Overridable in tests, mirroring
	// Client.dial.
	logOut io.Writer
}

// NewSourcePlugin builds a SourcePlugin. baseURL, username, token and
// webmailBaseURL must be non-empty — main.go fails startup loudly if any
// is empty after config expansion. caCertPath is optional (see
// NewClient's doc comment).
func NewSourcePlugin(baseURL, username, token, caCertPath, webmailBaseURL string) (*SourcePlugin, error) {
	client, err := NewClient(baseURL, username, token, caCertPath)
	if err != nil {
		return nil, err
	}
	return &SourcePlugin{
		client:         client,
		baseURL:        baseURL,
		webmailBaseURL: strings.TrimRight(webmailBaseURL, "/"),
		mailboxCache:   make(map[string]string),
		logOut:         os.Stderr,
	}, nil
}

func (p *SourcePlugin) Describe(_ context.Context, _ *toposv1.DescribeRequest) (*toposv1.DescribeResponse, error) {
	return &toposv1.DescribeResponse{
		SourceType:      sourceType,
		DisplayName:     displayName,
		ContractVersion: contractVersion,
		MatchVocabulary: matchVocabulary,
		Icon:            iconSVG,
		IconMime:        iconMIME,
	}, nil
}

// Match lists every mailbox once, selects those whose leaf name
// case-insensitively equals one of match_fields["folders"]'s values
// (D-05: any other key in the request map is ignored), EXAMINEs each
// matched mailbox and fetches ENVELOPE+INTERNALDATE+UID for every message
// in it, then merges results by normalized Message-ID (Pattern 2) before
// building the returned items. A webspace with zero matching mailbox leaf
// names returns a successful, empty response (never an error) — see
// 03-RESEARCH.md Pitfall 2 / this plan's must_haves.
func (p *SourcePlugin) Match(ctx context.Context, req *toposv1.MatchRequest) (*toposv1.MatchResponse, error) {
	keywords := req.GetMatchFields()["folders"].GetValues()
	if len(keywords) == 0 {
		return &toposv1.MatchResponse{}, nil
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
		// No mailbox leaf name matches this webspace's keywords: a
		// successful, empty sync — never an error, and never a wipe of a
		// sibling source's already-indexed rows for this webspace (the
		// caller, kernel/correlate, replaces only THIS source's rows).
		// This webspace has nothing to contribute, and the resolution
		// state below is shared across every webspace this one plugin
		// instance serves, so clearing it here would erase entries an
		// earlier webspace's Match call added moments ago in the same
		// sync cycle — no cache mutation of any kind happens on this
		// branch.
		return &toposv1.MatchResponse{}, nil
	}

	byMessageID := map[string]*matched{}
	var skippedNoMessageID int

	for _, mbox := range matchedMailboxes {
		mboxStatus, err := conn.Select(mbox.name, true) // readOnly=true -> IMAP EXAMINE
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "proton: examine %q: %v", mbox.name, err)
		}
		if mboxStatus.Messages == 0 {
			// A matched mailbox with zero messages contributes zero items
			// without failing the sync.
			continue
		}

		seqset := new(imap.SeqSet)
		seqset.AddRange(1, mboxStatus.Messages)

		items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchInternalDate, imap.FetchUid}
		messages := make(chan *imap.Message, 32)
		done := make(chan error, 1)
		go func() { done <- conn.Fetch(seqset, items, messages) }()

		for msg := range messages {
			if msg == nil || msg.Envelope == nil {
				continue
			}
			id := normalizeMessageID(msg.Envelope.MessageId)
			if id == "" {
				skippedNoMessageID++
				continue
			}
			if m, ok := byMessageID[id]; ok {
				m.labels = appendUniqueLabel(m.labels, mbox.leaf)
				continue
			}
			byMessageID[id] = &matched{
				envelope:     msg.Envelope,
				mailbox:      mbox.name,
				internalDate: msg.InternalDate,
				labels:       []string{mbox.leaf},
			}
		}
		if err := <-done; err != nil {
			return nil, status.Errorf(codes.Unavailable, "proton: fetch %q: %v", mbox.name, err)
		}
	}

	discovered := make(map[string]string, len(byMessageID))
	items := make([]*toposv1.Item, 0, len(byMessageID))
	for msgID, m := range byMessageID {
		sourceID := encodeSourceID(msgID)
		discovered[sourceID] = m.mailbox
		items = append(items, p.toItem(sourceID, m))
	}
	p.mergeMailboxCache(discovered)

	if skippedNoMessageID > 0 {
		// Count-only: never a subject, sender, Message-Id, mailbox name,
		// base URL or credential. This log is forwarded verbatim into the
		// kernel's log stream, and everything written there outlives the
		// process (T-03-05-03).
		fmt.Fprintf(p.logOut, "topos-plugin-proton: match: skipped %d message(s) with no Message-Id header\n", skippedNoMessageID)
	}

	return &toposv1.MatchResponse{Items: items}, nil
}

type mailboxInfo struct {
	name string
	leaf string
}

// listMailboxes runs LIST "" "*" against conn and returns every mailbox's
// name and hierarchy delimiter.
func listMailboxes(conn *imapclient.Client) ([]struct{ name, delimiter string }, error) {
	ch := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() { done <- conn.List("", "*", ch) }()

	var out []struct{ name, delimiter string }
	for info := range ch {
		out = append(out, struct{ name, delimiter string }{name: info.Name, delimiter: info.Delimiter})
	}
	if err := <-done; err != nil {
		return nil, err
	}
	return out, nil
}

// leafName returns the segment of mailboxName after its last hierarchy
// delimiter (e.g. "Labels/House Move" with delimiter "/" -> "House
// Move"). An empty delimiter (some servers report none for a flat
// namespace) leaves mailboxName unchanged.
func leafName(mailboxName, delimiter string) string {
	if delimiter == "" {
		return mailboxName
	}
	idx := strings.LastIndex(mailboxName, delimiter)
	if idx < 0 {
		return mailboxName
	}
	return mailboxName[idx+len(delimiter):]
}

// matchesAnyKeyword reports whether leaf case-insensitively equals any
// of keywords (exact match, no substring/prefix matching — D-03).
func matchesAnyKeyword(leaf string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.EqualFold(leaf, kw) {
			return true
		}
	}
	return false
}

// appendUniqueLabel appends label to labels if not already present.
func appendUniqueLabel(labels []string, label string) []string {
	for _, l := range labels {
		if l == label {
			return labels
		}
	}
	return append(labels, label)
}

// normalizeMessageID trims whitespace, then one leading '<' and one
// trailing '>', from a raw ENVELOPE Message-Id header value. Message-ID
// equality elsewhere is exact byte comparison of this normalized form —
// no case folding, no Unicode normalization.
func normalizeMessageID(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return s
}

// encodeSourceID implements the Task 2 decision (option-a): a pure
// function of the normalized Message-ID alone, encoded as URL-safe
// base64 with no padding (base64.RawURLEncoding). Contains only
// [A-Za-z0-9_-], so it is safe in a URL path segment with no escaping
// subtleties, and is fully reversible via decodeSourceID with no extra
// state to persist — see docs/api.md's "stable-ID scheme" section.
func encodeSourceID(normalizedMessageID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(normalizedMessageID))
}

// decodeSourceID reverses encodeSourceID, recovering the normalized
// Message-ID a source_id was built from.
func decodeSourceID(sourceID string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(sourceID)
	if err != nil {
		return "", fmt.Errorf("proton: decode source_id %q: %w", sourceID, err)
	}
	return string(b), nil
}

// formatSender returns the display sender ("personal name <mbox@host>"
// style collapsed to just the personal name when present, else
// "mbox@host") and the bare address, for the first From address in
// envelope — or ("", "") if envelope has no From address.
func formatSender(envelope *imap.Envelope) (label, address string) {
	if len(envelope.From) == 0 {
		return "", ""
	}
	from := envelope.From[0]
	address = from.Address()
	if from.PersonalName != "" {
		return from.PersonalName, address
	}
	return address, address
}

// toItem builds a toposv1.Item from one merged matched entry.
// Fidelity is always ANCHORED: no verified mapping exists from an IMAP
// Message-ID/UID to Proton's internal webmail message id (03-RESEARCH.md
// Pitfall 5), so DeepLink cannot point AT the matched message. It also
// cannot point at the matched mailbox's own webmail view: Proton
// addresses custom labels and folders by an internal id, not by name, so
// a link built from a label's leaf name resolves to nothing and Proton
// redirects to the inbox. Instead DeepLink is a search over the
// account's All Mail view narrowed by the message's own subject, sender
// and date (see deeplink.go's webmailSearchDeepLink), which lands the
// reader in a short filtered list containing the message — still
// honestly ANCHORED, because the target is adjacent to the message
// rather than the message itself.
func (p *SourcePlugin) toItem(sourceID string, m *matched) *toposv1.Item {
	title := m.envelope.Subject
	if title == "" {
		title = noSubjectPlaceholder
	}

	// groupID is formatSender's bare-address return (its SECOND return
	// value) — the same structured, selective term the deep link's sender
	// criterion uses below. groupLabel is the sender-authored display
	// name and must never feed the criterion.
	groupLabel, groupID := formatSender(m.envelope)

	var primary int64
	if !m.internalDate.IsZero() {
		primary = m.internalDate.Unix()
	}

	var secondary int64
	if !m.envelope.Date.IsZero() {
		secondary = m.envelope.Date.Unix()
	}

	// The deep link's date criterion follows the same precedence toItem
	// already applies for its own primary/secondary timestamps —
	// internalDate first, falling back to the envelope Date header, zero
	// when both are — so the link's date agrees with the timestamp the
	// stream sorts by.
	deepLinkDate := m.internalDate
	if deepLinkDate.IsZero() {
		deepLinkDate = m.envelope.Date
	}

	deepLink := webmailSearchDeepLink(p.webmailBaseURL, deepLinkCriteria{
		// The envelope's own subject, NOT the local title variable above:
		// title has already been substituted with the no-subject
		// placeholder by this point, and feeding it in would make the
		// placeholder itself a search term (L-3).
		Subject: m.envelope.Subject,
		Sender:  groupID,
		Date:    deepLinkDate,
	})

	return &toposv1.Item{
		SourceId:               sourceID,
		SourceType:             sourceType,
		Title:                  title,
		Preview:                "", // Match must not open message bodies (a body read per message would multiply sync cost by mailbox size)
		TimestampUnix:          primary,
		SecondaryTimestampUnix: secondary,
		GroupId:                groupID,
		GroupLabel:             groupLabel,
		Fidelity:               toposv1.LinkFidelity_LINK_FIDELITY_ANCHORED,
		DeepLink:               deepLink,
		Labels:                 m.labels,
		HasThumbnail:           false,
		Provenance: map[string]string{
			"source_type":      sourceType,
			"source_system":    p.baseURL,
			"source_id":        sourceID,
			"plugin":           "topos-plugin-proton",
			"contract_version": contractVersion,
		},
	}
}

// mergeMailboxCache upserts discovered's entries into the plugin's
// accumulated resolution state under the write lock. The field itself is
// never re-bound to a different map, and no key already present is ever
// deleted — this is deliberately an accumulate, not a replace (03-06 Task
// 1). Semantics are last-writer-wins PER KEY: a message that has moved to
// a different mailbox since it was last discovered is refreshed by
// whichever Match call rediscovers it, while every key no Match call in
// this invocation touched is preserved untouched. This is deliberately
// not "insert only if absent", which would pin a moved message to a
// mailbox it has already left.
func (p *SourcePlugin) mergeMailboxCache(discovered map[string]string) {
	p.mailboxMu.Lock()
	defer p.mailboxMu.Unlock()
	for sourceID, mailbox := range discovered {
		p.mailboxCache[sourceID] = mailbox
	}
}

// mailboxForSourceID resolves sourceID to the mailbox name Fetch should
// SELECT, or ok=false if this plugin's in-process cache has no entry
// (only expected immediately after a kernel restart, before the first
// sync completes).
func (p *SourcePlugin) mailboxForSourceID(sourceID string) (string, bool) {
	p.mailboxMu.RLock()
	defer p.mailboxMu.RUnlock()
	mbox, ok := p.mailboxCache[sourceID]
	return mbox, ok
}

// Fetch implements live content fetch on item-open (KERN-03) — never
// called from Match/sync. FULL and PREVIEW share one path; THUMBNAIL is
// always unavailable (an email has no image rendition).
func (p *SourcePlugin) Fetch(ctx context.Context, req *toposv1.FetchRequest) (*toposv1.FetchResponse, error) {
	switch req.GetVariant() {
	case toposv1.ContentVariant_CONTENT_VARIANT_FULL, toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW:
		return p.fetchFull(ctx, req.GetSourceId())
	case toposv1.ContentVariant_CONTENT_VARIANT_THUMBNAIL:
		return &toposv1.FetchResponse{Available: false, UnavailableReason: noThumbnailReason}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "proton: unspecified content variant")
	}
}

// fetchFull resolves sourceID's mailbox from the in-process cache,
// EXAMINEs it, re-resolves the current UID via UID SEARCH HEADER
// Message-Id (never a cached UID — UIDs are only meaningful within one
// SELECTed mailbox and are reassigned if UIDVALIDITY changes), then
// fetches the body with BODY.PEEK — the mechanism that stops the server
// implicitly setting \Seen (SRC-01's never-mark-read guarantee).
func (p *SourcePlugin) fetchFull(ctx context.Context, sourceID string) (*toposv1.FetchResponse, error) {
	msgID, err := decodeSourceID(sourceID)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "proton: %v", err)
	}

	mailbox, ok := p.mailboxForSourceID(sourceID)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "proton: source_id %q is not known — the index has not been synced since this plugin started", sourceID)
	}

	conn, err := p.client.connect(syncDialTimeout)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "proton: connect: %v", err)
	}
	defer conn.Logout()

	if _, err := conn.Select(mailbox, true); err != nil { // EXAMINE
		return nil, status.Errorf(codes.Unavailable, "proton: examine %q: %v", mailbox, err)
	}

	criteria := &imap.SearchCriteria{
		Header: map[string][]string{"Message-Id": {"<" + msgID + ">"}},
	}
	uids, err := conn.UidSearch(criteria)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "proton: search message-id: %v", err)
	}
	if len(uids) == 0 {
		return nil, status.Errorf(codes.NotFound, "proton: message %q not found in %q", sourceID, mailbox)
	}
	uid := uids[0]

	section := &imap.BodySectionName{Peek: true}
	fetchItems := []imap.FetchItem{section.FetchItem()}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- conn.UidFetch(seqset, fetchItems, messages) }()

	var raw []byte
	for msg := range messages {
		if msg == nil {
			continue
		}
		if body := msg.GetBody(section); body != nil {
			b, err := io.ReadAll(body)
			if err != nil {
				return nil, status.Errorf(codes.Unavailable, "proton: read body: %v", err)
			}
			raw = b
		}
	}
	if err := <-done; err != nil {
		return nil, status.Errorf(codes.Unavailable, "proton: fetch body: %v", err)
	}
	if raw == nil {
		return nil, status.Errorf(codes.NotFound, "proton: message %q body not returned by server", sourceID)
	}

	text, err := PlainTextPart(raw)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "proton: parse message %q: %v", sourceID, err)
	}

	resp := &toposv1.FetchResponse{
		Available: true,
		Text:      text,
		Provenance: map[string]string{
			"source_type": sourceType,
			"source_id":   sourceID,
		},
	}

	// The plugin — not the shared detail pane — decides which
	// representation IS the message (03-09-PLAN.md gap G-03-2). When the
	// extracted plain-text part carries anything a reader can see, it IS
	// the content: return it alone, with no rendition at all, and skip
	// the second MIME walk for an HTML part entirely. Skipping that walk
	// also means a message with a malformed HTML part but a readable
	// text part now renders instead of failing the whole fetch with an
	// internal error. The preference is resolved HERE, in the producer,
	// for two reasons: the rendition is served under a CSP that blocks
	// every subresource (kernel/httpapi/item.go), so an image-bearing
	// HTML design fetched instead could only ever render as broken
	// images; and the detail pane is shared by every source, so one
	// source's preference must not become a rule that reshapes another's
	// pane — plugins/silverbullet/plugin.go's fetchFull legitimately
	// returns a rendition AND text together, which a UI-side "prefer
	// text" rule would have silently broken.
	if HasRenderableText(text) {
		return resp, nil
	}

	// No renderable plain text: fall back to the HTML part as a text/html
	// rendition — the one rendition type the kernel's fixed allowlist
	// already permits (kernel/httpapi/item.go). Sanitization, wrapping
	// and theming moved to the kernel's rendition boundary
	// (kernel/httpapi/rendition.go, D-11): Data below is the RAW,
	// unsanitized HTML part, with ContentShape declaring the email
	// profile so the kernel knows which policy to apply. When neither
	// part yields anything, resp is returned unchanged: Available true,
	// empty Text, no rendition — a normal outcome, not an error.
	htmlPart, err := HTMLPart(raw)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "proton: parse message %q: %v", sourceID, err)
	}
	if len(htmlPart) > 0 {
		resp.MimeType = "text/html"
		resp.ContentShape = toposv1.ContentShape_CONTENT_SHAPE_EMAIL_HTML
		resp.SizeBytes = int64(len(htmlPart))
		resp.Data = []byte(htmlPart)
	}

	return resp, nil
}

// Health opens a connection with the (shorter) health dial timeout,
// logs in, and logs out. Any failure returns Reachable:false with a
// specific, actionable last_error naming the failing step — never a
// gRPC error, matching every other plugin. The password is never
// included in LastError.
func (p *SourcePlugin) Health(ctx context.Context, _ *toposv1.HealthRequest) (*toposv1.HealthResponse, error) {
	conn, err := p.client.connect(healthDialTimeout)
	if err != nil {
		return &toposv1.HealthResponse{Reachable: false, LastError: err.Error()}, nil
	}
	defer conn.Logout()

	return &toposv1.HealthResponse{
		Reachable:    true,
		LastSyncUnix: time.Now().Unix(),
	}, nil
}
