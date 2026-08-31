package main

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// digest is one (chat, local calendar day) unit — the item this plugin's
// Match ultimately returns one toposv1.Item per. Ports
// plugins/signal/digest.go's identical digest shape, renamed to ChatJID
// per this plan's assumption-delta decision (chat_jid is the primary
// identity from the outset, promoted over "group" — see 08-01-PLAN.md's
// assumption_delta_decision).
type digest struct {
	ChatJID         string
	ChatName        string
	Day             string // "2006-01-02", local calendar day
	MessageCount    int
	LastMessageUnix int64 // the day's LAST message time — the item's timestamp
	Preview         string
}

// previewRuneCap bounds the tail snippet's length in runes (never bytes —
// Snippet truncates by rune count so a multi-byte snippet is never cut
// mid-codepoint), mirroring plugins/signal/digest.go's identical cap.
const previewRuneCap = 500

// tailMessageCount is the number of a day's LAST messages the preview
// carries.
const tailMessageCount = 3

// sourceIDForDigest builds a stable, deterministic source_id from chatJID
// and day: base64.RawURLEncoding-encoded "chatJID:day", mirroring
// plugins/signal's identical encode/decode pair so the id is URL-path-safe
// and reversible. This must round-trip identically for a group JID and a
// 1:1 JID (this plan's own contract test, per the assumption-delta
// decision's advisory suggestion) — nothing here special-cases either
// kind's identity.
func sourceIDForDigest(chatJID, day string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(chatJID + ":" + day))
}

// decodeSourceID reverses sourceIDForDigest, recovering the (chatJID, day)
// pair a source_id was built from. Used by Fetch to re-derive which chat
// and day a digest's transcript belongs to.
func decodeSourceID(sourceID string) (chatJID, day string, err error) {
	b, err := base64.RawURLEncoding.DecodeString(sourceID)
	if err != nil {
		return "", "", fmt.Errorf("whatsapp: decode source_id %q: %w", sourceID, err)
	}
	parts := strings.SplitN(string(b), ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("whatsapp: source_id %q does not decode to a chatJID:day pair", sourceID)
	}
	return parts[0], parts[1], nil
}

// localDay converts sentAtUnixMs (epoch milliseconds) to its local
// calendar day. time.Local is correct here and needs no config knob: the
// kernel is a desktop-local process by project constraint, so its local
// timezone IS the user's. A message whose timestamp is exactly local
// midnight lands in the day BEGINNING at that instant (time.Time.Format
// on the exact midnight instant already produces that day's date — no
// special-casing needed here beyond using time.UnixMilli/.In(time.Local)
// consistently).
func localDay(sentAtUnixMs int64) time.Time {
	return time.UnixMilli(sentAtUnixMs).In(time.Local)
}

// localDayKey formats sentAtUnixMs's local calendar day as "2006-01-02".
func localDayKey(sentAtUnixMs int64) string {
	return localDay(sentAtUnixMs).Format("2006-01-02")
}

// buildDigests groups msgs by (ChatJID, local calendar day) and returns
// one digest per group with at least one message, in no particular order.
// A chat with zero messages on a day yields no digest for that day — there
// is simply no group for it to appear in.
func buildDigests(msgs []messageRecord, chatNames map[string]string) []digest {
	type key struct {
		chatJID string
		day     string
	}
	grouped := map[key][]messageRecord{}
	var order []key
	for _, m := range msgs {
		k := key{chatJID: m.ChatJID, day: localDayKey(m.SentAtUnixMs)}
		if _, seen := grouped[k]; !seen {
			order = append(order, k)
		}
		grouped[k] = append(grouped[k], m)
	}

	digests := make([]digest, 0, len(order))
	for _, k := range order {
		group := grouped[k]
		sort.Slice(group, func(i, j int) bool {
			if group[i].SentAtUnixMs != group[j].SentAtUnixMs {
				return group[i].SentAtUnixMs < group[j].SentAtUnixMs
			}
			return group[i].ID < group[j].ID
		})

		last := group[len(group)-1]
		digests = append(digests, digest{
			ChatJID:         k.chatJID,
			ChatName:        chatNames[k.chatJID],
			Day:             k.day,
			MessageCount:    len(group),
			LastMessageUnix: last.SentAtUnixMs / 1000,
			Preview:         tailSnippet(group),
		})
	}
	return digests
}

// digestTitle composes "{chat name} — {N} message(s)" with correct
// singular/plural grammar — composed here so the frontend needs no
// client-side pluralization logic, mirroring plugins/signal/digest.go's
// identical convention.
func digestTitle(chatName string, count int) string {
	if count == 1 {
		return fmt.Sprintf("%s — %d message", chatName, count)
	}
	return fmt.Sprintf("%s — %d messages", chatName, count)
}

// tailSnippet renders a preview line for each of the LAST tailMessageCount
// messages of chronologically-sorted-ascending sortedMsgs, prefixed with
// the sender's name, newline-joined, then truncated by Snippet. A deleted
// tail message is OMITTED from the snippet entirely (mirroring
// plugins/signal/digest.go's identical rule) — if omitting every deleted
// tail message empties the snippet completely, this returns "" and the
// frontend's existing empty-preview degrade handles the rest.
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
		lines = append(lines, fmt.Sprintf("%s: %s", m.SenderName, m.Body))
	}
	return Snippet(strings.Join(lines, "\n"))
}

// Snippet truncates s to at most previewRuneCap runes, by rune count never
// byte count, so a multi-byte preview is never cut mid-codepoint. Mirrors
// plugins/signal/digest.go's identical helper.
func Snippet(s string) string {
	if utf8.RuneCountInString(s) <= previewRuneCap {
		return s
	}
	runes := []rune(s)
	return string(runes[:previewRuneCap])
}
