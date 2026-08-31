// Command topos-plugin-proton: MIME part extraction from a peeked
// RFC822 message. Sanitization, wrapping and theming for the extracted
// HTML part moved to the kernel's rendition boundary
// (kernel/httpapi/rendition.go, D-11) — this file now owns only MIME
// extraction, never presentation.
package main

import (
	"bytes"
	"io"
	"strings"
	"unicode/utf8"

	message "github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

const (
	// maxPartBytes bounds a single MIME part read, well under
	// sdk.MaxMessageSize (64 MiB), so a crafted message with one enormous
	// part cannot exhaust memory (T-03-07).
	maxPartBytes = 8 * 1024 * 1024
	// maxParts bounds the number of MIME parts read from a single
	// message, so a crafted message with thousands of parts cannot
	// exhaust memory or spin forever (T-03-07).
	maxParts = 256
	// previewRuneCap mirrors plugins/silverbullet's Snippet rune cap —
	// truncation is by rune count, never byte count, so a multi-byte
	// preview is never cut mid-codepoint.
	previewRuneCap = 500
)

// PlainTextPart extracts the first text/plain inline part from a peeked
// RFC822 message via mail.CreateReader/NextPart. Every part read goes
// through io.LimitReader bounded by maxPartBytes, and the loop stops
// after maxParts parts, so a maliciously crafted message cannot exhaust
// memory (T-03-07). A message with no text/plain part returns empty text
// rather than an error.
func PlainTextPart(raw []byte) (string, error) {
	return extractPart(raw, "text/plain")
}

// HTMLPart extracts the first text/html inline part from a peeked RFC822
// message, under the identical io.LimitReader/maxParts bounds
// PlainTextPart already established (T-03-12). A message with no
// text/html part (a plain-text-only email) returns empty text and a nil
// error — plugin.go's Fetch falls through to 03-01's existing
// text-only behaviour in that case.
func HTMLPart(raw []byte) (string, error) {
	return extractPart(raw, "text/html")
}

// extractPart walks raw's MIME structure via mail.CreateReader/NextPart
// once and returns the first inline part whose Content-Type equals
// wantContentType. Shared by PlainTextPart and HTMLPart so both
// extractions apply the identical part-count/byte-size bounds (T-03-12: a
// crafted message with an enormous single part or thousands of parts must
// not exhaust memory or spin unbounded).
//
// go-message's CreateReader/NextPart can both return a non-fatal
// "unknown charset/transfer-encoding" error alongside a still-usable
// reader/part (message.IsUnknownCharset / message.IsUnknownEncoding) —
// this function treats that class of error as recoverable (keep reading
// with whatever best-effort decode go-message produced) rather than
// failing extraction outright, since one malformed part must not make
// the whole message's body permanently unavailable.
func extractPart(raw []byte, wantContentType string) (string, error) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && !isBenignParseError(err) {
		return "", err
	}
	if mr == nil {
		return "", nil
	}
	defer mr.Close()

	for i := 0; i < maxParts; i++ {
		p, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil && !isBenignParseError(perr) {
			return "", perr
		}
		if p == nil {
			continue
		}

		h, ok := p.Header.(*mail.InlineHeader)
		if !ok {
			continue
		}
		ct, _, _ := h.ContentType()
		if ct != wantContentType {
			continue
		}

		b, _ := io.ReadAll(io.LimitReader(p.Body, maxPartBytes))
		return string(b), nil
	}

	return "", nil
}

// isBenignParseError reports whether err is the "unknown charset" or
// "unknown transfer encoding" class of error go-message returns
// alongside a still-usable reader/part — see the package doc comments on
// mail.CreateReader and mail.Reader.NextPart.
func isBenignParseError(err error) bool {
	return message.IsUnknownCharset(err) || message.IsUnknownEncoding(err)
}

// HasRenderableText reports whether s carries content a reader can
// actually see once whitespace is trimmed. fetchFull uses this to decide
// whether a message's extracted text/plain part IS the message's
// content: a present-but-blank part (a common multipart/alternative
// artifact) must not suppress the HTML fallback that follows it.
// Whitespace is defined by strings.TrimSpace (unicode.IsSpace), not ASCII
// spaces only, so a no-break-space-only part is correctly treated as
// blank.
func HasRenderableText(s string) bool {
	return strings.TrimSpace(s) != ""
}

// Snippet truncates s to at most previewRuneCap runes, by rune count
// never byte count, so a multi-byte body preview is never cut
// mid-codepoint. Mirrors plugins/silverbullet's Snippet helper.
func Snippet(s string) string {
	if utf8.RuneCountInString(s) <= previewRuneCap {
		return s
	}
	runes := []rune(s)
	return string(runes[:previewRuneCap])
}
