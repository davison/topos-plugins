#!/usr/bin/env bash
#
# scripts/signal-schema-accept.sh — the Signal plugin's schema ceiling
# moves by verify-and-accept, not by hand (topos-plugins#23, M2-R5 of
# davison/topos#40).
#
# What it does, in order, on the operator's machine:
#   1. Runs the plugin's opt-in live read-set test against the real
#      Signal Desktop database (read-only, as always). If it did not run
#      or did not pass, NOTHING is accepted — a broken read set is fixed
#      in the plugin first.
#   2. Reads the database's schema version from the test's summary line.
#      At or below the current ceiling there is nothing to accept; the
#      script says so and exits 0.
#   3. Above it: lists Signal Desktop's migrations between the ceiling
#      and the new version from upstream (ts/sql/migrations, via gh api);
#      offline, only SIGNAL_SCHEMA_ACCEPT_OFFLINE=1 lets it write an
#      explicit placeholder the reviewer must fill in.
#   4. Rewrites plugins/signal/schemaguard.go: the constant, and a
#      provenance bullet in the file's own format — date, package,
#      migrations, the live counts — inserted before the "Raising this
#      constant" paragraph. Prints the diff. Never commits: the change
#      goes through a task and a review like any other.
#
# Usage: make signal-schema-accept            (or ./scripts/signal-schema-accept.sh)
#        ./scripts/signal-schema-accept.sh --dry-run   — print the diff, write nothing
#
# Needs: a cgo toolchain, the system sqlcipher package, gh (online) and
# a Signal Desktop database at ~/.config/Signal — the same things the
# live test needs.
set -euo pipefail

fail() {
  echo "signal-schema-accept: FAIL: $*" >&2
  exit 1
}

GUARD_FILE_DEFAULT="plugins/signal/schemaguard.go"

# current_ceiling <file>: the constant's value, or nothing.
current_ceiling() {
  sed -nE 's/^const highestSupportedSchemaVersion = ([0-9]+)$/\1/p' "$1"
}

# nothing_to_accept <ceiling> <version>: true when the database is at or
# below the ceiling.
nothing_to_accept() {
  [ "$2" -le "$1" ]
}

# package_version: the installed Signal Desktop package, for the record.
package_version() {
  pacman -Q signal-desktop 2>/dev/null \
    || dpkg-query -W -f 'signal-desktop ${Version}' signal-desktop 2>/dev/null \
    || rpm -q signal-desktop 2>/dev/null \
    || echo "signal-desktop (package version unknown on this system)"
}

# migrations_between <ceiling> <version>: comma-separated upstream
# migration file names numbered in (ceiling, version], from
# github.com/signalapp/Signal-Desktop ts/sql/migrations.
migrations_between() {
  local old="$1" new="$2" names
  if [ -n "${SIGNAL_SCHEMA_MIGRATIONS:-}" ]; then
    printf '%s' "$SIGNAL_SCHEMA_MIGRATIONS"
    return
  fi
  if names="$(gh api repos/signalapp/Signal-Desktop/contents/ts/sql/migrations --jq '.[].name' 2>/dev/null)"; then
    printf '%s\n' "$names" | awk -v o="$old" -v n="$new" -F- '($1+0) > o && ($1+0) <= n' | sort | paste -sd, -
    return
  fi
  [ "${SIGNAL_SCHEMA_ACCEPT_OFFLINE:-}" = "1" ] \
    || fail "could not list Signal Desktop's migrations between $old and $new (gh api repos/signalapp/Signal-Desktop/contents/ts/sql/migrations). Rerun online, or set SIGNAL_SCHEMA_ACCEPT_OFFLINE=1 to write a placeholder the reviewer must fill in before this is committed."
  printf 'MIGRATIONS NOT LISTED (offline run — fill in from ts/sql/migrations before committing)'
}

# accept <file> <ceiling> <version> <date> <package> <migrations> <summary> <dry-run 0|1>
# Rewrites the guard file: the constant, and a provenance bullet before
# the "Raising this constant" paragraph, in the file's own format.
accept() {
  local file="$1" old="$2" new="$3" date="$4" pkg="$5" migrations="$6" summary="$7" dry="$8"
  GUARD_FILE="$file" OLD="$old" NEW="$new" DATE="$date" PKG="$pkg" MIGRATIONS="$migrations" SUMMARY="$summary" DRY="$dry" python3 - <<'PY'
import os, re, sys, textwrap
f = os.environ["GUARD_FILE"]; old = os.environ["OLD"]; new = os.environ["NEW"]
date = os.environ["DATE"]; pkg = os.environ["PKG"]; migrations = os.environ["MIGRATIONS"]
summary = os.environ["SUMMARY"]; dry = os.environ["DRY"] == "1"
s = open(f).read()
kv = dict(p.split("=", 1) for p in summary.split()[1:])
para = (
    f"{new}: verified {date} (scripts/signal-schema-accept.sh, the verify-and-accept "
    f"flow of topos-plugins#23), against {pkg}. Signal Desktop's migrations between "
    f"{old} and {new}: {migrations}. Verified via the same unchanged tooling against the "
    f"real database at {new}: every read-set column present across all five tables; "
    f"readConversations {kv['conversations']} rows; readMessages {kv['messages']} records "
    f"across {kv['probed']} probed conversations, {kv['attachments']} with attachments, "
    f"{kv['reactions']} with reactions."
)
lines = textwrap.wrap(para, width=66)
bullet = "\n".join(["//   - " + lines[0]] + ["//     " + l for l in lines[1:]]) + "\n"
marker = "//\n// Raising this constant is a deliberate act,"
if marker not in s:
    sys.exit("signal-schema-accept: schemaguard.go has no 'Raising this constant' paragraph to anchor on")
s2 = s.replace(marker, bullet + marker, 1)
const_old = f"const highestSupportedSchemaVersion = {old}\n"
if const_old not in s2:
    sys.exit(f"signal-schema-accept: constant line for {old} not found")
s2 = s2.replace(const_old, f"const highestSupportedSchemaVersion = {new}\n", 1)
if dry:
    import difflib
    sys.stdout.writelines(difflib.unified_diff(s.splitlines(True), s2.splitlines(True), f"a/{f}", f"b/{f}"))
else:
    open(f, "w").write(s2)
PY
}

main() {
  local dry=0
  [ "${1:-}" = "--dry-run" ] && dry=1
  local script_dir repo_root
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  repo_root="$(cd "$script_dir/.." && pwd)"
  cd "$repo_root"
  local file="${SIGNAL_SCHEMA_GUARD_FILE:-$GUARD_FILE_DEFAULT}"
  local old; old="$(current_ceiling "$file")"
  [ -n "$old" ] || fail "no 'const highestSupportedSchemaVersion = N' line in $file"

  echo "signal-schema-accept: ceiling is $old; running the live read-set test against ~/.config/Signal (read-only)"
  local out rc=0
  out="$(WEBSPACES_SIGNAL_LIVE_SCHEMA=1 CGO_ENABLED=1 go test -tags libsqlcipher -run TestLiveSchemaReadSet -v ./plugins/signal/ 2>&1)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    printf '%s\n' "$out" | tail -40 >&2
    fail "the live read-set test did not pass — NOTHING is accepted. The plugin's read set is broken at the database's version; fix the plugin first (plugins/signal/schema_readset.go, plugin.go), then rerun."
  fi
  printf '%s\n' "$out" | grep -q -- "--- PASS: TestLiveSchemaReadSet" \
    || { printf '%s\n' "$out" | tail -10 >&2; fail "the live test did not run (skipped?) — it needs a cgo toolchain, the system sqlcipher package, and a Signal Desktop database at ~/.config/Signal"; }
  local summary; summary="$(printf '%s\n' "$out" | grep -o 'LIVE_SCHEMA_SUMMARY .*' | head -1)"
  [ -n "$summary" ] || fail "no LIVE_SCHEMA_SUMMARY line in the live test's output"
  local new; new="$(sed -nE 's/.*version=([0-9]+).*/\1/p' <<<"$summary")"
  [ -n "$new" ] || fail "no version in the summary line: $summary"

  if nothing_to_accept "$old" "$new"; then
    echo "signal-schema-accept: nothing to accept — the database is at schema $new, the ceiling is $old, and the read set is intact ($summary)"
    exit 0
  fi

  echo "signal-schema-accept: the database is at $new, above the ceiling $old, and the read set is intact — accepting"
  local migrations pkg date
  migrations="$(migrations_between "$old" "$new")"
  pkg="$(package_version)"
  date="$(date -u +%Y-%m-%d)"
  echo "signal-schema-accept: migrations in ($old, $new]: $migrations"
  echo "signal-schema-accept: package: $pkg"
  accept "$file" "$old" "$new" "$date" "$pkg" "$migrations" "$summary" "$dry"
  if [ "$dry" -eq 1 ]; then
    echo "signal-schema-accept: dry run — nothing written"
    exit 0
  fi
  gofmt -l "$file" | grep -q . && fail "the rewrite left $file unformatted — refusing to leave it that way; inspect the diff" || true
  git --no-pager diff -- "$file" || true
  echo "signal-schema-accept: $file now accepts up to $new. Review the diff above, run 'make test-signal', and commit it through a task — this script never commits."
}

# Source-guard: executed directly, run the flow; sourced (the smoke test
# seam), define functions only and do nothing else.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
