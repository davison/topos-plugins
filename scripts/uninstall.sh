#!/usr/bin/env bash
# Removes what `make install` placed under $PREFIX — and ONLY that. The
# removal set is closed and explicit, mirroring scripts/install.sh's
# placement set exactly:
#
#   - each FILE directly inside $PREFIX/lib/topos/plugins whose name
#     starts with "topos-plugin-" (a plugin binary);
#   - each FILE directly inside that directory named
#     topos-plugins-*.provenance.json or topos-plugins-*.provenance.sig
#     (a release's signed manifest pair — every pair present, should an
#     interrupted update ever have left an older one behind; a completed
#     update converges to exactly one).
#
# After removal, the lib/topos/plugins and lib/topos directories are
# removed with a NON-RECURSIVE rmdir only — a non-empty directory
# survives, and this script reports what remains rather than forcing
# it. No recursive removal appears anywhere here, and it never descends
# into subdirectories.
#
# The kernel at $PREFIX/bin is the kernel repository's `make uninstall`
# to remove; this script never names $PREFIX/bin at all. The operator's
# configuration, kernel index, and plugin stores live in home-relative
# locations it never names either: it takes no flag and offers no path
# — not even an opt-in one — that could touch them. The ABSENCE of that
# capability is the guarantee.
#
# Idempotent: an already-absent path is reported, not an error; a
# second run is a clean no-op that exits 0. Removal is by unlink, so
# running this while the installed kernel is live succeeds and leaves
# each running plugin process on its already-open file until it exits.

set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
PLUGINS_DIR="$PREFIX/lib/topos/plugins"
TOPOS_LIB_DIR="$PREFIX/lib/topos"

REMOVED=0

if [ -d "$PLUGINS_DIR" ]; then
  for f in "$PLUGINS_DIR"/topos-plugin-* "$PLUGINS_DIR"/topos-plugins-*.provenance.json "$PLUGINS_DIR"/topos-plugins-*.provenance.sig; do
    [ -e "$f" ] || continue
    if [ ! -f "$f" ]; then
      echo "uninstall: left in place (not a regular file): $f"
      continue
    fi
    rm -f "$f"
    echo "uninstall: removed $f"
    REMOVED=$((REMOVED + 1))
  done
else
  echo "uninstall: already absent: $PLUGINS_DIR"
fi

# Directory cleanup: non-recursive rmdir ONLY, innermost first. A
# directory that still holds anything survives, with its contents
# reported by name.
for dir in "$PLUGINS_DIR" "$TOPOS_LIB_DIR"; do
  if [ -d "$dir" ]; then
    if rmdir "$dir" 2>/dev/null; then
      echo "uninstall: removed empty directory $dir"
    else
      echo "uninstall: left in place (not empty): $dir"
      ls -A "$dir" | while IFS= read -r entry; do
        echo "uninstall:   remaining: $dir/$entry"
      done
    fi
  fi
done

if [ "$REMOVED" -eq 0 ]; then
  echo "uninstall: nothing left to remove under $PLUGINS_DIR"
else
  echo "uninstall: removed $REMOVED file(s) from $PLUGINS_DIR"
fi
