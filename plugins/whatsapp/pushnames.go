package main

import "sync"

// pushNameCache resolves a WhatsApp user JID string to its best-known
// push name — sourced from whatsmeow's own top-level
// HistorySync.Pushnames list, a one-time JID->pushname map WhatsApp
// delivers alongside history sync, separate from any individual
// message's own Info.PushName field. This plan's real-device spike
// (2026-08-10) found Info.PushName empty for essentially every
// history-sync-replayed message ("the only non-empty
// messages.sender_name is 'You'") — this cache is the best-effort
// fallback for the vast majority of captured messages that arrive via
// backfill rather than live. Purely in-memory and display-only: this
// cache is NEVER a match candidate (T-08-01's mitigation still applies
// — only group subjects match, via groupsync.go), only a
// transcript/digest sender-name label.
type pushNameCache struct {
	mu    sync.RWMutex
	names map[string]string
}

func newPushNameCache() *pushNameCache {
	return &pushNameCache{names: make(map[string]string)}
}

// merge adds entries from one HistorySync payload's own Pushnames list
// (or any other source of jid->name pairs). Never overwrites an existing
// entry with an empty name, and never overwrites a non-empty entry with
// another non-empty one (first-seen wins — an account's own historical
// push name is treated as stable enough not to churn on every sync).
func (c *pushNameCache) merge(entries map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for jid, name := range entries {
		if name == "" {
			continue
		}
		if _, exists := c.names[jid]; exists {
			continue
		}
		c.names[jid] = name
	}
}

// lookup returns the cached push name for jid, or "" if unknown.
func (c *pushNameCache) lookup(jid string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.names[jid]
}
