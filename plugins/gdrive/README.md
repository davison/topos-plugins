# topos-plugin-gdrive

A [topos](https://github.com/davison/topos) source plugin that makes a
chosen Google Drive folder appear as a source inside topos.

This repository is built clean-room, against nothing but the four
published inputs vendored under `contract/` plus `PRD.md` — see
`CLAUDE.md` for the rules every session in this repository works under,
and `CONTRACT-GAPS.md` for the running log of every question those inputs
could not answer.

## Setup

For the start-to-finish path — Google Cloud project through a syncing
source, with the publishing-status/verification-status distinction and
the seven-day Testing-status expiry warning explained in full — see
[`docs/setup.md`](docs/setup.md).

The Build/Install/One-time authorization sections below are the terse
reference version of the same steps.

## Build

```
make build
```

Produces a single static binary (`CGO_ENABLED=0`, `-trimpath`) whose
SHA-256 reproduces across builds — the same hash the host's pin-and-consent
flow compares against. A plain `go build` does not guarantee either
property; always build through `make build`.

## Install

Copy the built binary into topos's external plugins directory — the
operating-system-appropriate data path the vendored contract documents
under "Trust tiers" (an XDG-style data directory on Linux, with
platform-appropriate equivalents elsewhere). topos discovers it there on
its next launch, marked untrusted until an operator explicitly consents
and pins it through topos's own source-picker flow.

## One-time authorization

Before adding this plugin as a source, export the two OAuth credential
environment variables it and its source configuration both reference:

```
export GDRIVE_CLIENT_ID=...
export GDRIVE_CLIENT_SECRET=...
```

Then run the standalone authorization command once, in a terminal:

```
./topos-plugin-gdrive auth
```

This opens your browser, runs an OAuth loopback redirect against Google,
and stores the resulting refresh token in a plugin-owned file under your
XDG data directory. See `PRD.md`'s "Locked Decisions" section for why
this is a standalone command rather than anything topos itself drives.

## Contract gaps

Every question the four published inputs under `contract/` could not
answer is recorded in `CONTRACT-GAPS.md`, along with the resolution this
repository actually took. If you're extending this plugin and hit a
question the contract doesn't answer, append to that file before writing
the code that resolves it.

## Deliverables

Two things go back to the `topos` project that owns the published contract
this plugin was built against:

1. **The built binary.**
2. **`CONTRACT-GAPS.md`** — the gap log, delivered in whatever state it is
   in. It is append-only and is handed back as-is, including every entry
   that makes the published contract look incomplete — especially those.
