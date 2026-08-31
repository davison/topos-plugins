package main

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// digest is one (conversation, local calendar day) unit (D-01) — the
// item this plugin's Match ultimately returns one toposv1.Item per.
type digest struct {
	ConversationID   string
	ConversationName string
	Day              string // "2006-01-02", local calendar day (D-04)
	MessageCount     int
	LastMessageUnix  int64 // the day's LAST message time (D-04) — the item's timestamp
	Preview          string
}

// previewRuneCap bounds the tail snippet's length in runes (never
// bytes — Snippet truncates by rune count so a multi-byte snippet is
// never cut mid-codepoint), mirroring plugins/proton/body.go's identical
// cap.
const previewRuneCap = 500

// tailMessageCount is the number of a day's LAST messages the preview
// carries (D-02: "the last 2-3 messages of the day").
const tailMessageCount = 3

// sourceIDForDigest builds a stable, deterministic source_id from
// conversationID and day: base64.RawURLEncoding-encoded
// "conversationID:day", mirroring plugins/proton's
// encodeSourceID/decodeSourceID pair so the id is URL-path-safe and
// reversible. Determinism here is load-bearing: kernel/index.
// ReplaceWebspaceSourceItems upserts by source_id, so calling this twice
// with the same (conversationID, day) — as happens on every re-sync of
// today's digest — must return the identical string for D-04's "today's
// digest updates in place" guarantee to hold, with no new kernel
// primitive required.
func sourceIDForDigest(conversationID, day string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(conversationID + ":" + day))
}

// decodeSourceID reverses sourceIDForDigest, recovering the
// (conversationID, day) pair a source_id was built from. Used both by
// Match (never, today) and by Fetch (plan 04-03) to re-derive which
// conversation and day a digest's transcript belongs to.
func decodeSourceID(sourceID string) (conversationID, day string, err error) {
	b, err := base64.RawURLEncoding.DecodeString(sourceID)
	if err != nil {
		return "", "", fmt.Errorf("signal: decode source_id %q: %w", sourceID, err)
	}
	parts := strings.SplitN(string(b), ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("signal: source_id %q does not decode to a conversationID:day pair", sourceID)
	}
	return parts[0], parts[1], nil
}

// localDay converts sentAtUnixMs (epoch milliseconds) to its local
// calendar day (D-04). time.Local is correct here and needs no config
// knob: the kernel is a desktop-local process by project constraint
// (PROJECT.md Constraints: "Runs on the user's desktop machine"), so its
// local timezone IS the user's.
func localDay(sentAtUnixMs int64) time.Time {
	return time.UnixMilli(sentAtUnixMs).In(time.Local)
}

// localDayKey formats sentAtUnixMs's local calendar day as "2006-01-02".
func localDayKey(sentAtUnixMs int64) string {
	return localDay(sentAtUnixMs).Format("2006-01-02")
}

// buildDigests groups msgs by (ConversationID, local calendar day) and
// returns one digest per group with at least one message, in no
// particular order. conversationNames maps a conversation id to its
// display name for the digest's title. The day's message count includes
// EVERY message with activity that day, including a deleted-for-everyone
// one — so the digest title (which reads this count) never disagrees
// with what the thread view (which renders every one of these records,
// including the deleted ones as a tombstone bubble) actually shows.
func buildDigests(msgs []messageRecord, conversationNames map[string]string) []digest {
	type key struct {
		conversationID string
		day            string
	}
	grouped := map[key][]messageRecord{}
	var order []key
	for _, m := range msgs {
		k := key{conversationID: m.ConversationID, day: localDayKey(m.SentAtUnixMs)}
		if _, seen := grouped[k]; !seen {
			order = append(order, k)
		}
		grouped[k] = append(grouped[k], m)
	}

	digests := make([]digest, 0, len(order))
	for _, k := range order {
		group := grouped[k]
		sort.Slice(group, func(i, j int) bool { return group[i].SentAtUnixMs < group[j].SentAtUnixMs })

		last := group[len(group)-1]
		digests = append(digests, digest{
			ConversationID:   k.conversationID,
			ConversationName: conversationNames[k.conversationID],
			Day:              k.day,
			MessageCount:     len(group),
			LastMessageUnix:  last.SentAtUnixMs / 1000,
			Preview:          tailSnippet(group),
		})
	}
	return digests
}

// digestTitle composes "{conversation name} — {N} message(s)" with
// correct singular/plural grammar (D-02) — composed here so the frontend
// needs no client-side pluralization logic (04-UI-SPEC.md Copywriting
// Contract).
func digestTitle(conversationName string, count int) string {
	if count == 1 {
		return fmt.Sprintf("%s — %d message", conversationName, count)
	}
	return fmt.Sprintf("%s — %d messages", conversationName, count)
}

// tailSnippet renders a richness-aware preview line for each of the LAST
// tailMessageCount messages of chronologically-sorted-ascending
// sortedMsgs (D-02), prefixed with the sender's name, newline-joined,
// then truncated by Snippet. This is the ONLY message text this plugin
// ever returns to the index — D-03 permits nothing beyond it, and
// reactions/quotes are deliberately never surfaced here (the compact row
// has no space for that chrome — that richness is transcript-only, see
// render.go).
//
// Richness rules (04-UI-SPEC.md's E1 `partial` row, 04-03-PLAN.md Task
// 1's own action text):
//   - A tail message with no body but at least one attachment renders as
//     the attachment placeholder: the paperclip glyph, a space, and the
//     filename — or the paperclip glyph and the word "Attachment" when
//     no filename is available (attachmentPlaceholder, message.go).
//   - A deleted-for-everyone tail message is OMITTED from the snippet
//     entirely — never rendered as a tombstone line — because the
//     compact row has no space for that chrome.
//   - If omitting every deleted tail message empties the snippet
//     completely, tailSnippet returns "" and StreamRow's EXISTING
//     empty-preview degrade (title + metadata only) handles the rest —
//     no new fallback rule is added here.
func tailSnippet(sortedMsgs []messageRecord) string {
	start := 0
	if len(sortedMsgs) > tailMessageCount {
		start = len(sortedMsgs) - tailMessageCount
	}
	tail := sortedMsgs[start:]

	lines := make([]string, 0, len(tail))
	for _, m := range tail {
		if m.Deleted {
			continue
		}
		body := m.Body
		if body == "" && len(m.Attachments) > 0 {
			body = attachmentPlaceholder(m.Attachments[0])
		}
		lines = append(lines, fmt.Sprintf("%s: %s", m.SenderName, body))
	}
	return Snippet(strings.Join(lines, "\n"))
}

// Snippet truncates s to at most previewRuneCap runes, by rune count
// never byte count, so a multi-byte preview is never cut mid-codepoint.
// Mirrors plugins/proton/body.go's identical helper.
func Snippet(s string) string {
	if utf8.RuneCountInString(s) <= previewRuneCap {
		return s
	}
	runes := []rune(s)
	return string(runes[:previewRuneCap])
}
