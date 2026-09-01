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
	github.com/davison/topos/sdk v0.0.0-20260901181323-b3e18d5b6a06
	github.com/hashicorp/go-plugin v1.8.0
	github.com/mdp/qrterminal/v3 v3.2.1
	google.golang.org/grpc v1.83.0
	modernc.org/sqlite v1.54.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/beeper/argo-go v1.1.2 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/elliotchance/orderedmap/v3 v3.1.0 // indirect
	github.com/fatih/color v1.13.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-sqlite3 v1.14.49 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/petermattis/goid v0.0.0-20260713124913-97594f28f5ca // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	github.com/vektah/gqlparser/v2 v2.5.27 // indirect
	go.mau.fi/libsignal v0.2.2 // indirect
	go.mau.fi/util v0.9.12-0.20260717235539-f9ffa7eca58d // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/exp v0.0.0-20260709172345-9ea1abe57597 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260807164820-c8921c73eeea // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace github.com/davison/topos-plugins => ../..
