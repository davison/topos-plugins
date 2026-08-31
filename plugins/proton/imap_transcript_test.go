package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/backend/memory"
	imapclient "github.com/emersion/go-imap/client"
	imapserver "github.com/emersion/go-imap/server"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// recordingRelay is a loopback TCP relay that sits in front of a real IMAP
// server: it accepts the plugin's connection, dials the real server, copies
// bytes in both directions, and tees the client-to-server direction into a
// mutex-guarded buffer. This is Proof one's deterministic, no-live-Bridge
// wire-level test — 03-02-PLAN.md Task 2.
type recordingRelay struct {
	mu  sync.Mutex
	buf bytes.Buffer
	wg  sync.WaitGroup
}

// Write implements io.Writer so recordingRelay can be used directly as one
// of io.MultiWriter's destinations.
func (r *recordingRelay) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

// String returns the recorded transcript so far, safe for concurrent use.
func (r *recordingRelay) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// startRecordingRelay starts a loopback listener that relays every
// connection to serverAddr, tee-ing the client-to-server direction into the
// returned *recordingRelay. The listener is closed via t.Cleanup.
func startRecordingRelay(t *testing.T, serverAddr string) (string, *recordingRelay) {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	relay := &recordingRelay{}

	go func() {
		for {
			clientConn, err := l.Accept()
			if err != nil {
				return
			}
			relay.wg.Add(1)
			go func() {
				defer relay.wg.Done()
				serverConn, err := net.Dial("tcp", serverAddr)
				if err != nil {
					clientConn.Close()
					return
				}
				var innerWG sync.WaitGroup
				innerWG.Add(2)
				go func() {
					defer innerWG.Done()
					// The only direction this test asserts on: bytes the
					// plugin's IMAP client sends TOWARD the server.
					io.Copy(io.MultiWriter(serverConn, relay), clientConn)
				}()
				go func() {
					defer innerWG.Done()
					io.Copy(clientConn, serverConn)
				}()
				innerWG.Wait()
				clientConn.Close()
				serverConn.Close()
			}()
		}
	}()

	return l.Addr().String(), relay
}

// waitForRelayIdle blocks until every connection the relay has accepted has
// finished forwarding both directions (i.e. the plugin's Logout()-triggered
// connection close has propagated), or fails the test after a timeout.
func waitForRelayIdle(t *testing.T, relay *recordingRelay) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		relay.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the recording relay to finish forwarding")
	}
}

// seedInternalDate is the fixed IMAP INTERNALDATE newTestIMAPServer seeds
// every message with, distinct from seedEnvelopeDate by exactly one day so
// the two can never be confused for one another in an assertion. Package-
// level so every test in this package shares one source of truth (03-05
// Task 1).
var seedInternalDate = time.Date(2016, time.May, 12, 9, 15, 30, 0, time.UTC) // unix 1463044530

// seedEnvelopeDate matches the "Date:" header already present in
// sharedMessage below (Wed, 11 May 2016 14:31:59 +0000) — the message's own
// envelope Date, distinct from seedInternalDate, feeding
// Item.SecondaryTimestampUnix rather than the primary TimestampUnix.
var seedEnvelopeDate = time.Date(2016, time.May, 11, 14, 31, 59, 0, time.UTC) // unix 1462977119

// noMessageIDSubject is the distinctive subject seeded on
// "Labels/NoMessageID"'s single message — a test asserts this string never
// appears in the plugin's log output for the skip it causes (03-05 Task 2).
const noMessageIDSubject = "Unroutable seed subject"

// sharedMessageID and gammaMessageID are the normalized (angle-bracket-free)
// Message-Id values seeded below, extracted as package-level consts so
// mailbox_cache_test.go's regression tests share one source of truth with
// this fixture rather than re-deriving the encoded source_id from a literal
// header string (03-06 Task 1). sharedMessage's own Message-Id header is
// built from sharedMessageID by literal-string concatenation between angle
// brackets — Go permits const-to-const concatenation, so sharedMessage
// below stays a const.
const (
	sharedMessageID = "shared-message@example.com"
	gammaMessageID  = "gamma-message@example.com"
)

// deltaMessageID, epsilonMessageID and zetaMessageID are the three
// fixtures 03-09-PLAN.md Task 1 adds to exercise the representation
// choice fetchFull makes: which of an extracted plain-text part or a
// sanitized HTML rendition IS a message's content. No existing test is
// affected — every existing test passes a keyword list naming only the
// older mailboxes above, so DeltaTeam/EpsilonTeam/ZetaTeam are LISTed but
// never EXAMINEd there.
const (
	deltaMessageID   = "delta-message@example.com"
	epsilonMessageID = "epsilon-message@example.com"
	zetaMessageID    = "zeta-message@example.com"
)

// newTestIMAPServer starts a real github.com/emersion/go-imap server
// (server + backend/memory) on a loopback listener with insecure auth
// enabled, seeded with two mailboxes ("Labels/AlphaTeam",
// "Labels/BetaTeam") that both contain the SAME message (identical
// Message-Id) — exercising Match's dedup-by-Message-ID-merge-labels path
// (03-RESEARCH.md Pattern 2) alongside the read-only wire assertions — plus
// a third mailbox, "Labels/NoMessageID", holding a single message that
// deliberately omits the Message-Id header (03-05 Task 2), plus a fourth
// mailbox, "Labels/GammaTeam", holding a single message with a DISTINCT
// Message-Id (gammaMessageID) so two Match calls over disjoint keyword sets
// (e.g. "AlphaTeam" then "GammaTeam") discover disjoint source_ids — the
// precondition mailbox_cache_test.go's regression tests need and which
// AlphaTeam/BetaTeam alone cannot express, since those two mailboxes
// deliberately share one Message-Id (03-06 Task 1). Adding this fourth
// mailbox does not affect TestIMAPTranscript_ExamineAndPeekOnly or
// TestMatch_ItemTimestampIsInternalDate: both use the keyword list
// "AlphaTeam"/"BetaTeam" only, so "Labels/GammaTeam" (like
// "Labels/NoMessageID") is LISTed but never EXAMINEd there, and both still
// see exactly one item.
//
// Three more mailboxes — "Labels/DeltaTeam", "Labels/EpsilonTeam" and
// "Labels/ZetaTeam" — exist only to exercise fetchFull's representation
// choice (03-09-PLAN.md Task 1): DeltaTeam holds a multipart/alternative
// message (both a plain-text and an HTML part), EpsilonTeam holds a
// text/html-only message, and ZetaTeam holds a message whose only part is
// whitespace. No existing test is affected: every existing test's keyword
// list names only the older mailboxes, so these three are LISTed but
// never EXAMINEd there.
//
// Every seeded message's IMAP INTERNALDATE is explicit (seedInternalDate),
// never the zero time.Time: go-imap v1's memory backend (CreateMessage)
// substitutes time.Now() whenever the date argument is the zero value, so
// passing the zero value here would make any later "is the primary
// timestamp non-zero" assertion pass for a reason that has nothing to do
// with INTERNALDATE — a trap for the very regression this fixture exists to
// catch (03-05 Task 1).
func newTestIMAPServer(t *testing.T) (addr string) {
	t.Helper()

	bkd := memory.New()
	user, err := bkd.Login(nil, "username", "password")
	if err != nil {
		t.Fatalf("seed backend: login: %v", err)
	}

	for _, name := range []string{"Labels/AlphaTeam", "Labels/BetaTeam", "Labels/NoMessageID", "Labels/GammaTeam", "Labels/DeltaTeam", "Labels/EpsilonTeam", "Labels/ZetaTeam"} {
		if err := user.CreateMailbox(name); err != nil {
			t.Fatalf("seed backend: create mailbox %q: %v", name, err)
		}
	}

	const sharedMessage = "From: Alice <alice@example.com>\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Cross-label message\r\n" +
		"Date: Wed, 11 May 2016 14:31:59 +0000\r\n" +
		"Message-Id: <" + sharedMessageID + ">\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Hello from both labels."

	for _, name := range []string{"Labels/AlphaTeam", "Labels/BetaTeam"} {
		mbox, err := user.GetMailbox(name)
		if err != nil {
			t.Fatalf("seed backend: get mailbox %q: %v", name, err)
		}
		if err := mbox.CreateMessage(nil, seedInternalDate, bytes.NewReader([]byte(sharedMessage))); err != nil {
			t.Fatalf("seed backend: create message in %q: %v", name, err)
		}
	}

	noMessageIDMsg := "From: Carol <carol@example.com>\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: " + noMessageIDSubject + "\r\n" +
		"Date: Wed, 11 May 2016 14:31:59 +0000\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"This message deliberately has no Message-Id header."
	noMessageIDMbox, err := user.GetMailbox("Labels/NoMessageID")
	if err != nil {
		t.Fatalf("seed backend: get mailbox %q: %v", "Labels/NoMessageID", err)
	}
	if err := noMessageIDMbox.CreateMessage(nil, seedInternalDate, bytes.NewReader([]byte(noMessageIDMsg))); err != nil {
		t.Fatalf("seed backend: create message in %q: %v", "Labels/NoMessageID", err)
	}

	gammaMessage := "From: Dana <dana@example.com>\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Gamma team message\r\n" +
		"Date: Wed, 11 May 2016 14:31:59 +0000\r\n" +
		"Message-Id: <" + gammaMessageID + ">\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Hello from the gamma label."
	gammaMbox, err := user.GetMailbox("Labels/GammaTeam")
	if err != nil {
		t.Fatalf("seed backend: get mailbox %q: %v", "Labels/GammaTeam", err)
	}
	if err := gammaMbox.CreateMessage(nil, seedInternalDate, bytes.NewReader([]byte(gammaMessage))); err != nil {
		t.Fatalf("seed backend: create message in %q: %v", "Labels/GammaTeam", err)
	}

	// deltaMessage: a multipart/alternative message carrying both a
	// text/plain part (the marker sentence Task 1's test asserts on) and
	// a text/html part with a near-black-on-white inline colour style and
	// a remote-src img — the exact shape the readability gap was
	// reported against, and the shape 03-09-PLAN.md Task 1's
	// TestFetch_PrefersPlainTextOverHTMLRendition proves resolves to the
	// plain-text part alone.
	deltaMessage := "From: Erin <erin@example.com>\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Delta team multipart message\r\n" +
		"Date: Wed, 11 May 2016 14:31:59 +0000\r\n" +
		"Message-Id: <" + deltaMessageID + ">\r\n" +
		"Content-Type: multipart/alternative; boundary=\"delta-boundary\"\r\n" +
		"\r\n" +
		"--delta-boundary\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"This distinctive plain text marker sentence identifies the delta fixture.\r\n" +
		"--delta-boundary\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p style=\"color: #000000; background-color: #ffffff;\">Delta HTML paragraph.</p><img src=\"https://example.com/tracker-delta.png\">\r\n" +
		"--delta-boundary--\r\n"
	deltaMbox, err := user.GetMailbox("Labels/DeltaTeam")
	if err != nil {
		t.Fatalf("seed backend: get mailbox %q: %v", "Labels/DeltaTeam", err)
	}
	if err := deltaMbox.CreateMessage(nil, seedInternalDate, bytes.NewReader([]byte(deltaMessage))); err != nil {
		t.Fatalf("seed backend: create message in %q: %v", "Labels/DeltaTeam", err)
	}

	// epsilonMessage: a Content-Type: text/html single-part message with
	// no plain-text alternative — the HTML-only case whose sanitized
	// rendition must remain the fallback (Task 1) and must render
	// readably (Task 3).
	epsilonMessage := "From: Frank <frank@example.com>\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Epsilon team HTML-only message\r\n" +
		"Date: Wed, 11 May 2016 14:31:59 +0000\r\n" +
		"Message-Id: <" + epsilonMessageID + ">\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<h1>Epsilon heading</h1><p style=\"color: #111111;\">Epsilon paragraph text.</p><img src=\"https://example.com/tracker-epsilon.png\">"
	epsilonMbox, err := user.GetMailbox("Labels/EpsilonTeam")
	if err != nil {
		t.Fatalf("seed backend: get mailbox %q: %v", "Labels/EpsilonTeam", err)
	}
	if err := epsilonMbox.CreateMessage(nil, seedInternalDate, bytes.NewReader([]byte(epsilonMessage))); err != nil {
		t.Fatalf("seed backend: create message in %q: %v", "Labels/EpsilonTeam", err)
	}

	// zetaMessage: a Content-Type: text/plain single-part message whose
	// only body is whitespace (spaces and a CRLF) and which has no HTML
	// part — the "nothing renderable" edge that must resolve as
	// available-and-empty, never an error and never a fabricated
	// rendition.
	zetaMessage := "From: Grace <grace@example.com>\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Zeta team whitespace-only message\r\n" +
		"Date: Wed, 11 May 2016 14:31:59 +0000\r\n" +
		"Message-Id: <" + zetaMessageID + ">\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"   \r\n"
	zetaMbox, err := user.GetMailbox("Labels/ZetaTeam")
	if err != nil {
		t.Fatalf("seed backend: get mailbox %q: %v", "Labels/ZetaTeam", err)
	}
	if err := zetaMbox.CreateMessage(nil, seedInternalDate, bytes.NewReader([]byte(zetaMessage))); err != nil {
		t.Fatalf("seed backend: create message in %q: %v", "Labels/ZetaTeam", err)
	}

	s := imapserver.New(bkd)
	s.AllowInsecureAuth = true

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen imap server: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	go s.Serve(l)

	return l.Addr().String()
}

// foldersMatchReq builds a MatchRequest carrying only the "folders" field —
// the shape this plugin declares (matchVocabulary = ["folders"]) and every
// test in this package uses to drive Match, since proton's native
// categorization is IMAP mailbox/label leaf names.
func foldersMatchReq(values []string) *toposv1.MatchRequest {
	return &toposv1.MatchRequest{MatchFields: map[string]*toposv1.StringList{
		"folders": {Values: values},
	}}
}

// TestIMAPTranscript_ExamineAndPeekOnly is Proof one (03-02-PLAN.md Task 2):
// a full Describe/Match/Fetch/Health cycle against a local fake IMAP server,
// asserting the recorded client-to-server wire transcript contains EXAMINE
// and BODY.PEEK[ and contains none of the IMAP-mutating command substrings.
// This is the no-live-Bridge counterpart to TestSeenFlagUnchanged_LiveBridge
// (live_bridge_test.go).
func TestIMAPTranscript_ExamineAndPeekOnly(t *testing.T) {
	serverAddr := newTestIMAPServer(t)
	relayAddr, relay := startRecordingRelay(t, serverAddr)

	plugin, err := NewSourcePlugin("imap://bridge.invalid:143", "username", "password", "", "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}
	// Substitute a plaintext dial to the local relay — the exact seam
	// client.go's doc comment on the dial field describes: no TLS, no
	// pinned-host check, so this test exercises everything downstream of
	// the connection (EXAMINE/FETCH/SEARCH/BODY.PEEK wire behaviour)
	// without needing a live Bridge or a self-signed certificate.
	plugin.client.dial = func(timeout time.Duration) (*imapclient.Client, error) {
		conn, err := imapclient.Dial(relayAddr)
		if err != nil {
			return nil, err
		}
		conn.Timeout = timeout
		return conn, nil
	}

	ctx := context.Background()

	if _, err := plugin.Describe(ctx, &toposv1.DescribeRequest{}); err != nil {
		t.Fatalf("Describe: %v", err)
	}

	matchResp, err := plugin.Match(ctx, foldersMatchReq([]string{"AlphaTeam", "BetaTeam"}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(matchResp.GetItems()) != 1 {
		t.Fatalf("Match: got %d items, want exactly 1 (dedup across two matching mailboxes)", len(matchResp.GetItems()))
	}
	item := matchResp.GetItems()[0]
	wantLabels := map[string]bool{"AlphaTeam": true, "BetaTeam": true}
	if len(item.GetLabels()) != len(wantLabels) {
		t.Fatalf("Match item labels = %v, want both mailbox leaf names %v", item.GetLabels(), wantLabels)
	}
	for _, l := range item.GetLabels() {
		if !wantLabels[l] {
			t.Errorf("Match item labels contains unexpected label %q", l)
		}
	}

	fetchResp, err := plugin.Fetch(ctx, &toposv1.FetchRequest{
		SourceId: item.GetSourceId(),
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !fetchResp.GetAvailable() {
		t.Fatalf("Fetch: Available = false, want true")
	}

	if _, err := plugin.Health(ctx, &toposv1.HealthRequest{}); err != nil {
		t.Fatalf("Health: %v", err)
	}

	waitForRelayIdle(t, relay)

	transcript := relay.String()

	mustContain := []string{"EXAMINE ", "BODY.PEEK["}
	for _, want := range mustContain {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript missing required substring %q; transcript:\n%s", want, transcript)
		}
	}

	mustNotContain := []string{"SELECT ", "STORE ", "EXPUNGE", "APPEND ", " COPY ", " MOVE ", "DELETE "}
	for _, forbidden := range mustNotContain {
		if strings.Contains(transcript, forbidden) {
			t.Errorf("transcript contains forbidden mutating substring %q (PLUG-02 violation); transcript:\n%s", forbidden, transcript)
		}
	}
}

// TestDescribe_DeclaresFoldersVocabulary proves Describe reports the
// single declared match field this plugin reads from match_fields.
func TestDescribe_DeclaresFoldersVocabulary(t *testing.T) {
	plugin, err := NewSourcePlugin("imap://bridge.invalid:143", "username", "password", "", "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}
	resp, err := plugin.Describe(context.Background(), &toposv1.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(resp.GetMatchVocabulary()) != 1 || resp.GetMatchVocabulary()[0] != "folders" {
		t.Errorf("expected match_vocabulary [\"folders\"], got %v", resp.GetMatchVocabulary())
	}
}

// TestMatch_UndeclaredKeyIsIgnored proves a match_fields key outside this
// plugin's declared vocabulary ("tags", which proton never declares) is
// ignored entirely — only "folders" is read (D-05).
func TestMatch_UndeclaredKeyIsIgnored(t *testing.T) {
	serverAddr := newTestIMAPServer(t)

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

	req := &toposv1.MatchRequest{MatchFields: map[string]*toposv1.StringList{
		"folders": {Values: []string{"AlphaTeam", "BetaTeam"}},
		"tags":    {Values: []string{"should-be-ignored"}},
	}}
	resp, err := plugin.Match(context.Background(), req)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(resp.GetItems()) != 1 {
		t.Fatalf("expected the undeclared 'tags' key to be ignored and exactly 1 item matched via 'folders', got %d", len(resp.GetItems()))
	}
}
