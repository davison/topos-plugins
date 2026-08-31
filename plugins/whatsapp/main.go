// Command topos-plugin-whatsapp is the WhatsApp (linked-device, whatsmeow)
// source plugin subprocess, launched by the kernel's plugin host over the
// go-plugin gRPC handshake. Unlike Signal, this binary is pure Go — no
// build tag, no cgo — and unlike every other plugin in this repo, it holds
// a persistent connection (a background whatsmeow Client.Connect) for its
// entire process lifetime rather than opening-and-closing per RPC call
// (plugins/signal/main.go's identical bootstrap shape, minus the cgo
// divergence — see plugins/signal/main.go's own doc comment).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/user"
	"strings"

	goplugin "github.com/hashicorp/go-plugin"

	wastore "go.mau.fi/whatsmeow/store"

	"github.com/davison/topos/sdk"
)

// sourceConfig is decoded from the WEBSPACES_SOURCE_CONFIG environment
// variable the kernel's pluginhost sets before launching this subprocess —
// mirrors plugins/signal/main.go's identical sourceConfig shape: a
// local-path source with no base_url/token/username at all. Path is the
// directory holding this plugin's TWO owned databases: whatsmeow's own
// session store (whatsmeow.db) and this plugin's own message-content store
// (messages.db) — see messagestore.go and connect.go.
type sourceConfig struct {
	Path string `json:"path"`
}

// describeOnlyEnvVar, when set to "1", tells this subprocess to serve
// ONLY Describe (describeonly.go's describeOnlyPlugin) and skip
// NewSourcePlugin entirely — never opening this plugin's local message
// store, never calling startBackgroundClient, and therefore never
// acquiring storelock.go's exclusive per-data-directory flock. Set only by
// kernel/pluginhost.DescribePluginType's trial-launch path (CR-01,
// 08-REVIEW.md): unlike every other trial-launched plugin in this repo,
// a WhatsApp source is *always* already running (plugin.go's own doc
// comment — it holds a persistent connection for its entire process
// lifetime), so the ordinary non-flagged startup path here would always
// lose the lock race against that real instance and fail before ever
// reaching goplugin.Serve. Describe's answer (source_type/display_name/
// contract_version/match_vocabulary) is a fixed set of package-level
// constants that depends on none of that state, so this mode simply never
// touches it.
const describeOnlyEnvVar = "WEBSPACES_DESCRIBE_ONLY"

func main() {
	// Cosmetic fix (2026-08-10 real-device spike): whatsmeow's own
	// package-level DeviceProps.Os defaults to the literal string
	// "whatsmeow" — that's what a real phone's WhatsApp > Linked Devices
	// list showed for this plugin's linked session. SetOSInfo mutates
	// that shared global var, so it must run once, early, before EITHER
	// code path below ever constructs a whatsmeow.Client (link.go's
	// runLinkCLI or connect.go's startBackgroundClient) — both are
	// reached from this single main(), so calling it here covers both.
	wastore.SetOSInfo("topos", [3]uint32{0, 1, 0})

	linkMode := flag.Bool("link", false, "run the one-shot terminal QR link flow against -path, then exit")
	linkJSONMode := flag.Bool("link-json", false, "run the one-shot machine-readable QR link flow against -path (newline-delimited JSON events on stdout), then exit")
	linkPath := flag.String("path", "", "the plugin's own data directory (same value as [sources.whatsapp].path in config.toml)")
	flag.Parse()

	if err := validateLinkFlags(*linkMode, *linkJSONMode); err != nil {
		fatal(err)
	}

	if *linkMode {
		dir, err := expandHome(*linkPath)
		if err != nil {
			fatal(fmt.Errorf("expand -path: %w", err))
		}
		if dir == "" {
			fatal(fmt.Errorf("-link requires -path"))
		}
		if err := runLinkCLI(context.Background(), dir); err != nil {
			fatal(err)
		}
		os.Exit(0)
	}

	// -link-json (D-01): the machine-readable sibling of -link, driven by
	// kernel/httpapi/whatsapplink.go (Task 3) as a raw subprocess outside
	// the go-plugin gRPC handshake — like -link, this branch always exits
	// before goplugin.Serve is ever reached below.
	if *linkJSONMode {
		dir, err := expandHome(*linkPath)
		if err != nil {
			fatal(fmt.Errorf("expand -path: %w", err))
		}
		if dir == "" {
			fatal(fmt.Errorf("-link-json requires -path"))
		}
		if err := runLinkJSON(context.Background(), dir, os.Stdout, os.Stderr); err != nil {
			fatal(err)
		}
		os.Exit(0)
	}

	// describeOnlyEnvVar (CR-01, 08-REVIEW.md) short-circuits BEFORE
	// WEBSPACES_SOURCE_CONFIG is even required: a trial-launch that only
	// wants Describe's answer needs no path at all, and — the whole point
	// of this mode — must never reach NewSourcePlugin/acquireStoreLock
	// below, which is what a real running instance already holds
	// exclusively for its own data directory.
	var impl sdk.SourcePlugin
	if os.Getenv(describeOnlyEnvVar) == "1" {
		impl = describeOnlyPlugin{}
	} else {
		raw := os.Getenv("WEBSPACES_SOURCE_CONFIG")
		if raw == "" {
			fatal(fmt.Errorf("WEBSPACES_SOURCE_CONFIG is not set"))
		}

		var cfg sourceConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			fatal(fmt.Errorf("parse WEBSPACES_SOURCE_CONFIG: %w", err))
		}
		if cfg.Path == "" {
			fatal(fmt.Errorf("WEBSPACES_SOURCE_CONFIG: path is empty"))
		}

		dataDir, err := expandHome(cfg.Path)
		if err != nil {
			fatal(fmt.Errorf("expand path: %w", err))
		}

		sp, err := NewSourcePlugin(context.Background(), dataDir)
		if err != nil {
			fatal(fmt.Errorf("start whatsapp plugin: %w", err))
		}
		impl = sp
	}

	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: sdk.Handshake,
		Plugins: map[string]goplugin.Plugin{
			"source": &sdk.SourcePluginGRPCPlugin{Impl: impl},
		},
		// sdk.GRPCServer (not goplugin.DefaultGRPCServer) raises the gRPC
		// message-size ceiling so a unary Fetch response carrying a full
		// rendition doesn't hit the 4 MB default — every plugin in this
		// repo uses the same GRPCServer so the contract's message-size
		// guarantee holds uniformly.
		GRPCServer: sdk.GRPCServer,
	})
}

// expandHome expands a leading "~" in path to the current user's home
// directory — mirrors plugins/signal/main.go's identical helper.
func expandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return strings.Replace(path, "~", u.HomeDir, 1), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "topos-plugin-whatsapp:", err)
	os.Exit(1)
}

// validateLinkFlags rejects the -link/-link-json combination before either
// flow starts — a usage error, not a runtime one, so it is checked
// immediately after flag.Parse() and reported through the same fatal path
// every other pre-flow validation in main() uses.
func validateLinkFlags(linkMode, linkJSONMode bool) error {
	if linkMode && linkJSONMode {
		return fmt.Errorf("-link and -link-json are mutually exclusive")
	}
	return nil
}
