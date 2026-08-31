# paperless-ngx

Reads documents from a paperless-ngx instance over its REST API and
matches them on tags, with exact deep links back to the document.

## Install Requirements

None beyond a reachable paperless-ngx instance and an API token.

## Configuration

```toml
[sources.paperless]
plugin = "topos-plugin-paperless"
display_name = "paperless-ngx"
base_url = "${PAPERLESS_URL}"
token = "${PAPERLESS_TOKEN}"
api_version = "10"

[sources.paperless.agent]
read = false
handoff = false
```

Match vocabulary: `tags`.

`api_version` is the REST API version this plugin negotiates via the
Accept header — match it to your own paperless-ngx instance's supported
API version range. `base_url` and `token` use the environment-expansion
form exactly as the reference block below does — never a literal host or
token. The fully-commented reference block is reproduced below, under "Configuration reference".

## Gotchas

- An incompatible `api_version` is not validated at config load — it
  surfaces as an HTTP error from paperless-ngx itself at sync time.
- Matching on `tags` is exact and case-insensitive against tag names, same
  rule as every other source; a near-miss tag spelling silently matches
  nothing.

## Security & Privacy Notes

- **Read-only:** enforced by this repository's own AST scan —
  `readonly_test.go`'s `TestPluginsIssueOnlyGetRequests` walks every `.go`
  file under `plugins/` and fails the build on any non-GET HTTP
  reference.
- **Credentials:** `token` is a paperless-ngx API token, scoped to
  whatever access that instance's own token grants it.
- **Egress:** restricted to the configured `base_url` host by
  `outbound_hosts_test.go`'s `TestAllowHost_PredicateTable` and its
  redirect-following tests.

## Configuration reference

The fully-commented `[sources.<name>]` block for this plugin — moved verbatim from the kernel's former `config.example.toml` (davison/topos#24, M1-R2): every key with its purpose, default and validation rule. Copy it into your own `config.toml` under `[sources.<your-instance-name>]`; the kernel-level keys every source shares (`display_name`, `[sources.<name>.agent]`) are documented in the kernel's `config.example.toml`.

```toml
[sources.paperless]
# plugin: the plugin binary's filename, resolved inside [plugins] dir.
# Validation: none at load time; a missing file fails at startup, by path.
plugin = "topos-plugin-paperless"

# display_name: this instance's operator-authored label, shown by the UI
# and published on every HTTP response that names a source (D-09).
# Optional: when omitted, the instance's display name resolves to the
# instance id itself (the "paperless" map key above). Purely cosmetic
# (D-08) — editing this value never changes which instance an item, sync
# run, or agent grant belongs to; only renaming the [sources.<id>] map
# key itself does that.
# Validation: must be unique across every configured source instance
# (case-insensitive); two instances resolving to the same display name
# (whether by explicit display_name or by both leaving it unset with
# identical map keys, which TOML itself already forbids) fails config
# load, naming both offending instances.
display_name = "paperless-ngx"

# base_url: the paperless-ngx instance's base URL. ${PAPERLESS_URL} is
# expanded from the environment — never hardcode a real URL/host here if
# you intend to share this file; use the ${VAR} form even for a
# non-secret value like this one, for consistency with token, below.
# Validation: must be non-empty after expansion; an empty value (e.g. from
# an unset PAPERLESS_URL) fails config load, naming the missing variable.
base_url = "${PAPERLESS_URL}"

# token: the paperless-ngx API token. Secrets NEVER live in this file as
# literal values (D-04) — only a ${VAR} reference belongs here.
# Validation: must be non-empty after expansion; same missing-variable
# error shape as base_url.
token = "${PAPERLESS_TOKEN}"

# api_version: the paperless-ngx REST API version this plugin negotiates
# via the Accept header (e.g. "application/json; version=10"). Match your
# paperless-ngx instance's supported API version range.
# Validation: none enforced; an incompatible version will surface as an
# HTTP error from paperless-ngx itself at sync time.
api_version = "10"

# [sources.paperless.extras] — OPTIONAL per-instance table of
# provider-specific settings this plugin's own Describe RPC may declare
# expecting, beyond the built-in base_url/token/api_version fields above
# (D-12/D-13, Phase 11 PLUG-09). Every value is a string; a ${VAR}
# reference inside one expands from the environment exactly like
# base_url/token do. Reaches the plugin subprocess nested inside
# WEBSPACES_SOURCE_CONFIG as an "extras" object — omitted entirely (no
# key at all) when this table is absent, so a plugin's own JSON decode
# sees an unambiguous "no extras configured", never an empty object.
# paperless doesn't itself declare or read any extras key today; this
# block is left commented as the worked shape for a plugin that does —
# see docs/plugin-contract.md's "Configuration" and "Describe" sections
# for the full contract, including the optional Describe-side field
# declaration (key/label/required/secret/placeholder) a plugin may use to
# drive the kernel's own add-source form.
# Validation: an empty key, or one outside [A-Za-z_][A-Za-z0-9_.-]*, fails
# config load, naming the offending source instance and key.
# [sources.paperless.extras]
# region = "${PAPERLESS_REGION}"

[sources.paperless.agent]
read = false
handoff = false
```
