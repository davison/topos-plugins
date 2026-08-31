# WhatsApp

Runs as a WhatsApp linked device with its own persistent message store,
matching on group and contact names.

## Install Requirements

None at build time — the binary is pure Go — but one mandatory,
out-of-band linking step before first use (see Configuration, below).

## Configuration

```toml
[sources.whatsapp]
plugin = "topos-plugin-whatsapp"
display_name = "WhatsApp"
path = "~/.local/share/topos/whatsapp"

[sources.whatsapp.agent]
read = false
handoff = false
```

Match vocabulary: `groups`, `contacts`.

This source needs no `base_url`, no `token`, and no environment variable
at all — the linked-device session keys live only inside the session
store under `path`.

Linking is a one-time, out-of-band step run against the plugin binary
directly, never through the running kernel:

```bash
bin/topos-plugin-whatsapp -link -path ~/.local/share/topos/whatsapp
```

Scan the rendered QR code with your phone, then restart the kernel (or
the kernel checkout's `make dev` loop); the linked session survives
restarts with no second scan.
The fully-commented reference block is reproduced below, under "Configuration reference".

## Gotchas

- `path` holds two plugin-owned databases — the linked-device session
  store and the captured-message store — and must not collide with any
  other configured source's path, Signal's included.
- The linking step is run against the plugin binary directly and will not
  work through the running kernel.
- **Standing operational risk:** the linked device can be de-linked or
  banned by the platform at any time. The plugin degrades to named health
  states, already-captured rows survive, and there is no recovery beyond
  re-linking.

## Security & Privacy Notes

- **Read-only:** no message is ever sent from this plugin; enforced by
  `readonly_test.go`'s `TestReadOnly_NoSendCapableClientSelector`.
- **Credentials:** no credential is stored in topos config — the session
  store under `path` is plugin-owned and never touches another source's
  files. Path isolation is load-bearing, not a suggestion.
- **Egress:** restricted to WhatsApp's own linked-device endpoints by
  `outbound_hosts_test.go`'s
  `TestOutboundHosts_NoSelfConstructedHTTPClientOrUnlistedHostLiteral`.

## Configuration reference

The fully-commented `[sources.<name>]` block for this plugin — moved verbatim from the kernel's former `config.example.toml` (davison/topos#24, M1-R2): every key with its purpose, default and validation rule. Copy it into your own `config.toml` under `[sources.<your-instance-name>]`; the kernel-level keys every source shares (`display_name`, `[sources.<name>.agent]`) are documented in the kernel's `config.example.toml`.

```toml
[sources.whatsapp]
# plugin: the plugin binary's filename, resolved inside [plugins] dir.
# Validation: none at load time; a missing file fails at startup, by path.
plugin = "topos-plugin-whatsapp"

# display_name: the kernel-level key every source shares — optional, a
# human-readable label; see the kernel's config.example.toml.
display_name = "WhatsApp"

# path: this plugin's OWN data directory — home to TWO plugin-owned
# databases: whatsmeow's own linked-device session store (whatsmeow.db)
# and this plugin's own captured-message store (messages.db), neither of
# which is Signal Desktop's or any other source's own database. This path
# MUST NOT collide with [sources.signal]'s path or any other configured
# source's path — each source's own local state must stay isolated. A
# leading "~" is expanded by the plugin subprocess itself (main.go), not
# the kernel, mirroring [sources.signal]'s identical convention.
#
# Unlike Signal, this source needs NO base_url, NO token, and NO
# environment variable — the linked-device session keys live only inside
# whatsmeow's own session store under this path. Linking is a one-time,
# out-of-band step: run the plugin binary directly with its own -link
# flag (never through the running kernel):
#   bin/topos-plugin-whatsapp -link -path ~/.local/share/topos/whatsapp
# and scan the rendered ASCII QR code with your phone. Re-run the kernel
# (or the kernel checkout's `make dev` loop) after linking succeeds; the
# linked session survives a
# kernel restart with no second QR scan.
# Validation: kernel/config.Validate accepts a source declaring only
# path, in place of base_url+token — identical to [sources.signal].
path = "~/.local/share/topos/whatsapp"

[sources.whatsapp.agent]
read = false
handoff = false
```
