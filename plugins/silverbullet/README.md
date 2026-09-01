# SilverBullet

Reads pages from a SilverBullet space over its HTTP filesystem API and
matches on tags and page names, with exact deep links back to the page.

## Install Requirements

None beyond a reachable SilverBullet instance and an auth token.

## Configuration

```toml
[sources.silverbullet]
plugin = "topos-plugin-silverbullet"
display_name = "SilverBullet"
base_url = "${SILVERBULLET_URL}"
token = "${SB_AUTH_TOKEN}"
# ca_cert = "~/.config/topos/silverbullet-ca.pem"

[sources.silverbullet.agent]
read = false
handoff = false
```

Match vocabulary: `tags`, `pages`.

`ca_cert` is the optional, supported path for a self-signed or
private-CA instance: it pins the CA this source trusts, in addition to
the system trust store. `base_url` and `token` use the environment-
expansion form exactly as the reference block below does — never a literal
host or token. The fully-commented reference block is reproduced below, under "Configuration reference".

## Content search

The plugin implements the kernel's content search (`Search`, M2-R2):
membership (`tags` / `pages`) is applied first, exactly as `Match` applies
it, then every query and required term must occur in a member page's
title, tags, or body (the same frontmatter-stripped body `Match` reads). A
hit carries a short snippet around the first matching term. An empty
membership is refused rather than searched globally.

## Gotchas

- A self-signed instance fails to connect until `ca_cert` points at the
  CA certificate that signed it.
- Matching on `tags`/`pages` is exact and case-insensitive, same rule as
  every other source.

## Security & Privacy Notes

- **Read-only:** enforced by this repository's own AST scan —
  the read-only AST scan each REST plugin in this repository carries
  (its own `readonly_test.go`) fails the build on any non-GET HTTP
  reference.
- **Credentials:** `token` is the SilverBullet `SB_AUTH_TOKEN`, sent as a
  Bearer token on every request.
- **Egress:** restricted to the configured `base_url` host by
  `outbound_hosts_test.go`'s `TestAllowHost_PredicateTable` and its
  redirect-following tests. `ca_cert` is the supported way to trust a
  private CA — there is deliberately no option that disables certificate
  verification, and none should be added.

## Configuration reference

The fully-commented `[sources.<name>]` block for this plugin — moved verbatim from the kernel's former `config.example.toml` (davison/topos#24, M1-R2): every key with its purpose, default and validation rule. Copy it into your own `config.toml` under `[sources.<your-instance-name>]`; the kernel-level keys every source shares (`display_name`, `[sources.<name>.agent]`) are documented in the kernel's `config.example.toml`.

```toml
[sources.silverbullet]
# plugin: the plugin binary's filename, resolved inside [plugins] dir.
# Validation: none at load time; a missing file fails at startup, by path.
plugin = "topos-plugin-silverbullet"

# display_name: the kernel-level key every source shares — optional, a
# human-readable label; see the kernel's config.example.toml.
display_name = "SilverBullet"

# base_url: the SilverBullet instance's base URL. ${SILVERBULLET_URL} is
# expanded from the environment (the ${VAR} form — never a literal host).
# Unlike paperless-ngx, SilverBullet has no API-version negotiation, so
# there is no api_version key for this source.
# Validation: must be non-empty after expansion; an empty value (e.g. from
# an unset SILVERBULLET_URL) fails config load, naming the missing
# variable. A trailing slash is tolerated — the plugin normalizes it away
# before building requests.
base_url = "${SILVERBULLET_URL}"

# token: the SilverBullet SB_AUTH_TOKEN. Secrets NEVER live in this file as
# literal values (D-04) — only a ${VAR} reference belongs here. Sent as a
# Bearer token on every request regardless of whether this SilverBullet
# instance currently enforces it — SB_AUTH_TOKEN is still the documented,
# correct auth mechanism (some instances enforce it, some don't; this
# plugin behaves correctly either way).
# Validation: must be non-empty after expansion; same missing-variable
# error shape as base_url.
token = "${SB_AUTH_TOKEN}"

# sync_interval: OPTIONAL per-source override of the kernel's [sync] interval —
# lets one heavy or rate-limited source sync less often without slowing
# every other configured source down to match.
# Default: unset, meaning "use the global [sync] interval".
# Validation: same as [sync] interval — must parse as a positive Go
# duration; an unparseable or zero/negative value fails config load,
# naming this source's "sync_interval" key.
# sync_interval = "30m"

# ca_cert: OPTIONAL filesystem path to a PEM-encoded CA certificate this
# source's plugin client should trust in addition to the system trust
# store. Not part of the original plan for this phase — added live during
# Task 1 execution because a real SilverBullet instance may sit behind a
# self-signed TLS certificate the system trust store does not contain
# (confirmed against this deployment's actual instance). Leave unset (or
# omit the key) for an instance whose certificate already chains to a CA
# in your system's trust store.
# Validation: none enforced at config-load time; an unreadable or
# unparsable file falls back to the system trust store, and TLS
# verification then fails at sync/health time with a clear "unavailable"
# error rather than a load-time error.
# ca_cert = "~/.config/topos/silverbullet-ca.pem"

[sources.silverbullet.agent]
read = true
handoff = false
```
