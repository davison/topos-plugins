// Package main's sourceconfig.go decodes WEBSPACES_SOURCE_CONFIG and
// resolves the two OAuth client-credential values serve mode needs
// (CONTRACT-GAPS.md GAP-07). Per GAP-04's standing resolution, nothing in
// this file is called from main or from Describe — it is consulted lazily,
// from plugin.go's tokenSource method, the first time Health/Match/Fetch
// actually needs a client credential.
package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// sourceConfigEnvVar is the literal environment variable name the kernel
// sets on every launched plugin subprocess carrying this instance's
// [sources.<id>] configuration (contract/plugin-contract.md's
// "Configuration: WEBSPACES_SOURCE_CONFIG" section).
const sourceConfigEnvVar = "WEBSPACES_SOURCE_CONFIG"

// sourceConfig decodes only what this plugin uses out of
// WEBSPACES_SOURCE_CONFIG's JSON: the extras object. The top-level
// connection keys (base_url, token, path — see GAP-06) are deliberately
// not modeled here; a struct field holding the host's own top-level
// "token" value would be a secret-shaped field with no consumer in this
// plugin, since this plugin's own credential lives in extras, not there.
type sourceConfig struct {
	Extras map[string]string `json:"extras"`
}

// loadSourceConfig reads and decodes sourceConfigEnvVar via getenv (never
// os.Getenv directly, so tests can override it without process-env
// mutation, matching token.go's established injection shape). An unset or
// empty-string variable is not an error — the contract states a plugin
// with nothing to configure reads the variable and does nothing with it,
// and a trial launch reaches this plugin before an operator has saved
// anything (GAP-04). A non-empty value that fails to parse as JSON is an
// error naming only the variable, never the payload — the payload is the
// operator's resolved configuration and may carry a client secret.
func loadSourceConfig(getenv func(string) string) (*sourceConfig, error) {
	raw := getenv(sourceConfigEnvVar)
	if raw == "" {
		return &sourceConfig{}, nil
	}

	var cfg sourceConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("%s: could not decode as JSON", sourceConfigEnvVar)
	}
	return &cfg, nil
}

// clientCredentials resolves the OAuth client_id/client_secret this plugin
// needs, in this order for each value independently: the matching extras
// entry (client_id, client_secret) when non-empty after trimming
// whitespace, else the matching environment variable (GDRIVE_CLIENT_ID,
// GDRIVE_CLIENT_SECRET) when non-empty after trimming whitespace.
//
// Extras-first is the contract-compliant path and the only one that works
// under a real host launch: the kernel expands the operator's
// ${GDRIVE_CLIENT_ID}/${GDRIVE_CLIENT_SECRET} references into the extras
// object before this plugin ever sees the JSON
// (contract/plugin-contract.md's "Configuration" section). The raw
// environment-variable fallback exists only for direct local invocation —
// it is structurally unable to work under a real host launch, because the
// launch environment never hands a plugin subprocess GDRIVE_CLIENT_ID or
// GDRIVE_CLIENT_SECRET directly (contract/plugin-contract.md's "The launch
// environment" section; CONTRACT-GAPS.md GAP-07).
//
// When either value is still empty after both paths, the returned error
// names the extras key AND the environment variable for that value, and
// says nothing else — never a partially-resolved pair with a nil error.
func (c *sourceConfig) clientCredentials(getenv func(string) string) (clientID, clientSecret string, err error) {
	clientID = resolveCredential(c, "client_id", getenv, "GDRIVE_CLIENT_ID")
	clientSecret = resolveCredential(c, "client_secret", getenv, "GDRIVE_CLIENT_SECRET")

	var missing []string
	if clientID == "" {
		missing = append(missing, "extras key \"client_id\" or GDRIVE_CLIENT_ID")
	}
	if clientSecret == "" {
		missing = append(missing, "extras key \"client_secret\" or GDRIVE_CLIENT_SECRET")
	}
	if len(missing) > 0 {
		return "", "", fmt.Errorf("client credentials not configured: %s", strings.Join(missing, "; "))
	}
	return clientID, clientSecret, nil
}

// resolveCredential resolves a single credential value: extrasKey out of
// c.Extras when non-empty after trimming, else envVar out of getenv when
// non-empty after trimming, else "".
func resolveCredential(c *sourceConfig, extrasKey string, getenv func(string) string, envVar string) string {
	if c != nil {
		if v := strings.TrimSpace(c.Extras[extrasKey]); v != "" {
			return v
		}
	}
	return strings.TrimSpace(getenv(envVar))
}

// driveIDPattern is the character set a well-formed Drive file/folder id
// is drawn from (URL-safe base64 alphabet: letters, digits, underscore,
// hyphen). folderID validates the operator-configured folder_id against
// it BEFORE the value can reach folderwalk.go's interpolated Drive query
// string — the Drive query DSL supports quoting and boolean operators,
// and this validation is structurally independent of whatever order
// resync happens to call rootFolderName and walkFolder in (WR-01,
// T-03-21).
var driveIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// folderID resolves the configured Drive folder id: c.Extras["folder_id"],
// trimmed, extras-only. Unlike clientCredentials above, this has no
// environment-variable fallback — no GDRIVE_FOLDER_ID-shaped variable is
// declared anywhere in Describe's Extras (plugin.go), so there is nothing
// for a fallback to read. Empty after trimming returns a named error
// mentioning the extras key and nothing else; a non-empty value outside
// driveIDPattern's character set is rejected the same way — the error
// names the extras key and never echoes the rejected value, following
// loadSourceConfig's established discipline of naming the variable and
// never the payload.
func (c *sourceConfig) folderID() (string, error) {
	id := ""
	if c != nil {
		id = strings.TrimSpace(c.Extras["folder_id"])
	}
	if id == "" {
		return "", fmt.Errorf(`extras key "folder_id" not configured`)
	}
	if !driveIDPattern.MatchString(id) {
		return "", fmt.Errorf(`extras key "folder_id" is not a well-formed Drive folder id`)
	}
	return id, nil
}
