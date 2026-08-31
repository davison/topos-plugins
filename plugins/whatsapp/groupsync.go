package main

import (
	"context"
	"fmt"
	"time"
)

// groupSyncTimeout bounds the GetJoinedGroups IQ round trip triggered on
// every *events.Connected (eventhandler.go).
const groupSyncTimeout = 30 * time.Second

// joinedGroup is the minimal shape this plugin needs from whatsmeow's own
// *types.GroupInfo — decoupled here so upsertJoinedGroups is
// unit-testable with fakes, without constructing a real types.GroupInfo
// (whose many embedded structs carry fields this plugin never reads).
type joinedGroup struct {
	JID  string
	Name string
}

// syncJoinedGroups is called on every *events.Connected — the BLOCKING
// FIX this plan's real-device spike (2026-08-10) found: whatsmeow's
// history-sync payload carries NEITHER a group's own subject NOR a
// contact's name (confirmed live: `SELECT COUNT(*) FROM chats WHERE name
// != ”` returned 0 after a real history sync of 616 messages across 134
// chats), so match.go's group-name matching could never match real data
// without this — the digest layer was unreachable outside test fixtures.
// GetJoinedGroups is a live IQ query returning every group's own current
// subject directly from WhatsApp's servers — the ONLY source this plugin
// trusts for a group's name (T-08-01's mitigation unchanged: never a
// message sender's self-asserted push name). Runs in its own goroutine
// (see eventhandler.go's call site) so it never blocks whatsmeow's own
// event-dispatch loop.
func (p *SourcePlugin) syncJoinedGroups() {
	ctx, cancel := context.WithTimeout(context.Background(), groupSyncTimeout)
	defer cancel()

	groups, err := p.client.GetJoinedGroups(ctx)
	if err != nil {
		fmt.Fprintf(p.logOut, "%s: sync joined groups: %v\n", pluginName, err)
		return
	}

	fetched := make([]joinedGroup, 0, len(groups))
	for _, g := range groups {
		fetched = append(fetched, joinedGroup{JID: g.JID.String(), Name: g.Name})
	}

	if err := upsertJoinedGroups(p.store, fetched, time.Now().UnixMilli()); err != nil {
		fmt.Fprintf(p.logOut, "%s: upsert joined groups: %v\n", pluginName, err)
		return
	}
	// Count-only: never a group name. This log is forwarded verbatim
	// into the kernel's log stream.
	fmt.Fprintf(p.logOut, "%s: synced %d joined group name(s)\n", pluginName, len(fetched))
}

// upsertJoinedGroups upserts a chat row (is_group=true) for every group
// in groups, keyed by JID, with its own subject. now is used as the
// row's updated_at_unix_ms. Skips any entry with an empty JID (defensive
// — GetJoinedGroups is not expected to ever return one).
func upsertJoinedGroups(store *messageStore, groups []joinedGroup, now int64) error {
	for _, g := range groups {
		if g.JID == "" {
			continue
		}
		if err := store.UpsertChatName(g.JID, true, g.Name, now); err != nil {
			return fmt.Errorf("whatsapp: upsert joined group %q: %w", g.JID, err)
		}
	}
	return nil
}
