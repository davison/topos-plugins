# Proton Mail

Reads mail over IMAP from a running Proton Mail Bridge, matching on
folders, and never marks mail read.

## Install Requirements

A running Proton Mail Bridge instance — this plugin talks to Bridge, not
to Proton directly.

## Configuration

```toml
[sources.proton]
plugin = "topos-plugin-proton"
display_name = "Proton Mail"
base_url = "imaps://${PROTON_BRIDGE_ADDR}"
username = "${PROTON_BRIDGE_USER}"
token = "${PROTON_BRIDGE_PASS}"
ca_cert = "~/.config/topos/proton-bridge-cert.pem"
webmail_base_url = "${PROTON_WEBMAIL_BASE}"

[sources.proton.agent]
read = false
handoff = false
```

Match vocabulary: `folders`.

Two hard rules: `base_url`'s scheme (`imap`/`imaps`) must match what
Bridge itself reports for this account's own IMAP connection security,
and `ca_cert` is required, not optional, because Bridge presents a
self-signed certificate. `webmail_base_url` is the base the deep links
back into Proton's web client are built from. `username` and `token` use
the environment-expansion form exactly as the reference block below does —
never a literal host, username, or token. The fully-commented reference block is reproduced below, under "Configuration reference".

## Content search

The plugin implements the kernel's content search (`Search`, M2-R2):
within the member mailboxes (the `folders` membership, exactly as `Match`
applies it) it issues IMAP `SEARCH TEXT` for every query and required
term — the Bridge searches headers and bodies server-side — and fetches
only the matching envelopes, read-only (`EXAMINE`), never a body. Hits
therefore carry no snippet; `matched_in` is the subject when it alone
holds every term, otherwise the body. An empty membership is refused
rather than searched globally.

## Gotchas

- Bridge binds loopback only, so reaching it from another machine needs a
  forwarder running on the Bridge host — this plugin cannot work around
  that.
- A scheme mismatch between `base_url` and Bridge's own reported setting
  fails to connect.
- `token` is Bridge's own generated password (Bridge -> Settings), not
  your real Proton account password — pasting the real account password
  will not work.

## Security & Privacy Notes

- **Read-only:** mail is fetched without ever marking it read; enforced by
  `readonly_test.go`'s `TestPluginIssuesNoIMAPMutatingCommands`.
- **Credentials:** the Bridge password (`token`) is scoped to Bridge alone
  and cannot sign in to the real Proton account, even if leaked.
- **Egress:** restricted to the configured Bridge host by
  `outbound_hosts_test.go`'s `TestAllowHost_PredicateTable`.

## Configuration reference

The fully-commented `[sources.<name>]` block for this plugin — moved verbatim from the kernel's former `config.example.toml` (davison/topos#24, M1-R2): every key with its purpose, default and validation rule. Copy it into your own `config.toml` under `[sources.<your-instance-name>]`; the kernel-level keys every source shares (`display_name`, `[sources.<name>.agent]`) are documented in the kernel's `config.example.toml`.

```toml
[sources.proton]
# plugin: the plugin binary's filename, resolved inside [plugins] dir.
# Validation: none at load time; a missing file fails at startup, by path.
plugin = "topos-plugin-proton"

# display_name: the kernel-level key every source shares — optional, a
# human-readable label; see the kernel's config.example.toml.
display_name = "Proton Mail"

# base_url: "imaps://host:port" for implicit TLS, or "imap://host:port"
# for a plaintext connect immediately followed by a mandatory STARTTLS —
# the scheme you use MUST match what Proton Mail Bridge -> Settings
# reports as this account's IMAP connection security. ${PROTON_BRIDGE_ADDR}
# is the LAN address:port of the socat/stunnel forwarder set up on the
# home server running Bridge (Bridge itself only ever binds
# 127.0.0.1, so this forwarder step is not optional).
# Validation: must be non-empty after expansion, and the scheme must be
# exactly "imap" or "imaps"; anything else fails plugin startup, naming
# the invalid scheme.
base_url = "imaps://${PROTON_BRIDGE_ADDR}"

# username: the Bridge-generated Bridge username for this account (Bridge
# -> Settings) — NOT your real Proton account email address, and never a
# literal secret value here (${VAR} only, D-04).
# Validation: must be non-empty after expansion; same missing-variable
# error shape as base_url.
username = "${PROTON_BRIDGE_USER}"

# token: the Bridge-generated Bridge password (Bridge -> Settings) —
# reused as the IMAP LOGIN password. This is scoped to Bridge alone and
# cannot be used to sign in to the Proton account itself, even if leaked.
# Validation: must be non-empty after expansion; same missing-variable
# error shape as base_url.
token = "${PROTON_BRIDGE_PASS}"

# ca_cert: filesystem path to Bridge's exported TLS certificate (Bridge ->
# Settings -> Advanced -> "Export TLS certificates"). Required in
# practice — Bridge's certificate is self-signed, so the system trust
# store will not verify it. A leading "~" is expanded to your home
# directory.
# Validation: none enforced at config-load time; an unreadable or
# unparsable file falls back to the system trust store, and TLS
# verification then fails at sync/health time with a clear "unavailable"
# error rather than a load-time error.
ca_cert = "~/.config/topos/proton-bridge-cert.pem"

# webmail_base_url: this account's Proton webmail root, INCLUDING the
# account index (e.g. "https://mail.proton.me/u/0") — read from the
# address bar with any Proton view open (not specifically a label view;
# the value is no longer used to build one). Used only to build a
# clickable ANCHORED link into that account's All Mail view, narrowed by
# a search for the message's subject, sender and date — narrower than a
# subject-only search, so a generic subject ("Invoice") lands the reader
# in a short filtered list rather than every message that ever shared it,
# but still a filtered LIST containing the message, never the message
# itself. Proton addresses custom labels and folders by an internal id
# rather than by name, so a link built from a label's NAME is not
# addressable and lands on the inbox — All Mail is the name-addressable
# system view this plugin targets instead. The plugin never fetches this
# URL itself.
# Validation: must be non-empty after expansion; same missing-variable
# error shape as base_url.
webmail_base_url = "${PROTON_WEBMAIL_BASE}"

[sources.proton.agent]
read = false
handoff = false

# --- Two named instances of one plugin type (D-08/D-10) ---------------
#
# The kernel can launch the SAME plugin binary more than once — once per
# [sources.<id>] entry. Two instances of one plugin type always have
# distinct display names, distinct item id namespaces, distinct sync
# history, and independent agent grants; they never merge into one row
# anywhere in the kernel's index or HTTP API. This example illustrates
# the shape by splitting a single Proton account (above) into two: a
# personal inbox and a work inbox, both running topos-plugin-proton.
# Uncomment both blocks below (and give each its own real connection
# details/env vars — Bridge supports multiple linked accounts
# simultaneously) to run them alongside, or instead of, [sources.proton]
# above.
#
# [sources.home-email]
# plugin = "topos-plugin-proton"
# display_name = "Home email"
# base_url = "imaps://${PROTON_HOME_BRIDGE_ADDR}"
# username = "${PROTON_HOME_BRIDGE_USER}"
# token = "${PROTON_HOME_BRIDGE_PASS}"
# ca_cert = "~/.config/topos/proton-bridge-cert.pem"
# webmail_base_url = "${PROTON_HOME_WEBMAIL_BASE}"
#
# [sources.home-email.agent]
# read = true
# handoff = false
#
# [sources.work-email]
# plugin = "topos-plugin-proton"
# display_name = "Work email"
# base_url = "imaps://${PROTON_WORK_BRIDGE_ADDR}"
# username = "${PROTON_WORK_BRIDGE_USER}"
# token = "${PROTON_WORK_BRIDGE_PASS}"
# ca_cert = "~/.config/topos/proton-bridge-cert.pem"
# webmail_base_url = "${PROTON_WORK_WEBMAIL_BASE}"
#
# [sources.work-email.agent]
# read = false
# handoff = false
#
# Grants are per instance, never per plugin kind: granting home-email
# read access above never admits work-email's items through /agent/v1,
# even though both instances share one source_type ("proton") and one
# plugin binary. The kernel's config.example.toml's "[webspaces.<name>]"
# section shows how each instance gets its own independent match
# configuration too.
```
