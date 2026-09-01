#!/usr/bin/env bash
#
# scripts/signal-schema-accept-smoke.sh — hermetic proof of the
# verify-and-accept rewrite (topos-plugins#23). No cgo, no database, no
# network: sources scripts/signal-schema-accept.sh and drives its
# functions against a fixture copy of plugins/signal/schemaguard.go.
set -euo pipefail

fail() {
  echo "signal-schema-accept-smoke: FAIL: $*" >&2
  exit 1
}

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/signal-schema-accept.sh
source ./scripts/signal-schema-accept.sh

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cp plugins/signal/schemaguard.go "$TMP/schemaguard.go"

echo "==> Case: the ceiling is read from the constant"
OLD="$(current_ceiling "$TMP/schemaguard.go")"
[ -n "$OLD" ] || fail "current_ceiling found no constant"
[ "$OLD" = "$(sed -nE 's/^const highestSupportedSchemaVersion = ([0-9]+)$/\1/p' plugins/signal/schemaguard.go)" ] || fail "current_ceiling disagrees with the file: $OLD"
echo "==> Case PASS: the ceiling is read from the constant ($OLD)"

echo "==> Case: at or below the ceiling there is nothing to accept"
nothing_to_accept "$OLD" "$OLD" || fail "equal version was not a no-op"
nothing_to_accept "$OLD" "$((OLD - 10))" || fail "lower version was not a no-op"
nothing_to_accept "$OLD" "$((OLD + 10))" && fail "higher version was treated as a no-op" || true
echo "==> Case PASS: at or below the ceiling there is nothing to accept"

echo "==> Case: the migrations list is injectable, and the offline placeholder is explicit"
M="$(SIGNAL_SCHEMA_MIGRATIONS="1790-a.std.ts,1800-b.std.ts" migrations_between "$OLD" "$((OLD + 20))")"
[ "$M" = "1790-a.std.ts,1800-b.std.ts" ] || fail "injected migrations not honoured: $M"
echo "==> Case PASS: the migrations list is injectable"

echo "==> Case: accept rewrites the fixture — constant, bullet, position, gofmt"
NEW="$((OLD + 20))"
SUMMARY="LIVE_SCHEMA_SUMMARY version=$NEW conversations=210 probed=5 messages=279 attachments=29 reactions=22"
accept "$TMP/schemaguard.go" "$OLD" "$NEW" "2026-09-02" "signal-desktop 8.30.0-1" "1790-a.std.ts,1800-b.std.ts" "$SUMMARY" 0
[ "$(current_ceiling "$TMP/schemaguard.go")" = "$NEW" ] || fail "constant not raised to $NEW"
grep -q "^//   - $NEW: verified 2026-09-02 (scripts/signal-schema-accept.sh" "$TMP/schemaguard.go" || fail "provenance bullet missing or malformed"
grep -q "1790-a.std.ts,1800-b.std.ts" "$TMP/schemaguard.go" || fail "migrations not recorded"
grep -q "readConversations 210 rows; readMessages 279 records" "$TMP/schemaguard.go" || fail "live counts not recorded"
# the bullet sits immediately before the "Raising this constant" paragraph, after the previous bullet
BULLET_LINE="$(grep -n "^//   - $NEW: verified" "$TMP/schemaguard.go" | cut -d: -f1)"
RAISING_LINE="$(grep -n "^// Raising this constant is a deliberate act" "$TMP/schemaguard.go" | cut -d: -f1)"
PREV_LINE="$(grep -n "^//   - $OLD: verified" "$TMP/schemaguard.go" | cut -d: -f1)"
[ "$PREV_LINE" -lt "$BULLET_LINE" ] || fail "new bullet is not after the $OLD bullet"
[ "$BULLET_LINE" -lt "$RAISING_LINE" ] || fail "new bullet is not before the Raising paragraph"
awk -v b="$BULLET_LINE" -v r="$RAISING_LINE" 'NR>b && NR<r' "$TMP/schemaguard.go" | grep -vE '^//( |$)' | grep -q . && fail "non-comment text between the bullet and the Raising paragraph" || true
[ -z "$(gofmt -l "$TMP/schemaguard.go")" ] || fail "rewritten fixture is not gofmt-clean"
# every bullet line stays within the file's comment width
awk -v b="$BULLET_LINE" -v r="$RAISING_LINE" 'NR>=b && NR<r && length($0) > 78 {print; exit 1}' "$TMP/schemaguard.go" || fail "a bullet line exceeds 78 columns"
echo "==> Case PASS: accept rewrites the fixture"

echo "==> Case: --dry-run writes nothing"
cp plugins/signal/schemaguard.go "$TMP/dry.go"
OUT="$(accept "$TMP/dry.go" "$OLD" "$NEW" "2026-09-02" "pkg" "m" "$SUMMARY" 1)"
printf '%s' "$OUT" | grep -q "^+const highestSupportedSchemaVersion = $NEW" || fail "dry run did not print the constant change"
cmp -s "$TMP/dry.go" plugins/signal/schemaguard.go || fail "dry run modified the file"
echo "==> Case PASS: --dry-run writes nothing"

# ---------------------------------------------------------------------
# main, end to end, through the two seams: every refusal leaves the
# fixture byte-for-byte unchanged; only a passed test with a summary
# above the ceiling (and a migrations list) writes.
# ---------------------------------------------------------------------
export SIGNAL_SCHEMA_GUARD_FILE="$TMP/main.go"
PASS_LINE='--- PASS: TestLiveSchemaReadSet (0.01s)'
SUMMARY_HI="LIVE_SCHEMA_SUMMARY version=$NEW conversations=210 probed=5 messages=279 attachments=29 reactions=22"
SUMMARY_EQ="LIVE_SCHEMA_SUMMARY version=$OLD conversations=210 probed=5 messages=279 attachments=29 reactions=22"

# refuse_case <name> <live-cmd> <expected-substring> [env=val ...]: main must
# exit non-zero, say why, and leave the fixture untouched.
refuse_case() {
  local name="$1" live="$2" expect="$3"; shift 3
  cp plugins/signal/schemaguard.go "$SIGNAL_SCHEMA_GUARD_FILE"
  local rc=0 out
  out="$(env "$@" SIGNAL_SCHEMA_LIVE_CMD="$live" bash -c 'source ./scripts/signal-schema-accept.sh; main' 2>&1)" || rc=$?
  [ "$rc" -ne 0 ] || fail "$name: main did not refuse"
  printf '%s' "$out" | grep -q -- "$expect" || fail "$name: refusal did not say why (expected '$expect'):
$out"
  cmp -s "$SIGNAL_SCHEMA_GUARD_FILE" plugins/signal/schemaguard.go || fail "$name: the guard file was modified despite the refusal"
  echo "==> Case PASS: refuses when $name"
}
echo "==> Case: main refuses without writing"
refuse_case "the live test fails"            "printf '%s\n' '--- FAIL: TestLiveSchemaReadSet (0.01s)'; exit 1"  "did not pass — NOTHING is accepted"
refuse_case "the live test is skipped"       "printf '%s\n' '--- SKIP: TestLiveSchemaReadSet (0.00s)'; exit 0"  "did not run"
refuse_case "the summary line is missing"    "printf '%s\n' '$PASS_LINE'; exit 0"                                "no LIVE_SCHEMA_SUMMARY"
refuse_case "the summary has no version"     "printf '%s\n' '$PASS_LINE' 'LIVE_SCHEMA_SUMMARY conversations=1'; exit 0" "no version in the summary"
refuse_case "upstream is offline and no override is set" "printf '%s\n' '$PASS_LINE' '$SUMMARY_HI'; exit 0" "could not list Signal Desktop's migrations" SIGNAL_SCHEMA_GH=false SIGNAL_SCHEMA_ACCEPT_OFFLINE=
echo "==> Case PASS: main refuses without writing"

echo "==> Case: main is a no-op at the ceiling"
cp plugins/signal/schemaguard.go "$SIGNAL_SCHEMA_GUARD_FILE"
OUT="$(SIGNAL_SCHEMA_LIVE_CMD="printf '%s\n' '$PASS_LINE' '$SUMMARY_EQ'; exit 0" SIGNAL_SCHEMA_GH=false bash -c 'source ./scripts/signal-schema-accept.sh; main' 2>&1)" || fail "no-op run exited non-zero: $OUT"
printf '%s' "$OUT" | grep -q "nothing to accept" || fail "no-op run did not say so: $OUT"
cmp -s "$SIGNAL_SCHEMA_GUARD_FILE" plugins/signal/schemaguard.go || fail "no-op run modified the guard file"
echo "==> Case PASS: main is a no-op at the ceiling"

echo "==> Case: main writes only when the test passed above the ceiling"
cp plugins/signal/schemaguard.go "$SIGNAL_SCHEMA_GUARD_FILE"
OUT="$(SIGNAL_SCHEMA_LIVE_CMD="printf '%s\n' '$PASS_LINE' '$SUMMARY_HI'; exit 0" SIGNAL_SCHEMA_MIGRATIONS="1790-a.std.ts,1800-b.std.ts" bash -c 'source ./scripts/signal-schema-accept.sh; main' 2>&1)" || fail "accepting run exited non-zero: $OUT"
[ "$(current_ceiling "$SIGNAL_SCHEMA_GUARD_FILE")" = "$NEW" ] || fail "accepting run did not raise the constant"
grep -q "^//   - $NEW: verified" "$SIGNAL_SCHEMA_GUARD_FILE" || fail "accepting run wrote no provenance bullet"
printf '%s' "$OUT" | grep -q "now accepts up to $NEW" || fail "accepting run did not report: $OUT"
echo "==> Case PASS: main writes only when the test passed above the ceiling"

echo "==> Case: the offline placeholder is explicit when overridden"
cp plugins/signal/schemaguard.go "$SIGNAL_SCHEMA_GUARD_FILE"
SIGNAL_SCHEMA_LIVE_CMD="printf '%s\n' '$PASS_LINE' '$SUMMARY_HI'; exit 0" SIGNAL_SCHEMA_GH=false SIGNAL_SCHEMA_ACCEPT_OFFLINE=1 bash -c 'source ./scripts/signal-schema-accept.sh; main' >/dev/null 2>&1 || fail "offline override run failed"
grep -q "MIGRATIONS NOT LISTED (offline run" "$SIGNAL_SCHEMA_GUARD_FILE" || fail "offline placeholder not written"
echo "==> Case PASS: the offline placeholder is explicit when overridden"
unset SIGNAL_SCHEMA_GUARD_FILE

echo "signal-schema-accept-smoke: all cases passed"
