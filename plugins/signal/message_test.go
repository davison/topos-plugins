package main

import "testing"

func TestParseMessage_PlainText(t *testing.T) {
	rec, err := parseMessage("m1", "c1", 1000, "Dad", "hello there", false, "", nil, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rec.Body != "hello there" || rec.SenderName != "Dad" || rec.SentAtUnixMs != 1000 {
		t.Errorf("unexpected record: %+v", rec)
	}
	if rec.Deleted || rec.Edited {
		t.Errorf("expected a plain message to be neither deleted nor edited, got %+v", rec)
	}
}

func TestParseMessage_DeletedForEveryone_ByColumn(t *testing.T) {
	// isErasedColumn=true alone (no blob) must mark the record deleted
	// and clear its body, regardless of what the body column held.
	rec, err := parseMessage("m1", "c1", 1000, "Dad", "this should never surface", true, "", nil, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !rec.Deleted {
		t.Fatal("expected Deleted=true from the isErased column alone")
	}
	if rec.Body != "" {
		t.Errorf("expected body to be ignored for a deleted message, got %q", rec.Body)
	}
}

func TestParseMessage_DeletedForEveryone_ByBlobField(t *testing.T) {
	// deletedForEveryone in the blob alone (isErasedColumn=false) must
	// also mark the record deleted — 04-03-PLAN.md Task 1: "check both
	// signals and treat either as deleted".
	rec, err := parseMessage("m1", "c1", 1000, "Dad", "this should never surface", false, `{"deletedForEveryone":true}`, nil, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !rec.Deleted {
		t.Fatal("expected Deleted=true from the blob's deletedForEveryone field alone")
	}
	if rec.Body != "" {
		t.Errorf("expected body to be ignored for a deleted message, got %q", rec.Body)
	}
}

func TestParseMessage_Edited_LatestTextOnly(t *testing.T) {
	// body is the SQL column value — already the latest revision per
	// Signal Desktop's own schema (the messages row IS the current
	// state; edited_messages/editHistory record PRIOR revisions only).
	// The blob's editHistory carries an OLDER revision's text, which
	// must never surface as rec.Body.
	rec, err := parseMessage(
		"m1", "c1", 2000, "Dad", "the latest text",
		false,
		`{"editHistory":[{"body":"an OLDER revision, never surfaced","timestamp":1000}]}`,
		nil, nil,
	)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !rec.Edited {
		t.Fatal("expected Edited=true when editHistory is non-empty")
	}
	if rec.Body != "the latest text" {
		t.Errorf("expected the latest (SQL-column) text, got %q", rec.Body)
	}
}

func TestParseMessage_EmptyEditHistoryIsNotEdited(t *testing.T) {
	rec, err := parseMessage("m1", "c1", 1000, "Dad", "hi", false, `{"editHistory":[]}`, nil, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rec.Edited {
		t.Error("expected an empty editHistory array to NOT mark the message edited")
	}
}

func TestParseMessage_AttachmentOnlyNoBody(t *testing.T) {
	atts := []attachment{{Filename: "photo.jpg", ContentType: "image/jpeg"}}
	rec, err := parseMessage("m1", "c1", 1000, "Dad", "", false, "", atts, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rec.Body != "" {
		t.Errorf("expected empty body, got %q", rec.Body)
	}
	if len(rec.Attachments) != 1 || rec.Attachments[0].Filename != "photo.jpg" {
		t.Errorf("expected the attachment's filename to be available, got %+v", rec.Attachments)
	}
}

func TestParseMessage_AttachmentWithNoFilename(t *testing.T) {
	atts := []attachment{{Filename: "", ContentType: "image/jpeg"}}
	rec, err := parseMessage("m1", "c1", 1000, "Dad", "", false, "", atts, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(rec.Attachments) != 1 {
		t.Fatalf("expected the attachment to still be present, got %+v", rec.Attachments)
	}
	if rec.Attachments[0].Filename != "" {
		t.Errorf("expected an empty filename to be preserved (not synthesized), got %q", rec.Attachments[0].Filename)
	}
	if got := attachmentPlaceholder(rec.Attachments[0]); got != "📎 Attachment" {
		t.Errorf("expected the fixed fallback placeholder, got %q", got)
	}
}

func TestParseMessage_Reactions(t *testing.T) {
	reacts := []reaction{{Emoji: "👍", ReactorName: "Alice"}, {Emoji: "👍", ReactorName: "Bob"}}
	rec, err := parseMessage("m1", "c1", 1000, "Dad", "great news", false, "", nil, reacts)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(rec.Reactions) != 2 {
		t.Fatalf("expected 2 reactions, got %d", len(rec.Reactions))
	}
	lines := reactionLines(rec.Reactions)
	if len(lines) != 1 || lines[0] != "👍 Alice, Bob" {
		t.Errorf("expected one grouped reaction line, got %v", lines)
	}
}

func TestParseMessage_Quote(t *testing.T) {
	rec, err := parseMessage("m1", "c1", 1000, "Dad", "yes exactly", false, `{"quote":{"text":"what time works?"}}`, nil, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rec.QuoteExcerpt != "what time works?" {
		t.Errorf("expected the quoted excerpt to be available, got %q", rec.QuoteExcerpt)
	}
}

func TestParseMessage_NoQuoteIsEmptyExcerpt(t *testing.T) {
	rec, err := parseMessage("m1", "c1", 1000, "Dad", "hi", false, `{}`, nil, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rec.QuoteExcerpt != "" {
		t.Errorf("expected no quote excerpt for a non-reply message, got %q", rec.QuoteExcerpt)
	}
}

func TestParseMessage_MalformedBlobYieldsRecordNotError(t *testing.T) {
	rec, err := parseMessage("m1", "c1", 1000, "Dad", "hi", false, `{not valid json`, nil, nil)
	if err != nil {
		t.Fatalf("expected a malformed blob to yield a record and a NIL error, got error: %v", err)
	}
	if rec.Body != "hi" || rec.SenderName != "Dad" {
		t.Errorf("expected the SQL-column-derived fields to survive a malformed blob, got %+v", rec)
	}
	if rec.Edited || rec.QuoteExcerpt != "" {
		t.Errorf("expected no blob-derived richness from a malformed blob, got %+v", rec)
	}
}

func TestSenderDisplayName_ResolvesViaMap(t *testing.T) {
	names := map[string]string{"svc-1": "Alice"}
	if got := senderDisplayName(names, "svc-1"); got != "Alice" {
		t.Errorf("expected resolved name, got %q", got)
	}
}

func TestSenderDisplayName_FallsBackToIdentifierNeverEmpty(t *testing.T) {
	names := map[string]string{}
	if got := senderDisplayName(names, "svc-unknown"); got != "svc-unknown" {
		t.Errorf("expected fallback to the raw identifier, got %q", got)
	}
	if got := senderDisplayName(names, ""); got == "" {
		t.Error("expected a non-empty fallback even for an empty identifier")
	}
}

func TestAttachmentPlaceholder_WithAndWithoutFilename(t *testing.T) {
	if got := attachmentPlaceholder(attachment{Filename: "report.pdf"}); got != "📎 report.pdf" {
		t.Errorf("expected filename placeholder, got %q", got)
	}
	if got := attachmentPlaceholder(attachment{}); got != "📎 Attachment" {
		t.Errorf("expected fallback placeholder, got %q", got)
	}
}
