// Command topos-plugin-signal is the Signal (local Signal Desktop
// SQLCipher database) source plugin subprocess, launched by the kernel's
// plugin host over the go-plugin gRPC handshake — the first cgo-enabled
// plugin in this repo (see go.mod's replace directive and this repo's
// Makefile "signal" target for the sqlcipher system-package prerequisite
// that build requires). Follows the same shape as
// plugins/proton/main.go, adapted for a local-path source with no
// base_url/token at all.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"strings"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/davison/topos/sdk"
)

// sourceConfig is decoded from the WEBSPACES_SOURCE_CONFIG environment
// variable the kernel's pluginhost sets before launching this subprocess.
// Unlike every other plugin in this repo, this is a local-path source
// (04-CONTEXT.md Claude's Discretion note): Path is Signal Desktop's own
// config directory, and there is no base_url/token/username at all — the
// SQLCipher decryption key is resolved entirely at runtime from files
// inside Path (keyresolve.go), never from this project's own config or
// environment (kernel/pluginhost.launch marshals every known source-
// config key regardless of plugin, so this struct simply ignores the
// keys it doesn't need).
type sourceConfig struct {
	Path string `json:"path"`
}

func main() {
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

	configDir, err := expandHome(cfg.Path)
	if err != nil {
		fatal(fmt.Errorf("expand path: %w", err))
	}

	impl := NewSourcePlugin(configDir)

	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: sdk.Handshake,
		Plugins: map[string]goplugin.Plugin{
			"source": &sdk.SourcePluginGRPCPlugin{Impl: impl},
		},
		// sdk.GRPCServer (not goplugin.DefaultGRPCServer) raises the gRPC
		// message-size ceiling so a unary Fetch response carrying a full
		// rendition doesn't hit the 4 MB default (D-Task1, 01-01) — this
		// plugin doesn't produce large renditions yet (plan 04-03), but
		// every plugin uses the same GRPCServer so the contract's
		// message-size guarantee holds uniformly.
		GRPCServer: sdk.GRPCServer,
	})
}

// expandHome expands a leading "~" in path to the current user's home
// directory — mirrors kernel/config.expandSourceCACertPathsHome's
// identical convention, applied here in the plugin subprocess itself:
// kernel/config.Source.Path is deliberately passed through unexpanded
// (see kernel/config/types.go's Path doc comment) since the kernel never
// needs to open this path itself.
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
	fmt.Fprintln(os.Stderr, "topos-plugin-signal:", err)
	os.Exit(1)
}
