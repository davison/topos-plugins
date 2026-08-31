// Chat-transcript HTML renderer: the source-agnostic bubble/run
// transcript builder Phase 5's WhatsApp plugin reuses (04-03-PLAN.md
// Task 2). Sanitization, wrapping and theming moved to the kernel's
// rendition boundary (kernel/httpapi/rendition.go, D-11) — this file now
// builds the transcript's structural markup (message bubbles grouped
// into sender runs) and HTML-escapes every interpolated text field, but
// no longer sanitizes with a bluemonday policy and no longer wraps or
// themes the result. Escaping every interpolated text field before
// assembly (escapeText below) is what guarantees the kernel's chat
// content-shape policy never has to distinguish this plugin's own
// structural markup from a forged one embedded in a message body
// (T-05-17): a message body containing markup renders as literal text.
package main

import (
	"bytes"
	"html"
	"strings"
	"time"
)

// ownSenderLabel is the fixed display name readMessages/buildSenderNames
// (plugin.go) already resolve the account's own messages to. Both
// buildMessageRuns (below, for own-vs-other alignment) and plugin.go's
// own resolution logic key off this single constant so the two can never
// drift out of sync.
const ownSenderLabel = "You"

// runGapThreshold is the maximum gap between two consecutive messages
// from the same sender that still collapses them into one run
// (04-UI-SPEC.md `## UI Considerations` E2 `populated` row: "a sender
// change or a >5 min gap starts a new run").
const runGapThreshold = 5 * time.Minute

// deletedMessageCopy and editedSuffix are the exact, fixed copy
// 04-UI-SPEC.md's Copywriting Contract specifies for a tombstone bubble
// and an edited-message suffix, respectively.
const (
	deletedMessageCopy = "This message was deleted"
	editedSuffix       = "(edited)"
)

// escapeText HTML-escapes s (a single untrusted text field — a message
// body, a quoted excerpt, an attachment filename, a reactor or sender
// display name) via html.EscapeString on its own, BEFORE it is
// concatenated into the assembled transcript's markup. Every interpolated
// string this file writes into HTML goes through this function — never
// the assembled document as a whole — so a crafted value can never break
// out of its own element boundary, forge a sibling bubble, or flip a
// run's ownership alignment (T-05-17): the kernel's chat content-shape
// policy performs the actual security sanitization
// (kernel/httpapi/rendition.go) over the ASSEMBLED fragment this file
// produces; this function's job is narrower and different — guarantee
// this plugin's OWN generated structure cannot be forged by message
// content, by ensuring message content can never contain a live "<" or
// "&" that the kernel's policy would otherwise have to interpret as
// markup.
func escapeText(s string) string {
	return html.EscapeString(s)
}

// messageRun is a maximal run of consecutive messages from the SAME
// sender, no two of which are more than runGapThreshold apart
// (04-UI-SPEC.md E2 `populated`). The sender name renders once at the
// run's top (own runs render no name at all — ownership is signalled by
// alignment/background alone, never text or colour); the timestamp
// renders once, at the end of the run's LAST bubble.
type messageRun struct {
	SenderName string
	IsOwn      bool
	Messages   []messageRecord
}

// buildMessageRuns groups chronologically-sorted-ascending msgs into
// runs: a new run starts whenever the sender differs from the previous
// message's sender, or the gap since the previous message exceeds
// runGapThreshold — never on any other signal (no day boundary check
// here: callers already scope msgs to a single day, per D-04).
func buildMessageRuns(msgs []messageRecord) []messageRun {
	var runs []messageRun
	gapMs := runGapThreshold.Milliseconds()

	for _, m := range msgs {
		if n := len(runs); n > 0 {
			last := &runs[n-1]
			prev := last.Messages[len(last.Messages)-1]
			sameSender := last.SenderName == m.SenderName
			withinGap := m.SentAtUnixMs-prev.SentAtUnixMs <= gapMs
			if sameSender && withinGap {
				last.Messages = append(last.Messages, m)
				continue
			}
		}
		runs = append(runs, messageRun{
			SenderName: m.SenderName,
			IsOwn:      m.SenderName == ownSenderLabel,
			Messages:   []messageRecord{m},
		})
	}
	return runs
}

// formatTimestamp renders sentAtUnixMs as a local wall-clock time
// ("3:04 PM") — the transcript never spans more than one calendar day by
// construction (D-04), so no date component is ever needed here; the
// pane's own header already shows the digest's date (04-UI-SPEC.md E2
// `populated`: "no day-header divider is rendered").
func formatTimestamp(sentAtUnixMs int64) string {
	return time.UnixMilli(sentAtUnixMs).In(time.Local).Format("3:04 PM")
}

// renderTranscript renders msgs (chronologically ascending, already
// scoped to one conversation's one day) into an HTML fragment — NOT yet a
// complete document, and NOT sanitized by this plugin: the kernel's
// rendition boundary (kernel/httpapi/rendition.go) sanitizes, wraps and
// themes it. Each message's own text fields are escaped individually via
// escapeText before being concatenated into the run/bubble markup below.
func renderTranscript(msgs []messageRecord) []byte {
	var buf bytes.Buffer
	for _, run := range buildMessageRuns(msgs) {
		align := "other"
		if run.IsOwn {
			align = "own"
		}

		buf.WriteString(`<div class="run ` + align + `">`)
		if !run.IsOwn {
			buf.WriteString(`<div class="sender-name">` + escapeText(run.SenderName) + `</div>`)
		}
		for i, m := range run.Messages {
			buf.WriteString(renderBubble(m, align, i == len(run.Messages)-1))
		}
		buf.WriteString(`</div>`)
	}
	return buf.Bytes()
}

// renderBubble renders one message as a bubble div. showTimestamp is
// true only for the run's LAST message (04-UI-SPEC.md E2 `populated`).
// Field order within the bubble — quoted excerpt, then body (or the
// deleted tombstone in its place), then attachment placeholder chips,
// then reaction line(s) — is fixed by 04-03-PLAN.md Task 2's own action
// text.
func renderBubble(m messageRecord, align string, showTimestamp bool) string {
	var b strings.Builder
	b.WriteString(`<div class="bubble ` + align + `">`)

	if m.QuoteExcerpt != "" {
		b.WriteString(`<div class="quote">` + escapeText(m.QuoteExcerpt) + `</div>`)
	}

	switch {
	case m.Deleted:
		// Sender/timestamp chrome is unchanged by deletion — only the
		// body content is replaced, never omitted from the bubble
		// entirely (04-UI-SPEC.md E2 `partial`).
		b.WriteString(`<div class="tombstone">` + deletedMessageCopy + `</div>`)
	case m.Body != "":
		b.WriteString(`<div class="body">` + escapeText(m.Body) + `</div>`)
	}

	for _, att := range m.Attachments {
		b.WriteString(`<div class="attachment">` + escapeText(attachmentPlaceholder(att)) + `</div>`)
	}
	for _, line := range reactionLines(m.Reactions) {
		b.WriteString(`<div class="reaction">` + escapeText(line) + `</div>`)
	}

	switch {
	case showTimestamp:
		// The edited suffix is appended directly after the timestamp
		// with a single space, on the SAME line — never its own line —
		// per 04-UI-SPEC.md Typography's exact rule. Only the run's
		// last bubble ever carries a timestamp, so this is the only
		// place the suffix can be co-located with one.
		ts := escapeText(formatTimestamp(m.SentAtUnixMs))
		if m.Edited {
			ts += " " + editedSuffix
		}
		b.WriteString(`<div class="timestamp">` + ts + `</div>`)
	case m.Edited:
		// A non-last bubble in a run carries no timestamp element at
		// all — its own edited status still renders, just without a
		// timestamp to attach to.
		b.WriteString(`<div class="edited-suffix">` + editedSuffix + `</div>`)
	}

	b.WriteString(`</div>`)
	return b.String()
}
