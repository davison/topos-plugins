#!/usr/bin/env bash
# Places (or removes, with --uninstall) the locally built Signal plugin
# binary for an installed topos instance (M1-R7/DIST-04,
# davison/topos-plugins#12) — the placement half of the flow the kernel
# checkout offered before the plugin split (davison/topos#13 removed it
# with the plugin; this repository is its home now).
#
# Usage: install-signal.sh [--uninstall]
#
# The build itself is NOT here: `make install-signal` reaches this
# repository's single `build-signal` definition as a prerequisite (the
# one place the cgo/libsqlcipher build flags live — duplicating them
# here is exactly the drift the Makefile's one-place-only comments
# guard against). This script only resolves the destination, places
# atomically, and prints the one-time steps.
#
# Destination: the installed instance's EXTERNAL plugin directory —
# deliberately NOT $PREFIX/lib/topos/plugins. A locally built binary
# carries no signed provenance and appears in no release manifest, and a
# trusted-directory binary no evidence vouches for cannot launch from
# there (the kernel's launch gate, davison/topos
# docs/plugin-trust.md). That gate is the trust system working
# correctly; the supported path is the external directory plus the
# app's one-time untrusted-add consent-and-pin flow, which this script
# automates the placement half of and prints the rest of.
#
# The default destination reproduces the kernel's own Linux
# external-directory default (defaultExternalPluginsDir in the kernel's
# cmd/topos/main.go): $XDG_DATA_HOME/topos/plugins-external when
# XDG_DATA_HOME is set and non-empty, else
# $HOME/.local/share/topos/plugins-external. An operator whose config
# names a different [plugins] external_dir points this script at it via
# TOPOS_EXTERNAL_PLUGINS_DIR.
#
# This file is source-guarded, like scripts/install.sh: sourcing it
# defines its functions and runs nothing — the seam
# scripts/install-smoke.sh uses to drive destination resolution and the
# footgun refusal offline. Only direct execution places or removes.

set -euo pipefail

BINARY_NAME="topos-plugin-signal"

fail() {
  echo "install-signal: FAIL: $*" >&2
  exit 1
}

# resolve_external_dir prints "<dir>|<source-label>" for the current
# environment — the override, the XDG default, or the home fallback, in
# that order. Pure resolution; the footgun check is
# refuse_trusted_destination's, so each is independently testable.
resolve_external_dir() {
  if [ -n "${TOPOS_EXTERNAL_PLUGINS_DIR:-}" ]; then
    printf '%s|%s' "$TOPOS_EXTERNAL_PLUGINS_DIR" "TOPOS_EXTERNAL_PLUGINS_DIR override"
  elif [ -n "${XDG_DATA_HOME:-}" ]; then
    printf '%s|%s' "$XDG_DATA_HOME/topos/plugins-external" "kernel default (XDG_DATA_HOME)"
  else
    printf '%s|%s' "$HOME/.local/share/topos/plugins-external" "kernel default (~/.local/share)"
  fi
}

# refuse_trusted_destination <dir>: a trusted plugins directory under a
# prefix is never a valid destination for a locally built binary — no
# release manifest names its hash, so the kernel refuses it at launch
# (launch_failure: manifest_unverified) before any subprocess is
# created. Refused here, by name, before anything is written.
refuse_trusted_destination() {
  case "$1" in
    */lib/topos/plugins | */lib/topos/plugins/)
      fail "refusing destination '$1': that is an installed instance's TRUSTED plugins directory, and a locally built binary placed there is refused by the kernel's launch gate (launch_failure: manifest_unverified — no release manifest vouches for local bytes). The supported destination is the EXTERNAL plugin directory ([plugins] external_dir) — the kernel's default is \$XDG_DATA_HOME/topos/plugins-external (or ~/.local/share/topos/plugins-external)."
      ;;
  esac
}

# refuse_root: the external plugin directory is the RUNNING USER's data
# directory, so a root-run install resolves root's — a place no
# operator's kernel looks — and would report success (topos-plugins#20:
# the kernel and fleet installs need sudo for a /usr/local PREFIX, so
# running this one the same way is the natural next move). Refused by
# name unless the operator names the instance's directory explicitly.
# INSTALL_SIGNAL_EUID is the smoke suite's hook; real runs read EUID.
refuse_root() {
  local euid="${INSTALL_SIGNAL_EUID:-${EUID:-$(id -u)}}"
  if [ "$euid" = "0" ] && [ -z "${TOPOS_EXTERNAL_PLUGINS_DIR:-}" ]; then
    fail "refusing to run as root: the external plugin directory is the RUNNING USER's data directory (\$XDG_DATA_HOME/topos/plugins-external), and root's is never an installed instance's — under sudo this would place the binary where no kernel looks, and report success. Run 'make install-signal' / 'make uninstall-signal' WITHOUT sudo (the kernel's and the fleet's 'make install' need it for a /usr/local PREFIX; this step must not have it), or name the instance's directory explicitly with TOPOS_EXTERNAL_PLUGINS_DIR=<dir>."
  fi
}

main() {

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILT_BINARY="$REPO_ROOT/bin/$BINARY_NAME"

refuse_root
RESOLVED="$(resolve_external_dir)"
DEST_DIR="${RESOLVED%|*}"
DEST_SOURCE="${RESOLVED##*|}"
refuse_trusted_destination "$DEST_DIR"
DEST="$DEST_DIR/$BINARY_NAME"

# --- uninstall mode ---------------------------------------------------
if [ "${1:-}" = "--uninstall" ]; then
  echo "install-signal: external plugin directory: $DEST_DIR ($DEST_SOURCE)"
  if [ -f "$DEST" ]; then
    rm -f "$DEST"
    echo "install-signal: removed $DEST"
  else
    echo "install-signal: already absent: $DEST — nothing to remove"
  fi
  # The directory itself and every other binary in it are untouched:
  # this removal path reaches exactly one file, ever.
  exit 0
fi

# --- install mode -----------------------------------------------------
if [ ! -f "$BUILT_BINARY" ]; then
  fail "built binary not found at $BUILT_BINARY — run this via 'make install-signal' (which builds it through the repository's own 'build-signal' target first; requires the system sqlcipher package, see plugins/signal/README.md)"
fi

echo "install-signal: external plugin directory: $DEST_DIR ($DEST_SOURCE)"

mkdir -p "$DEST_DIR" || fail "cannot create $DEST_DIR"

# Atomic placement, same technique as scripts/install.sh: temporary name
# inside the destination directory, mode, then rename — safe to re-run
# while the installed kernel is serving (the live subprocess keeps its
# already-open file; the new bytes surface as a pin mismatch to
# re-accept).
tmp="$(mktemp "$DEST_DIR/.topos-install-signal.XXXXXX")" \
  || fail "cannot create a temporary file in $DEST_DIR"
cp "$BUILT_BINARY" "$tmp"
chmod 0755 "$tmp"
mv -f "$tmp" "$DEST"

echo "install-signal: placed $DEST"
echo ""
echo "install-signal: one-time steps (plugins/signal/README.md 'Installing beside an installed kernel'):"
echo "install-signal:   This binary is untrusted by construction — it was built locally, so no"
echo "install-signal:   signed release manifest (and no kernel build manifest) vouches for it."
echo "install-signal:   1. Restart (or start) your installed kernel."
echo "install-signal:   2. Add the Signal source through the app's untrusted-add consent flow —"
echo "install-signal:      the same explicit consent-and-pin path any external binary goes through."
echo "install-signal:   3. It then runs pinned and badged untrusted."
echo "install-signal:   Re-running 'make install-signal' later produces new bytes: the changed"
echo "install-signal:   binary must be re-accepted through the chip's re-pin flow."
echo "install-signal:   If your config's [plugins] external_dir names a different directory,"
echo "install-signal:   re-run with TOPOS_EXTERNAL_PLUGINS_DIR=<that directory>."

}

# Source-guard: executed directly, run the flow; sourced (the smoke test
# seam), define functions only and do nothing else.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
