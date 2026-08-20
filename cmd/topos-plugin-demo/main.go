// Command topos-plugin-demo is the topos-plugins repository's own trivial
// seed plugin (16-04-PLAN.md Task 1, D-04): a genuine, minimal
// contract-conformant source plugin whose only job is to prove this
// repository's tag-triggered signing pipeline end to end. It is modelled
// on topos/testdata/external-plugin, adapted to this repository's own
// module path and depending on github.com/davison/topos/sdk at a
// released version rather than a workspace replace.
//
// This binary is never trusted by directory placement — first-party trust
// is signed by a key in the kernel's embedded key set, that key lives in
// this repository's own CI, and everything else is external-tier by
// construction. See README.md.
//
// Its pre-Serve fatal-guard shape (a single required "path" key) mirrors
// every contract-conformant plugin in the topos kernel repository: a
// plugin MUST fail startup loudly, by name, non-zero, when a required
// config key is empty — never start up silently and fail later, mid-Match,
// with a confusing downstream error
// (docs/plugin-contract.md "Configuration: WEBSPACES_SOURCE_CONFIG").
package main

import (
	"encoding/json"
	"fmt"
	"os"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/davison/topos/sdk"
)

// sourceConfig is decoded from the WEBSPACES_SOURCE_CONFIG environment
// variable the kernel's pluginhost sets before launching this subprocess
// (docs/plugin-contract.md "Configuration"). Path is the one kernel-known
// key this plugin cares about; it is never opened, only checked for
// non-emptiness.
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

	impl := NewSourcePlugin()

	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: sdk.Handshake,
		Plugins: map[string]goplugin.Plugin{
			"source": &sdk.SourcePluginGRPCPlugin{Impl: impl},
		},
		// sdk.GRPCServer (not goplugin.DefaultGRPCServer) raises the gRPC
		// message-size ceiling to match the kernel's own dial options —
		// see sdk/shared.go's MaxMessageSize doc comment. This proof
		// plugin never sends a rendition large enough to need it, but
		// every contract-conformant plugin uses the same GRPCServer so
		// the message-size guarantee holds uniformly.
		GRPCServer: sdk.GRPCServer,
	})
}

// fatal writes a named, non-secret error message to stderr and exits
// non-zero — mirrors every other contract-conformant plugin's identical
// helper. Never called with anything that could contain a secret value
// (docs/plugin-contract.md "Logging").
func fatal(err error) {
	fmt.Fprintln(os.Stderr, "topos-plugin-demo:", err)
	os.Exit(1)
}
