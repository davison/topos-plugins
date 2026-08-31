// Command topos-plugin-paperless is the paperless-ngx source plugin
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

type sourceConfig struct {
	BaseURL    string `json:"base_url"`
	Token      string `json:"token"`
	APIVersion string `json:"api_version"`
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
	if cfg.APIVersion == "" {
		cfg.APIVersion = "10"
	}

	impl := NewSourcePlugin(cfg.BaseURL, cfg.Token, cfg.APIVersion)

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
	fmt.Fprintln(os.Stderr, "topos-plugin-paperless:", err)
	os.Exit(1)
}
