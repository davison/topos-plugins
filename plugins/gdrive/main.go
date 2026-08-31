// Command topos-plugin-gdrive is a topos source plugin that makes a
// chosen Google Drive folder appear as a source inside topos. It is a
// clean-room build against exactly four published inputs (see this
// repository's own CLAUDE.md); this file's shape mirrors
// contract/mock/main.go's goplugin.Serve wiring, minus that mock's
// fixture-only scaffolding (readinessWindowFromEnv, launchDelayFromEnv,
// renditionFixtureEnabled — none of which are part of the plugin
// contract).
//
// This binary is dual-mode, dispatching on its first argument
// (PRD.md "Design Guidance"): the literal lowercase string "auth" as
// os.Args[1] runs the standalone authorization flow and exits; any other
// invocation, including zero arguments (the way a plugin host launches a
// plugin subprocess), falls through to ordinary serve mode.
//
// main performs no environment-variable read of any kind, and no
// WEBSPACES_SOURCE_CONFIG parsing or required-key validation — this is
// the deliberate GAP-04 resolution (CONTRACT-GAPS.md): Describe must
// succeed with nothing configured, so no config validation happens
// anywhere in Phase 1's main() or Describe. Fail-loud-on-missing-required-
// key enforcement is deferred to the point a real Drive call is actually
// attempted (Match/Health, Phase 2+).
package main

import (
	"fmt"
	"os"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/davison/topos/sdk"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "auth" {
		if err := runAuth(); err != nil {
			fmt.Fprintln(os.Stderr, "topos-plugin-gdrive:", err)
			os.Exit(1)
		}
		return
	}

	goplugin.Serve(&goplugin.ServeConfig{
		// HandshakeConfig is the imported sdk.Handshake value, never a
		// locally restated cookie key/value pair — a typo there fails the
		// handshake with an opaque connection error, not a compile error.
		HandshakeConfig: sdk.Handshake,
		Plugins: map[string]goplugin.Plugin{
			// The plugin-map key must be the literal "source" — any other
			// key makes the host's dispense fail opaquely after a
			// successful handshake (contract/plugin-contract.md:151-154).
			"source": &sdk.SourcePluginGRPCPlugin{
				Impl: NewSourcePlugin(),
			},
		},
		// sdk.GRPCServer, not go-plugin's own default server constructor —
		// the raised message-size ceiling is a uniform contract guarantee.
		GRPCServer: sdk.GRPCServer,
	})
}
