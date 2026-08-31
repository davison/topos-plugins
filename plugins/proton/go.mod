module github.com/davison/topos-plugins/plugins/proton

go 1.25.0

require (
	github.com/emersion/go-imap v1.2.1
	github.com/emersion/go-message v0.18.2
	github.com/microcosm-cc/bluemonday v1.0.27
)

require (
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	golang.org/x/net v0.56.0 // indirect
)

// golang.org/x/text is pulled in transitively by github.com/emersion/go-message's
// charset handling. hashicorp/go-plugin and google.golang.org/grpc (both
// imported directly by main.go/plugin.go) and every other indirect
// dependency below are already required by the workspace-local
// github.com/davison/topos/sdk module (see go.work) and resolve via
// Go's workspace build list without needing a duplicate require here —
// `go mod tidy` cannot be run cleanly against this module in isolation
// because github.com/davison/topos/sdk has no published remote
// (mirrors plugins/silverbullet and plugins/mock's go.mod, which have
// the same limitation).
require golang.org/x/text v0.38.0 // indirect
