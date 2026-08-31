package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

const (
	sourceType      = "signal"
	displayName     = "Signal"
	contractVersion = "topos.v2"

	// pluginName identifies this plugin in Item.Provenance's "plugin" key
	// and in this process's own log lines.
	pluginName = "topos-plugin-signal"

	// iconMIME is the declared mime for iconSVG below, returned verbatim
	// from Describe (09-02-PLAN.md Task 2, 09-UI-SPEC.md Fix 10).
	iconMIME = "image/svg+xml"
)

// matchVocabulary is the field-name vocabulary this plugin declares and
// reads from MatchRequest.match_fields — Signal's own native categorization
// is its conversations (group names, or a 1:1's nickname/system-contact
// name per D-06).
var matchVocabulary = []string{"conversations"}

// iconSVG is this plugin's identity icon — a Lucide MessageCircle glyph
// with its stroke color baked to the literal --muted-foreground hex (see
// assets/icon.svg's own provenance comment). Deliberately a different
// bubble shape from WhatsApp's MessageSquare glyph so the two chat
// sources stay tellable apart by icon alone. Returned verbatim from
// Describe's Icon field; the kernel caches it at that call site and
// serves it at GET /api/plugins/topos-plugin-signal/icon.
//
// Source-Project: @lucide/svelte (lucide-icons/lucide)
// Source-File:    dist/icons/message-circle.svelte
// Source-Version: @lucide/svelte v1.27.0
// Source-License: ISC
//
//go:embed assets/icon.svg
var iconSVG []byte

// noThumbnailReason is the fixed unavailable_reason for the THUMBNAIL
// content variant — a Signal digest has no image rendition, ever.
const noThumbnailReason = "Signal digests have no thumbnail rendition"

// transcriptMimeType is what Fetch's FULL/PREVIEW branch returns for a
// Signal digest — a self-contained, sanitized HTML document (render.go)
// routed by the kernel's existing rendition allowlist
// (kernel/httpapi/item.go) into DetailPane's existing `html` body-variant
// iframe branch, with zero proto change and zero new frontend branch
// (04-RESEARCH.md Architecture).
const transcriptMimeType = "text/html"

// SourcePlugin implements sdk.SourcePlugin by reading Signal Desktop's
// local SQLCipher database strictly read-only. plugins/proton/plugin.go's
// SourcePlugin is this file's closest analog (04-PATTERNS.md), but unlike
// Proton this plugin caches nothing across calls: Match re-derives
// everything from the database fresh every time (no long-lived
// mailbox-style cache), because the database itself — not a remote
// server round trip — is the only "connection" this plugin ever holds,
// and holding it open across calls would work against the byte-identical
// / live-writer safety goals this phase's success criteria centre on.
type SourcePlugin struct {
	// configDir is Signal Desktop's own config directory ("~" already
	// expanded by main.go) — the source of both config.json (key
	// resolution) and sql/db.sqlite (the message database itself).
	configDir string

	// logOut is the plugin's log sink — os.Stderr in production, parsed
	// and re-emitted through the kernel's hclog so plugin and kernel logs
	// interleave sanely (plugins/proton/plugin.go's identical field).
	// Overridable in tests.
	logOut io.Writer
}

// NewSourcePlugin builds a SourcePlugin. configDir must be non-empty and
// already have any leading "~" expanded — main.go fails startup loudly
// otherwise.
func NewSourcePlugin(configDir string) *SourcePlugin {
	return &SourcePlugin{configDir: configDir, logOut: os.Stderr}
}

func (p *SourcePlugin) configPath() string { return filepath.Join(p.configDir, "config.json") }
func (p *SourcePlugin) dbPath() string     { return filepath.Join(p.configDir, "sql", "db.sqlite") }

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

// openGuarded resolves the key, opens db.sqlite read-only, and guards the
// schema version ceiling — the three preconditions every RPC that touches
// the database needs, in the order 04-RESEARCH.md Pattern 2 requires (the
// schema guard runs BEFORE any messages/conversations query). Callers
// must Close() the returned *sql.DB.
//
// Each of the three ways this can fail returns a distinct, named,
// actionable error — 04-02-PLAN.md Task 3, ROADMAP criterion 5's "fails
// loudly, never confusingly" half: Health (below) renders whichever one
// fires verbatim in LastError, with no per-cause branching anywhere else
// in this codebase (04-UI-SPEC.md's Copywriting Contract note: the
// specific wording is this function's responsibility, not a UI design
// choice).
//  1. The database file itself is missing — Signal Desktop may not be
//     installed for this user, or has never been run. Checked first and
//     independently of config.json, so this cause is never masked by a
//     config-read failure that would otherwise fire first.
//  2. Key resolution fails — either config.json itself is unreadable/
//     unparsable, or resolveKey/resolveSafeStorageKey reject its
//     contents, or (Pitfall 4) a safeStorage-resolved key decrypts to a
//     plausible-looking value that still fails to actually open the
//     database. All three collapse into the same "key resolution failed"
//     message shape, naming the backend if one was declared.
//  3. The schema version exceeds highestSupportedSchemaVersion —
//     guardSchemaVersion's own message, unchanged here.
func (p *SourcePlugin) openGuarded() (*sql.DB, error) {
	dbPath := p.dbPath()
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf(
			"signal: Signal Desktop's database was not found at %s — Signal Desktop may not be installed for this user, or has not been run yet (%v)",
			dbPath, err,
		)
	}

	cfg, err := readSignalConfig(p.configPath())
	if err != nil {
		return nil, fmt.Errorf("signal: key resolution failed reading %s: %w", p.configPath(), err)
	}
	rawHexKey, err := resolveKey(cfg)
	if err != nil {
		return nil, fmt.Errorf("signal: key resolution failed (declared safeStorageBackend=%q): %w", cfg.SafeStorageBackend, err)
	}

	db, err := openReadOnly(dbPath, rawHexKey)
	if err != nil {
		if cfg.EncryptedKey != "" {
			// The safeStorage-resolved key looked plausible (passed
			// resolveSafeStorageKey's length check) but still failed to
			// actually open the database — Pitfall 4's backend-mismatch
			// case. Named distinctly so this never reads as a generic
			// "file is not a database"/corruption error.
			return nil, fmt.Errorf("%w (declared backend=%s): %v", errSafeStorageBackendMismatch, cfg.SafeStorageBackend, err)
		}
		return nil, fmt.Errorf("signal: key resolution failed opening the database with the resolved key: %w", err)
	}
	if err := guardSchemaVersion(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Match resolves keywords against Signal's own conversations (D-05/D-06,
// match.go), then groups the matched conversations' FULL message history
// — no time window (D-08) — into conversation-day digests (D-01/D-02/
// D-04, digest.go). A zero-length keyword list, and zero matched
// conversations, both return a successful EMPTY response — never an
// error (plugins/proton's identical precedent: a webspace with no
// matching content is empty, not failed).
func (p *SourcePlugin) Match(_ context.Context, req *toposv1.MatchRequest) (*toposv1.MatchResponse, error) {
	keywords := req.GetMatchFields()["conversations"].GetValues()
	if len(keywords) == 0 {
		return &toposv1.MatchResponse{}, nil
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
		return &toposv1.MatchResponse{}, nil
	}

	matchedByID := make(map[string]conversation, len(matched))
	convIDs := make([]string, 0, len(matched))
	names := make(map[string]string, len(matched))
	for _, c := range matched {
		matchedByID[c.ID] = c
		convIDs = append(convIDs, c.ID)
		names[c.ID] = conversationDisplayName(c)
	}

	// senderNames resolves any sender (not just the matched conversations'
	// own contacts — a group can carry messages from any member Signal
	// Desktop knows) by service id, built from the FULL conversation set
	// this Match already read, never a second query.
	senderNames := buildSenderNames(convs, ownAci)

	msgs, err := readMessages(db, convIDs, senderNames)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "signal: %v", err)
	}

	digests := buildDigests(msgs, names)

	items := make([]*toposv1.Item, 0, len(digests))
	for _, d := range digests {
		items = append(items, p.toItem(d, matchedByID[d.ConversationID]))
	}

	// Count-only: never a conversation name, sender name or message
	// body. This log is forwarded verbatim into the kernel's log stream.
	fmt.Fprintf(p.logOut, "%s: match: %d matched conversation(s), %d digest(s)\n", pluginName, len(matched), len(items))

	return &toposv1.MatchResponse{Items: items}, nil
}

// toItem builds a toposv1.Item from one digest and the (already
// matched) conversation it belongs to.
func (p *SourcePlugin) toItem(d digest, conv conversation) *toposv1.Item {
	sourceID := sourceIDForDigest(d.ConversationID, d.Day)
	return &toposv1.Item{
		SourceId:      sourceID,
		SourceType:    sourceType,
		Title:         digestTitle(d.ConversationName, d.MessageCount),
		Preview:       d.Preview,
		TimestampUnix: d.LastMessageUnix,
		GroupId:       d.ConversationID,
		GroupLabel:    "", // 04-UI-SPEC.md: left empty — the title already carries the identifying context
		Fidelity:      toposv1.LinkFidelity_LINK_FIDELITY_CONVERSATION_ONLY,
		DeepLink:      conversationDeepLink(conv.Type, conv.E164),
		Labels:        []string{d.ConversationName},
		HasThumbnail:  false,
		Provenance: map[string]string{
			"source_type":      sourceType,
			"source_system":    p.configDir,
			"source_id":        sourceID,
			"plugin":           pluginName,
			"contract_version": contractVersion,
		},
	}
}

// Fetch implements live content fetch on item-open (KERN-03) — never
// called from Match/sync. THUMBNAIL is always unavailable (a Signal
// digest has no image rendition, ever). FULL and PREVIEW share one path
// (fetchTranscript): a Signal digest has nothing FULL offers beyond what
// PREVIEW already renders — there is no separate "extracted text plus a
// richer inline preview" distinction for a chat transcript the way there
// is for an email's plain-text-vs-HTML choice, so both variants return
// the identical wrapped transcript document.
func (p *SourcePlugin) Fetch(_ context.Context, req *toposv1.FetchRequest) (*toposv1.FetchResponse, error) {
	switch req.GetVariant() {
	case toposv1.ContentVariant_CONTENT_VARIANT_THUMBNAIL:
		return &toposv1.FetchResponse{Available: false, UnavailableReason: noThumbnailReason}, nil
	case toposv1.ContentVariant_CONTENT_VARIANT_FULL, toposv1.ContentVariant_CONTENT_VARIANT_PREVIEW:
		return p.fetchTranscript(req.GetSourceId())
	default:
		return nil, status.Error(codes.InvalidArgument, "signal: unspecified content variant")
	}
}

// fetchTranscript re-derives sourceID's (conversationID, day) pair,
// re-opens the database read-only (never a handle cached from Match, and
// never a cached copy of the content itself — content is fetched live on
// open, per the hybrid data model), re-reads that conversation's
// messages for that day, renders them into a sanitized, self-contained
// HTML transcript document (render.go), and returns it as MimeType
// transcriptMimeType.
func (p *SourcePlugin) fetchTranscript(sourceID string) (*toposv1.FetchResponse, error) {
	conversationID, day, err := decodeSourceID(sourceID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "signal: source_id %q is not a recognised digest id: %v", sourceID, err)
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

	found := false
	for _, c := range convs {
		if c.ID == conversationID {
			found = true
			break
		}
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "signal: conversation %q (from source_id %q) was not found", conversationID, sourceID)
	}

	senderNames := buildSenderNames(convs, ownAci)
	msgs, err := readMessages(db, []string{conversationID}, senderNames)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "signal: %v", err)
	}

	var dayMsgs []messageRecord
	for _, m := range msgs {
		if localDayKey(m.SentAtUnixMs) == day {
			dayMsgs = append(dayMsgs, m)
		}
	}
	if len(dayMsgs) == 0 {
		return nil, status.Errorf(codes.NotFound, "signal: no messages found for conversation %q on day %q (source_id %q)", conversationID, day, sourceID)
	}
	sort.Slice(dayMsgs, func(i, j int) bool { return dayMsgs[i].SentAtUnixMs < dayMsgs[j].SentAtUnixMs })

	// renderTranscript returns an UNSANITIZED, UNWRAPPED fragment — D-11
	// moved sanitization, wrapping and theming to the kernel's rendition
	// boundary (kernel/httpapi/rendition.go). ContentShape declares the
	// chat profile so the kernel applies the right policy.
	fragment := renderTranscript(dayMsgs)

	return &toposv1.FetchResponse{
		Available:    true,
		MimeType:     transcriptMimeType,
		ContentShape: toposv1.ContentShape_CONTENT_SHAPE_CHAT_TRANSCRIPT,
		SizeBytes:    int64(len(fragment)),
		Data:         fragment,
		Provenance: map[string]string{
			"source_type": sourceType,
			"source_id":   sourceID,
		},
	}, nil
}

// Health attempts key resolution, a read-only open, and the schema guard
// — this plugin's equivalent of "reachability" is "can we open the
// database at all", not a network dial. Any failure returns
// Reachable:false with a specific, actionable LastError naming the
// failing step — never a gRPC error, matching every other plugin. Never
// includes the key or any config.json field value.
func (p *SourcePlugin) Health(_ context.Context, _ *toposv1.HealthRequest) (*toposv1.HealthResponse, error) {
	db, err := p.openGuarded()
	if err != nil {
		return &toposv1.HealthResponse{Reachable: false, LastError: err.Error()}, nil
	}
	defer db.Close()

	return &toposv1.HealthResponse{
		Reachable:    true,
		LastSyncUnix: time.Now().Unix(),
	}, nil
}

// --- Database row reading (this plugin's own SQL layer; no separate
// client file exists yet — plugins/proton/plugin.go's listMailboxes is
// the closest analog for "row-reading helpers living alongside Match in
// plugin.go rather than a dedicated client file"). ---

// conversationFields is the subset of a conversations.json blob this
// plugin needs beyond what conversations' own SQL columns already carry
// (id, type, name, profileName, profileFamilyName, e164, serviceId are
// all real columns — see readConversations' SELECT — so only the four
// system/nickname fields below, which have no SQL column of their own,
// need a JSON unmarshal).
type conversationFields struct {
	SystemGivenName    string `json:"systemGivenName"`
	SystemFamilyName   string `json:"systemFamilyName"`
	NicknameGivenName  string `json:"nicknameGivenName"`
	NicknameFamilyName string `json:"nicknameFamilyName"`
}

// readConversations reads every group and 1:1 conversation row, resolving
// each row's system/nickname name fields from its JSON blob (see
// conversationFields) and marking IsNoteToSelf by comparing serviceId
// against ownAci (empty ownAci — an unlinked install — marks nothing).
func readConversations(db *sql.DB, ownAci string) ([]conversation, error) {
	rows, err := db.Query(`
		SELECT id, type, name, profileName, profileFamilyName, e164, serviceId, json
		FROM conversations
		WHERE type IN ('private', 'group')
	`)
	if err != nil {
		return nil, fmt.Errorf("query conversations: %w", err)
	}
	defer rows.Close()

	var out []conversation
	for rows.Next() {
		var id, typ string
		var name, profileName, profileFamilyName, e164, serviceID sql.NullString
		var rawJSON string
		if err := rows.Scan(&id, &typ, &name, &profileName, &profileFamilyName, &e164, &serviceID, &rawJSON); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}

		var fields conversationFields
		if err := json.Unmarshal([]byte(rawJSON), &fields); err != nil {
			return nil, fmt.Errorf("parse conversation record: %w", err)
		}

		out = append(out, conversation{
			ID:                 id,
			Type:               typ,
			Name:               name.String,
			SystemGivenName:    fields.SystemGivenName,
			SystemFamilyName:   fields.SystemFamilyName,
			NicknameGivenName:  fields.NicknameGivenName,
			NicknameFamilyName: fields.NicknameFamilyName,
			ProfileName:        profileName.String,
			ProfileFamilyName:  profileFamilyName.String,
			E164:               e164.String,
			ServiceID:          serviceID.String,
			IsNoteToSelf:       ownAci != "" && serviceID.String == ownAci,
		})
	}
	return out, rows.Err()
}

// accountIdentityItem is the shape of the items table's "value" column
// (itself a JSON blob) for the "uuid_id" row: Signal Desktop's own
// persisted "<aci>.<deviceId>" account identity string.
type accountIdentityItem struct {
	Value string `json:"value"`
}

// readOwnAci reads the account's own ACI (Signal's per-account stable
// identifier) from the items table's "uuid_id" row, stripping the
// trailing ".<deviceId>" suffix Signal Desktop stores it with — so it
// compares equal to a conversation's own bare serviceId column. Returns
// ("", nil) for a fresh, never-linked install (no items row yet): Note to
// Self detection then simply excludes nothing, which is safe because an
// unlinked install also has zero real conversations.
func readOwnAci(db *sql.DB) (string, error) {
	var rawJSON string
	err := db.QueryRow(`SELECT json FROM items WHERE id = 'uuid_id'`).Scan(&rawJSON)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read account identity: %w", err)
	}

	var item accountIdentityItem
	if err := json.Unmarshal([]byte(rawJSON), &item); err != nil {
		return "", fmt.Errorf("parse account identity: %w", err)
	}
	aci, _, _ := strings.Cut(item.Value, ".")
	return aci, nil
}

// buildSenderNames maps every conversation's own service id to the best
// available DISPLAY name for messages it sends — display purposes only,
// never matching (D-06 restricts matching alone; see
// conversationDisplayName's own doc comment). The account's own service
// id maps to the fixed "You" label rather than its own conversation
// display name, matching this transcript's own outgoing-message
// convention.
func buildSenderNames(convs []conversation, ownAci string) map[string]string {
	out := make(map[string]string, len(convs)+1)
	for _, c := range convs {
		if c.Type != "private" || c.ServiceID == "" {
			continue
		}
		out[c.ServiceID] = conversationDisplayName(c)
	}
	if ownAci != "" {
		out[ownAci] = ownSenderLabel
	}
	return out
}

// unknownSenderName is the fallback readMessages uses when a message's
// sourceServiceId has no entry in senderNames (a sender Signal Desktop
// has no conversation record for at all — rare, but not impossible for
// an old/orphaned message).
const unknownSenderName = "Unknown"

// realAttachmentTypes is the set of message_attachments.attachmentType
// values this plugin treats as a genuine file the message's OWN sender
// attached — confirmed by direct, hands-on introspection of a real, live
// db.sqlite during this task. Excluded deliberately: "preview" (a link-
// preview thumbnail describing a URL in the body, not a file the sender
// attached), "quote" (a thumbnail copied from the message THIS one is
// replying to, describing that OTHER message, not this one) and
// "long-message" (Signal's own body-overflow mechanism for messages
// exceeding its inline length limit — the message's own text stored as
// an attachment, not a user-facing file). "contact" (a shared vCard) is
// included: it is a genuine attachment the sender chose to send.
var realAttachmentTypes = map[string]bool{
	"attachment": true,
	"sticker":    true,
	"contact":    true,
}

// readAttachments reads every real attachment (message_attachments,
// current revision only — editHistoryIndex = -1 excludes a PRIOR edit
// revision's now-superseded attachments — and attachmentType in
// realAttachmentTypes) for every conversation in conversationIDs,
// grouped by messageId, ordered within a message by orderInMessage.
// message.go's messageBlobFields doc comment records why this reads a
// dedicated SQL table rather than the message row's own json blob.
func readAttachments(db *sql.DB, conversationIDs []string) (map[string][]attachment, error) {
	if len(conversationIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(conversationIDs))
	args := make([]any, 0, len(conversationIDs))
	for i, id := range conversationIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(`
		SELECT messageId, fileName, contentType, attachmentType
		FROM message_attachments
		WHERE conversationId IN (%s)
		  AND editHistoryIndex = -1
		ORDER BY orderInMessage ASC
	`, strings.Join(placeholders, ","))

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query message_attachments: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]attachment)
	for rows.Next() {
		var messageID string
		var fileName, contentType, attachmentType sql.NullString
		if err := rows.Scan(&messageID, &fileName, &contentType, &attachmentType); err != nil {
			return nil, fmt.Errorf("scan message_attachments row: %w", err)
		}
		if !realAttachmentTypes[attachmentType.String] {
			continue
		}
		out[messageID] = append(out[messageID], attachment{
			Filename:    fileName.String,
			ContentType: contentType.String,
		})
	}
	return out, rows.Err()
}

// readReactions reads every reaction (the dedicated reactions table —
// message.go's messageBlobFields doc comment records why this, not the
// message row's json blob, is this plugin's source of truth for
// reactions) for every conversation in conversationIDs, grouped by
// messageId, with each reactor's identifier resolved to a display name
// via senderNames the identical way a message's own sender is resolved
// (senderDisplayName, message.go) — a self-reaction therefore correctly
// resolves to "You" via the same senderNames entry buildSenderNames sets
// for the account's own service id.
func readReactions(db *sql.DB, conversationIDs []string, senderNames map[string]string) (map[string][]reaction, error) {
	if len(conversationIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(conversationIDs))
	args := make([]any, 0, len(conversationIDs))
	for i, id := range conversationIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(`
		SELECT messageId, emoji, fromId
		FROM reactions
		WHERE conversationId IN (%s)
		ORDER BY timestamp ASC
	`, strings.Join(placeholders, ","))

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query reactions: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]reaction)
	for rows.Next() {
		var messageID, emoji string
		var fromID sql.NullString
		if err := rows.Scan(&messageID, &emoji, &fromID); err != nil {
			return nil, fmt.Errorf("scan reactions row: %w", err)
		}
		out[messageID] = append(out[messageID], reaction{
			Emoji:       emoji,
			ReactorName: senderDisplayName(senderNames, fromID.String),
		})
	}
	return out, rows.Err()
}

// readMessages reads every real chat message (type IN
// ('incoming','outgoing') — excluding system/notification event rows
// such as 'profile-change', 'group-v2-change', 'call-history', which are
// not messages a day's "N messages" count should include) for every
// conversation in conversationIDs, with NO time window (D-08: full
// history backfill), parsing each row into a messageRecord (message.go)
// via parseMessage — resolving sender display name via senderNames
// (buildSenderNames' output), and attaching that message's own
// attachments/reactions (readAttachments/readReactions above).
//
// sourceServiceId is empty for an OUTGOING message in a 1:1 (private)
// conversation — Signal Desktop leaves the sender implicit there (the
// conversationId itself already identifies the pairing), unlike a GROUP
// conversation's own outgoing rows, which DO carry the account's own
// service id (confirmed against this task's real, live db.sqlite for
// both conversation shapes). readMessages therefore falls back to the
// fixed "You" label for any outgoing row with no sourceServiceId, rather
// than misreporting it as unknownSenderName.
func readMessages(db *sql.DB, conversationIDs []string, senderNames map[string]string) ([]messageRecord, error) {
	if len(conversationIDs) == 0 {
		return nil, nil
	}

	attachmentsByMsg, err := readAttachments(db, conversationIDs)
	if err != nil {
		return nil, err
	}
	reactionsByMsg, err := readReactions(db, conversationIDs, senderNames)
	if err != nil {
		return nil, err
	}

	placeholders := make([]string, len(conversationIDs))
	args := make([]any, 0, len(conversationIDs))
	for i, id := range conversationIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(`
		SELECT id, conversationId, sent_at, type, sourceServiceId, body, isErased, json
		FROM messages
		WHERE conversationId IN (%s)
		  AND type IN ('incoming', 'outgoing')
		  AND sent_at IS NOT NULL
		ORDER BY sent_at ASC
	`, strings.Join(placeholders, ","))

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var out []messageRecord
	for rows.Next() {
		var id, conversationID, msgType string
		var sentAt int64
		var isErased int
		var sourceServiceID, body, rawJSON sql.NullString
		if err := rows.Scan(&id, &conversationID, &sentAt, &msgType, &sourceServiceID, &body, &isErased, &rawJSON); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}

		senderName := senderNames[sourceServiceID.String]
		switch {
		case senderName != "":
			// resolved via senderNames.
		case sourceServiceID.String == "" && msgType == "outgoing":
			senderName = ownSenderLabel
		default:
			senderName = unknownSenderName
		}

		rec, err := parseMessage(id, conversationID, sentAt, senderName, body.String, isErased != 0, rawJSON.String, attachmentsByMsg[id], reactionsByMsg[id])
		if err != nil {
			// parseMessage never actually returns a non-nil error today
			// (see its own doc comment) — handled anyway so a future
			// change to that contract fails loudly here rather than
			// silently dropping a row.
			return nil, fmt.Errorf("parse message %q: %w", id, err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
