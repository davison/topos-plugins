package main

import (
	"strings"
	"testing"
	"time"
)

// TestDigest_LocalMidnightBoundary proves a message whose timestamp is
// exactly local midnight lands in the day BEGINNING at that instant, not
// the preceding one.
func TestDigest_LocalMidnightBoundary(t *testing.T) {
	midnight := time.Date(2026, 8, 10, 0, 0, 0, 0, time.Local)
	got := localDayKey(midnight.UnixMilli())
	want := "2026-08-10"
	if got != want {
		t.Fatalf("localDayKey(exact midnight) = %q, want %q (must not fall into the preceding day)", got, want)
	}
}

// TestDigest_RunBoundaryExactlyFiveMinutes proves two consecutive
// same-sender messages exactly runGapThreshold (5 minutes) apart still
// collapse into ONE run (the boundary is "> threshold", not ">="), while a
// gap one millisecond beyond the threshold starts a new run.
func TestDigest_RunBoundaryExactlyFiveMinutes(t *testing.T) {
	base := time.Date(2026, 8, 10, 9, 0, 0, 0, time.Local).UnixMilli()

	exactlyFive := []messageRecord{
		{ID: "1", SenderName: "Alice", SentAtUnixMs: base, Body: "first"},
		{ID: "2", SenderName: "Alice", SentAtUnixMs: base + runGapThreshold.Milliseconds(), Body: "second"},
	}
	runs := buildMessageRuns(exactlyFive)
	if len(runs) != 1 {
		t.Fatalf("exactly-5-minutes gap: want 1 run (still adjacent), got %d", len(runs))
	}

	overFive := []messageRecord{
		{ID: "1", SenderName: "Alice", SentAtUnixMs: base, Body: "first"},
		{ID: "2", SenderName: "Alice", SentAtUnixMs: base + runGapThreshold.Milliseconds() + 1, Body: "second"},
	}
	runs = buildMessageRuns(overFive)
	if len(runs) != 2 {
		t.Fatalf("over-5-minutes gap: want 2 runs (new run), got %d", len(runs))
	}
}

// TestDigest_SingularPluralTitle proves a one-message day titles singular
// and a two-message day titles plural.
func TestDigest_SingularPluralTitle(t *testing.T) {
	if got, want := digestTitle("Family", 1), "Family — 1 message"; got != want {
		t.Fatalf("digestTitle(1) = %q, want %q", got, want)
	}
	if got, want := digestTitle("Family", 2), "Family — 2 messages"; got != want {
		t.Fatalf("digestTitle(2) = %q, want %q", got, want)
	}
}

// TestDigest_SourceIDRoundTrip proves sourceIDForDigest/decodeSourceID
// round-trip identically for a group JID and a 1:1 JID — no code path may
// special-case either kind's identity (this plan's own assumption-delta
// contract test).
func TestDigest_SourceIDRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		chatJID string
		day     string
	}{
		{"group JID", "123456789@g.us", "2026-08-10"},
		{"1:1 JID", "447700900000@s.whatsapp.net", "2026-08-10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := sourceIDForDigest(c.chatJID, c.day)
			gotJID, gotDay, err := decodeSourceID(id)
			if err != nil {
				t.Fatalf("decodeSourceID: %v", err)
			}
			if gotJID != c.chatJID || gotDay != c.day {
				t.Fatalf("round-trip: want (%q, %q), got (%q, %q)", c.chatJID, c.day, gotJID, gotDay)
			}
		})
	}
}

// TestDigest_RuneBoundaryTruncation proves a body containing multi-byte
// runes truncates on a rune boundary, never mid-codepoint.
func TestDigest_RuneBoundaryTruncation(t *testing.T) {
	// A string of multi-byte runes (each 3 bytes in UTF-8) well beyond
	// previewRuneCap runes.
	rune3byte := "世"
	long := strings.Repeat(rune3byte, previewRuneCap+50)

	got := Snippet(long)

	if n := len([]rune(got)); n != previewRuneCap {
		t.Fatalf("Snippet: want exactly %d runes, got %d", previewRuneCap, n)
	}
	if !strings.HasSuffix(got, rune3byte) {
		t.Fatalf("Snippet: result does not end on a whole rune boundary: %q", got[len(got)-10:])
	}
}

// TestDigest_ZeroMessageDayYieldsNoDigest proves a chat with zero messages
// on a day yields no digest for that day — buildDigests only ever
// produces a group for a (chat, day) pair that actually has at least one
// message.
func TestDigest_ZeroMessageDayYieldsNoDigest(t *testing.T) {
	digests := buildDigests(nil, map[string]string{"123@g.us": "Family"})
	if len(digests) != 0 {
		t.Fatalf("buildDigests(no messages) = %d digests, want 0", len(digests))
	}
}
