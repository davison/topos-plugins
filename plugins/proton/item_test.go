package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	imap "github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

// newTestPluginDialingServer builds a SourcePlugin whose client.dial is
// substituted to connect directly to serverAddr — the exact seam
// TestIMAPTranscript_ExamineAndPeekOnly uses (client.go's doc comment on
// the dial field), minus the recording relay: these tests assert on
// returned Go values, not on the wire transcript.
func newTestPluginDialingServer(t *testing.T, serverAddr string) *SourcePlugin {
	t.Helper()

	plugin, err := NewSourcePlugin("imap://bridge.invalid:143", "username", "password", "", "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}
	plugin.client.dial = func(timeout time.Duration) (*imapclient.Client, error) {
		conn, err := imapclient.Dial(serverAddr)
		if err != nil {
			return nil, err
		}
		conn.Timeout = timeout
		return conn, nil
	}
	return plugin
}

// TestMatch_ItemTimestampIsInternalDate is the 03-05 Task 1 regression: the
// message's IMAP INTERNALDATE (seedInternalDate) must become
// Item.TimestampUnix, and the message's own envelope Date header
// (seedEnvelopeDate) must become Item.SecondaryTimestampUnix — two distinct
// fields from two distinct sources. A fix that wrongly sourced the primary
// timestamp from the envelope Date would make this test's first two
// assertions collide and its third assertion fail.
func TestMatch_ItemTimestampIsInternalDate(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	plugin := newTestPluginDialingServer(t, serverAddr)

	ctx := context.Background()
	matchResp, err := plugin.Match(ctx, foldersMatchReq([]string{"AlphaTeam", "BetaTeam"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(matchResp.GetItems()) != 1 {
		t.Fatalf("Match: got %d items, want exactly 1 (dedup across two matching mailboxes must not lose the timestamp)", len(matchResp.GetItems()))
	}

	item := matchResp.GetItems()[0]

	wantPrimary := seedInternalDate.Unix()
	if got := item.GetTimestampUnix(); got != wantPrimary {
		t.Errorf("item.TimestampUnix = %d, want %d (seedInternalDate.Unix())", got, wantPrimary)
	}

	wantSecondary := seedEnvelopeDate.Unix()
	if got := item.GetSecondaryTimestampUnix(); got != wantSecondary {
		t.Errorf("item.SecondaryTimestampUnix = %d, want %d (seedEnvelopeDate.Unix())", got, wantSecondary)
	}

	if item.GetTimestampUnix() == item.GetSecondaryTimestampUnix() {
		t.Errorf("item.TimestampUnix (%d) must differ from item.SecondaryTimestampUnix (%d) — INTERNALDATE and the envelope Date are distinct sources", item.GetTimestampUnix(), item.GetSecondaryTimestampUnix())
	}
}

// TestToItem_ZeroInternalDateYieldsZeroTimestamp asserts toItem's zero-value
// guard: a matched entry whose internalDate is the zero time.Time must
// yield Item.TimestampUnix == 0 — the "no date" sentinel the kernel's
// ordering and web/src/lib/format.ts's formatItemDate already handle —
// never the large negative seconds value time.Time{}.Unix() produces.
func TestToItem_ZeroInternalDateYieldsZeroTimestamp(t *testing.T) {
	plugin, err := NewSourcePlugin("imap://bridge.invalid:143", "username", "password", "", "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}

	m := &matched{
		envelope: &imap.Envelope{
			Subject:   "No internal date",
			MessageId: "<no-internal-date@example.com>",
		},
		mailbox: "Labels/AlphaTeam",
		labels:  []string{"AlphaTeam"},
		// internalDate deliberately left as the zero time.Time.
	}

	item := plugin.toItem("test-source-id", m)

	if got := item.GetTimestampUnix(); got != 0 {
		t.Errorf("item.TimestampUnix = %d, want 0 for a zero internalDate (got the zero time.Time's negative Unix seconds instead of the guarded sentinel)", got)
	}
}

// TestMatch_EmptyMessageIDSkipIsLogged is the 03-05 Task 2 regression: a
// Match run over a mailbox holding one message with no Message-Id header
// must skip it (zero items, nil error — 03-RESEARCH.md Pitfall 6) and log
// exactly one count-only line naming the skip — closing the half-
// implemented 03-01 must-have ("a counted, logged skip"). The log line must
// never carry the message's own content.
func TestMatch_EmptyMessageIDSkipIsLogged(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	plugin := newTestPluginDialingServer(t, serverAddr)

	var logBuf bytes.Buffer
	plugin.logOut = &logBuf

	ctx := context.Background()
	matchResp, err := plugin.Match(ctx, foldersMatchReq([]string{"NoMessageID"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(matchResp.GetItems()) != 0 {
		t.Fatalf("Match: got %d items, want 0 (a message with no Message-Id must be skipped, never indexed under an empty-string key)", len(matchResp.GetItems()))
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "skipped 1 message") {
		t.Errorf("plugin log output = %q, want it to contain %q", logged, "skipped 1 message")
	}
	if strings.Contains(logged, noMessageIDSubject) {
		t.Errorf("plugin log output = %q, must NOT contain the seeded message subject %q (T-03-05-03)", logged, noMessageIDSubject)
	}
}
