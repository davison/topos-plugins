package main

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// webmailAllMailSegment is the fixed, name-addressable Proton webmail
// system-view path segment this plugin's deep link targets. Proton
// addresses SYSTEM views (inbox, all-mail, sent, drafts, ...) by name, but
// custom labels and folders only by an internal id this plugin has no way
// to resolve — so a system view is the only name-addressable target
// available to a plugin with no id mapping, and All Mail is the correct
// one: a matched message may live in any folder, label, archive or trash
// view, and All Mail is guaranteed to contain it regardless of which
// mailbox the match came from.
const webmailAllMailSegment = "all-mail"

// deepLinkKeywordRuneCap bounds every free-text criterion's length in
// RUNES, mirroring body.go's Snippet — the package's existing rune-cap
// precedent. A cap exists at all because a pathological subject or sender
// would otherwise produce a URL long enough to be truncated by a browser
// or a link handler, which fails silently rather than loudly. One cap for
// the whole file (L-4): an email address cannot legitimately approach
// this bound, so a separate, smaller cap for the sender criterion would
// be a second rule bought for nothing.
const deepLinkKeywordRuneCap = 500

// deepLinkDateWindowHalfWidth is how far, on EACH side of a message's own
// date, the emitted date-range bounds reach. It is fixed at 24 hours
// because the largest real-world timezone offset is under 14 hours: a
// window reaching a full day either side of the message always spans the
// message's own local date no matter which timezone Proton resolves the
// bounds in, and it also absorbs an exclusive-rather-than-inclusive upper
// bound. Narrower would be more precise and could exclude the very
// message this link is meant to find; L-6 resolves that trade in one
// direction only — never tighten this constant without re-deriving the
// bracket guarantee TestWebmailSearchDeepLink_DateWindowBracketsMessageDate
// asserts.
const deepLinkDateWindowHalfWidth = 24 * time.Hour

// The four hash-parameter names below are this plan's assumption register
// rows A-2 and A-3 (see the plan's assumption_register table) — Proton's
// real parameter names and date unit cannot be established from inside
// this repository, only confirmed live against a real account (Task 2's
// blocking checkpoint). Each constant is referenced exactly ONCE by
// webmailSearchDeepLink, so correcting a wrong guess is a single-line edit
// here, not a search across the file. searchParamKeyword is NOT a guess:
// it is the 03-10 link already confirmed working live (assumption A-1).
const (
	searchParamKeyword = "keyword"
	searchParamFrom    = "from"
	searchParamBegin   = "begin"
	searchParamEnd     = "end"
)

// encodeKeywordFragment percent-encodes s with net/url's query escaper,
// then replaces every plus sign the escaper produces for a space with the
// percent-encoded space "%20" instead. A fragment may be read either by a
// form-style parser (which decodes a plus as a space) or by a straight
// percent-decoder (which does not) — "%20" is the single form both decode
// identically. The escaper is also what makes a hostile subject or sender
// inert: it percent-encodes every fragment, query, parameter and path
// character (#, &, =, /, ?, ...) that could otherwise restructure the URL
// it is embedded in.
func encodeKeywordFragment(s string) string {
	escaped := url.QueryEscape(s)
	return strings.ReplaceAll(escaped, "+", "%20")
}

// capRunes truncates s to at most runeCap RUNES, never bytes, so a
// multi-byte value is never cut mid-codepoint. The single truncation rule
// for this file (L-4): both free-text criteria call this same helper with
// the same cap.
func capRunes(s string, runeCap int) string {
	if utf8.RuneCountInString(s) <= runeCap {
		return s
	}
	runes := []rune(s)
	return string(runes[:runeCap])
}

// deepLinkCriteria carries every field webmailSearchDeepLink can narrow a
// search by. Each field is independently optional (L-3): a zero-value
// field simply contributes no parameter to the built link. A struct
// rather than positional parameters, specifically because a live verdict
// against a real Proton account (Task 2) may call for dropping a
// criterion outright — a named field is then deleted from one
// construction site, not threaded through a signature change.
type deepLinkCriteria struct {
	// Subject is the message's own ENVELOPE subject — never a
	// placeholder or display substitute (L-3). Empty means "no subject
	// criterion".
	Subject string
	// Sender is the bare "mbox@host" address of the message's first
	// From entry (formatSender's SECOND return value), never the
	// sender-authored personal display name. Empty means "no sender
	// criterion".
	Sender string
	// Date is the message's own chronological date. The zero time.Time
	// means "no date criterion"; any non-zero value produces a window
	// of deepLinkDateWindowHalfWidth on each side of it.
	Date time.Time
}

// webmailSearchDeepLink builds a link into webmailBaseURL's All Mail
// view, optionally pre-filled with a search narrowed by criteria.
// webmailBaseURL is trimmed of any trailing separator before joining, so
// a base supplied either with or without one produces an identical
// result.
//
// Each criterion in criteria is independently omitted when the field it
// is built from is absent (L-3): a subject with no renderable content
// (absent, empty, or whitespace-only — reusing body.go's
// HasRenderableText, the package's one definition of "is there anything
// here") contributes no keyword parameter; an equally blank sender
// contributes no sender parameter; a zero-value Date contributes no date
// bounds. When NO criterion has a value the returned link carries no
// fragment at all — never a search for the empty string, and never a
// search for a placeholder.
//
// The two free-text criteria are trimmed, capped to deepLinkKeywordRuneCap
// RUNES via capRunes, and percent-encoded with the same
// encodeKeywordFragment — the sender receives IDENTICAL treatment to the
// subject, which is what makes a hostile From value inert (L-2); it is
// never special-cased as "addresses are safe". The date criterion is
// computed by the plugin, never supplied by a sender, so it needs no
// percent-encoding, but is still emitted through the same ordered
// parameter slice as every other criterion.
//
// Parameter order is FIXED and documented here because it is what makes
// the test table's exact-string assertions possible at all: keyword,
// sender, lower date bound, upper date bound. It is never built from a
// map.
func webmailSearchDeepLink(webmailBaseURL string, criteria deepLinkCriteria) string {
	base := strings.TrimRight(webmailBaseURL, "/")
	link := base + "/" + webmailAllMailSegment

	var params []string

	subject := strings.TrimSpace(criteria.Subject)
	if HasRenderableText(subject) {
		capped := capRunes(subject, deepLinkKeywordRuneCap)
		params = append(params, searchParamKeyword+"="+encodeKeywordFragment(capped))
	}

	sender := strings.TrimSpace(criteria.Sender)
	if HasRenderableText(sender) {
		capped := capRunes(sender, deepLinkKeywordRuneCap)
		params = append(params, searchParamFrom+"="+encodeKeywordFragment(capped))
	}

	if !criteria.Date.IsZero() {
		lower := criteria.Date.Add(-deepLinkDateWindowHalfWidth).Unix()
		upper := criteria.Date.Add(deepLinkDateWindowHalfWidth).Unix()
		params = append(params, searchParamBegin+"="+strconv.FormatInt(lower, 10))
		params = append(params, searchParamEnd+"="+strconv.FormatInt(upper, 10))
	}

	if len(params) == 0 {
		return link
	}

	return link + "#" + strings.Join(params, "&")
}
