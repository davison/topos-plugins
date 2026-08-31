# topos-plugins

This repository is where [`topos`](https://github.com/davison/topos)'s
source plugins live — one Go module per plugin under `plugins/`, tied
together by the repository's `go.work`, built, signed and published by
this repository's own tag-triggered release workflow, and installed onto
a machine beside an installed topos kernel by this repository's own
`make install` ([davison/topos#6](https://github.com/davison/topos/issues/6)).

| Plugin | Moved from | Notes |
|---|---|---|
| `plugins/filesystem` | [davison/topos@d9a37b1](https://github.com/davison/topos/tree/d9a37b1/plugins/filesystem) | clean copy, module path renamed; history stays in the kernel repo |
| `plugins/paperless` | [davison/topos@d9a37b1](https://github.com/davison/topos/tree/d9a37b1/plugins/paperless) | clean copy, module path renamed |
| `plugins/proton` | [davison/topos@d9a37b1](https://github.com/davison/topos/tree/d9a37b1/plugins/proton) | clean copy, module path renamed |
| `plugins/silverbullet` | [davison/topos@d9a37b1](https://github.com/davison/topos/tree/d9a37b1/plugins/silverbullet) | clean copy, module path renamed |
| `plugins/whatsapp` | [davison/topos@d9a37b1](https://github.com/davison/topos/tree/d9a37b1/plugins/whatsapp) | clean copy, module path renamed |
| `plugins/signal` | [davison/topos@d9a37b1](https://github.com/davison/topos/tree/d9a37b1/plugins/signal) | clean copy, module path renamed; cgo — built locally (`make build-signal`), never shipped prebuilt |
| `plugins/gdrive` | [davison/topos-plugin-gdrive@563a21b](https://github.com/davison/topos-plugin-gdrive/tree/563a21b) | folded in, superseding that repo: Go module only (sources, testdata, README); the clean-room scaffolding stays behind |

`cmd/topos-plugin-demo/` is the trivial plugin that first proved the
signing pipeline. It is no longer shipped in releases; it stays as the
fixture binary the install smoke test signs and installs, and as the
smallest complete plugin in the tree.

## Installing the fleet

An installed topos instance is two independent installs into one
`PREFIX`: the kernel, placed by the kernel repository's `make install`
at `$PREFIX/bin/topos`, and the plugins, placed by **this** repository's
`make install` at `$PREFIX/lib/topos/plugins/`. Either can be installed,
updated or removed without the other — the kernel finds whatever fleet
is in that directory with its stock `[plugins] dir = "plugins"` config
value, and trusts each binary through the signed release manifest
placed beside it.

```sh
make install                  # latest published stable release
make install VERSION=0.2.0    # a specific release (leading v optional)
make install PREFIX=$HOME/.local   # a no-sudo user-local install
```

`make install` needs `curl` and `sha256sum` only — no Go toolchain, no
credentials (public releases download anonymously) — plus a checkout
of this repository for the `Makefile` and `scripts/install.sh`. With no
`VERSION`, the latest published stable release is resolved by following
the releases/`latest` redirect, and the landing URL is validated before
anything downloads: the host must be exactly `https://github.com`, the
path must be this repository's own release-tag path, and the tag must
be bare three-part `v<major>.<minor>.<patch>` semver — a prerelease tag
can never be auto-selected.

`PREFIX` defaults to `/usr/local` and must be the same prefix the
kernel was installed into. A `PREFIX` your user cannot write fails
loudly, naming the directory and the `sudo make install` re-run form;
the installer never runs `sudo` itself.

### What gets written

Exactly one directory, and nothing else:

| Path | Contents |
|------|----------|
| `$PREFIX/lib/topos/plugins/topos-plugin-<name>` | each published plugin binary (mode 0755) |
| `$PREFIX/lib/topos/plugins/topos-plugins-<tag>.provenance.json` + `.sig` | the release's signed manifest pair (mode 0644) — the evidence the kernel trusts the binaries by |

`$PREFIX/bin` is never touched. The operator's config, index and plugin
stores live in the home/XDG locations the installer never names.

### What is verified, and in what order

The sequence is preflight → stage → verify → place → converge, and
nothing is placed until everything has verified — an abort before
placement leaves `$PREFIX` byte-unchanged (see "What a failed install
leaves behind" below for the two placement passes).

1. **Checksums.** Every asset the release's `checksums.txt` names is
   downloaded into a staging directory and verified with
   `sha256sum -c`. The asset list is derived from `checksums.txt`
   itself, against an allowlist of name shapes (a plugin binary, the
   manifest pair, the verifier) — a name with a path separator or a
   leading dot is rejected by name.
2. **Provenance — mandatory.** The release must publish exactly one
   signed manifest pair, and every staged plugin binary is verified
   against it with `topos-provenance verify` — the exact verifier the
   kernel's own launch gate calls, never a re-implementation. A
   signature that does not verify, a key the verifier does not accept,
   a manifest built for another platform, or a binary the manifest
   does not name (or names with another digest) aborts the install
   naming the binary. A release with no manifest at all is refused
   before any binary is fetched: an unsigned fleet would launch
   untrusted or not at all, source by source, with nothing naming why.

The verifier is resolved in this order, and the first found is used:

1. `$PREFIX/bin/topos-provenance` — an installed kernel's own copy,
   from the kernel repository's release, untouched by whatever
   produced the payload being checked;
2. `topos-provenance` on `PATH` — operator-controlled; `make verifier`
   builds one into `bin/` at the pinned kernel revision;
3. the copy this release ships alongside the fleet — **last**, only
   when nothing more trustworthy exists.

A payload that ships evidence must have that evidence checked, so a
release with a manifest and no resolvable verifier is a loud abort,
never a silent skip. The shipped copy is used from the staging
directory and never placed anywhere: placing it would let one release
payload seed the first tier for every later install.

**Bootstrap-trust caveat.** Provenance verification exists to catch an
attacker who can tamper with release artifacts *and* regenerate
`checksums.txt` to match — someone who controls this repository's
release pipeline but not its ed25519 signing key. Under exactly that
threat model, such an attacker could also publish a `topos-provenance`
that unconditionally reports success. The resolution order narrows that
window as far as ordering can: on a machine with an installed kernel
that ships the verifier, or with one built by `make verifier` on
`PATH`, the shipped copy is never consulted. On a machine with neither,
the shipped copy is the only option, and that is an inherent limit of
a verifier shipped beside what it verifies — build one yourself
(`make verifier`, or from the kernel repository) and put it on `PATH`
for the strongest guarantee.

### Updating

Updating is re-running: `make install` (or `make install VERSION=<newer>`)
into the same prefix converges the directory on the selected release.
Each binary the release publishes is replaced — copies are staged
inside the directory first and then renamed into place in one pass,
so a running kernel and its plugin processes are never truncated
mid-execution — and then a binary an older release placed that the new
one no longer publishes is **retired** (removed) — after its evidence
is authenticated: a candidate is deleted only when the verifier
confirms a validly-signed older manifest of this repository vouches
for the exact bytes on disk. A same-name file whose bytes differ (a
replacement you made), or one whose valid evidence is another
publisher's, is left in place and reported — never ours to remove.
The older release's manifest pair goes in either case, so
`$PREFIX/lib/topos/plugins` holds exactly the selected release's fleet
and evidence, never a retired plugin kept trusted by a stale manifest. The installer names
every file it wrote, retired or removed. Restart the kernel to pick up
the new fleet.

Re-installing the same version is safe and is the repair path: every
asset is re-downloaded, re-verified and re-placed, ending in a
byte-identical tree. Downgrading is the same operation with an older
tag.

### What a failed install leaves behind

- **Before placement** — a checksum mismatch, a provenance refusal, a
  missing manifest, no resolvable verifier, a missing asset, a rejected
  `checksums.txt` name, a destination that is not a regular file — the
  install aborts naming the cause and `$PREFIX` is byte-unchanged (the
  writability probe may have created the empty plugins directory).
- **During the copy pass** — a full disk, say — the staged copies are
  removed and the directory is left as it was.
- **During the rename pass** — however it fails or is interrupted (an
  I/O error, a permissions race, a signal): each destination is wholly
  old or wholly new bytes, never torn. Re-run the same version to
  finish.
- **Between placement and convergence** — the new fleet is in place;
  re-running the same version performs the retirement.

### Uninstalling

```sh
make uninstall
```

removes exactly what `make install` placed — the `topos-plugin-*`
binaries and the `topos-plugins-*.provenance.{json,sig}` pair (every
pair, should an interrupted update have left an older one) directly
inside `$PREFIX/lib/topos/plugins` — then removes that
directory and `lib/topos` with a non-recursive `rmdir` only when they
are left empty; anything else there survives and is reported by name.
The kernel at `$PREFIX/bin` is the kernel repository's `make uninstall`
to remove. Idempotent: a second run is a clean no-op that exits 0.

### Signal

The Signal plugin is the fleet's one cgo build — it dynamically links
the system SQLCipher library, so no prebuilt binary is published. Build
it locally (see `plugins/signal/README.md` for the per-distro
`sqlcipher` package and the SQLite version floor):

```sh
make build-signal      # -> bin/topos-plugin-signal
```

A locally built binary carries no signed provenance, so it does not go
into `$PREFIX/lib/topos/plugins` (the kernel would refuse it there at
launch). Place it in the installed instance's **external** plugin
directory (by default `$XDG_DATA_HOME/topos/plugins-external`, or the
config's `[plugins] external_dir`) and add the Signal source once
through the app's untrusted-add consent flow — it runs pinned and
badged, exactly as it did when the plugin lived in the kernel repo.

### Verifying the install machinery itself

```sh
make install-check
```

runs the hermetic gate `ci.yml` runs on every push: a fixture release
signed with a throwaway key — the demo plugin as the binary, the
verifier built at the pinned kernel revision and relinked to accept
that key — installed through `install.sh`'s file:// test seam. It pins
the happy path, the in-place update (including retiring a plugin the
newer release no longer publishes — and NOT retiring a same-name
foreign replacement or a binary named only by forged evidence), a
corrupted asset, an injected placement-copy failure (destinations
preserved, every staged copy removed), a binary
tampered after signing with `checksums.txt` regenerated to match, an
unsigned release, no resolvable verifier, the installed-verifier
preference, a traversal-shaped `checksums.txt`, a destination that is not a
regular file, an unwritable prefix,
the idempotent re-run, the uninstall data-safety cycle and the
latest-release URL validator. No network beyond the Go module cache;
nothing written outside its own temp tree.

## Building locally

```sh
make build           # the six static plugins -> bin/ (CGO_ENABLED=0)
make build-signal    # signal -> bin/ (cgo, -tags libsqlcipher)
make test            # every workspace module except signal
make verifier        # topos-provenance at the pinned kernel revision -> bin/
```

`bin/` is the directory the kernel checkout's `make dev` adopts through
its `DEV_PLUGINS_DIR` knob (default `../topos-plugins/bin`): a
side-by-side dev loop hashes each binary found there into the dev
kernel's link-time manifest at build time, so the fleet runs at the
trusted tier in the dev instance with no consent-pin churn.

Signal's own suite: `cd plugins/signal && CGO_ENABLED=1 go test -tags libsqlcipher ./...`.

## Trust boundary

**First-party trust is signed by a key in the kernel's embedded key set.
That key lives in this repository's CI (a GitHub Actions secret named
`TOPOS_PROVENANCE_SIGNING_KEY`). Everything else is external-tier by
construction.**

Concretely: whoever can push a tag matching `v*.*.*` to this repository,
plus whoever can read GitHub's own secret store, controls what the
`topos` kernel will accept as a first-party plugin binary signed by this
repository's key. There is no other privileged path — no config edit, no
file drop, no name shadowing confers trust in the consuming kernel (see
`kernel/pluginhost/provenance.go` in the `topos` repository). Losing this
repository's signing secret means generating a new key and shipping a new
`topos` kernel release that embeds it (the kernel's key set is additive —
see that repository's D-03).

## What the release workflow guarantees

Pushing a tag matching `v*.*.*` builds the static fleet (`make build`)
and the provenance verifier at the pinned kernel revision
(`make verifier`, pinned by `TOPOS_PROVENANCE_REF` — one file, one
commit, never a floating ref), signs a release manifest naming every
binary's SHA-256 with this repository's private key (never printed,
never logged — passed to the signing step only through the
environment), and publishes a GitHub Release carrying:

- `topos-plugin-<name>` — one binary per static plugin (filesystem,
  gdrive, paperless, proton, silverbullet, whatsapp)
- `topos-plugins-<tag>.provenance.json` — the signed release manifest
- `topos-plugins-<tag>.provenance.sig` — its ed25519 signature
- `topos-provenance` — the verifier, for machines with no other
- `checksums.txt` — SHA-256 of every asset above

The signing step runs `topos`'s own `cmd/topos-provenance sign` — this
repository never reimplements the manifest format or the signature
scheme.
