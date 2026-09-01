module github.com/davison/topos-plugins/plugins/signal

go 1.25.0

require github.com/mattn/go-sqlite3 v1.14.49

require (
	github.com/davison/topos/sdk v0.0.0-20260901181323-b3e18d5b6a06
	github.com/hashicorp/go-plugin v1.8.0
	github.com/keybase/dbus v0.0.0-20220506165403-5aa21ea2c23a
	github.com/keybase/go-keychain v0.0.1
	golang.org/x/crypto v0.54.0
	google.golang.org/grpc v1.83.0
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fatih/color v1.13.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260807164820-c8921c73eeea // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// go.mod replace (Task 1 checkpoint, 04-01-PLAN.md, option-a): the
// libsqlcipher build tag that dynamically links the system's own
// SQLCipher library — the driver strategy the checkpoint authorised over
// the CLAUDE.md-pinned mutecomm/go-sqlcipher/v4 (stale, bundles a
// pre-3.51.3 SQLite core; see 04-RESEARCH.md Pitfall 1) — originates from
// upstream PR github.com/mattn/go-sqlite3#1109, open since November 2022
// and still unmerged. This replace pins the exact fork+commit the
// checkpoint authorised: jgiannuzzi/go-sqlite3's `sqlcipher` branch at
// commit f208443ec79de7edaf1b80276806005a5c0cf340 (verified live against
// the GitHub API during the Task 1 checkpoint, last updated 2026-07-01 —
// the live head of PR #1109 at authorisation time). The fork's own go.mod
// still declares module path github.com/mattn/go-sqlite3 (it has never
// been renamed) — that is exactly why a `replace`, rather than a plain
// `require` of a different module path, is the correct mechanism here.
replace github.com/mattn/go-sqlite3 => github.com/jgiannuzzi/go-sqlite3 v1.14.17-0.20230327162135-f208443ec79d

replace github.com/davison/topos-plugins => ../..
