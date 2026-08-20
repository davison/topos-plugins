# topos-plugins

This repository is the seed of what Phase 17 of
[`topos`](https://github.com/davison/topos) fills with its real out-of-repo
source plugins. Today it holds exactly one thing: a trivial, real plugin
(`cmd/topos-plugin-demo/`) and a tag-triggered release workflow that signs its published
binary with this repository's own ed25519 key. Nothing here is
operator-facing yet — this repository exists to prove the signing pipeline
that the real plugin split will build on top of, in its final home.

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

Pushing a tag matching `v*.*.*` builds `cmd/topos-plugin-demo/`'s plugin binary
(`topos-plugin-demo`), signs a release manifest naming its SHA-256 with
this repository's private key (never printed, never logged — passed to the
signing step only through the environment), and publishes a GitHub Release
carrying:

- `topos-plugin-demo` — the built binary
- `topos-plugins-<tag>.provenance.json` — the signed release manifest
- `topos-plugins-<tag>.provenance.sig` — its ed25519 signature
- `checksums.txt` — SHA-256 of every asset above

The signing step invokes `topos`'s own `cmd/topos-provenance sign` at a
pinned module version — this repository never reimplements the manifest
format or the signature scheme.

## Building locally

```
go build ./...
```

## What this repository is not

It is not yet a real plugin catalog. Phase 17 of `topos` fills this
repository with real, out-of-repo source plugins; until then,
`cmd/topos-plugin-demo/` is here only to give the release pipeline
something genuine to build and sign.
