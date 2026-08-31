// Command topos-plugin-proton is the Proton Mail (IMAP, via Proton
// Mail Bridge) source plugin subprocess, launched by the kernel's plugin
// host over the go-plugin gRPC handshake. Follows the exact shape of
// plugins/silverbullet/main.go — see that file for the pattern this one
// copies.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/davison/topos/sdk"
)

// sourceConfig is decoded from the WEBSPACES_SOURCE_CONFIG environment
// variable the kernel's pluginhost sets before launching this subprocess.
// IMAP auth is username+password, not a bearer token, so this plugin
// decodes an extra "username" key beyond the paperless/silverbullet
// shape — Token is reused as the IMAP password (kernel/config/types.go's
// Token doc comment). webmail_base_url is required (not optional like
// ca_cert) because Match cannot build a deep link without it, and no
// absolute webmail URL literal may exist anywhere in this plugin's
// non-test Go files (internal/audit's egress scan — see client.go).
type sourceConfig struct {
	BaseURL        string `json:"base_url"`
	Username       string `json:"username"`
	Token          string `json:"token"`
	CACert         string `json:"ca_cert"`
	WebmailBaseURL string `json:"webmail_base_url"`
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

	if cfg.BaseURL == "" {
		fatal(fmt.Errorf("WEBSPACES_SOURCE_CONFIG: base_url is empty"))
	}
	if cfg.Username == "" {
		fatal(fmt.Errorf("WEBSPACES_SOURCE_CONFIG: username is empty"))
	}
	if cfg.Token == "" {
		fatal(fmt.Errorf("WEBSPACES_SOURCE_CONFIG: token is empty"))
	}
	if cfg.WebmailBaseURL == "" {
		fatal(fmt.Errorf("WEBSPACES_SOURCE_CONFIG: webmail_base_url is empty"))
	}

	impl, err := NewSourcePlugin(cfg.BaseURL, cfg.Username, cfg.Token, cfg.CACert, cfg.WebmailBaseURL)
	if err != nil {
		fatal(fmt.Errorf("build source plugin: %w", err))
	}

	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: sdk.Handshake,
		Plugins: map[string]goplugin.Plugin{
			"source": &sdk.SourcePluginGRPCPlugin{Impl: impl},
		},
		// sdk.GRPCServer (not goplugin.DefaultGRPCServer) raises the gRPC
		// message-size ceiling so a unary Fetch response carrying a full
		// message body doesn't hit the 4 MB default (D-Task1, 01-01).
		GRPCServer: sdk.GRPCServer,
	})
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "topos-plugin-proton:", err)
	os.Exit(1)
}
