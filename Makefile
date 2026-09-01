.PHONY: build build-signal test test-signal verifier install uninstall install-check install-signal uninstall-signal

# STATIC_PLUGINS names, in exactly one place, the plugins `make build`
# produces with CGO_ENABLED=0 — the same list release.yml builds, signs
# and publishes (it calls this target). Signal is deliberately absent:
# it dynamically links the system SQLCipher library, so it is built
# locally by `build-signal` and never shipped prebuilt.
STATIC_PLUGINS := filesystem gdrive paperless proton silverbullet whatsapp

# TOPOS_PROVENANCE_REF is the davison/topos revision the verifier is
# built at — read from the file of that name, the one place it is
# written (comment and blank lines dropped).
TOPOS_PROVENANCE_REF := $(shell sed -e 's/\#.*//' -e '/^[[:space:]]*$$/d' TOPOS_PROVENANCE_REF | head -n1)

# PREFIX is the install root `make install` places a release into:
# plugin binaries and their signed provenance pair at
# $(PREFIX)/lib/topos/plugins/ — the directory the installed topos
# kernel (at $(PREFIX)/bin/topos, placed by the kernel repository's own
# `make install`) resolves with its stock `[plugins] dir = "plugins"`.
# Override per invocation (`make install PREFIX=$$HOME/.local`) for a
# no-sudo user-local install; it must match the kernel's PREFIX.
PREFIX ?= /usr/local

# build compiles the static fleet into bin/ — the directory the kernel
# checkout's `make dev` adopts through DEV_PLUGINS_DIR (its default is
# ../topos-plugins/bin), hashing each binary into the dev kernel's
# link-time manifest at build time so the fleet runs at the trusted
# tier in a dev loop.
build:
	mkdir -p bin
	for p in $(STATIC_PLUGINS); do \
		(cd "plugins/$$p" && CGO_ENABLED=0 go build -o "../../bin/topos-plugin-$$p" .) || exit 1; \
	done

# build-signal compiles the one cgo plugin into the same bin/, against
# the system SQLCipher library (see plugins/signal/README.md for the
# per-distro package and the SQLite version floor).
build-signal:
	mkdir -p bin
	cd plugins/signal && CGO_ENABLED=1 go build -tags libsqlcipher -o ../../bin/topos-plugin-signal .

# test-signal builds and tests the one cgo module against the system
# SQLCipher library — the suite `test` (below) skips. Requires the
# sqlcipher package; the untagged form is deliberately not offered
# (without -tags libsqlcipher the suite fails on the SQLite version
# floor rather than testing the real driver).
test-signal:
	cd plugins/signal && CGO_ENABLED=1 go build -tags libsqlcipher ./... && CGO_ENABLED=1 go test -tags libsqlcipher ./...

# test builds and tests every workspace module except signal, whose
# suite needs CGO_ENABLED=1 against the system SQLCipher library — the
# same loop ci.yml runs; signal is proven locally with
# `cd plugins/signal && CGO_ENABLED=1 go test -tags libsqlcipher ./...`.
test:
	./scripts/signal-schema-accept-smoke.sh
	set -e; for mod in $$(go list -m -f '{{.Dir}}'); do \
		case "$$mod" in */plugins/signal) echo "=== plugins/signal: skipped (cgo/SQLCipher — local-only)"; continue;; esac; \
		rel="$${mod#$(CURDIR)/}"; [ "$$rel" != "$(CURDIR)" ] || rel=.; echo "=== $$rel"; \
		(cd "$$mod" && go build ./... && go test ./...); \
	done

# verifier builds the kernel's own provenance CLI at the pinned revision
# into bin/topos-provenance. This is the binary the release workflow
# signs with and publishes beside the fleet, and the one to put on PATH
# for `make install` when no installed kernel provides one. GOWORK=off:
# the pinned kernel module is built on its own terms, never resolved
# through this workspace.
verifier:
	mkdir -p bin
	GOWORK=off GOBIN="$(CURDIR)/bin" CGO_ENABLED=0 go install github.com/davison/topos/cmd/topos-provenance@$(TOPOS_PROVENANCE_REF)

# install downloads a published topos-plugins release, verifies every
# asset's SHA-256 against that release's own checksums.txt AND every
# plugin binary against the release's signed provenance manifest, then
# places the binaries and the manifest pair under
# $(PREFIX)/lib/topos/plugins/ — see scripts/install.sh (preflight ->
# stage -> verify -> place; nothing is placed until everything has
# verified, and $(PREFIX)/bin is never touched). Two forms:
#   make install                 — the latest published stable release
#   make install VERSION=0.2.0   — exactly that tag (leading v optional)
# Re-running with a newer VERSION is the update path. Needs curl +
# sha256sum only; the provenance verifier is resolved from an installed
# kernel's $(PREFIX)/bin/topos-provenance, then PATH, then the release's
# own shipped copy (last — see README.md "Installing the fleet").
install:
	PREFIX="$(PREFIX)" ./scripts/install.sh $(VERSION)

# uninstall removes what `make install` placed and ONLY that: the
# topos-plugin-* binaries and topos-plugins-*.provenance.{json,sig}
# files directly inside $(PREFIX)/lib/topos/plugins, then that directory
# and lib/topos by non-recursive rmdir when empty. The kernel at
# $(PREFIX)/bin, the operator's config, index and plugin stores are
# never named. Idempotent.
uninstall:
	PREFIX="$(PREFIX)" ./scripts/uninstall.sh

# install-signal builds the Signal plugin (through build-signal — the
# ONE place the cgo flags live) and places it atomically into the
# installed instance's EXTERNAL plugin directory (M1-R7/DIST-04),
# never $(PREFIX)/lib/topos/plugins: a locally built binary carries no
# signed provenance, so the trusted directory would refuse it at
# launch. scripts/install-signal.sh resolves the kernel's own default
# ($XDG_DATA_HOME/topos/plugins-external, ~/.local/share fallback;
# TOPOS_EXTERNAL_PLUGINS_DIR overrides for a config that names its own
# [plugins] external_dir) and prints the one-time consent-and-pin
# steps.
install-signal: build-signal
	./scripts/install-signal.sh

# uninstall-signal removes the locally built Signal binary from the
# external plugin directory — exactly one file, never the directory or
# anything else in it. Deliberately separate from `uninstall`: the
# Signal binary lives OUTSIDE the prefix, in a directory `uninstall` is
# forbidden to touch (its removal set is closed over what `install`
# places).
# signal-schema-accept moves the Signal plugin's schema ceiling by
# verify-and-accept (scripts/signal-schema-accept.sh, topos-plugins#23):
# the live read-set test against the operator's real database, then a
# rewrite of schemaguard.go — constant and provenance — that the operator
# reviews and commits through a task. Never commits; nothing to accept
# is exit 0. Needs cgo, the system sqlcipher and gh (online).
signal-schema-accept:
	./scripts/signal-schema-accept.sh

uninstall-signal:
	./scripts/install-signal.sh --uninstall

# install-check runs the hermetic behavioural guard for install and
# uninstall (scripts/install-smoke.sh): a fixture release signed with a
# throwaway key, served through install.sh's file:// test seam — no
# network beyond the Go module cache, no credentials, nothing written
# outside its own temp tree.
install-check:
	./scripts/install-smoke.sh
