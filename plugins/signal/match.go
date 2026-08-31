package main

import "strings"

// conversation is this plugin's own normalized view of one row from
// Signal Desktop's conversations table — only the fields match.go and
// digest.go actually need, never the full JSON blob (D-03's minimal-
// plaintext-exposure discipline extends to what this plugin returns to
// the kernel; reading a bit more into local memory to build this struct
// is the same shape every other plugin's Match implementation already
// uses).
type conversation struct {
	ID   string
	Type string // "private" | "group"
	Name string // the conversation's OWN name — a group's own name; empty for a 1:1

	// SystemGivenName/SystemFamilyName is the address-book/system contact
	// name Signal Desktop learned from OS contacts integration — one of
	// D-06's two permitted 1:1 match candidates.
	SystemGivenName  string
	SystemFamilyName string

	// NicknameGivenName/NicknameFamilyName is the nickname the USER set
	// for this contact from within Signal itself — D-06's other
	// permitted 1:1 match candidate, distinct from both the system
	// contact name above and the contact's own profile name below.
	NicknameGivenName  string
	NicknameFamilyName string

	// ProfileName/ProfileFamilyName is the CONTACT's own self-chosen
	// Signal profile name — deliberately NEVER a match candidate (D-06):
	// a contact must not be able to pull themselves into a webspace by
	// renaming their own profile. Kept on this struct only so this
	// package's own tests can assert it is never read for matching.
	ProfileName       string
	ProfileFamilyName string

	E164      string // used only by deeplink.go, never for matching
	ServiceID string // Signal's per-account/contact stable identifier; used only for Note-to-Self detection and sender-name lookup, never for matching

	// IsNoteToSelf marks the account owner's own conversation with
	// themselves (D-05: never eligible, regardless of any name it
	// carries) — computed by the caller (plugin.go's readConversations)
	// by comparing this conversation's own service id against the
	// account's own, never by display name.
	IsNoteToSelf bool
}

// matchesAnyKeyword reports whether name case-insensitively equals any of
// keywords (exact match, no substring/prefix matching — Phase 1 D-03).
// Behavior mirrors plugins/proton/plugin.go's function of the same name.
func matchesAnyKeyword(name string, keywords []string) bool {
	if name == "" {
		return false
	}
	for _, kw := range keywords {
		if strings.EqualFold(name, kw) {
			return true
		}
	}
	return false
}

// joinName joins given and family with a single space, omitting either
// side if empty — never produces a leading/trailing/doubled space.
func joinName(given, family string) string {
	given = strings.TrimSpace(given)
	family = strings.TrimSpace(family)
	switch {
	case given != "" && family != "":
		return given + " " + family
	case given != "":
		return given
	default:
		return family
	}
}

// candidateNames returns the name(s) c is eligible to match a webspace
// keyword against. Note to Self returns zero candidates unconditionally
// (D-05) — eligibleConversations also filters it out before calling this,
// but this defends a caller that forgets to. A group's only candidate is
// its own name. A 1:1's candidates are the user's own names for that
// contact ONLY: the in-app nickname and the address-book/system contact
// name — never c.ProfileName/c.ProfileFamilyName (D-06). A contact must
// not be able to pull themselves into a webspace by renaming their own
// profile.
func candidateNames(c conversation) []string {
	if c.IsNoteToSelf {
		return nil
	}
	switch c.Type {
	case "group":
		if c.Name == "" {
			return nil
		}
		return []string{c.Name}
	case "private":
		var out []string
		if nickname := joinName(c.NicknameGivenName, c.NicknameFamilyName); nickname != "" {
			out = append(out, nickname)
		}
		if system := joinName(c.SystemGivenName, c.SystemFamilyName); system != "" {
			out = append(out, system)
		}
		return out
	default:
		return nil
	}
}

// matchesConversation reports whether c has at least one candidate name
// (D-06) matching any of keywords (Phase 1 D-03's exact/case-insensitive
// rule).
func matchesConversation(c conversation, keywords []string) bool {
	for _, candidate := range candidateNames(c) {
		if matchesAnyKeyword(candidate, keywords) {
			return true
		}
	}
	return false
}

// eligibleConversations filters convs to groups and 1:1 chats, excluding
// Note to Self (D-05), and returns only those matching at least one of
// keywords. An empty keyword list returns zero matches (matchesAnyKeyword
// has nothing to compare against).
func eligibleConversations(convs []conversation, keywords []string) []conversation {
	var out []conversation
	for _, c := range convs {
		if c.IsNoteToSelf {
			continue
		}
		if c.Type != "group" && c.Type != "private" {
			continue
		}
		if matchesConversation(c, keywords) {
			out = append(out, c)
		}
	}
	return out
}

// conversationDisplayName returns the name digest titles (D-02) use for
// c: the group's own name for a group, or the best available 1:1 display
// name (nickname, else system contact name, else profile name, else a
// fixed placeholder) for a 1:1 — display purposes only, never used for
// matching (D-06 restricts matching alone, not how a matched
// conversation's own name is shown once it has already matched).
func conversationDisplayName(c conversation) string {
	if c.Type == "group" {
		return c.Name
	}
	if nickname := joinName(c.NicknameGivenName, c.NicknameFamilyName); nickname != "" {
		return nickname
	}
	if system := joinName(c.SystemGivenName, c.SystemFamilyName); system != "" {
		return system
	}
	if profile := joinName(c.ProfileName, c.ProfileFamilyName); profile != "" {
		return profile
	}
	return "Unknown conversation"
}
