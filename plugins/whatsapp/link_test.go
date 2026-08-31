package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// TestLinkEvent_MarshalsToOneLine proves every linkEvent kind marshals to
// exactly one line of JSON with no embedded newline, so a line-oriented
// reader (kernel/httpapi/whatsapplink.go, Task 3) can never split one event
// across two reads.
func TestLinkEvent_MarshalsToOneLine(t *testing.T) {
	qrEv, err := newQRLinkEvent("fake-qr-code-data-not-a-real-pairing-payload", 20*time.Second)
	if err != nil {
		t.Fatalf("newQRLinkEvent: %v", err)
	}

	events := []linkEvent{
		qrEv,
		pairedLinkEvent(),
		newErrorLinkEvent(linkErrorCodeFailed, "boom"),
		timeoutLinkEvent(),
		pairingAcceptedLinkEvent(),
		alreadyLinkedLinkEvent(),
	}
	for _, ev := range events {
		line, err := marshalLinkEvent(ev)
		if err != nil {
			t.Fatalf("marshal event kind %q: %v", ev.Kind, err)
		}
		if bytes.ContainsRune(line, '\n') {
			t.Fatalf("event kind %q marshaled with an embedded newline: %q", ev.Kind, line)
		}
		if !json.Valid(line) {
			t.Fatalf("event kind %q did not marshal to valid JSON: %q", ev.Kind, line)
		}
	}
}

// TestLinkEvent_QRPayloadShape proves a qr event carries a non-empty
// png_data_uri beginning with the PNG data-URI prefix and a positive
// expires_in_seconds.
func TestLinkEvent_QRPayloadShape(t *testing.T) {
	ev, err := newQRLinkEvent("fake-qr-code-data-not-a-real-pairing-payload", 20*time.Second)
	if err != nil {
		t.Fatalf("newQRLinkEvent: %v", err)
	}
	if ev.Kind != linkEventKindQR {
		t.Fatalf("expected kind %q, got %q", linkEventKindQR, ev.Kind)
	}
	const pngDataURIPrefix = "data:image/png;base64,"
	if ev.PNGDataURI == "" || !strings.HasPrefix(ev.PNGDataURI, pngDataURIPrefix) {
		t.Fatalf("png_data_uri does not start with %q: %q", pngDataURIPrefix, ev.PNGDataURI)
	}
	if ev.ExpiresInSeconds <= 0 {
		t.Fatalf("expires_in_seconds must be positive, got %d", ev.ExpiresInSeconds)
	}
}

// TestLinkEvent_ProgressEventsCarryOnlyKind proves a pairing_accepted line
// and an already_linked line each carry only a "kind" key — no
// png_data_uri, no expires_in_seconds, no code, no message — asserted on
// the decoded map's key set, not on a substring.
func TestLinkEvent_ProgressEventsCarryOnlyKind(t *testing.T) {
	tests := []struct {
		name string
		ev   linkEvent
		kind string
	}{
		{"pairing_accepted", pairingAcceptedLinkEvent(), string(linkEventKindPairingAccepted)},
		{"already_linked", alreadyLinkedLinkEvent(), string(linkEventKindAlreadyLinked)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, err := marshalLinkEvent(tt.ev)
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(line, &decoded); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}
			if len(decoded) != 1 {
				t.Fatalf("expected exactly one key, got %d: %v", len(decoded), decoded)
			}
			if kind, ok := decoded["kind"]; !ok || kind != tt.kind {
				t.Fatalf("expected kind %q, got %v", tt.kind, decoded)
			}
		})
	}
}

// TestLinkJSON_PairingAcceptedEmitsProgressEvent proves driving
// jsonLinkEmitter.pairingAccepted() writes exactly one event line to
// stdout whose kind is pairing_accepted, and also writes its existing
// human diagnostic to stderr.
func TestLinkJSON_PairingAcceptedEmitsProgressEvent(t *testing.T) {
	var stdout, stderr bytes.Buffer
	emitter := newJSONLinkEmitter(&stdout, &stderr)
	emitter.pairingAccepted()

	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("expected exactly one stdout event line, got %d: %s", len(lines), stdout.String())
	}
	var ev linkEvent
	if err := json.Unmarshal(lines[0], &ev); err != nil {
		t.Fatalf("unmarshal stdout event: %v", err)
	}
	if ev.Kind != linkEventKindPairingAccepted {
		t.Fatalf("expected kind %q, got %q", linkEventKindPairingAccepted, ev.Kind)
	}
	if !strings.Contains(stderr.String(), "pairing accepted") {
		t.Fatalf("expected the existing human diagnostic on stderr, got %q", stderr.String())
	}
}

// TestLinkJSON_AlreadyLinkedEmitsProgressEventWithoutDeviceID proves driving
// jsonLinkEmitter.alreadyLinked(deviceID) writes exactly one event line
// to stdout whose kind is already_linked, that line does not contain the
// device id anywhere, while the stderr diagnostic still does.
func TestLinkJSON_AlreadyLinkedEmitsProgressEventWithoutDeviceID(t *testing.T) {
	const distinctiveDeviceID = "fake-distinctive-device-id-12345"

	var stdout, stderr bytes.Buffer
	emitter := newJSONLinkEmitter(&stdout, &stderr)
	emitter.alreadyLinked(distinctiveDeviceID)

	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("expected exactly one stdout event line, got %d: %s", len(lines), stdout.String())
	}
	var ev linkEvent
	if err := json.Unmarshal(lines[0], &ev); err != nil {
		t.Fatalf("unmarshal stdout event: %v", err)
	}
	if ev.Kind != linkEventKindAlreadyLinked {
		t.Fatalf("expected kind %q, got %q", linkEventKindAlreadyLinked, ev.Kind)
	}
	if bytes.Contains(stdout.Bytes(), []byte(distinctiveDeviceID)) {
		t.Fatalf("stdout event leaked the device id: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), distinctiveDeviceID) {
		t.Fatalf("expected the stderr diagnostic to still name the device id, got %q", stderr.String())
	}
}

// TestLinkJSON_NeverLeaksRawPayload proves the raw QR pairing payload string
// itself never appears in any emitted event (stdout) or diagnostic log line
// (stderr) — only the rendered image does. The payload is a live pairing
// credential for the duration of its validity.
func TestLinkJSON_NeverLeaksRawPayload(t *testing.T) {
	const rawPayload = "2@SUPER-SECRET-PAIRING-PAYLOAD-DO-NOT-LEAK,abcXYZ123=="

	var stdout, stderr bytes.Buffer
	emitter := newJSONLinkEmitter(&stdout, &stderr)
	emitter.code(rawPayload, 20*time.Second)
	emitter.alreadyLinked("some-device-id")
	emitter.pairingAccepted()
	emitter.loggedIn(true)

	if bytes.Contains(stdout.Bytes(), []byte(rawPayload)) {
		t.Fatalf("raw QR payload leaked into the stdout event stream: %s", stdout.String())
	}
	if bytes.Contains(stderr.Bytes(), []byte(rawPayload)) {
		t.Fatalf("raw QR payload leaked into stderr diagnostics: %s", stderr.String())
	}
}

// TestLinkJSON_ErrorEvents proves an error event carries a non-empty human
// message, and that a store-in-use failure produces an error event whose
// code is distinguishable from a generic link failure.
func TestLinkJSON_ErrorEvents(t *testing.T) {
	var stdout bytes.Buffer
	emitter := newJSONLinkEmitter(&stdout, io.Discard)
	emitter.writeEvent(newErrorLinkEvent(linkErrorCodeStoreInUse, ErrStoreInUse.Error()))
	emitter.writeEvent(newErrorLinkEvent(linkErrorCodeFailed, "some other link failure"))

	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected 2 event lines, got %d: %s", len(lines), stdout.String())
	}

	var storeInUseEv, genericEv linkEvent
	if err := json.Unmarshal(lines[0], &storeInUseEv); err != nil {
		t.Fatalf("unmarshal first event: %v", err)
	}
	if err := json.Unmarshal(lines[1], &genericEv); err != nil {
		t.Fatalf("unmarshal second event: %v", err)
	}

	if storeInUseEv.Message == "" || genericEv.Message == "" {
		t.Fatalf("error events must carry a non-empty message: %+v / %+v", storeInUseEv, genericEv)
	}
	if storeInUseEv.Code == genericEv.Code {
		t.Fatalf("store-in-use code %q must differ from the generic failure code %q", storeInUseEv.Code, genericEv.Code)
	}
	if storeInUseEv.Code != linkErrorCodeStoreInUse {
		t.Fatalf("expected store-in-use code %q, got %q", linkErrorCodeStoreInUse, storeInUseEv.Code)
	}
}

// TestLinkASCII proves the ASCII terminal mode (Plan 08-01, D-04's CLI
// recovery path) still runs and still prints its "Scan with your phone to
// link" instruction line, unchanged by this plan's refactor into a shared
// link-flow core.
func TestLinkASCII(t *testing.T) {
	var stdout bytes.Buffer
	emitter := asciiLinkEmitter{out: &stdout}
	emitter.code("fake-ascii-qr-code", 20*time.Second)

	if !strings.Contains(stdout.String(), "Scan with your phone to link") {
		t.Fatalf("ASCII link mode did not print its instruction line: %q", stdout.String())
	}
}

// TestValidateLinkFlags_MutuallyExclusive proves -link and -link-json are
// mutually exclusive, the usage-error check main() applies before either
// flow starts.
func TestValidateLinkFlags_MutuallyExclusive(t *testing.T) {
	if err := validateLinkFlags(true, true); err == nil {
		t.Fatal("expected an error when both -link and -link-json are set, got nil")
	}
	if err := validateLinkFlags(true, false); err != nil {
		t.Fatalf("-link alone must be valid, got: %v", err)
	}
	if err := validateLinkFlags(false, true); err != nil {
		t.Fatalf("-link-json alone must be valid, got: %v", err)
	}
	if err := validateLinkFlags(false, false); err != nil {
		t.Fatalf("neither flag set must be valid, got: %v", err)
	}
}
