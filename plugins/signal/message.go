package main

import (
	"encoding/json"
	"strings"
)

// attachment is one file-shaped attachment on a message. Filename may be
// empty (Signal Desktop does not always know one — 04-03-PLAN.md Task 1's
// own behavior list requires this case to render without one). Never
// decrypted, fetched or thumbnailed this phase (04-UI-SPEC.md Copywriting
// Contract) — this struct carries only enough to render a text
// placeholder chip.
type attachment struct {
	Filename    string
	ContentType string
}

// reaction is one emoji reaction to a message, with the reactor's
// identifier already resolved to a display name (senderDisplayName,
// below) — the transcript and digest snippet render a name, never a raw
// Signal service id.
type reaction struct {
	Emoji       string
	ReactorName string
}

// messageRecord is this plugin's own rich, normalized view of one Signal
// message — the single structure both digest.go's tail snippet and
// render.go's transcript read, so message richness (attachments,
// reactions, quotes, edit state, deleted state) is parsed exactly once
// per row (04-03-PLAN.md Task 1).
type messageRecord struct {
	ID             string
	ConversationID string
	SentAtUnixMs   int64 // messages.sent_at, epoch milliseconds
	SenderName     string
	Body           string // "" for a deleted-for-everyone message, regardless of what the body column held
	Deleted        bool   // deleted for everyone — see parseMessage's doc comment for the two signals this checks
	Edited         bool   // Body already carries the LATEST revision; this flag only marks that an edit happened
	Attachments    []attachment
	Reactions      []reaction
	QuoteExcerpt   string // "" when the message is not a reply
}

// attachmentGlyph and attachmentFallbackLabel are the exact copy
// 04-UI-SPEC.md's Copywriting Contract specifies for an attachment
// placeholder chip: the paperclip glyph, a space, and the filename — or
// the paperclip glyph and the word "Attachment" when no filename is
// available. Shared by digest.go's tail snippet and render.go's
// transcript so the two surfaces can never drift.
const (
	attachmentGlyph         = "📎"
	attachmentFallbackLabel = "Attachment"
)

// attachmentPlaceholder renders att's fixed placeholder copy — see
// attachmentGlyph/attachmentFallbackLabel's doc comment for the exact
// rule. Never attempts to decrypt, fetch or thumbnail the real file.
func attachmentPlaceholder(att attachment) string {
	if att.Filename != "" {
		return attachmentGlyph + " " + att.Filename
	}
	return attachmentGlyph + " " + attachmentFallbackLabel
}

// reactionLines groups reactions by emoji and renders one line per
// distinct emoji: the emoji, a space, and that emoji's reactor names
// comma-joined (04-UI-SPEC.md Copywriting Contract, e.g. "👍 Alice,
// Bob") — order follows each emoji's first appearance in reactions, so
// output is deterministic for a given input order.
func reactionLines(reactions []reaction) []string {
	if len(reactions) == 0 {
		return nil
	}
	var order []string
	grouped := map[string][]string{}
	for _, r := range reactions {
		if _, seen := grouped[r.Emoji]; !seen {
			order = append(order, r.Emoji)
		}
		grouped[r.Emoji] = append(grouped[r.Emoji], r.ReactorName)
	}
	lines := make([]string, 0, len(order))
	for _, emoji := range order {
		lines = append(lines, emoji+" "+strings.Join(grouped[emoji], ", "))
	}
	return lines
}

// unknownSenderIdentifier is what senderDisplayName falls back to when a
// reaction (or any other identifier lookup) has no usable identifier at
// all — distinct from unknownSenderName (plugin.go), which is the
// fallback for a message's own sender when senderNames has no entry for
// a REAL identifier; an empty identifier is a different, rarer case
// (a malformed reaction row) worth naming separately in a future log
// line if it's ever observed to fire.
const unknownSenderIdentifier = "Unknown"

// senderDisplayName resolves identifier (typically a Signal service id)
// to its best available display name via senderNames — the same map
// plugin.go's buildSenderNames already builds for a message's own
// sender — falling back to the identifier itself, NEVER an empty string,
// when no name is known (04-03-PLAN.md Task 1: "Fall back to the
// identifier itself when no name is known, never to an empty string").
// Used by readReactions (plugin.go) to resolve each reaction's reactor
// the same way a message's own sender is resolved, so a self-reaction
// correctly resolves to "You" via the identical senderNames entry
// buildSenderNames already sets for the account's own service id.
func senderDisplayName(senderNames map[string]string, identifier string) string {
	if name, ok := senderNames[identifier]; ok && name != "" {
		return name
	}
	if identifier == "" {
		return unknownSenderIdentifier
	}
	return identifier
}

// maxMessageBlobBytes bounds how much of a message row's json blob
// parseMessage ever parses, mirroring plugins/proton/body.go's
// maxPartBytes discipline (T-04-16): a crafted or corrupt row must never
// be able to exhaust memory. A blob truncated by this bound simply fails
// json.Unmarshal, which parseMessage already treats as "no richness
// beyond the SQL columns", never an error.
const maxMessageBlobBytes = 1 * 1024 * 1024

// messageBlobFields is the subset of a messages.json blob's own fields
// this plugin reads beyond what SQL columns already carry (body,
// sourceServiceId, isErased are real columns; deletedForEveryone,
// editHistory and quote have no SQL column of their own — confirmed by
// direct, hands-on schema introspection of a real, live db.sqlite during
// this task, mirroring 04-01-SUMMARY.md's own conversations-blob
// precedent: read what the schema actually has, not what research
// illustrated).
//
// Attachments and reactions are DELIBERATELY NOT read from this blob,
// unlike 04-RESEARCH.md's illustrative "everything richer... lives only
// in that blob" framing: this machine's real schema keeps neither there
// at all (a message with hasAttachments=1 carries no "attachments" key
// in its own json blob in any sample this task inspected). Both live in
// their own dedicated, indexed SQL tables — message_attachments and
// reactions, each joined by messages.id — and are supplied to
// parseMessage as already-resolved parameters (see plugin.go's
// readAttachments/readReactions), never parsed from rawJSON here. This
// is a Rule 1 ground-truth correction, recorded in 04-03-SUMMARY.md.
type messageBlobFields struct {
	DeletedForEveryone bool            `json:"deletedForEveryone"`
	EditHistory        json.RawMessage `json:"editHistory"`
	Quote              *struct {
		Text string `json:"text"`
	} `json:"quote"`
}

// parseMessage decodes one message row into a messageRecord.
// attachments and reactions are supplied by the caller — already read
// from message_attachments/reactions, never parsed from rawJSON (see
// messageBlobFields's doc comment). isErasedColumn is the messages.
// isErased SQL column value; rawJSON's own "deletedForEveryone" field is
// a SECOND, independent signal some Signal Desktop versions rely on
// instead of (or alongside) the column — 04-03-PLAN.md Task 1 requires
// checking BOTH and treating either as deleted, since which one a given
// row actually carries is a Signal-Desktop-version detail this plugin
// must not assume.
//
// Parsing is defensive: an absent or unparseable rawJSON yields a record
// with the SQL-column-derived fields intact (id, conversation, sent-at,
// sender, body, deleted-by-column, the supplied attachments/reactions)
// and no blob-derived richness (no edited flag, no quote excerpt) —
// NEVER an error, so one malformed row can never fail an entire day's
// digest (T-04-16). The returned error is always nil today; the return
// shape exists so a future caller can distinguish "parsed" from
// "malformed" without a signature change, and so this function's own
// test can assert the malformed-blob case explicitly returns (record,
// nil) rather than (zero-value, error).
func parseMessage(id, conversationID string, sentAtUnixMs int64, senderName, body string, isErasedColumn bool, rawJSON string, attachments []attachment, reactions []reaction) (messageRecord, error) {
	rec := messageRecord{
		ID:             id,
		ConversationID: conversationID,
		SentAtUnixMs:   sentAtUnixMs,
		SenderName:     senderName,
		Body:           body,
		Deleted:        isErasedColumn,
		Attachments:    attachments,
		Reactions:      reactions,
	}

	if rawJSON == "" {
		if rec.Deleted {
			rec.Body = ""
		}
		return rec, nil
	}

	bounded := rawJSON
	if len(bounded) > maxMessageBlobBytes {
		bounded = bounded[:maxMessageBlobBytes]
	}

	var fields messageBlobFields
	if err := json.Unmarshal([]byte(bounded), &fields); err != nil {
		// Malformed or truncated blob: keep the SQL-column-derived
		// fields, add no blob-derived richness — never fail the row.
		if rec.Deleted {
			rec.Body = ""
		}
		return rec, nil
	}

	if fields.DeletedForEveryone {
		rec.Deleted = true
	}
	if rec.Deleted {
		// Whichever signal fired, the body is never presented — some
		// Signal Desktop versions erase the blob and leave the body
		// column, some the reverse (04-03-PLAN.md Task 1).
		rec.Body = ""
	}

	var editHistory []json.RawMessage
	if len(fields.EditHistory) > 0 {
		_ = json.Unmarshal(fields.EditHistory, &editHistory)
	}
	rec.Edited = len(editHistory) > 0

	if fields.Quote != nil {
		rec.QuoteExcerpt = strings.TrimSpace(fields.Quote.Text)
	}

	return rec, nil
}
