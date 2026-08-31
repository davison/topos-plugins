# plugins/signal

Reads Signal Desktop's own local SQLCipher database (`~/.config/Signal/sql/db.sqlite`)
strictly read-only and turns matched conversations into conversation-day
digests (SRC-02). This is the repository's **first cgo plugin** — every
other plugin here (`paperless`, `silverbullet`, `proton`, `mock`) is pure
Go and builds `CGO_ENABLED=0`. This one links against your system's own
SQLCipher library, so it needs a C toolchain and the `sqlcipher` package
present at build time.

## Build prerequisite: install `sqlcipher` first

Before running `make build-signal` or `make test-signal` (both from
this repository's root), install the system `sqlcipher` package:

```bash
# Arch
sudo pacman -S sqlcipher

# Debian / Ubuntu
sudo apt-get install libsqlcipher-dev
```

`make build-signal` and `make test-signal` both build with
`CGO_ENABLED=1` and the `libsqlcipher` build tag, and both will fail to
link if this package isn't installed. No other plugin in this
repository requires a C toolchain — the kernel, and every other plugin,
stay `CGO_ENABLED=0`. Always use the tag: without it the suite fails on
the SQLite version floor instead of testing the real driver.

## Installing beside an installed kernel

```bash
make install-signal      # build + place into the external plugin directory
make uninstall-signal    # remove exactly that one file
```

`make install-signal` builds through `build-signal` and places the
binary atomically into the installed instance's **external** plugin
directory — the kernel's default (`$XDG_DATA_HOME/topos/plugins-external`,
falling back to `~/.local/share/topos/plugins-external`), or the
directory your config's `[plugins] external_dir` names via
`TOPOS_EXTERNAL_PLUGINS_DIR=<dir>`. It never touches the trusted
`lib/topos/plugins` directory (and refuses it by name): a locally built
binary carries no signed provenance, so the kernel's launch gate would
refuse it there. After placing, restart the kernel and add the Signal
source once through the app's untrusted-add consent flow — it runs
pinned and badged untrusted, and rebuilt bytes are re-accepted through
the chip's re-pin flow.

## SQLCipher version floor: refuses to run below SQLite 3.51.3

SQLite **3.51.3** (2026-03-13) fixed a critical WAL-reset
database-corruption bug — exactly the failure class a read-only reader of
a live, actively-written WAL database (Signal Desktop sets
`journal_mode=WAL`) is exposed to. This plugin reads `sqlite_version()`
immediately after opening the database and **refuses to run** if the
linked SQLite core is below that floor, naming the version it found. On
Arch, the `sqlcipher` package (`4.14.0-1` as of this writing) already
carries a SQLite 3.51.3+ baseline. If your distro's packaged `sqlcipher`
is older, this plugin will fail loudly at startup rather than silently
reintroducing that corruption risk — upgrade your system package, don't
work around the check.

## Configuration: a local-path source, no credentials

Unlike every other source in this project, Signal has no `base_url` or
`token` — it reads a local file, not a network endpoint. The only
required key is `path`, Signal Desktop's own config directory:

```toml
[sources.signal]
plugin = "topos-plugin-signal"
path = "~/.config/Signal"   # Signal Desktop's own config directory — "~" is expanded by this plugin

[sources.signal.agent]
read = false
handoff = false
```

The fully-commented reference block is reproduced below, under "Configuration reference". There
is no key, token, or secret to configure here at all — the SQLCipher
decryption key is resolved entirely at runtime from files already inside
`path` (Signal Desktop's own `config.json`), branching automatically
between the legacy plaintext-key shape and the modern Electron
`safeStorage`-wrapped shape depending on what your own install actually
uses. Never put a key or a path to one in this project's own config —
there is nothing to put there.

Match vocabulary: `conversations` — the field an explicit
`[webspaces.<name>.match.<instance>]` block for this plugin names. The
fully-commented reference block is reproduced below, under
"Configuration reference".

## Read-only, by construction and by test

This plugin never writes to Signal Desktop's database. It opens
`db.sqlite` with a `mode=ro` DSN, never `INSERT`s, `UPDATE`s, `DELETE`s,
`VACUUM`s, or checkpoints the WAL — and never copies the database file
anywhere. This is mechanically enforced, not just documented:
`readonly_test.go` walks this package's own AST and fails the build on
any write-shaped SQL reference (including one hidden inside a string
literal), and `byte_identical_test.go` proves a full Match+Fetch cycle
leaves a fixture (and, opt-in, your real) database byte-identical.

## Running the opt-in live tests

Two test suites in this package are opt-in and run against your **real**
`~/.config/Signal/sql/db.sqlite` rather than a fixture — they never write
to it, and both prove exactly that:

```bash
# Byte-identical proof against your real database (never copies it)
WEBSPACES_SIGNAL_LIVE_IT=1 go test -tags libsqlcipher -run TestLiveDatabaseByteIdentical -v ./...
```

Safe to run repeatedly — it never mutates, checkpoints, or copies your
Signal Desktop database. (The kernel-side end-to-end smoke script this
section once named, `signal-readonly-smoke.sh`, retired with the plugin
split — the end-to-end story is the kernel's own dev loop and the
operator's live instance now.)

## Gotchas

- A distro `sqlcipher` package older than the 3.51.3 SQLite floor fails
  loudly at startup, naming the version it found. The fix is to upgrade
  the system package, not to work around the check.
- This plugin binary is not published as a prebuilt artifact; `make
  build-signal` builds it and `make install-signal` places it (see
  "Installing beside an installed kernel", above).

## Security & Privacy Notes

- **Read-only:** this plugin never writes to Signal Desktop's database. It
  opens `db.sqlite` with a `mode=ro` DSN and never `INSERT`s, `UPDATE`s,
  `DELETE`s, `VACUUM`s, or checkpoints the WAL. `readonly_test.go` walks
  this package's own AST and fails the build on any write-shaped SQL
  reference; `byte_identical_test.go` proves a full Match+Fetch cycle
  leaves the database byte-identical.
- **Credentials:** the SQLCipher decryption key is never stored in this
  project's config — it is resolved entirely at runtime from Signal
  Desktop's own `config.json` under `path`, branching automatically
  between the legacy plaintext-key shape and the modern Electron
  `safeStorage`-wrapped shape. There is nothing secret to put in topos
  config.
- **Egress:** none — this plugin talks only to a local file on disk, never
  the network.

## Configuration reference

The fully-commented `[sources.<name>]` block for this plugin — moved verbatim from the kernel's former `config.example.toml` (davison/topos#24, M1-R2): every key with its purpose, default and validation rule. Copy it into your own `config.toml` under `[sources.<your-instance-name>]`; the kernel-level keys every source shares (`display_name`, `[sources.<name>.agent]`) are documented in the kernel's `config.example.toml`.

```toml
[sources.signal]
# plugin: the plugin binary's filename, resolved inside [plugins] dir.
# Validation: none at load time; a missing file fails at startup, by path.
plugin = "topos-plugin-signal"

# display_name: the kernel-level key every source shares — optional, a
# human-readable label; see the kernel's config.example.toml.
display_name = "Signal"

# path: Signal Desktop's own config directory — the source of both
# config.json (SQLCipher key resolution) and sql/db.sqlite (the message
# database itself, opened strictly read-only). A leading "~" is expanded
# by the plugin subprocess itself, not the kernel (the Path field's doc
# comment in the kernel's config types: https://github.com/davison/topos/blob/main/kernel/config/types.go).
#
# This source needs NO base_url, NO token, and NO environment variable at
# all — unlike a REST-backed source, the SQLCipher decryption
# key is resolved entirely at runtime from files inside this directory
# (Signal Desktop's own config.json), never stored in this project's own
# config or environment (SRC-02).
# Validation: the kernel's config validation (https://github.com/davison/topos/blob/main/kernel/config/config.go) accepts a source declaring only
# path, in place of base_url+token; a source declaring none of the three
# fails config load naming both accepted shapes.
# Default Signal Desktop location on Linux: "~/.config/Signal".
path = "~/.config/Signal"

[sources.signal.agent]
read = false
handoff = false
```
