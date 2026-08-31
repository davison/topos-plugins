package main

import "strings"

// matchesAnyKeyword reports whether name case-insensitively equals any of
// keywords via Unicode simple case folding (strings.EqualFold) — exact
// match only, no substring or prefix matching. Mirrors
// plugins/signal/match.go's identical function.
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

// candidateNames returns the single name c is eligible to match a
// webspace keyword against (D-05): a group chat's candidate is its own
// cached subject; a 1:1 chat's candidate is its store's contact_name,
// populated ONLY from the user's own address book (D-06 — never a
// remote-supplied push/profile name; messagestore.go's own chatRecord
// doc comment records that no such column even exists to become a
// candidate). An empty candidate — an unset group subject, or a 1:1 with
// an unsaved contact (D-07) — returns ZERO candidates: that chat is
// simply unmatchable, with no phone-number/JID fallback rule of any
// kind, mirroring plugins/signal/match.go's identical "no candidates at
// all" treatment of its own excluded case (Note to Self).
func candidateNames(c chatRecord) []string {
	name := c.Name
	if !c.IsGroup {
		name = c.ContactName
	}
	if name == "" {
		return nil
	}
	return []string{name}
}

// matchesChat reports whether c has at least one candidate name matching
// any of keywords.
func matchesChat(c chatRecord, keywords []string) bool {
	for _, candidate := range candidateNames(c) {
		if matchesAnyKeyword(candidate, keywords) {
			return true
		}
	}
	return false
}

// eligibleChats filters chats against TWO DISJOINT keyword lists (D-05):
// a group chat is tested against groupKeywords ONLY, and a 1:1 chat
// against contactKeywords ONLY — a value typed into the "groups" field
// can never silently match a 1:1 chat, and vice versa. Returns the union
// of both kinds' matches; a chat is either a group or a 1:1 (never both),
// so the union can never contain a duplicate chat by construction — no
// separate de-duplication pass is needed. Both keyword lists empty
// returns zero matches (matchesAnyKeyword has nothing to compare
// against).
func eligibleChats(chats []chatRecord, groupKeywords, contactKeywords []string) []chatRecord {
	var out []chatRecord
	for _, c := range chats {
		keywords := groupKeywords
		if !c.IsGroup {
			keywords = contactKeywords
		}
		if len(keywords) == 0 {
			continue
		}
		if matchesChat(c, keywords) {
			out = append(out, c)
		}
	}
	return out
}
