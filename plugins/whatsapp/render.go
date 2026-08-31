// Chat-transcript HTML renderer — reuses plugins/signal/render.go's
// source-agnostic bubble/run transcript builder near-verbatim (08-PATTERNS.md:
// "reuse verbatim... the source-agnostic bubble/run transcript builder
// Phase 5's [8's] WhatsApp plugin reuses"), simplified to this plan's
// narrower messageRecord shape (no quote/attachment/reaction structured
// fields — an attachment-only message's placeholder text already lives in
// Body by capture time, eventhandler.go). Sanitization, wrapping and
// theming happen at the kernel's rendition boundary
// (kernel/httpapi/rendition.go) — this file only builds the transcript's
// structural markup and HTML-escapes every interpolated text field
// (escapeText below), which is what guarantees the kernel's chat
// content-shape policy never has to distinguish this plugin's own
// structural markup from a forged one embedded in a message body
// (T-08-04/T-05-17): a message body containing markup renders as literal
// text.
package main

import (
	"bytes"
	"html"
	"time"
)

// ownSenderLabel is the fixed display name eventhandler.go already
// resolves an outgoing (IsFromMe) message's sender to. buildMessageRuns
// below determines run ownership from messageRecord.IsFromMe directly
// (WhatsApp's own store gives us that flag natively, unlike Signal's
// derived-from-name convention) — SenderName is set to this label purely
// for DISPLAY (the sender-name element on a non-own run), so the two can
// never drift out of sync.
const ownSenderLabel = "You"

// runGapThreshold is the maximum gap between two consecutive messages from
// the same sender that still collapses them into one run — a sender
// change OR a gap over this threshold starts a new run (mirrors
// plugins/signal/render.go's identical 5-minute rule).
const runGapThreshold = 5 * time.Minute

// deletedMessageCopy and editedSuffix are the fixed copy for a tombstone
// bubble and an edited-message suffix, mirroring
// plugins/signal/render.go's identical constants.
const (
	deletedMessageCopy = "This message was deleted"
	editedSuffix       = "(edited)"
)

// escapeText HTML-escapes s (a single untrusted text field — a message
// body or a sender display name) via html.EscapeString on its own, BEFORE
// it is concatenated into the assembled transcript's markup. Every
// interpolated string this file writes into HTML goes through this
// function — never the assembled document as a whole — so a crafted value
// can never break out of its own element boundary, forge a sibling
// bubble, or flip a run's ownership alignment (T-08-04): the kernel's
// chat content-shape policy performs the actual security sanitization
// over the ASSEMBLED fragment this file produces; this function's job is
// narrower — guarantee this plugin's OWN generated structure cannot be
// forged by message content.
func escapeText(s string) string {
	return html.EscapeString(s)
}

// messageRun is a maximal run of consecutive messages from the SAME
// sender, no two of which are more than runGapThreshold apart. The sender
// name renders once at the run's top (own runs render no name at all —
// ownership is signalled by alignment/background alone); the timestamp
// renders once, at the end of the run's LAST bubble.
type messageRun struct {
	SenderName string
	IsOwn      bool
	Messages   []messageRecord
}

// buildMessageRuns groups chronologically-sorted-ascending msgs into runs:
// a new run starts whenever the sender differs from the previous
// message's sender, OR the gap since the previous message EXCEEDS
// runGapThreshold — a gap of EXACTLY runGapThreshold still collapses into
// the same run (the boundary is ">", not ">="), so two consecutive
// same-sender messages exactly 5 minutes apart form two runs only once
// the gap is strictly greater than 5 minutes. No day-boundary check here
// — callers already scope msgs to a single day.
func buildMessageRuns(msgs []messageRecord) []messageRun {
	var runs []messageRun
	gapMs := runGapThreshold.Milliseconds()

	for _, m := range msgs {
		if n := len(runs); n > 0 {
			last := &runs[n-1]
			prev := last.Messages[len(last.Messages)-1]
			sameSender := last.SenderName == displaySenderName(m)
			withinGap := m.SentAtUnixMs-prev.SentAtUnixMs <= gapMs
			if sameSender && withinGap {
				last.Messages = append(last.Messages, m)
				continue
			}
		}
		runs = append(runs, messageRun{
			SenderName: displaySenderName(m),
			IsOwn:      m.IsFromMe,
			Messages:   []messageRecord{m},
		})
	}
	return runs
}

// displaySenderName resolves m's display name for run grouping/rendering:
// ownSenderLabel for an outgoing message (the fixed "You" label, never the
// raw sender_jid), else the captured sender_name (falling back to the
// sender_jid itself when no push name was ever captured — never an empty
// string).
func displaySenderName(m messageRecord) string {
	if m.IsFromMe {
		return ownSenderLabel
	}
	if m.SenderName != "" {
		return m.SenderName
	}
	if m.SenderJID != "" {
		return m.SenderJID
	}
	return "Unknown"
}

// formatTimestamp renders sentAtUnixMs as a local wall-clock time
// ("3:04 PM") — the transcript never spans more than one calendar day by
// construction, so no date component is ever needed here.
func formatTimestamp(sentAtUnixMs int64) string {
	return time.UnixMilli(sentAtUnixMs).In(time.Local).Format("3:04 PM")
}

// renderTranscript renders msgs (chronologically ascending, already
// scoped to one chat's one day) into an HTML fragment — NOT yet a complete
// document, and NOT sanitized by this plugin: the kernel's rendition
// boundary sanitizes, wraps and themes it. Emits only class tokens from
// kernel/httpapi/rendition.go's closed chatTranscriptClassTokens
// allowlist: "run", "own", "other", "sender-name", "bubble", "tombstone",
// "timestamp", "edited-suffix", "body".
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

// renderBubble renders one message as a bubble div. showTimestamp is true
// only for the run's LAST message.
func renderBubble(m messageRecord, align string, showTimestamp bool) string {
	var b bytes.Buffer
	b.WriteString(`<div class="bubble ` + align + `">`)

	switch {
	case m.Deleted:
		// Sender/timestamp chrome is unchanged by deletion — only the
		// body content is replaced, never omitted from the bubble
		// entirely.
		b.WriteString(`<div class="tombstone">` + deletedMessageCopy + `</div>`)
	case m.Body != "":
		b.WriteString(`<div class="body">` + escapeText(m.Body) + `</div>`)
	}

	switch {
	case showTimestamp:
		// The edited suffix is appended directly after the timestamp
		// with a single space, on the SAME line — only the run's last
		// bubble ever carries a timestamp, so this is the only place
		// the suffix can be co-located with one.
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
