// Command topos-plugin-silverbullet is the SilverBullet source plugin
// subprocess, launched by the kernel's plugin host over the go-plugin
// gRPC handshake.
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
// Unlike the paperless plugin, there is no api_version field — SilverBullet
// has no API-version negotiation. ca_cert is new (a deviation from the
// plan's originally sketched two-field config): the path to a PEM-encoded
// CA certificate to trust for this instance's TLS connection, needed for
// the user's real self-signed-certificate deployment (see client.go's
// NewClient doc comment). An absent ca_cert means "use the system trust
// store," the correct default for a non-self-signed deployment.
type sourceConfig struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
	CACert  string `json:"ca_cert"`
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
	if cfg.Token == "" {
		fatal(fmt.Errorf("WEBSPACES_SOURCE_CONFIG: token is empty"))
	}

	impl := NewSourcePlugin(cfg.BaseURL, cfg.Token, cfg.CACert)

	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: sdk.Handshake,
		Plugins: map[string]goplugin.Plugin{
			"source": &sdk.SourcePluginGRPCPlugin{Impl: impl},
		},
		// sdk.GRPCServer (not goplugin.DefaultGRPCServer) raises the
		// gRPC message-size ceiling so a unary Fetch response carrying a
		// full rendition doesn't hit the 4 MB default (D-Task1, 01-01).
		GRPCServer: sdk.GRPCServer,
	})
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "topos-plugin-silverbullet:", err)
	os.Exit(1)
}
