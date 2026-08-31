# Filesystem

Reads documents out of a local (or network-mounted) folder and turns each
matched file into a stream item — a bare, no-op reader of whatever the
operating system already presents at the configured path, so a network
share (NFS, SMB) works exactly like a local directory with no
source-specific mount handling of its own.

## Install Requirements

None beyond a readable directory. This plugin builds `CGO_ENABLED=0`, like
every other plugin except Signal — no system package, SDK, or version
floor is required.

## Configuration

```toml
[sources.docs]
plugin = "topos-plugin-filesystem"
path = "~/Documents/household"
recursive = false

[sources.docs.agent]
read = false
handoff = false
```

`path` is the folder this instance reads from. A leading `~` is expanded
by the plugin itself, not the kernel (`kernel/config`'s `Path` field is
stored unexpanded, the same convention `plugins/signal` and
`plugins/whatsapp` already use).

`recursive` (optional, default `false`) toggles subfolder walking: `false`
reads the configured folder's own top level only; `true` walks every
depth beneath it. The conservative default exists because an operator's
first filesystem source is very often pointed at a folder whose full
depth they haven't thought through — starting flat and opting into depth
is safer than the reverse.

Match vocabulary: `folders`.

### `include_glob` / `exclude_glob` extras

```toml
[sources.docs.extras]
include_glob = "**/*.pdf,**/*.md"
exclude_glob = "**/node_modules/**,**/.git/**"
```

Both are optional, each a single **comma-separated** string of
[`doublestar`](https://github.com/bmatcuk/doublestar) glob patterns
(arbitrary-depth `**` supported, unlike Go's stdlib `path/filepath.Match`).
Resolution order, evaluated per candidate file:

1. **`exclude_glob` always wins.** A file matching any exclude pattern is
   dropped, full stop — no include pattern can override an exclude.
2. **A declared `include_glob` REPLACES the default extension allowlist
   entirely for this instance**, rather than widening it — it does not
   layer on top. A file matching an include pattern is included even if
   its extension is outside the default allowlist below (classified
   metadata-only if the extension is unrecognized, never guessing a MIME
   type); a file that does NOT match any include pattern is excluded even
   if its extension otherwise would have qualified. This same resolution
   order **re-runs at Fetch time** against the instance's own extras, not
   just at Match/walk time: a file admitted by an include pattern opens as
   an honest metadata-only preview rather than a "not found," and a file
   outside the instance's scope answers "not found" at Fetch even when it
   exists on disk.
3. **With no `include_glob` declared at all**, scope falls back to the
   default extension allowlist alone (below).

## Default document allowlist

With no `include_glob` override, a file is included only if its extension
(case-insensitive) is one of:

| Category | Extensions | Preview |
|---|---|---|
| PDF | `.pdf` | Inline — raw bytes, `application/pdf` |
| Images (renderable) | `.png`, `.jpg`, `.jpeg`, `.gif`, `.webp` | Inline — raw bytes, matching image MIME |
| Markdown | `.md`, `.markdown` | Inline — kernel-rendered HTML (`goldmark`, `CONTENT_SHAPE_MARKDOWN_HTML`) |
| Plain text | `.txt`, `.text`, `.log`, `.csv` | Inline — `text/plain`, honestly truncated past 256 KiB rather than silently cut |
| Office documents | `.doc`, `.docx`, `.xls`, `.xlsx`, `.ppt`, `.pptx`, `.odt`, `.ods`, `.odp`, `.rtf` | Metadata + deep link only, no inline preview |
| Images (unrenderable) | `.svg`, `.bmp`, `.tif`, `.tiff`, `.heic` | Metadata + deep link only, no inline preview |

A file whose extension is outside this table entirely is skipped — it
never appears in the stream at all — unless `include_glob` explicitly
names it (see above).

**Why office documents show no inline preview:** this plugin declares no
office-format conversion. Server-side conversion (LibreOffice headless or
similar) was raised and deliberately deferred during this phase's own
design discussion as new, heavyweight machinery — revisit only if
metadata-plus-deep-link proves genuinely insufficient in practice. An
office-format or unrenderable-image item still syncs, still appears in
the stream, and still opens correctly via "Open in …" (see below) — it
simply has no inline rendition, by design, not by omission.

## Folder-vocabulary match values (`folders`)

**What every file reports.** Every file this source contributes carries
the configured `path`'s own base name as a label, at every depth (e.g.
`path = "~/Documents/household"` gives every file — top-level or nested —
the label `household`). A nested file (`recursive = true` only)
additionally carries one label per containing-directory path segment, plus
the cumulative relative directory path. So a file at
`receipts/2026/invoice.pdf` under `path = "~/Documents/household"` reports
`household`, `receipts`, `2026`, and `receipts/2026`, in that order. A
subfolder whose name equals the root's own (e.g. a `household/household/`
nesting) reports that value once, not twice.

**How to match everything from this instance.** Name the configured
folder's base name and nothing else:

```toml
[webspaces.house-move.match.docs]
folders = ["household"]
```

This is the correct — and only — way to express "everything from this
instance" for a filesystem source. There is no wildcard for it.

**Match values are literals, and this page's globs are not.** A `folders`
value is compared as an exact, case-insensitive string against the labels
above — **never as glob patterns** — deliberately different from the
`include_glob`/`exclude_glob` extras documented above on this same page,
which ARE doublestar patterns. A value of a bare asterisk (`*`), or a
doubled asterisk (`**`), is therefore treated as a literal folder name,
matches no label, and yields an empty stream for that source while
`Health` and the sync both stay green. See `docs/plugin-contract.md`'s
own match rule (rule 2, "Match exact and case-insensitive, never
substring or prefix", D-04) — this exact-match discipline is a
cross-plugin invariant, not something specific to this plugin.

## Symlink and dot-file policy

- A dot-prefixed file or directory (`.git`, `.env`, …) is excluded by
  default. It is reachable only via an explicit `include_glob` pattern
  that names it directly — never via the default extension allowlist
  alone, even for a dot-file whose extension would otherwise qualify.
- A symlinked directory is never descended into. This is structural
  (`filepath.WalkDir`'s own `Lstat` semantics never auto-follow a
  symlinked directory), not a special case this plugin coded — it closes
  the ancestor-symlink-loop class by construction and keeps an in-tree
  link from silently widening the folder an operator consented to expose.
- A symlinked regular file is classified and included like any other
  file, but its resolved real path is re-validated as still inside the
  resolved configured root before it becomes an item — a symlink pointing
  outside the folder never becomes an item.
- A configured root which is itself a symlink or bind mount (the common
  `~/Documents` -> `~/dotfiles/Documents` dotfile-manager pattern) is
  fully supported: the root is resolved once before the walk begins, so
  its legitimately in-tree symlinked files are included in the stream
  rather than silently dropped.
- The identical resolved-containment check re-runs at Fetch time (when an
  item's preview or full content is served) and at open time (when
  `POST /api/items/{id}/open` execs the desktop's own file handler) — not
  just at Match/walk time. Both re-resolve symlinks on the joined path and
  the configured root and fail closed on resolution failure, so a file
  swapped for an outward-pointing symlink between one sync and the next
  request is refused rather than served or opened.

## When the mount goes away

`Health` calls `os.ReadDir` on the configured root — not `os.Stat`, which
on Linux only needs search permission on the parent directory and so
cannot distinguish "permission denied" from "empty and readable." A
vanished network mount, a revoked permission, or a genuinely empty folder
each report distinctly: the first two report `reachable: false` with the
OS's own error named; an empty-but-readable folder reports healthy with
zero items. A sync in progress when the root itself becomes unreadable
fails the whole sync with a named error rather than silently reporting
zero items — the empty-folder state and the mount-is-gone state are never
conflated as "nothing here."

## The sync interval is the real freshness bound

This plugin holds no persisted cache and calls no filesystem
change-notification API (`inotify` or equivalent) — every sync is a fresh
`filepath.WalkDir` pass returning the complete current item set. This
matters specifically for a network mount: NFS and SMB clients generally
do not deliver local change events for another client's writes, so a
change-notification approach would silently miss remote edits on exactly
the kind of mount this plugin is built to read. The configured sync
interval (`[sync] interval`, or this instance's own `sync_interval`
override) is therefore the actual, honest freshness bound on a network
mount — not a documented caveat to work around, but the correct
mechanism given what the underlying protocols can guarantee.

## Gotchas

- A per-sync tree is capped at 25,000 items; exceeding it fails the sync
  with a named error pointing at `exclude_glob` rather than silently
  truncating the result (a silent truncation would delete real items on
  the very next sync, since the kernel treats each sync's returned set as
  the complete truth).
- `include_glob` **replaces** the default allowlist rather than widening
  it — a common mistake is expecting `include_glob = "**/*.pdf"` to ADD
  PDFs to the existing default set; it instead narrows scope to exactly
  what the pattern matches, dropping markdown/text/office files that were
  previously included by the default allowlist alone. Combine with
  `exclude_glob` if you want "everything the default allowlist covers,
  minus a few paths" instead.
- Opening a file ("Open in …") execs the desktop's own `xdg-open` against
  the resolved absolute path — it needs a working desktop file-association
  setup on the machine running the kernel, same as clicking the file in a
  file manager would.

## Security & Privacy Notes

- **Read-only:** this plugin never writes to the configured folder. A
  committed AST guard (`readonly_test.go`) walks this package's own
  source and fails the build on any `os` write-selector reference, with
  two negative-control fixtures proving the scan itself isn't vacuous.
- **Credentials:** none — a filesystem source has no token, key, or
  secret to configure at all; the operating system's own filesystem
  permissions are the only access control in play.
- **Egress:** none — this plugin talks only to the local filesystem
  (including a network mount the OS presents as a filesystem), never a
  network socket of its own.

## Configuration reference

The fully-commented `[sources.<name>]` block for this plugin — moved verbatim from the kernel's former `config.example.toml` (davison/topos#24, M1-R2): every key with its purpose, default and validation rule. Copy it into your own `config.toml` under `[sources.<your-instance-name>]`; the kernel-level keys every source shares (`display_name`, `[sources.<name>.agent]`) are documented in the kernel's `config.example.toml`.

```toml
[sources.filesystem]
# plugin: the plugin binary's filename, resolved inside [plugins] dir.
# Validation: none at load time; a missing file fails at startup, by path.
plugin = "topos-plugin-filesystem"

# display_name: the kernel-level key every source shares — optional, a
# human-readable label; see the kernel's config.example.toml.
display_name = "Household Docs"

# path: the local (or network-mounted) folder this instance reads
# documents from. A leading "~" is expanded by the plugin subprocess
# itself, not the kernel — identical convention to [sources.signal]'s
# path, above.
#
# This source needs NO base_url, NO token, and NO environment variable at
# all — a filesystem source's only "connection detail" is the path
# itself.
# Validation: kernel/config.Validate accepts a source declaring only
# path, in place of base_url+token — identical to [sources.signal] and
# [sources.whatsapp].
path = "~/Documents/household"

# recursive: whether this instance walks every depth beneath path (true)
# or its own top level only (false). Optional; omitted (or explicitly
# false) is the conservative default — a possibly-large, unfamiliar tree
# stays shallow until you opt into depth.
# Validation: none — false is always a legal value.
recursive = false

# Match vocabulary: "folders". Every file this instance contributes
# carries the configured path's own base name as a label, at every depth
# — name that value in a webspace's folders match block (or keywords) to
# match "everything from this instance". A nested file (recursive = true)
# additionally carries one label per containing-directory segment plus
# the cumulative relative path. CAUTION: these values are compared as
# exact, case-insensitive strings — never as glob patterns — unlike the
# include_glob/exclude_glob keys documented immediately below. See
# the filesystem plugin README's "Folder-vocabulary match values" section
# (the plugin lives in davison/topos-plugins)
# for the worked example and the full precedence rule.

# [sources.filesystem.extras] — OPTIONAL glob-based scope overrides this
# plugin's own Describe RPC declares (see [sources.paperless.extras],
# above, for the general extras mechanism). Both keys are a single
# comma-separated string of doublestar glob patterns (arbitrary-depth
# "**" supported). exclude_glob always wins over include_glob; a declared
# include_glob REPLACES the default document-extension allowlist for this
# instance rather than widening it — see the filesystem plugin's README for
# the full precedence rule and the default allowlist by extension.
# Validation: an empty key, or one outside [A-Za-z_][A-Za-z0-9_.-]*, fails
# config load, naming the offending source instance and key — identical
# to every other extras table. A malformed glob pattern fails that sync
# with a named error, not config load (the pattern's own validity can
# only be checked at match time).
# [sources.filesystem.extras]
# include_glob = "**/*.pdf,**/*.md"
# exclude_glob = "**/node_modules/**,**/.git/**"

[sources.filesystem.agent]
read = false
handoff = false
```
