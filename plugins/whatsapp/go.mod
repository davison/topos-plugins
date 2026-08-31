module github.com/davison/topos-plugins/plugins/whatsapp

go 1.25.0

// go.mod require (Task 1 checkpoint, 08-01-PLAN.md, 2026-08-10): unlike
// every other third-party module this repo pins, go.mau.fi/whatsmeow has
// NEVER published a tagged release — every consumer, including this one,
// pins an exact commit pseudo-version with no changelog to review between
// bumps. 08-RESEARCH.md's Package Legitimacy Audit flagged this module SUS
// on that basis alone (not on any code-quality or maintenance-activity
// signal — the project is actively published and imported by 300+ Go
// modules, including the Mautrix WhatsApp bridge running in production for
// thousands of users) and required this exact checkpoint acknowledgment
// before pinning. Task 1's checkpoint returned "approved" for the pinned
// pseudo-version below, re-verified live against `go list -m -json
// go.mau.fi/whatsmeow@latest` on 2026-08-10 (identical to 08-RESEARCH.md's
// own snapshot) with the pinned commit's own go.mod dependency tree
// confirmed 100% cgo-free (closes 08-RESEARCH.md Assumption A4 for this
// exact commit). No `replace` directive is needed here, unlike
// plugins/signal/go.mod's SQLCipher fork situation — whatsmeow's own
// upstream has no missing-feature gap this plugin needs to route around.
// Any future bump of this line is a DELIBERATE, REVIEWED action — never a
// side effect of `go get -u` or `go mod tidy` — and should be preceded by
// re-running the same audit this comment records.
require go.mau.fi/whatsmeow v0.0.0-20260806224404-e277b766ab33

// go.mod require (08-RESEARCH.md Package Legitimacy Audit, UNVERIFIED row;
// 08-03-PLAN.md Task 1 audit, 2026-08-10): the QR-to-PNG encoder D-01's
// in-app link mode needs (kernel/httpapi/whatsapplink.go relays
// png_data_uri to the browser; plugins/whatsapp/link.go's runLinkJSON
// renders it). Verified live against the Go module proxy this session,
// per the same manual protocol 08-RESEARCH.md's own header describes
// (gsd-tools query package-legitimacy check only covers npm/pypi/crates):
//   $ go list -m -versions rsc.io/qr
//   rsc.io/qr v0.1.0 v0.2.0
//   $ go list -m -json rsc.io/qr@latest
//   {"Path":"rsc.io/qr","Version":"v0.2.0","Time":"2018-06-05T10:54:35Z", ...}
// rsc.io/qr passed on first check — no fallback to skip2/go-qrcode or
// yeqown/go-qrcode was needed. Selected over both alternatives because:
// (1) real registry existence and a real (if short) tagged-version
// history, not a hallucinated/typosquatted name; (2) zero transitive
// dependencies (its own go.mod declares none) — smaller supply-chain
// surface than either alternative; (3) `(*qr.Code).PNG() []byte` returns
// PNG bytes directly with no image/png round trip, matching this plugin's
// exact need; (4) authored by rsc.io — Russ Cox's own personal Go-module
// namespace (Russ Cox: Go project co-founder/former tech lead), a
// maintainer-identity signal at least as strong as any of Phase 1-7's own
// already-pinned dependencies. The 2018-06-05 last-release date reflects
// QR encoding being a stable, closed algorithm (ISO/IEC 18004) with no
// ongoing protocol churn to track — not abandonment risk, unlike
// go.mau.fi/whatsmeow's own untagged-rolling-main situation above, which
// genuinely does carry that risk. Confined to this plugin module — the
// root kernel go.mod gains no QR dependency.
require rsc.io/qr v0.2.0

require (
	github.com/mdp/qrterminal/v3 v3.2.1
	modernc.org/sqlite v1.54.0
)
