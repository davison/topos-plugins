package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// disallowedClientSelectors is derived by reading go.mau.fi/whatsmeow's
// OWN exported *Client method surface at the pinned commit (go.mod:
// v0.0.0-20260806224404-e277b766ab33 — `grep -hoE '^func \(cli \*Client\)
// [A-Z][A-Za-z0-9_]*' *.go` against the module cache), never from
// training-data recall. It covers every SEND-CAPABLE, MUTATING, or
// PRESENCE-BROADCASTING method the client exposes — this plugin's whole
// threat model (T-08-02) is that the WhatsApp client type legitimately
// exposes a bidirectional surface that must simply never be invoked,
// unlike Signal's read-only SQL boundary (plugins/signal/readonly_test.go
// scans for write-shaped SQL instead; this package's own local-store
// writes, messagestore.go, are legitimate and are NOT selector names
// whatsmeow's Client exposes, so they can never collide with this set).
//
// Categorised exactly as this task's own action text enumerates, so a
// future whatsmeow bump can be re-audited category by category:
var disallowedClientSelectors = map[string]bool{
	// Message sending — creates or delivers message/app-state content.
	"SendMessage":     true,
	"SendFBMessage":   true,
	"SendPeerMessage": true,
	"SendAppState":    true, // mutates account-level app state (archive/mute/pin/contact edits) via a server round trip
	"RevokeMessage":   true, // itself sent as a protocol message to the chat, deleting for everyone

	// Reactions.
	"NewsletterSendReaction": true,
	// (a regular chat/group reaction is sent via SendMessage + the local,
	// no-network appstate.BuildReaction content builder — already
	// covered by the SendMessage ban above; Build* methods construct
	// content locally and never touch the network on their own, so they
	// are deliberately NOT in this set.)

	// Read receipts / delivery acknowledgement.
	"MarkRead":                          true,
	"SendProtocolMessageReceipt":        true,
	"SendMediaRetryReceipt":             true,
	"SendHistorySyncServerErrorReceipt": true,

	// Chat-presence / typing indicators.
	"SendChatPresence": true,

	// Presence and availability broadcast.
	"SendPresence":      true,
	"SubscribePresence": true, // actively signals interest in a contact's presence to WhatsApp's servers — not passive

	// Group creation/join/leave/participant mutation.
	"CreateGroup":                    true,
	"JoinGroupWithInvite":            true,
	"JoinGroupWithLink":              true,
	"LeaveGroup":                     true,
	"LinkGroup":                      true,
	"UnlinkGroup":                    true,
	"UpdateGroupParticipants":        true,
	"UpdateGroupRequestParticipants": true,
	"SetGroupAnnounce":               true,
	"SetGroupDescription":            true,
	"SetGroupJoinApprovalMode":       true,
	"SetGroupLocked":                 true,
	"SetGroupMemberAddMode":          true,
	"SetGroupName":                   true,
	"SetGroupPhoto":                  true,
	"SetGroupTopic":                  true,

	// Newsletter mutation.
	"CreateNewsletter":               true,
	"FollowNewsletter":               true,
	"UnfollowNewsletter":             true,
	"NewsletterMarkViewed":           true,
	"NewsletterSubscribeLiveUpdates": true,
	"NewsletterToggleMute":           true,
	"UploadNewsletter":               true,
	"UploadNewsletterReader":         true,
	"AcceptTOSNotice":                true, // newsletter-creation terms acceptance — a server-side account mutation

	// Privacy/profile-setting mutation.
	"SetPrivacySetting":           true,
	"SetDefaultDisappearingTimer": true,
	"SetDisappearingTimer":        true,
	"UpdateBlocklist":             true,
	"SetStatusMessage":            true, // broadcasts a new "about" text to contacts

	// Call handling — an observable action on the caller's side.
	"RejectCall": true,

	// Media mutation.
	"DeleteMedia": true, // deletes media at a direct path from WhatsApp's OWN servers

	// Active logout.
	"Logout": true,

	// The escape hatch: whatsmeow's own doc comment says this "allows
	// access to all unexported methods in Client" — a single call site
	// here would bypass every other entry in this set at once.
	"DangerousInternals": true,
}

// TestReadOnly_NoSendCapableClientSelector walks the Go AST (not text —
// nothing built by string concatenation can trip or defeat this check) of
// every non-test .go file in this package's own directory and fails the
// build if any file references a selector name in
// disallowedClientSelectors, regardless of receiver type — mirroring
// plugins/signal/readonly_test.go's identical AST-walk mechanism and
// failure-message shape, retargeted per 08-PATTERNS.md's own note: this
// plugin's boundary is BEHAVIOURAL (a method that must never be called),
// not SQL-shaped, because this plugin's own messagestore.go legitimately
// executes write SQL against its OWN local database — that is scoped OUT
// of this scan entirely by keying on whatsmeow Client selector names, not
// on SQL keywords the way Signal's variant does.
func TestReadOnly_NoSendCapableClientSelector(t *testing.T) {
	var offenses []string

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		offenses = append(offenses, scanFileForDisallowedClientCalls(t, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}

	if len(offenses) > 0 {
		t.Fatalf(
			"T-08-02: this plugin must never call a send-capable, mutating, or "+
				"presence-broadcasting whatsmeow Client method — found:\n%s",
			strings.Join(offenses, "\n"),
		)
	}

	// Negative control: prove the scanner is not vacuous by running it
	// against a fixture source string that DOES reference a disallowed
	// selector, through the identical scan function used above.
	const fixtureSend = `package fixture

func doSend(cli *whatsmeowClient) {
	cli.SendMessage(nil, nil, nil)
}
`
	if got := scanSourceForDisallowedClientCalls(t, "fixture_send.go", fixtureSend); len(got) == 0 {
		t.Fatal("negative control (SendMessage selector): expected the scanner to report at least one offence against a known-violating fixture, got none — the scanner may have become vacuous")
	}

	const fixtureLogout = `package fixture

func doLogout(cli *whatsmeowClient) {
	cli.Logout(nil)
}
`
	if got := scanSourceForDisallowedClientCalls(t, "fixture_logout.go", fixtureLogout); len(got) == 0 {
		t.Fatal("negative control (Logout selector): expected the scanner to report at least one offence against a known-violating fixture, got none — the scanner may have become vacuous")
	}
}

func scanFileForDisallowedClientCalls(t *testing.T, path string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return scanASTForDisallowedClientCalls(fset, file)
}

func scanSourceForDisallowedClientCalls(t *testing.T, filename, src string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", filename, err)
	}
	return scanASTForDisallowedClientCalls(fset, file)
}

// scanASTForDisallowedClientCalls walks file's AST and flags any
// *ast.SelectorExpr whose selector name is in disallowedClientSelectors,
// regardless of receiver — matching plugins/signal/readonly_test.go's own
// receiver-agnostic convention (a selector NAME collision is flagged even
// if, in principle, some other unrelated type happened to expose an
// identically-named method — the same trade-off Signal's own scanner
// already accepts for Exec/ExecContext).
func scanASTForDisallowedClientCalls(fset *token.FileSet, file *ast.File) []string {
	var offenses []string
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if disallowedClientSelectors[sel.Sel.Name] {
			offenses = append(offenses, fmt.Sprintf(
				"%s: disallowed whatsmeow Client selector %q referenced", fset.Position(sel.Pos()), sel.Sel.Name,
			))
		}
		return true
	})
	return offenses
}
