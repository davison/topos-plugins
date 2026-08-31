// TestSeenFlagUnchanged_LiveBridge is the direct, real end-to-end proof of
// SRC-01's second success criterion — "reading an email inside webspaces
// never marks it read in Proton" — against the actual Proton Mail Bridge
// instance. TestIMAPTranscript_ExamineAndPeekOnly (imap_transcript_test.go)
// is this test's no-network counterpart: it proves the plugin only ever
// issues EXAMINE/BODY.PEEK[ at the wire level against a local fake server,
// with no live Bridge required. This file is the live confirmation that a
// real message's \Seen flag genuinely does not change.
//
// How to run:
//
//	WEBSPACES_PROTON_LIVE_IT=1 \
//	PROTON_BRIDGE_ADDR=host:port \
//	PROTON_BRIDGE_USER=bridge-username \
//	PROTON_BRIDGE_PASS=bridge-password \
//	go test -run TestSeenFlagUnchanged_LiveBridge -v ./...
//
// Optionally set PROTON_BRIDGE_CACERT to the exported Bridge certificate's
// path; it defaults to ~/.config/topos/proton-bridge-cert.pem (the path
// 03-01's Task 1 documented). With WEBSPACES_PROTON_LIVE_IT unset (the
// default for every other test run, including CI), this test reports
// "skipped", never "failed".
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	imap "github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"

	toposv1 "github.com/davison/topos/sdk/gen/topos/v1"
)

// targetMailboxLiveIT is the well-known top-level mailbox every IMAP
// account has, used as this test's watched mailbox — chosen so this test
// needs no per-account fixture setup beyond having at least one message in
// INBOX.
const targetMailboxLiveIT = "INBOX"

func TestSeenFlagUnchanged_LiveBridge(t *testing.T) {
	addr := os.Getenv("PROTON_BRIDGE_ADDR")
	user := os.Getenv("PROTON_BRIDGE_USER")
	pass := os.Getenv("PROTON_BRIDGE_PASS")
	if os.Getenv("WEBSPACES_PROTON_LIVE_IT") != "1" || addr == "" || user == "" || pass == "" {
		t.Skip("live-Bridge test skipped: set WEBSPACES_PROTON_LIVE_IT=1 and PROTON_BRIDGE_ADDR/PROTON_BRIDGE_USER/PROTON_BRIDGE_PASS to run it — see this file's doc comment")
	}

	certPath := os.Getenv("PROTON_BRIDGE_CACERT")
	if certPath == "" {
		certPath = defaultBridgeCertPathLiveIT(t)
	}
	tlsConfig := liveTLSConfigLiveIT(t, certPath)

	// Step 1: an independent IMAP connection (not the plugin's Client)
	// records the newest message's Message-Id and current flag set.
	normalizedMsgID, initialFlags := liveFetchNewestMessageFlags(t, addr, user, pass, tlsConfig)

	// Step 2: a full Match + Fetch(FULL) cycle through the plugin, exactly
	// as the kernel would run it during a sync and an item-open.
	plugin, err := NewSourcePlugin("imap://"+addr, user, pass, certPath, "https://mail.proton.me/u/0")
	if err != nil {
		t.Fatalf("NewSourcePlugin: %v", err)
	}

	ctx := context.Background()
	matchResp, err := plugin.Match(ctx, foldersMatchReq([]string{targetMailboxLiveIT}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}

	sourceID := encodeSourceID(normalizedMsgID)
	var found bool
	for _, item := range matchResp.GetItems() {
		if item.GetSourceId() == sourceID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Match did not return the watched message (Message-Id %q) among %d items in %q", normalizedMsgID, len(matchResp.GetItems()), targetMailboxLiveIT)
	}

	if _, err := plugin.Fetch(ctx, &toposv1.FetchRequest{
		SourceId: sourceID,
		Variant:  toposv1.ContentVariant_CONTENT_VARIANT_FULL,
	}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Step 3: a second, independent IMAP connection re-reads the same
	// message's flags by Message-Id (never a cached UID/seqnum, which
	// could have shifted if new mail arrived during this test).
	finalFlags := liveFetchFlagsByMessageID(t, addr, user, pass, tlsConfig, normalizedMsgID)

	initialSeen := containsFlagLiveIT(initialFlags, imap.SeenFlag)
	finalSeen := containsFlagLiveIT(finalFlags, imap.SeenFlag)
	if initialSeen != finalSeen {
		t.Fatalf(
			"SRC-01 violation: \\Seen flag changed for Message-Id %q — before: %v (seen=%v), after: %v (seen=%v)",
			normalizedMsgID, initialFlags, initialSeen, finalFlags, finalSeen,
		)
	}
}

// defaultBridgeCertPathLiveIT returns ~/.config/topos/proton-bridge-cert.pem
// with "~" expanded to the current user's home directory — the path
// 03-01's Task 1 documented as where the exported Bridge certificate lives.
func defaultBridgeCertPathLiveIT(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home directory for default cert path: %v", err)
	}
	return filepath.Join(home, ".config", "webspaces", "proton-bridge-cert.pem")
}

// liveTLSConfigLiveIT builds the same ServerName-override + RootCAs-pinning
// TLS config production code uses (client.go's bridgeCertServerName
// constant), but fails the test loudly (rather than client.go's soft
// fallback-to-system-trust-store behaviour) if the cert cannot be read —
// this test's whole point is exercising the real pinned connection.
func liveTLSConfigLiveIT(t *testing.T, certPath string) *tls.Config {
	t.Helper()
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read Bridge certificate at %q: %v (export it via Bridge -> Settings -> Advanced -> \"Export TLS certificates\", or set PROTON_BRIDGE_CACERT)", certPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatalf("parse Bridge certificate at %q: not a valid PEM certificate", certPath)
	}
	return &tls.Config{ServerName: bridgeCertServerName, RootCAs: pool}
}

// liveDialLiveIT opens a connection completely independent of the plugin's
// own Client type — a plaintext TCP dial followed by mandatory StartTLS
// (matching this deployment's documented STARTTLS Bridge configuration),
// then LOGIN. Every command this helper issues is read-only: EXAMINE, never
// SELECT, and FETCH FLAGS only, never FETCH BODY.
func liveDialLiveIT(t *testing.T, addr, user, pass string, tlsConfig *tls.Config) *imapclient.Client {
	t.Helper()
	conn, err := imapclient.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, addr)
	if err != nil {
		t.Fatalf("live dial %s: %v", addr, err)
	}
	if err := conn.StartTLS(tlsConfig); err != nil {
		conn.Close()
		t.Fatalf("live starttls: %v", err)
	}
	conn.Timeout = 10 * time.Second
	if err := conn.Login(user, pass); err != nil {
		conn.Close()
		// The hint references credentials.go's shared authentication-order
		// constant by value rather than restating it, so this test's
		// diagnosis and the shipped runtime advice can never again drift
		// apart (03-08).
		t.Fatalf("live login: %v (%s)", err, bridgeAuthOrderNote)
	}
	return conn
}

// liveFetchNewestMessageFlags EXAMINEs targetMailboxLiveIT and FETCHes the
// highest-numbered message's ENVELOPE, FLAGS and UID, returning its
// normalized Message-Id and its current flag set.
func liveFetchNewestMessageFlags(t *testing.T, addr, user, pass string, tlsConfig *tls.Config) (normalizedMessageID string, flags []string) {
	t.Helper()
	conn := liveDialLiveIT(t, addr, user, pass, tlsConfig)
	defer conn.Logout()

	mboxStatus, err := conn.Select(targetMailboxLiveIT, true) // readOnly=true -> EXAMINE
	if err != nil {
		t.Fatalf("examine %q: %v", targetMailboxLiveIT, err)
	}
	if mboxStatus.Messages == 0 {
		t.Fatalf("%q has no messages to watch — this live test needs at least one message present", targetMailboxLiveIT)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(mboxStatus.Messages)

	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags, imap.FetchUid}
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- conn.Fetch(seqset, items, messages) }()

	var envelope *imap.Envelope
	for msg := range messages {
		if msg != nil && msg.Envelope != nil {
			envelope = msg.Envelope
			flags = msg.Flags
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("fetch newest message in %q: %v", targetMailboxLiveIT, err)
	}
	if envelope == nil {
		t.Fatalf("fetch newest message in %q: no envelope returned", targetMailboxLiveIT)
	}

	normalizedMessageID = normalizeMessageID(envelope.MessageId)
	if normalizedMessageID == "" {
		t.Fatalf("the newest message in %q has no Message-Id header — this live test needs a message with one", targetMailboxLiveIT)
	}
	return normalizedMessageID, flags
}

// liveFetchFlagsByMessageID EXAMINEs targetMailboxLiveIT, resolves
// normalizedMessageID's current UID via UID SEARCH HEADER Message-Id (never
// a cached UID — the same re-resolution discipline plugin.go's fetchFull
// applies), and FETCHes its flag set.
func liveFetchFlagsByMessageID(t *testing.T, addr, user, pass string, tlsConfig *tls.Config, normalizedMessageID string) []string {
	t.Helper()
	conn := liveDialLiveIT(t, addr, user, pass, tlsConfig)
	defer conn.Logout()

	if _, err := conn.Select(targetMailboxLiveIT, true); err != nil { // EXAMINE
		t.Fatalf("re-examine %q: %v", targetMailboxLiveIT, err)
	}

	criteria := &imap.SearchCriteria{
		Header: map[string][]string{"Message-Id": {"<" + normalizedMessageID + ">"}},
	}
	uids, err := conn.UidSearch(criteria)
	if err != nil {
		t.Fatalf("re-search message-id %q: %v", normalizedMessageID, err)
	}
	if len(uids) == 0 {
		t.Fatalf("watched message %q vanished from %q between snapshots", normalizedMessageID, targetMailboxLiveIT)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uids[0])

	items := []imap.FetchItem{imap.FetchFlags}
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- conn.UidFetch(seqset, items, messages) }()

	var flags []string
	for msg := range messages {
		if msg != nil {
			flags = msg.Flags
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("re-fetch flags for %q: %v", normalizedMessageID, err)
	}
	return flags
}

// containsFlagLiveIT reports whether flags contains want, matched
// case-sensitively (IMAP system flags like \Seen are fixed-case).
func containsFlagLiveIT(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}
