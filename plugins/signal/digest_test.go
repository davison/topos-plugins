package main

import (
	"strings"
	"testing"
	"time"
)

// localMs builds an epoch-millisecond timestamp for the given local wall
// clock time — using time.Local throughout (matching digest.go's own
// D-04 convention) so these tests are correct regardless of the test
// runner's actual system timezone.
func localMs(year int, month time.Month, day, hour, min int) int64 {
	return time.Date(year, month, day, hour, min, 0, 0, time.Local).UnixMilli()
}

// rec is a tiny messageRecord fixture builder for this file's own tests
// — a plain, non-deleted, non-edited, richness-free message unless a
// test explicitly sets a field afterward.
func rec(convID, sender, body string, sentAtUnixMs int64) messageRecord {
	return messageRecord{ConversationID: convID, SenderName: sender, Body: body, SentAtUnixMs: sentAtUnixMs}
}

func TestBuildDigests_TwoLocalDaysProduceTwoDigests(t *testing.T) {
	msgs := []messageRecord{
		rec("c1", "Dad", "morning", localMs(2026, 8, 1, 9, 0)),
		rec("c1", "Dad", "next day", localMs(2026, 8, 2, 9, 0)),
	}
	got := buildDigests(msgs, map[string]string{"c1": "Dad"})
	if len(got) != 2 {
		t.Fatalf("expected 2 digests for messages spanning two local days, got %d: %+v", len(got), got)
	}
}

func TestBuildDigests_TimestampIsDaysLastMessageNotFirst(t *testing.T) {
	first := localMs(2026, 8, 1, 8, 0)
	last := localMs(2026, 8, 1, 22, 0)
	msgs := []messageRecord{
		rec("c1", "Dad", "morning", first),
		rec("c1", "Dad", "night", last),
	}
	got := buildDigests(msgs, map[string]string{"c1": "Dad"})
	if len(got) != 1 {
		t.Fatalf("expected 1 digest, got %d", len(got))
	}
	if got[0].LastMessageUnix != last/1000 {
		t.Errorf("expected timestamp to be the day's LAST message (%d), got %d", last/1000, got[0].LastMessageUnix)
	}
}

func TestBuildDigests_MessageCountIncludesDeletedMessages(t *testing.T) {
	deleted := rec("c1", "Dad", "", localMs(2026, 8, 1, 9, 0))
	deleted.Deleted = true
	msgs := []messageRecord{
		rec("c1", "Dad", "morning", localMs(2026, 8, 1, 8, 0)),
		deleted,
	}
	got := buildDigests(msgs, map[string]string{"c1": "Dad"})
	if len(got) != 1 {
		t.Fatalf("expected 1 digest, got %d", len(got))
	}
	if got[0].MessageCount != 2 {
		t.Errorf("expected the day's message count to include the deleted message, got %d", got[0].MessageCount)
	}
}

func TestSourceIDForDigest_DeterministicAndRoundTrips(t *testing.T) {
	id1 := sourceIDForDigest("c1", "2026-08-01")
	id2 := sourceIDForDigest("c1", "2026-08-01")
	if id1 != id2 {
		t.Fatalf("expected sourceIDForDigest to be deterministic, got %q and %q", id1, id2)
	}

	convID, day, err := decodeSourceID(id1)
	if err != nil {
		t.Fatalf("decodeSourceID: %v", err)
	}
	if convID != "c1" || day != "2026-08-01" {
		t.Errorf("expected round-trip to (c1, 2026-08-01), got (%q, %q)", convID, day)
	}
}

func TestSourceIDForDigest_DifferentDaysProduceDifferentIDs(t *testing.T) {
	id1 := sourceIDForDigest("c1", "2026-08-01")
	id2 := sourceIDForDigest("c1", "2026-08-02")
	if id1 == id2 {
		t.Fatalf("expected different days to produce different source_ids, both were %q", id1)
	}
}

func TestSourceIDForDigest_SameDayDifferentConversationsProduceDifferentIDs(t *testing.T) {
	id1 := sourceIDForDigest("c1", "2026-08-01")
	id2 := sourceIDForDigest("c2", "2026-08-01")
	if id1 == id2 {
		t.Fatalf("expected different conversations to produce different source_ids, both were %q", id1)
	}
}

func TestDigestTitle_SingularAndPluralGrammar(t *testing.T) {
	if got := digestTitle("Dad", 1); got != "Dad — 1 message" {
		t.Errorf("expected singular grammar, got %q", got)
	}
	if got := digestTitle("Dad", 2); got != "Dad — 2 messages" {
		t.Errorf("expected plural grammar for 2, got %q", got)
	}
	if got := digestTitle("Dad", 0); got != "Dad — 0 messages" {
		t.Errorf("expected plural grammar for 0, got %q", got)
	}
}

func TestTailSnippet_LastMessagesChronologicalSenderPrefixedNoEarlierLeak(t *testing.T) {
	msgs := []messageRecord{
		rec("c1", "Alice", "one", 1),
		rec("c1", "Bob", "two", 2),
		rec("c1", "Alice", "three", 3),
		rec("c1", "Bob", "four", 4),
	}
	got := tailSnippet(msgs)
	want := "Bob: two\nAlice: three\nBob: four"
	if got != want {
		t.Errorf("expected tail snippet %q, got %q", want, got)
	}
	if strings.Contains(got, "one") {
		t.Errorf("expected the tail snippet to exclude messages before the tail, got %q", got)
	}
}

func TestTailSnippet_TruncatesByRuneCountNotByteCount(t *testing.T) {
	// A multi-byte rune (é is 2 bytes in UTF-8) repeated well past
	// previewRuneCap runes — truncating by byte count would cut mid-rune
	// and corrupt the string; truncating by rune count never does.
	long := strings.Repeat("é", previewRuneCap+50)
	msgs := []messageRecord{rec("c1", "A", long, 1)}
	got := tailSnippet(msgs)
	if !strings.HasPrefix(got, "A: ") {
		t.Fatalf("expected sender prefix, got %q", got[:min(20, len(got))])
	}
	body := strings.TrimPrefix(got, "A: ")
	runeCount := 0
	for range body {
		runeCount++
	}
	if runeCount > previewRuneCap {
		t.Errorf("expected at most %d runes, got %d", previewRuneCap, runeCount)
	}
	if !strings.HasSuffix(body, "é") && runeCount > 0 {
		t.Errorf("expected truncation to land on a full rune boundary, got trailing bytes %q", body[len(body)-4:])
	}
}

func TestTailSnippet_AllMessagesSameSenderStillPrefixedEachLine(t *testing.T) {
	msgs := []messageRecord{
		rec("c1", "Dad", "one", 1),
		rec("c1", "Dad", "two", 2),
	}
	got := tailSnippet(msgs)
	want := "Dad: one\nDad: two"
	if got != want {
		t.Errorf("expected each line prefixed even for a single sender, got %q", got)
	}
}

func TestTailSnippet_AttachmentOnlyLastMessageRendersPlaceholder(t *testing.T) {
	withAttachment := rec("c1", "Dad", "", 2)
	withAttachment.Attachments = []attachment{{Filename: "photo.jpg"}}
	msgs := []messageRecord{
		rec("c1", "Dad", "one", 1),
		withAttachment,
	}
	got := tailSnippet(msgs)
	want := "Dad: one\nDad: 📎 photo.jpg"
	if got != want {
		t.Errorf("expected the attachment-only tail line to render the placeholder, got %q", got)
	}
}

func TestTailSnippet_AttachmentWithNoFilenameUsesFallback(t *testing.T) {
	withAttachment := rec("c1", "Dad", "", 1)
	withAttachment.Attachments = []attachment{{}}
	got := tailSnippet([]messageRecord{withAttachment})
	if got != "Dad: 📎 Attachment" {
		t.Errorf("expected the fallback Attachment placeholder, got %q", got)
	}
}

func TestTailSnippet_DeletedTailMessageIsOmittedNotTombstoned(t *testing.T) {
	deleted := rec("c1", "Dad", "", 2)
	deleted.Deleted = true
	msgs := []messageRecord{
		rec("c1", "Dad", "one", 1),
		deleted,
	}
	got := tailSnippet(msgs)
	if got != "Dad: one" {
		t.Errorf("expected the deleted tail message to be omitted entirely (no tombstone line), got %q", got)
	}
}

func TestTailSnippet_AllTailMessagesDeletedYieldsEmptyPreview(t *testing.T) {
	d1 := rec("c1", "Dad", "", 1)
	d1.Deleted = true
	d2 := rec("c1", "Mum", "", 2)
	d2.Deleted = true
	got := tailSnippet([]messageRecord{d1, d2})
	if got != "" {
		t.Errorf("expected an empty preview when every tail message was deleted, got %q", got)
	}
}

func TestBuildDigests_FullyDeletedTailStillProducesADigestWithEmptyPreview(t *testing.T) {
	deleted := rec("c1", "Dad", "", localMs(2026, 8, 1, 9, 0))
	deleted.Deleted = true
	got := buildDigests([]messageRecord{deleted}, map[string]string{"c1": "Dad"})
	if len(got) != 1 {
		t.Fatalf("expected 1 digest even when its only message was deleted, got %d", len(got))
	}
	if got[0].Preview != "" {
		t.Errorf("expected an empty preview, got %q", got[0].Preview)
	}
	if got[0].MessageCount != 1 {
		t.Errorf("expected the deleted message to still count toward the day's total, got %d", got[0].MessageCount)
	}
}
