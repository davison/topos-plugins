#!/usr/bin/env bash
# Hermetic gate for `make install` / `make uninstall` (M1-R5): builds a
# fixture release on local disk — a real plugin binary, a real signed
# provenance manifest under a throwaway key, and a verifier relinked to
# accept that key — and installs from it with NO network beyond the Go
# module cache and NO credentials, then asserts every refusal and every
# repair behaviour by name.
#
# The fixture release is a directory tree shaped like the real GitHub
# release surface — download/<tag>/ holding the flat assets release.yml
# publishes plus a checksums.txt over them — served to scripts/install.sh
# through its TOPOS_PLUGINS_RELEASE_BASE_URL test seam as a file:// URL.
# That seam changes WHICH release is fetched, never which checks run:
# every checksum, allowlist, provenance and placement rule runs
# identically here and against the real CDN.
#
# The verifier is built at the kernel revision TOPOS_PROVENANCE_REF names
# — the same one `make verifier` and the release workflow build — first
# as shipped, then relinked with the throwaway public key through the
# kernel's link-time provenanceKeysExtra seam (davison/topos D-12), the
# same relink the kernel's own install-smoke performs. The relinked
# verifier is what the fixture release ships and what the post-install
# assertions call — a real verification against a real key policy, never
# a self-signed check.
#
# House style follows the kernel's scripts/install-smoke.sh: required-
# tool preflight loop, mktemp -d work dir, cleanup trap, loud FAIL:
# messages naming the specific violation. Nothing here binds a port or
# writes outside $WORK. Live replacement over a running process is not
# rehearsed here (there is no kernel to run the fixture under); the
# tmp-then-rename placement that makes it safe is the kernel install
# script's proven discipline, carried over verbatim.
#
# Every install below runs with PATH stripped of any directory holding a
# topos-provenance (PATH_NO_VERIFIER), so the resolution order is what
# the fixture dictates — never whatever the operator happens to have on
# PATH.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

for tool in curl sha256sum mktemp go cmp stat; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: required tool '$tool' not found on PATH" >&2
    exit 1
  fi
done

WORK="$(mktemp -d)"
cleanup() {
  # An unwritable-prefix case leaves a read-only directory behind; make
  # it writable again so the work tree can be removed.
  chmod -R u+w "$WORK" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

REF="$(sed -e 's/#.*//' -e '/^[[:space:]]*$/d' TOPOS_PROVENANCE_REF | head -n1)"
[ -n "$REF" ] || fail "TOPOS_PROVENANCE_REF names no revision"

PATH_NO_VERIFIER=""
IFS=: read -ra PATH_ENTRIES <<< "$PATH"
for d in "${PATH_ENTRIES[@]}"; do
  [ -n "$d" ] || continue
  [ -x "$d/topos-provenance" ] && continue
  PATH_NO_VERIFIER="${PATH_NO_VERIFIER:+$PATH_NO_VERIFIER:}$d"
done

# ---------------------------------------------------------------------
# Step 1: the verifier at the pin, a throwaway key, the relinked verifier.
# ---------------------------------------------------------------------
echo "==> building topos-provenance at $REF"
GOWORK=off GOBIN="$WORK/unsigned" CGO_ENABLED=0 \
  go install "github.com/davison/topos/cmd/topos-provenance@$REF" \
  || fail "go install topos-provenance@$REF"

mkdir -p "$WORK/keys"
KEY_SPEC="$("$WORK/unsigned/topos-provenance" keygen --key-id install-smoke --out-dir "$WORK/keys")" \
  || fail "keygen failed"
[ -n "$KEY_SPEC" ] || fail "keygen printed an empty key spec"

echo "==> relinking the verifier to accept the throwaway key"
GOWORK=off GOBIN="$WORK/verifier" CGO_ENABLED=0 \
  go install -ldflags "-X github.com/davison/topos/kernel/pluginhost.provenanceKeysExtra=$KEY_SPEC" \
  "github.com/davison/topos/cmd/topos-provenance@$REF" \
  || fail "relink topos-provenance with provenanceKeysExtra"
VERIFIER="$WORK/verifier/topos-provenance"

# ---------------------------------------------------------------------
# Step 2: two fixture binaries with different bytes (the second stripped)
# — the demo plugin, so the fixture is a real plugin binary shape.
# ---------------------------------------------------------------------
echo "==> building fixture plugin binaries"
mkdir -p "$WORK/fixture-a" "$WORK/fixture-b"
CGO_ENABLED=0 go build -o "$WORK/fixture-a/topos-plugin-smoke" ./cmd/topos-plugin-demo || fail "build fixture a"
CGO_ENABLED=0 go build -ldflags=-s -o "$WORK/fixture-b/topos-plugin-smoke" ./cmd/topos-plugin-demo || fail "build fixture b"
if cmp -s "$WORK/fixture-a/topos-plugin-smoke" "$WORK/fixture-b/topos-plugin-smoke"; then
  fail "the two fixture builds are byte-identical — the update case needs different bytes"
fi

# build_release <release-root> <tag> <binary> [ship_verifier=1] [sign=1]
# Assembles download/<tag>/ under <release-root>: the binary as
# topos-plugin-smoke, its signed manifest pair (when sign=1), the
# relinked verifier as topos-provenance (when ship_verifier=1), and a
# checksums.txt over every asset present.
build_release() {
  local root="$1" tag="$2" bin="$3" ship_verifier="${4:-1}" sign="${5:-1}"
  local dir="$root/download/$tag"
  mkdir -p "$dir"
  cp "$bin" "$dir/topos-plugin-smoke"
  if [ "$sign" = 1 ]; then
    "$VERIFIER" sign \
      --key-id install-smoke \
      --repo davison/topos-plugins \
      --tag "$tag" \
      --version "${tag#v}" \
      --key-file "$WORK/keys/install-smoke.key" \
      --out-dir "$dir" \
      "$dir/topos-plugin-smoke" >/dev/null \
      || fail "sign fixture release $tag"
  fi
  if [ "$ship_verifier" = 1 ]; then
    cp "$VERIFIER" "$dir/topos-provenance"
  fi
  regen_checksums "$dir"
}

# regen_checksums <release-dir>: rewrites checksums.txt over every
# regular file present except itself (flat, sorted — release.yml's own
# shape).
regen_checksums() {
  (cd "$1" && find . -maxdepth 1 -type f ! -name checksums.txt -printf '%f\n' | LC_ALL=C sort | xargs sha256sum > checksums.txt)
}

# run_install <prefix> <base-url> <tag>: runs install.sh, tolerating a
# non-zero exit; captures combined output into $INSTALL_OUT and the
# exit status into $INSTALL_RC. Assertions are the caller's.
run_install() {
  INSTALL_RC=0
  INSTALL_OUT="$(PATH="$PATH_NO_VERIFIER" PREFIX="$1" TOPOS_PLUGINS_RELEASE_BASE_URL="$2" ./scripts/install.sh "$3" 2>&1)" \
    || INSTALL_RC=$?
}

# assert_prefix_untouched <prefix>: no files placed. The writability
# preflight may legitimately have created the empty plugins directory —
# a failed install's defined state is "no FILES placed".
assert_prefix_untouched() {
  local prefix="$1"
  if [ -e "$prefix/bin" ]; then
    fail "a failed install created $prefix/bin"
  fi
  if [ -d "$prefix/lib/topos/plugins" ] && [ -n "$(ls -A "$prefix/lib/topos/plugins")" ]; then
    fail "a failed install left entries in $prefix/lib/topos/plugins:
$(ls -A "$prefix/lib/topos/plugins")"
  fi
}

# manifest_of_prefix <dir>: recursive path+SHA-256 manifest, sorted.
manifest_of_prefix() {
  (cd "$1" && find . -type f | LC_ALL=C sort | xargs -r sha256sum)
}

TAG_A="v9.0.1-smoke"
TAG_B="v9.0.2-smoke"
MANIFEST_A="topos-plugins-$TAG_A.provenance.json"
MANIFEST_B="topos-plugins-$TAG_B.provenance.json"

echo "==> building fixture releases $TAG_A and $TAG_B"
build_release "$WORK/release" "$TAG_A" "$WORK/fixture-a/topos-plugin-smoke"
build_release "$WORK/release" "$TAG_B" "$WORK/fixture-b/topos-plugin-smoke"

# ---------------------------------------------------------------------
# Case: happy path — the shipped verifier is the only one resolvable,
# the binary and its manifest pair land with the right modes, nothing
# else is written, and the installed directory verifies.
# ---------------------------------------------------------------------
echo "==> Case: install from fixture release"
PREFIX_DIR="$WORK/prefix"
run_install "$PREFIX_DIR" "file://$WORK/release" "$TAG_A"
[ "$INSTALL_RC" -eq 0 ] || fail "install failed (rc=$INSTALL_RC)
$INSTALL_OUT"
PLUGINS="$PREFIX_DIR/lib/topos/plugins"
for spec in "topos-plugin-smoke:755" "$MANIFEST_A:644" "${MANIFEST_A%.json}.sig:644"; do
  path="$PLUGINS/${spec%%:*}"; want="${spec##*:}"
  [ -f "$path" ] || fail "expected installed file missing: $path"
  mode="$(stat -c '%a' "$path")"
  [ "$mode" = "$want" ] || fail "expected mode $want on $path, got $mode"
done
cmp -s "$PLUGINS/topos-plugin-smoke" "$WORK/fixture-a/topos-plugin-smoke" || fail "placed binary differs from the release's"
[ ! -e "$PLUGINS/topos-provenance" ] || fail "the staged verifier was placed into $PLUGINS — it must never be"
[ ! -e "$PREFIX_DIR/bin" ] || fail "\$PREFIX/bin exists after install — this installer must never create it"
if [ -n "$(find "$PLUGINS" -name '.topos-plugins-install.*')" ]; then
  fail "a temporary placement file was left behind in $PLUGINS"
fi
printf '%s' "$INSTALL_OUT" | grep -q "verifier: .*/topos-provenance" || fail "install output did not name the verifier it used
$INSTALL_OUT"
"$VERIFIER" verify --dir "$PLUGINS" >"$WORK/verify-a.out" 2>&1 || fail "installed directory does not verify:
$(cat "$WORK/verify-a.out")"
grep -q "topos-plugin-smoke: OK ($MANIFEST_A)" "$WORK/verify-a.out" || fail "verify did not name $MANIFEST_A as the evidence:
$(cat "$WORK/verify-a.out")"
echo "==> Case PASS: install from fixture release"

# ---------------------------------------------------------------------
# Case: update — a newer tag into the same prefix replaces the binary,
# leaves the older manifest pair beside the newer one, says so, and the
# directory still verifies (the kernel's any-match scan, D-08).
# ---------------------------------------------------------------------
echo "==> Case: update to a newer release in place"
run_install "$PREFIX_DIR" "file://$WORK/release" "$TAG_B"
[ "$INSTALL_RC" -eq 0 ] || fail "update failed (rc=$INSTALL_RC)
$INSTALL_OUT"
cmp -s "$PLUGINS/topos-plugin-smoke" "$WORK/fixture-b/topos-plugin-smoke" || fail "update did not replace the binary with the newer release's bytes"
for f in "$MANIFEST_A" "${MANIFEST_A%.json}.sig" "$MANIFEST_B" "${MANIFEST_B%.json}.sig"; do
  [ -f "$PLUGINS/$f" ] || fail "after update, expected $f in $PLUGINS"
done
printf '%s' "$INSTALL_OUT" | grep -q "1 older release manifest(s) remain beside $MANIFEST_B" || fail "update did not report the older manifest left beside the new one
$INSTALL_OUT"
"$VERIFIER" verify --dir "$PLUGINS" >"$WORK/verify-b.out" 2>&1 || fail "updated directory does not verify:
$(cat "$WORK/verify-b.out")"
grep -q "topos-plugin-smoke: OK ($MANIFEST_B)" "$WORK/verify-b.out" || fail "after update, verify did not name $MANIFEST_B:
$(cat "$WORK/verify-b.out")"
echo "==> Case PASS: update to a newer release in place"

# ---------------------------------------------------------------------
# Case: corrupted asset — a byte appended after checksums.txt was
# written; the install aborts naming the asset and places nothing.
# ---------------------------------------------------------------------
echo "==> Case: corrupted asset"
CORRUPT="$WORK/release-corrupt"
mkdir -p "$CORRUPT/download"
cp -r "$WORK/release/download/$TAG_A" "$CORRUPT/download/$TAG_A"
printf 'x' >> "$CORRUPT/download/$TAG_A/topos-plugin-smoke"
run_install "$WORK/prefix-corrupt" "file://$CORRUPT" "$TAG_A"
[ "$INSTALL_RC" -ne 0 ] || fail "corrupted asset: install exited 0, expected a checksum refusal"
printf '%s' "$INSTALL_OUT" | grep -q "SHA-256 verification failed" || fail "corrupted asset: not a checksum refusal
$INSTALL_OUT"
printf '%s' "$INSTALL_OUT" | grep -q "topos-plugin-smoke" || fail "corrupted asset: refusal did not name the asset
$INSTALL_OUT"
assert_prefix_untouched "$WORK/prefix-corrupt"
echo "==> Case PASS: corrupted asset"

# ---------------------------------------------------------------------
# Case: tampered after signing — the binary is altered AND checksums.txt
# regenerated to match (the attacker who controls the release pipeline
# but not the signing key). SHA-256 passes; provenance must refuse,
# naming the binary; nothing is placed. This is the case mandatory
# provenance exists for.
# ---------------------------------------------------------------------
echo "==> Case: tampered after signing (checksums regenerated)"
TAMPER="$WORK/release-tamper"
mkdir -p "$TAMPER/download"
cp -r "$WORK/release/download/$TAG_A" "$TAMPER/download/$TAG_A"
printf 'x' >> "$TAMPER/download/$TAG_A/topos-plugin-smoke"
regen_checksums "$TAMPER/download/$TAG_A"
run_install "$WORK/prefix-tamper" "file://$TAMPER" "$TAG_A"
[ "$INSTALL_RC" -ne 0 ] || fail "tampered: install exited 0, expected a provenance refusal"
printf '%s' "$INSTALL_OUT" | grep -q "provenance verification failed" || fail "tampered: not a provenance refusal
$INSTALL_OUT"
printf '%s' "$INSTALL_OUT" | grep -q "topos-plugin-smoke" || fail "tampered: refusal did not name the binary
$INSTALL_OUT"
assert_prefix_untouched "$WORK/prefix-tamper"
echo "==> Case PASS: tampered after signing"

# ---------------------------------------------------------------------
# Case: unsigned release — no manifest pair in checksums.txt at all.
# Refused before any binary is fetched, by name.
# ---------------------------------------------------------------------
echo "==> Case: release without a provenance manifest is refused"
build_release "$WORK/release-unsigned" "$TAG_A" "$WORK/fixture-a/topos-plugin-smoke" 1 0
run_install "$WORK/prefix-unsigned" "file://$WORK/release-unsigned" "$TAG_A"
[ "$INSTALL_RC" -ne 0 ] || fail "unsigned: install exited 0, expected a refusal"
printf '%s' "$INSTALL_OUT" | grep -q "exactly one signed provenance manifest pair" || fail "unsigned: refusal did not name the missing manifest pair
$INSTALL_OUT"
assert_prefix_untouched "$WORK/prefix-unsigned"
echo "==> Case PASS: release without a provenance manifest is refused"

# ---------------------------------------------------------------------
# Case: no verifier resolvable — the release ships evidence but no
# topos-provenance, nothing on PATH, no installed kernel copy. A loud
# abort naming the three places checked; nothing placed.
# ---------------------------------------------------------------------
echo "==> Case: no verifier resolvable"
build_release "$WORK/release-noverifier" "$TAG_A" "$WORK/fixture-a/topos-plugin-smoke" 0 1
run_install "$WORK/prefix-noverifier" "file://$WORK/release-noverifier" "$TAG_A"
[ "$INSTALL_RC" -ne 0 ] || fail "no verifier: install exited 0, expected a refusal"
printf '%s' "$INSTALL_OUT" | grep -q "no topos-provenance verifier could be resolved" || fail "no verifier: refusal did not say so
$INSTALL_OUT"
assert_prefix_untouched "$WORK/prefix-noverifier"
echo "==> Case PASS: no verifier resolvable"

# ---------------------------------------------------------------------
# Case: resolution order — an installed kernel's $PREFIX/bin/
# topos-provenance is preferred over the release's shipped copy. The
# seeded one is a stub that refuses with a marker; the install must
# fail carrying that marker, proving the staged copy was never
# consulted while a more trustworthy candidate existed.
# ---------------------------------------------------------------------
echo "==> Case: installed verifier preferred over the shipped one"
SEED_PREFIX="$WORK/prefix-seeded"
mkdir -p "$SEED_PREFIX/bin"
cat > "$SEED_PREFIX/bin/topos-provenance" <<'STUB'
#!/bin/sh
echo "SEEDED-VERIFIER-CONSULTED"
exit 1
STUB
chmod 0755 "$SEED_PREFIX/bin/topos-provenance"
SEED_DIGEST="$(sha256sum "$SEED_PREFIX/bin/topos-provenance")"
run_install "$SEED_PREFIX" "file://$WORK/release" "$TAG_A"
[ "$INSTALL_RC" -ne 0 ] || fail "seeded verifier: install exited 0 — the stub verifier was not the one consulted"
printf '%s' "$INSTALL_OUT" | grep -q "SEEDED-VERIFIER-CONSULTED" || fail "seeded verifier: the installed copy was not preferred over the shipped one
$INSTALL_OUT"
[ "$(sha256sum "$SEED_PREFIX/bin/topos-provenance")" = "$SEED_DIGEST" ] || fail "seeded verifier: the installer altered \$PREFIX/bin/topos-provenance"
if [ -d "$SEED_PREFIX/lib/topos/plugins" ] && [ -n "$(ls -A "$SEED_PREFIX/lib/topos/plugins")" ]; then
  fail "seeded verifier: a refused install placed files"
fi
echo "==> Case PASS: installed verifier preferred over the shipped one"

# ---------------------------------------------------------------------
# Case: traversal-shaped checksums.txt — a manifest line whose name
# would escape the staging tree is rejected by name; nothing is placed.
# ---------------------------------------------------------------------
echo "==> Case: traversal-shaped checksums.txt"
TRAV="$WORK/release-traversal"
mkdir -p "$TRAV/download"
cp -r "$WORK/release/download/$TAG_A" "$TRAV/download/$TAG_A"
printf '%s  ../topos-plugin-escape\n' "$(printf 'x' | sha256sum | cut -d' ' -f1)" >> "$TRAV/download/$TAG_A/checksums.txt"
run_install "$WORK/prefix-traversal" "file://$TRAV" "$TAG_A"
[ "$INSTALL_RC" -ne 0 ] || fail "traversal: install exited 0, expected a rejection"
printf '%s' "$INSTALL_OUT" | grep -q "disallowed path (rejected): .*\.\./topos-plugin-escape" || fail "traversal: the offending line was not rejected by name
$INSTALL_OUT"
assert_prefix_untouched "$WORK/prefix-traversal"
[ ! -e "$WORK/topos-plugin-escape" ] || fail "traversal: a file escaped the staging tree"
echo "==> Case PASS: traversal-shaped checksums.txt"

# ---------------------------------------------------------------------
# Case: unwritable prefix — fails loud naming the directory and the sudo
# re-run form, never escalates, leaves the target read-only and empty.
# Skipped as root, who can write anywhere.
# ---------------------------------------------------------------------
if [ "$(id -u)" -ne 0 ]; then
  echo "==> Case: unwritable prefix"
  RO_PREFIX="$WORK/prefix-readonly"
  mkdir -p "$RO_PREFIX"
  chmod 0555 "$RO_PREFIX"
  run_install "$RO_PREFIX" "file://$WORK/release" "$TAG_A"
  [ "$INSTALL_RC" -ne 0 ] || fail "unwritable prefix: install exited 0"
  printf '%s' "$INSTALL_OUT" | grep -q "cannot write to $RO_PREFIX/lib/topos/plugins" || fail "unwritable prefix: refusal did not name the directory
$INSTALL_OUT"
  printf '%s' "$INSTALL_OUT" | grep -q "sudo make install" || fail "unwritable prefix: refusal did not name the escalated re-run
$INSTALL_OUT"
  [ -z "$(ls -A "$RO_PREFIX")" ] || fail "unwritable prefix: something was created inside the read-only prefix"
  chmod 0755 "$RO_PREFIX"
  echo "==> Case PASS: unwritable prefix"
else
  echo "==> Case SKIP: unwritable prefix (running as root)"
fi

# ---------------------------------------------------------------------
# Case: idempotent re-run — two installs of one tag leave byte-identical
# prefix trees.
# ---------------------------------------------------------------------
echo "==> Case: idempotent re-run"
IDEM_PREFIX="$WORK/prefix-idem"
run_install "$IDEM_PREFIX" "file://$WORK/release" "$TAG_A"
[ "$INSTALL_RC" -eq 0 ] || fail "idempotent re-run: first install failed (rc=$INSTALL_RC)
$INSTALL_OUT"
manifest_of_prefix "$IDEM_PREFIX" > "$WORK/idem-1"
run_install "$IDEM_PREFIX" "file://$WORK/release" "$TAG_A"
[ "$INSTALL_RC" -eq 0 ] || fail "idempotent re-run: second install failed (rc=$INSTALL_RC)
$INSTALL_OUT"
manifest_of_prefix "$IDEM_PREFIX" > "$WORK/idem-2"
cmp -s "$WORK/idem-1" "$WORK/idem-2" || fail "idempotent re-run: the second install changed bytes in $IDEM_PREFIX"
echo "==> Case PASS: idempotent re-run"

# ---------------------------------------------------------------------
# Case: uninstall — after two releases were installed in place, with a
# seeded kernel at $PREFIX/bin/topos and a foreign file in the plugins
# directory: every binary and every manifest pair goes, the kernel and
# the foreign file stay byte-identical, the directory survives and is
# reported as not empty; once the foreign file is gone, a second run
# removes the empty directories; a third is a clean no-op.
# ---------------------------------------------------------------------
echo "==> Case: uninstall data-safety cycle"
mkdir -p "$PREFIX_DIR/bin"
printf 'not a real kernel\n' > "$PREFIX_DIR/bin/topos"
printf 'operator notes\n' > "$PLUGINS/README.operator"
KERNEL_DIGEST="$(sha256sum "$PREFIX_DIR/bin/topos")"
FOREIGN_DIGEST="$(sha256sum "$PLUGINS/README.operator")"
UN_OUT="$(PREFIX="$PREFIX_DIR" ./scripts/uninstall.sh 2>&1)" || fail "uninstall exited non-zero
$UN_OUT"
for f in topos-plugin-smoke "$MANIFEST_A" "${MANIFEST_A%.json}.sig" "$MANIFEST_B" "${MANIFEST_B%.json}.sig"; do
  [ ! -e "$PLUGINS/$f" ] || fail "uninstall left $PLUGINS/$f"
done
[ "$(sha256sum "$PREFIX_DIR/bin/topos")" = "$KERNEL_DIGEST" ] || fail "uninstall touched the kernel at \$PREFIX/bin/topos"
[ "$(sha256sum "$PLUGINS/README.operator")" = "$FOREIGN_DIGEST" ] || fail "uninstall touched the operator's foreign file"
printf '%s' "$UN_OUT" | grep -q "left in place (not empty): $PLUGINS" || fail "uninstall did not report the surviving directory
$UN_OUT"
printf '%s' "$UN_OUT" | grep -q "removed 5 file(s)" || fail "uninstall did not report removing exactly the 5 placed files
$UN_OUT"
rm "$PLUGINS/README.operator"
UN2_OUT="$(PREFIX="$PREFIX_DIR" ./scripts/uninstall.sh 2>&1)" || fail "second uninstall exited non-zero
$UN2_OUT"
[ ! -e "$PREFIX_DIR/lib/topos" ] || fail "second uninstall left $PREFIX_DIR/lib/topos after the foreign file was gone"
printf '%s' "$UN2_OUT" | grep -q "nothing left to remove" || fail "second uninstall did not report a no-op
$UN2_OUT"
UN3_OUT="$(PREFIX="$PREFIX_DIR" ./scripts/uninstall.sh 2>&1)" || fail "third uninstall exited non-zero
$UN3_OUT"
printf '%s' "$UN3_OUT" | grep -q "already absent" || fail "third uninstall did not report the absent directory
$UN3_OUT"
[ "$(sha256sum "$PREFIX_DIR/bin/topos")" = "$KERNEL_DIGEST" ] || fail "a later uninstall touched the kernel"
echo "==> Case PASS: uninstall data-safety cycle"

# ---------------------------------------------------------------------
# Case: latest-release URL validation, offline — install.sh is sourced
# (its source guard runs nothing) and validate_latest_url is driven
# directly: the one accepted shape, and each refusal by name.
# ---------------------------------------------------------------------
echo "==> Case: latest-release URL validation"
# shellcheck source=scripts/install.sh
source ./scripts/install.sh
GOOD="$(validate_latest_url "https://github.com/davison/topos-plugins/releases/tag/v0.2.0")" || fail "validator refused the accepted shape"
[ "$GOOD" = "v0.2.0" ] || fail "validator printed '$GOOD' for the accepted shape, expected v0.2.0"
expect_refusal() {
  local url="$1" why="$2" out rc=0
  out="$(validate_latest_url "$url" 2>&1)" || rc=$?
  [ "$rc" -ne 0 ] || fail "validator accepted $why: $url"
  printf '%s' "$out" | grep -q "latest-release" || fail "validator refusal for $why did not say why: $out"
}
expect_refusal "http://github.com/davison/topos-plugins/releases/tag/v0.2.0" "a non-https scheme"
expect_refusal "https://example.com/davison/topos-plugins/releases/tag/v0.2.0" "another host"
expect_refusal "https://github.com/davison/topos/releases/tag/v1.2.0" "another repository's release"
expect_refusal "https://github.com/davison/topos-plugins/releases/tag/v0.3.0-rc1" "a prerelease tag"
expect_refusal "https://github.com/davison/topos-plugins/releases/tag/nightly" "a moving tag"
expect_refusal "" "an empty URL"
echo "==> Case PASS: latest-release URL validation"

echo "install-smoke: all cases passed"
