#!/usr/bin/env bash
# Installs a published topos-plugins release into $PREFIX (M1-R5): every
# published plugin binary and the release's signed provenance manifest
# pair at $PREFIX/lib/topos/plugins/, each SHA-256-verified against that
# release's own checksums.txt AND every binary verified against the
# signed manifest BEFORE anything is written to $PREFIX.
#
# Usage: install.sh [version]       (with or without a leading "v")
#
# With no version argument, the latest published STABLE release is
# resolved by following the release root's `latest` redirect and
# validating the effective URL it lands on: scheme and host must be
# exactly https://github.com, the path must be this repository's own
# release-tag path, and the trailing tag must be three-part
# v<major>.<minor>.<patch> semver — which structurally excludes any
# prerelease-suffixed tag, as a second guard the script enforces itself
# rather than trusting the endpoint's semantics. No credential, token,
# or GitHub CLI is involved.
#
# This is the kernel repository's scripts/install.sh discipline
# (davison/topos, INST-01/INST-02, 16-05-PLAN.md Task 1) carried to the
# fleet, with two deliberate differences: provenance verification is
# MANDATORY here (every release this repository publishes is signed; a
# fleet with no evidence would launch untrusted or not at all — exactly
# the silent source absence M1-R6 forbids), and $PREFIX/bin is never
# touched (the kernel is the kernel repository's `make install` to
# place, update and remove — the two flows are independent, which is
# the point of M1-R5).
#
# This file is source-guarded: sourcing it defines its functions and
# runs nothing — the seam scripts/install-smoke.sh uses to drive the
# validator offline. Only direct execution runs the install flow.
#
# Environment:
#   PREFIX                          install root (default /usr/local)
#   TOPOS_PLUGINS_RELEASE_BASE_URL  release base URL (default the GitHub
#                                   releases root). A test seam exactly
#                                   like the kernel's
#                                   TOPOS_RELEASE_BASE_URL: it changes
#                                   WHICH release is fetched (e.g. a
#                                   file:// fixture tree), never which
#                                   checks run.
#
# Sequence: preflight -> stage -> verify -> place. Placement does not
# begin until every asset has verified, so an abort at any earlier point
# leaves $PREFIX byte-unchanged. Each placed file is copied to a
# temporary name inside the destination directory and completed with an
# atomic mv -f rename — never written directly over the destination —
# so a re-run over a running kernel/plugin process replaces the file
# without a text-file-busy failure and an interrupted run leaves every
# destination either wholly old bytes or wholly new bytes.
#
# Updating IS re-running: `install.sh <newer tag>` replaces each binary
# and places the newer release's manifest pair beside any older one.
# The kernel scans every manifest in the directory and trusts a binary
# the moment any validly-signed one names its on-disk digest
# (kernel/pluginhost/provenance.go, D-08), so the older manifest is
# inert, not a conflict; `uninstall.sh` removes them all.
#
# This script never escalates privileges: an unwritable $PREFIX fails
# loud, naming the directory and the `sudo make install` re-run for the
# operator to perform deliberately.

set -euo pipefail

fail() {
  echo "install: FAIL: $*" >&2
  exit 1
}

REPO_PATH="davison/topos-plugins"
DEFAULT_RELEASE_BASE_URL="https://github.com/$REPO_PATH/releases"

# resolve_latest_effective_url: issues a redirect-following HEAD request
# against the release root's `latest` path and prints the final
# effective URL. Decides NOTHING about whether that answer is acceptable
# — validate_latest_url below is the sole authority on that, so the
# network step and the safety check stay independently testable.
resolve_latest_effective_url() {
  curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "${TOPOS_PLUGINS_RELEASE_BASE_URL:-$DEFAULT_RELEASE_BASE_URL}/latest"
}

# validate_latest_url <effective-url>: validates the URL the `latest`
# redirect landed on and prints the release tag it names. Refuses by
# name when: the input is empty; the scheme/host are not exactly
# https://github.com; the path is not this repository's own release-tag
# path; or the trailing segment is not a bare three-part
# v<digits>.<digits>.<digits> tag.
validate_latest_url() {
  local url="${1:-}"
  if [ -z "$url" ]; then
    fail "latest-release resolution returned an empty effective URL"
  fi
  case "$url" in
    https://github.com/*) ;;
    *)
      fail "latest-release URL refused: scheme/host is not https://github.com — got: $url"
      ;;
  esac
  local path="${url#https://github.com}"
  case "$path" in
    /$REPO_PATH/releases/tag/*) ;;
    *)
      fail "latest-release URL refused: not this repository's release-tag path (/$REPO_PATH/releases/tag/...) — got: $url"
      ;;
  esac
  local tag="${path#/$REPO_PATH/releases/tag/}"
  if ! printf '%s' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    fail "latest-release URL refused: tag '$tag' is not a three-part stable v<major>.<minor>.<patch> release (prerelease tags are never auto-selected)"
  fi
  printf '%s' "$tag"
}

main() {

VERSION_ARG="${1:-}"
if [ -z "$VERSION_ARG" ]; then
  EFFECTIVE_URL="$(resolve_latest_effective_url)" \
    || fail "could not resolve the latest release from ${TOPOS_PLUGINS_RELEASE_BASE_URL:-$DEFAULT_RELEASE_BASE_URL}/latest — pass an explicit version (make install VERSION=<tag>) if the network is unavailable"
  TAG="$(validate_latest_url "$EFFECTIVE_URL")"
  echo "install: resolved latest stable release: $TAG"
else
  # Normalise to a single tag form: accept "0.2.0" or "v0.2.0", use "v0.2.0".
  TAG="v${VERSION_ARG#v}"
fi

PREFIX="${PREFIX:-/usr/local}"
TOPOS_PLUGINS_RELEASE_BASE_URL="${TOPOS_PLUGINS_RELEASE_BASE_URL:-$DEFAULT_RELEASE_BASE_URL}"

BIN_DIR="$PREFIX/bin"
PLUGINS_DIR="$PREFIX/lib/topos/plugins"

# --- preflight --------------------------------------------------------
for tool in curl sha256sum mktemp; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    fail "required tool '$tool' not found on PATH"
  fi
done

# Probe the ONE destination's writability BEFORE any download work is
# thrown away. mkdir -p is a no-op on an existing directory and never
# escalates; a failure here (or a directory we cannot write) names the
# directory and the escalated re-run form — this script itself must
# never run sudo. $BIN_DIR is deliberately not probed or created: this
# installer never writes there.
if ! mkdir -p "$PLUGINS_DIR" 2>/dev/null || [ ! -w "$PLUGINS_DIR" ]; then
  fail "cannot write to $PLUGINS_DIR — re-run as: sudo make install (this script never escalates privileges itself)"
fi

# --- stage ------------------------------------------------------------
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

DOWNLOAD_BASE="$TOPOS_PLUGINS_RELEASE_BASE_URL/download/$TAG"

# -f makes an HTTP error a non-zero curl exit, not a saved error page.
if ! curl -fsSL "$DOWNLOAD_BASE/checksums.txt" -o "$STAGE/checksums.txt"; then
  fail "could not fetch checksums.txt for release $TAG from $DOWNLOAD_BASE"
fi

# Derive the asset list from checksums.txt's second column — that file
# IS the release's manifest of assets (release.yml writes it over the
# exact set it publishes); a second hardcoded list here is the drift
# that discipline guards against. Every derived name must match an
# allowlist shape, because the manifest's names are untrusted text that
# becomes local write paths:
#   - topos-plugin-<name>, <name> lowercase letters, digits and hyphens
#     — a plugin binary;
#   - topos-plugins-<anything without a path separator>.provenance.json
#     and .provenance.sig — the signed release manifest pair (its
#     basename carries the tag, which may contain dots);
#   - topos-provenance — the verifier CLI the release ships (staged and
#     used, never placed — see the verify step);
# Anything else — anything with a slash, a parent segment, a leading
# dot, or a name outside these shapes — is rejected by name.
ASSETS=()
PLUGIN_BINARIES=()
PROVENANCE_MANIFESTS=()
PROVENANCE_SIGS=()
while IFS= read -r line; do
  [ -n "$line" ] || continue
  # sha256sum output: "<64 hex>  <path>" (two-space separator; a binary
  # -mode marker would make it " *<path>", which the allowlist rejects).
  rel="${line#*  }"
  if [ "$rel" = "$line" ] || [ -z "$rel" ]; then
    fail "checksums.txt line is not a sha256sum entry: $line"
  fi
  case "$rel" in
    */* | .* | *' '*)
      fail "checksums.txt names a disallowed path (rejected): $line"
      ;;
  esac
  case "$rel" in
    topos-provenance)
      ;;
    topos-plugins-*.provenance.json)
      PROVENANCE_MANIFESTS+=("$rel")
      ;;
    topos-plugins-*.provenance.sig)
      PROVENANCE_SIGS+=("$rel")
      ;;
    topos-plugin-*)
      if ! printf '%s' "${rel#topos-plugin-}" | grep -Eq '^[a-z0-9-]+$'; then
        fail "checksums.txt names a disallowed path (rejected): $line"
      fi
      PLUGIN_BINARIES+=("$rel")
      ;;
    *)
      fail "checksums.txt names a disallowed path (rejected): $line"
      ;;
  esac
  ASSETS+=("$rel")
done < "$STAGE/checksums.txt"

if [ "${#PLUGIN_BINARIES[@]}" -eq 0 ]; then
  fail "checksums.txt for $TAG lists no plugin binaries"
fi

# Provenance is mandatory: exactly one signed manifest pair per release.
# A release without one is refused HERE, before any download of the
# binaries — never installed as an unsigned fleet the kernel would then
# refuse or run untrusted, source by source, with nothing naming why.
if [ "${#PROVENANCE_MANIFESTS[@]}" -ne 1 ] || [ "${#PROVENANCE_SIGS[@]}" -ne 1 ]; then
  fail "release $TAG must publish exactly one signed provenance manifest pair (topos-plugins-<tag>.provenance.json + .sig); checksums.txt lists ${#PROVENANCE_MANIFESTS[@]} manifest(s) and ${#PROVENANCE_SIGS[@]} signature(s) — an unsigned fleet is refused, nothing was written to $PREFIX"
fi
if [ "${PROVENANCE_MANIFESTS[0]%.provenance.json}" != "${PROVENANCE_SIGS[0]%.provenance.sig}" ]; then
  fail "release $TAG's provenance manifest (${PROVENANCE_MANIFESTS[0]}) and signature (${PROVENANCE_SIGS[0]}) do not share a basename — refused, nothing was written to $PREFIX"
fi

# Release assets are published as flat basenames and checksums.txt
# records them flat too, so the staging directory mirrors the release
# one-to-one and `sha256sum -c` runs unmodified.
for rel in "${ASSETS[@]}"; do
  if ! curl -fsSL "$DOWNLOAD_BASE/$rel" -o "$STAGE/$rel"; then
    fail "could not download asset '$rel' for release $TAG from $DOWNLOAD_BASE — nothing was written to $PREFIX"
  fi
done

# A staged topos-provenance CLI needs its execute bit set BEFORE the
# verify step can invoke it (curl -o never sets it).
if [ -f "$STAGE/topos-provenance" ]; then
  chmod +x "$STAGE/topos-provenance"
fi

# --- verify: checksums ------------------------------------------------
# Every asset must verify before ANY placement begins — an abort here
# leaves $PREFIX byte-unchanged.
if ! (cd "$STAGE" && sha256sum -c checksums.txt >"$STAGE/verify.out" 2>&1); then
  failed="$(grep -v ': OK$' "$STAGE/verify.out" 2>/dev/null || true)"
  fail "SHA-256 verification failed for release $TAG — nothing was written to $PREFIX
${failed}"
fi

# --- verify: provenance -----------------------------------------------
# SHA-256 proves transport integrity only — "these are the bytes
# checksums.txt named" — never publisher authenticity. Every staged
# plugin binary is now verified against the staged signed manifest by
# the same `topos-provenance verify` the kernel's own launch gate calls
# (kernel/pluginhost.VerifySignedProvenance): a signature that does not
# verify, a key the verifier does not accept, a manifest built for
# another platform, or a binary the manifest does not name (or names
# with another digest) all abort here, naming the binary — nothing has
# been placed.
#
# Verifier resolution order — the kernel install script's own
# (davison/topos 16-REVIEW.md WR-01): a verifier that ships in the same
# payload it would be checking is the LEAST trustworthy candidate (an
# attacker who controls this repository's release pipeline but not its
# signing key could ship one that always says yes), so it is consulted
# LAST, only when nothing more trustworthy exists:
#   1. $BIN_DIR/topos-provenance — an installed kernel's own copy, from
#      the kernel repository's release, untouched by whatever produced
#      THIS payload;
#   2. topos-provenance on PATH — operator-controlled (`make verifier`
#      in this checkout builds one at the pinned kernel revision);
#   3. the staged copy this release ships.
# A payload that ships evidence must have that evidence checked, so
# resolving NO verifier is a loud abort, never a silent skip. The staged
# copy is never placed anywhere: placing it would let one release
# payload seed tier 1 for every later install.
PROVENANCE_VERIFIER=""
if [ -x "$BIN_DIR/topos-provenance" ]; then
  PROVENANCE_VERIFIER="$BIN_DIR/topos-provenance"
elif command -v topos-provenance >/dev/null 2>&1; then
  PROVENANCE_VERIFIER="$(command -v topos-provenance)"
elif [ -x "$STAGE/topos-provenance" ]; then
  PROVENANCE_VERIFIER="$STAGE/topos-provenance"
fi

if [ -z "$PROVENANCE_VERIFIER" ]; then
  fail "release $TAG carries a signed provenance manifest but no topos-provenance verifier could be resolved (checked $BIN_DIR/topos-provenance, PATH, and the staged payload) — a payload that ships evidence must have that evidence checked; build one with \`make verifier\` and put bin/ on PATH, or install a topos kernel release that ships it; nothing was written to $PREFIX"
fi

if ! "$PROVENANCE_VERIFIER" verify --dir "$STAGE" >"$STAGE/provenance-verify.out" 2>&1; then
  fail "provenance verification failed for release $TAG (verifier: $PROVENANCE_VERIFIER) — nothing was written to $PREFIX
$(cat "$STAGE/provenance-verify.out")"
fi

# --- place ------------------------------------------------------------
# Copy to a temporary name INSIDE the destination directory, set mode,
# then complete with an atomic same-directory rename. Never write
# directly over the destination path: the rename is what makes a re-run
# over a live kernel/plugin process safe and what makes an interrupted
# run leave whole-old or whole-new bytes, never a torn file. Binaries
# are 0755; the manifest pair is data, 0644. The staged verifier is not
# in this loop by construction.
WRITTEN=()
for rel in "${PLUGIN_BINARIES[@]}" "${PROVENANCE_MANIFESTS[@]}" "${PROVENANCE_SIGS[@]}"; do
  dest="$PLUGINS_DIR/$rel"
  case "$rel" in
    topos-plugin-*) mode=0755 ;;
    *) mode=0644 ;;
  esac
  tmp="$(mktemp "$PLUGINS_DIR/.topos-plugins-install.XXXXXX")" \
    || fail "cannot create a temporary file in $PLUGINS_DIR — re-run as: sudo make install (this script never escalates privileges itself)"
  cp "$STAGE/$rel" "$tmp"
  chmod "$mode" "$tmp"
  mv -f "$tmp" "$dest"
  WRITTEN+=("$dest")
done

# --- report -----------------------------------------------------------
echo "install: topos-plugins $TAG installed into $PLUGINS_DIR (verifier: $PROVENANCE_VERIFIER)"
for path in "${WRITTEN[@]}"; do
  echo "install:   wrote $path"
done
OLDER=0
for f in "$PLUGINS_DIR"/topos-plugins-*.provenance.json; do
  [ -e "$f" ] || continue
  [ "$(basename "$f")" != "${PROVENANCE_MANIFESTS[0]}" ] || continue
  OLDER=$((OLDER + 1))
done
if [ "$OLDER" -gt 0 ]; then
  echo "install:   $OLDER older release manifest(s) remain beside ${PROVENANCE_MANIFESTS[0]} — inert (the kernel trusts a binary the moment any valid manifest names its digest); make uninstall removes them all"
fi
echo "install:   topos-plugin-signal is not shipped prebuilt (cgo/SQLCipher) — build it locally with make build-signal and place it in the kernel's external plugin directory; see README.md"
echo "install:   restart the installed kernel to pick up the new fleet"

}

# Source-guard: executed directly, run the install flow; sourced (the
# install-smoke test seam), define functions only and do nothing else.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
