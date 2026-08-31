package main

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	imap "github.com/emersion/go-imap"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// TestWebmailSearchDeepLink_Table is a URL contract, exact-match table —
// see the plan's must_haves.truths for the behavior each row proves.
func TestWebmailSearchDeepLink_Table(t *testing.T) {
	base := "https://mail.proton.me/u/1"

	// A fixed message date used by every row that exercises the date
	// criterion, so the expected lower/upper bounds below are computable
	// by hand and are not themselves derived from the code under test.
	msgDate := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	lowerBound := msgDate.Add(-deepLinkDateWindowHalfWidth).Unix()
	upperBound := msgDate.Add(deepLinkDateWindowHalfWidth).Unix()

	tests := []struct {
		name     string
		base     string
		criteria deepLinkCriteria
		want     string
	}{
		{
			name:     "ordinary subject with spaces",
			base:     base,
			criteria: deepLinkCriteria{Subject: "Weekly team sync notes"},
			want:     base + "/all-mail#keyword=Weekly%20team%20sync%20notes",
		},
		{
			name:     "absent subject",
			base:     base,
			criteria: deepLinkCriteria{Subject: ""},
			want:     base + "/all-mail",
		},
		{
			name:     "empty subject",
			base:     base,
			criteria: deepLinkCriteria{Subject: ""},
			want:     base + "/all-mail",
		},
		{
			name:     "whitespace-only subject",
			base:     base,
			criteria: deepLinkCriteria{Subject: "   \t\n  "},
			want:     base + "/all-mail",
		},
		{
			// One fragment marker '#', an ampersand, an equals sign, a path
			// separator and a query marker — every one of those percent-
			// encoded, so the produced URL still carries exactly one
			// fragment marker and the same path segment.
			name:     "hostile punctuation subject",
			base:     base,
			criteria: deepLinkCriteria{Subject: "re: #urgent? a&b=c /path"},
			want:     base + "/all-mail#keyword=re%3A%20%23urgent%3F%20a%26b%3Dc%20%2Fpath",
		},
		{
			name:     "base with trailing separator",
			base:     base + "/",
			criteria: deepLinkCriteria{Subject: "trailing base"},
			want:     base + "/all-mail#keyword=trailing%20base",
		},
		{
			name: "every criterion present, fixed order keyword-sender-begin-end",
			base: base,
			criteria: deepLinkCriteria{
				Subject: "Invoice",
				Sender:  "billing@example.com",
				Date:    msgDate,
			},
			want: base + "/all-mail#keyword=Invoice&from=billing%40example.com&begin=" +
				strconv.FormatInt(lowerBound, 10) + "&end=" + strconv.FormatInt(upperBound, 10),
		},
		{
			name:     "no criterion has a value: bare All Mail, zero fragment markers",
			base:     base,
			criteria: deepLinkCriteria{},
			want:     base + "/all-mail",
		},
		{
			name: "subject absent but sender present: no keyword parameter, no empty value",
			base: base,
			criteria: deepLinkCriteria{
				Sender: "sender@example.com",
			},
			want: base + "/all-mail#from=sender%40example.com",
		},
		{
			name: "sender present, date zero: no date bounds appear",
			base: base,
			criteria: deepLinkCriteria{
				Subject: "Invoice",
				Sender:  "sender@example.com",
			},
			want: base + "/all-mail#keyword=Invoice&from=sender%40example.com",
		},
		{
			name: "date present, sender empty: no sender parameter appears",
			base: base,
			criteria: deepLinkCriteria{
				Date: msgDate,
			},
			want: base + "/all-mail#begin=" + strconv.FormatInt(lowerBound, 10) + "&end=" + strconv.FormatInt(upperBound, 10),
		},
		{
			name: "hostile sender: fragment marker, ampersand, equals, path separator, query marker, whitespace all percent-encoded",
			base: base,
			criteria: deepLinkCriteria{
				Sender: "re: #urgent? a&b=c /path",
			},
			want: base + "/all-mail#from=re%3A%20%23urgent%3F%20a%26b%3Dc%20%2Fpath",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := webmailSearchDeepLink(tt.base, tt.criteria)
			if got != tt.want {
				t.Errorf("webmailSearchDeepLink(%q, %+v) = %q, want %q", tt.base, tt.criteria, got, tt.want)
			}
			// Every produced URL must carry AT MOST one fragment marker —
			// a hostile subject or sender percent-encodes its own '#'
			// rather than introducing a second real one.
			if n := strings.Count(got, "#"); n > 1 {
				t.Errorf("webmailSearchDeepLink(%q, %+v) = %q, contains %d fragment markers, want at most 1", tt.base, tt.criteria, got, n)
			}
			if !strings.Contains(got, "/"+webmailAllMailSegment) {
				t.Errorf("webmailSearchDeepLink(%q, %+v) = %q, want it to contain the All Mail path segment %q", tt.base, tt.criteria, got, webmailAllMailSegment)
			}
		})
	}
}

// TestWebmailSearchDeepLink_OverCapMultiByteSubjectStaysValidUTF8 asserts
// the rune cap is applied by RUNE count, never byte count: a subject of
// multi-byte characters longer than the cap must produce a keyword that
// percent-decodes to valid UTF-8 whose rune count is exactly the cap.
func TestWebmailSearchDeepLink_OverCapMultiByteSubjectStaysValidUTF8(t *testing.T) {
	base := "https://mail.proton.me/u/1"
	// "世" is a 3-byte rune; repeating it well past the cap means a
	// byte-count truncation would produce an invalid partial codepoint at
	// the boundary, while a rune-count truncation never does.
	subject := strings.Repeat("世", deepLinkKeywordRuneCap+50)

	got := webmailSearchDeepLink(base, deepLinkCriteria{Subject: subject})

	const marker = "#keyword="
	idx := strings.Index(got, marker)
	if idx == -1 {
		t.Fatalf("webmailSearchDeepLink(%q, over-cap subject) = %q, want a %q fragment", base, got, marker)
	}
	rest := got[idx+len(marker):]
	// The keyword is the only parameter present in this row, so the rest
	// of the fragment IS the encoded keyword.
	encodedKeyword := rest

	decoded, err := url.QueryUnescape(encodedKeyword)
	if err != nil {
		t.Fatalf("url.QueryUnescape(%q): %v", encodedKeyword, err)
	}

	if !utf8.ValidString(decoded) {
		t.Fatalf("decoded keyword %q is not valid UTF-8 (byte-truncated mid-codepoint)", decoded)
	}
	if gotRunes := utf8.RuneCountInString(decoded); gotRunes != deepLinkKeywordRuneCap {
		t.Errorf("decoded keyword rune count = %d, want %d (the cap)", gotRunes, deepLinkKeywordRuneCap)
	}
}

// TestWebmailSearchDeepLink_OverCapMultiByteSenderStaysValidUTF8 mirrors
// the subject over-cap assertion onto the sender criterion — proving one
// shared truncation rule (capRunes) rather than a second invented one
// (L-4).
func TestWebmailSearchDeepLink_OverCapMultiByteSenderStaysValidUTF8(t *testing.T) {
	base := "https://mail.proton.me/u/1"
	sender := strings.Repeat("世", deepLinkKeywordRuneCap+50)

	got := webmailSearchDeepLink(base, deepLinkCriteria{Sender: sender})

	const marker = "#from="
	idx := strings.Index(got, marker)
	if idx == -1 {
		t.Fatalf("webmailSearchDeepLink(%q, over-cap sender) = %q, want a %q fragment", base, got, marker)
	}
	encodedSender := got[idx+len(marker):]

	decoded, err := url.QueryUnescape(encodedSender)
	if err != nil {
		t.Fatalf("url.QueryUnescape(%q): %v", encodedSender, err)
	}

	if !utf8.ValidString(decoded) {
		t.Fatalf("decoded sender %q is not valid UTF-8 (byte-truncated mid-codepoint)", decoded)
	}
	if gotRunes := utf8.RuneCountInString(decoded); gotRunes != deepLinkKeywordRuneCap {
		t.Errorf("decoded sender rune count = %d, want %d (the cap)", gotRunes, deepLinkKeywordRuneCap)
	}
}

// TestWebmailSearchDeepLink_DateWindowBracketsMessageDate is the
// executable form of L-6: for a message date expressed in a NON-UTC
// location and positioned near a local midnight, the emitted lower bound
// must be strictly less than the message's own epoch second and the
// emitted upper bound strictly greater — proving the window brackets the
// message regardless of which local date boundary the message sits near.
// This fails if anyone later tightens the window to a same-day boundary.
func TestWebmailSearchDeepLink_DateWindowBracketsMessageDate(t *testing.T) {
	// A fixed offset location (avoids depending on the tzdata database
	// being installed in the test environment) fifty minutes before local
	// midnight — deliberately close to a day boundary, which is exactly
	// the case a same-day window would fail.
	loc := time.FixedZone("UTC-5", -5*60*60)
	msgDate := time.Date(2026, 6, 1, 23, 50, 0, 0, loc)
	msgEpoch := msgDate.Unix()

	got := webmailSearchDeepLink("https://mail.proton.me/u/1", deepLinkCriteria{Date: msgDate})

	beginIdx := strings.Index(got, "begin=")
	endIdx := strings.Index(got, "&end=")
	if beginIdx == -1 || endIdx == -1 {
		t.Fatalf("webmailSearchDeepLink(..., date-only criteria) = %q, want both begin and end parameters", got)
	}

	beginStr := got[beginIdx+len("begin=") : endIdx]
	endStr := got[endIdx+len("&end="):]

	beginVal, err := strconv.ParseInt(beginStr, 10, 64)
	if err != nil {
		t.Fatalf("begin=%q: not a plain base-10 integer: %v", beginStr, err)
	}
	endVal, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		t.Fatalf("end=%q: not a plain base-10 integer: %v", endStr, err)
	}

	if beginVal >= msgEpoch {
		t.Errorf("begin=%d, want strictly less than the message's own epoch second %d", beginVal, msgEpoch)
	}
	if endVal <= msgEpoch {
		t.Errorf("end=%d, want strictly greater than the message's own epoch second %d", endVal, msgEpoch)
	}
}

// TestToItem_DeepLinkIsAWebmailSearchNotALabelPath asserts toItem builds
// DeepLink from the constructor over the envelope's own subject, sender
// and date, and that the result never contains the matched label's leaf
// name — the assertion that would catch a partial fix that merely
// appended a search fragment onto the old label path.
func TestToItem_DeepLinkIsAWebmailSearchNotALabelPath(t *testing.T) {
	plugin, err := NewSourcePlugin("imap://bridge.invalid:143", "username", "password", "", "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}

	internalDate := time.Date(2026, 4, 1, 8, 30, 0, 0, time.UTC)
	m := &matched{
		envelope: &imap.Envelope{
			Subject:   "House move update",
			MessageId: "<house-move-update@example.com>",
			From: []*imap.Address{
				{PersonalName: "Alex", MailboxName: "alex", HostName: "example.com"},
			},
		},
		mailbox:      "Labels/House Move",
		labels:       []string{"House Move"},
		internalDate: internalDate,
	}

	item := plugin.toItem("test-source-id", m)

	_, senderAddress := formatSender(m.envelope)
	want := webmailSearchDeepLink(plugin.webmailBaseURL, deepLinkCriteria{
		Subject: m.envelope.Subject,
		Sender:  senderAddress,
		Date:    internalDate,
	})
	if got := item.GetDeepLink(); got != want {
		t.Errorf("item.DeepLink = %q, want %q (the constructor's own output for this message's criteria)", got, want)
	}
	if strings.Contains(item.GetDeepLink(), "House Move") {
		t.Errorf("item.DeepLink = %q, must not contain the label leaf name %q", item.GetDeepLink(), "House Move")
	}
}

// TestToItem_EmptyFromListOmitsSenderCriterionWithoutPanic asserts an
// envelope with no From address still produces a working link — the
// sender criterion is simply omitted, exactly like any other absent
// field (L-3) — and that building it never panics.
func TestToItem_EmptyFromListOmitsSenderCriterionWithoutPanic(t *testing.T) {
	plugin, err := NewSourcePlugin("imap://bridge.invalid:143", "username", "password", "", "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}

	m := &matched{
		envelope: &imap.Envelope{
			Subject:   "Automated notification",
			MessageId: "<automated-notification@example.com>",
			From:      nil,
		},
		mailbox: "INBOX",
		labels:  []string{"INBOX"},
	}

	item := plugin.toItem("test-source-id", m)

	if got := item.GetDeepLink(); strings.Contains(got, "from=") {
		t.Errorf("item.DeepLink = %q, must not contain a sender parameter when envelope has no From address", got)
	}
	if got := item.GetDeepLink(); !strings.Contains(got, "keyword=Automated") {
		t.Errorf("item.DeepLink = %q, want the subject criterion still present", got)
	}
}

// TestToItem_EmptySubjectNeverSearchesPlaceholder asserts the criteria
// subject is taken from the envelope directly, never from toItem's
// display Title (which has already been substituted with the
// "(no subject)" placeholder by the time DeepLink is built) — a message
// whose envelope subject is genuinely empty must never produce a search
// for the placeholder string the stream renders as its title (L-3).
func TestToItem_EmptySubjectNeverSearchesPlaceholder(t *testing.T) {
	plugin, err := NewSourcePlugin("imap://bridge.invalid:143", "username", "password", "", "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}

	m := &matched{
		envelope: &imap.Envelope{
			Subject:   "",
			MessageId: "<no-subject@example.com>",
			From: []*imap.Address{
				{PersonalName: "", MailboxName: "sender", HostName: "example.com"},
			},
		},
		mailbox: "INBOX",
		labels:  []string{"INBOX"},
	}

	item := plugin.toItem("test-source-id", m)

	if got := item.GetTitle(); got != noSubjectPlaceholder {
		t.Fatalf("item.Title = %q, want the placeholder %q (test setup assumption)", got, noSubjectPlaceholder)
	}
	if got := item.GetDeepLink(); strings.Contains(got, "keyword=") {
		t.Errorf("item.DeepLink = %q, must not carry a keyword parameter for a genuinely empty envelope subject — the placeholder must never become a search term", got)
	}
}

// TestToItem_FidelityRemainsAnchored asserts the fidelity declaration is
// unchanged by this plan: the link lands adjacent to the message, not on
// it, so it stays ANCHORED — asserted, not assumed.
func TestToItem_FidelityRemainsAnchored(t *testing.T) {
	plugin, err := NewSourcePlugin("imap://bridge.invalid:143", "username", "password", "", "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}

	m := &matched{
		envelope: &imap.Envelope{
			Subject:   "House move update",
			MessageId: "<house-move-update@example.com>",
		},
		mailbox: "Labels/House Move",
		labels:  []string{"House Move"},
	}

	item := plugin.toItem("test-source-id", m)

	if got := item.GetFidelity(); got != toposv1.LinkFidelity_LINK_FIDELITY_ANCHORED {
		t.Errorf("item.Fidelity = %v, want LINK_FIDELITY_ANCHORED", got)
	}
}
