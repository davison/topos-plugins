# plugins/signal

Reads Signal Desktop's own local SQLCipher database (`~/.config/Signal/sql/db.sqlite`)
strictly read-only and turns matched conversations into conversation-day
digests (SRC-02). This is the repository's **first cgo plugin** — every
other plugin here (`paperless`, `silverbullet`, `proton`, `mock`) is pure
Go and builds `CGO_ENABLED=0`. This one links against your system's own
SQLCipher library, so it needs a C toolchain and the `sqlcipher` package
present at build time.

## Build prerequisite: install `sqlcipher` first

Before running `make signal` or `make test-signal`, install the system
`sqlcipher` package:

```bash
# Arch
sudo pacman -S sqlcipher

# Debian / Ubuntu
sudo apt-get install libsqlcipher-dev
```

`make signal` and `make test-signal` both build with `CGO_ENABLED=1` and
the `libsqlcipher` build tag, and both will fail to link if this package
isn't installed. No other plugin in this repository requires a C
toolchain — the kernel itself, and every other plugin, stay
`CGO_ENABLED=0`.

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

See `config.example.toml` for the fully-commented reference block. There
is no key, token, or secret to configure here at all — the SQLCipher
decryption key is resolved entirely at runtime from files already inside
`path` (Signal Desktop's own `config.json`), branching automatically
between the legacy plaintext-key shape and the modern Electron
`safeStorage`-wrapped shape depending on what your own install actually
uses. Never put a key or a path to one in this project's own config —
there is nothing to put there.

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

# Full end-to-end sync + serve smoke test against your real database,
# using a throwaway config that never touches your real
# ~/.config/topos/config.toml
SIGNAL_SMOKE_KEYWORD="your-real-conversation-or-group-name" ./scripts/signal-readonly-smoke.sh
```

Both are safe to run repeatedly — neither ever mutates, checkpoints, or
copies your Signal Desktop database.
