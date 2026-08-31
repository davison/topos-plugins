package main

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// attachmentPlaceholder is the fixed body text a message with no plaintext
// but a known non-text content type (image, video, audio, document,
// sticker, contact card, location) is captured with — never the file's
// own bytes, per the hybrid data model's "content stays in the source"
// rule.
const attachmentPlaceholder = "📎 Attachment"

// handleEvent is registered via Client.AddEventHandler (connect.go) and
// runs continuously, fully decoupled from when the kernel next calls
// Match — this is the background writer half of this plugin's
// architecture (T-08-02's mitigation: no send-capable Client method is
// ever called from here or anywhere else in this plugin).
func (p *SourcePlugin) handleEvent(evt any) {
	switch e := evt.(type) {
	case *events.Connected:
		p.setHealthState(healthStateLinked, "")
		fmt.Fprintf(p.logOut, "%s: connected\n", pluginName)
		// BLOCKING FIX (2026-08-10 real-device spike): history sync
		// alone never populates a group's own subject — see
		// groupsync.go's own doc comment. Runs in its own goroutine so
		// this IQ round trip never blocks whatsmeow's own event-dispatch
		// loop.
		go p.syncJoinedGroups()
	case *events.LoggedOut:
		// health.go's healthStateFromLogoutReason translates e.Reason
		// into the correct named cause (de-link / ban / session-expiry —
		// Task 1's own taxonomy) — never a single generic "logged out"
		// message the way 08-01's own predecessor code read.
		p.setHealthState(healthStateFromLogoutReason(e.Reason), "")
	case *events.TemporaryBan:
		// A dedicated event type (reason 402), distinct from the
		// LoggedOut/ConnectFailure family above — whatsmeow's own
		// TempBanReason.String() already composes a code+description
		// ("101: you sent too many messages..."), captured here as this
		// state's dynamic detail per this task's own action text.
		p.setHealthState(healthStateBanned, e.Code.String())
	case *events.ConnectFailure:
		// The truly-unrecognised-reason fallback whatsmeow's own
		// connectionevents.go dispatches when a connect failure is
		// neither a recognised logout, a temp ban, nor one of the
		// auto-retried transient codes — mapped to de-linked, never
		// silently to healthy (this task's own explicit requirement).
		p.setHealthState(healthStateDelinked, fmt.Sprintf("connect failure %d: %s", int(e.Reason), e.Message))
	case *events.StreamReplaced:
		p.setHealthState(healthStateStreamReplaced, "")
	case *events.Message:
		p.handleMessageEvent(e)
	case *events.HistorySync:
		p.handleHistorySync(e)
	case *events.GroupInfo:
		p.handleGroupInfoEvent(e)
	case *events.Contact:
		p.handleContactEvent(e)
	}
}

// handleMessageEvent appends a live or history-sync-replayed message to
// this plugin's own message store (messagestore.go) — the ONLY place this
// plugin ever writes a message row. A captured message from a chat
// matching no webspace's match configuration is still captured here (the
// plugin's own store necessarily captures every inbound message the
// linked device receives), but Match (plugin.go) only ever converts a
// MATCHED chat's rows into Items — capture must never become exposure.
func (p *SourcePlugin) handleMessageEvent(e *events.Message) {
	if e.Message.GetReactionMessage() != nil {
		return // a reaction is not a message in its own right in this plan's schema
	}

	chatJID := e.Info.Chat.String()
	isGroup := e.Info.IsGroup

	if err := p.store.EnsureChat(chatJID, isGroup); err != nil {
		fmt.Fprintf(p.logOut, "%s: ensure chat: %v\n", pluginName, err)
		return
	}

	// D-05/D-06: for a 1:1 chat, this chat's JID IS the contact's own
	// JID (there is no separate "chat" entity distinct from the contact
	// in a 1:1) — resolve and cache its saved address-book name on every
	// message so match.go's candidateNames has something to read without
	// a live lookup on the hot Match path. A group chat has no
	// resolveContactName equivalent here; its name comes exclusively
	// from handleGroupInfoEvent/syncJoinedGroups.
	if !isGroup {
		name := p.resolveContactName(e.Info.Chat)
		if err := p.store.UpsertContactName(chatJID, name, e.Info.Timestamp.UnixMilli()); err != nil {
			fmt.Fprintf(p.logOut, "%s: upsert contact name: %v\n", pluginName, err)
		}
	}

	if pm := e.Message.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			targetID := pm.GetKey().GetID()
			if err := p.store.MarkDeleted(chatJID, targetID); err != nil {
				fmt.Fprintf(p.logOut, "%s: mark deleted: %v\n", pluginName, err)
			}
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			targetID := pm.GetKey().GetID()
			newBody := extractMessageText(pm.GetEditedMessage())
			if err := p.store.MarkEdited(chatJID, targetID, newBody); err != nil {
				fmt.Fprintf(p.logOut, "%s: mark edited: %v\n", pluginName, err)
			}
		}
		return // every ProtocolMessage variant (recognised or not) carries no chat content of its own
	}

	body := extractMessageText(e.Message)
	if body == "" {
		return // no plaintext or known-media content this plugin captures (e.g. a receipt/control payload)
	}

	// Real-device spike (2026-08-10): a history-sync-replayed message's
	// own Info.PushName is empty for nearly every message ("the only
	// non-empty messages.sender_name is 'You'"). Fall back to the
	// best-effort pushNames cache (populated from HistorySync's own
	// top-level Pushnames list, handleHistorySync below) before falling
	// back further to the bare sender JID — never an empty string.
	senderName := e.Info.PushName
	if senderName == "" {
		senderName = p.pushNames.lookup(e.Info.Sender.ToNonAD().String())
	}
	if e.Info.IsFromMe {
		senderName = ownSenderLabel
	}

	rec := messageRecord{
		ID:           e.Info.ID,
		ChatJID:      chatJID,
		SenderJID:    e.Info.Sender.String(),
		SenderName:   senderName,
		IsFromMe:     e.Info.IsFromMe,
		SentAtUnixMs: e.Info.Timestamp.UnixMilli(),
		Body:         body,
	}
	if err := p.store.Append(rec); err != nil {
		fmt.Fprintf(p.logOut, "%s: append message: %v\n", pluginName, err)
	}
}

// handleHistorySync replays a whatsmeow first-link backfill payload
// through the identical handleMessageEvent path a live message uses (via
// Client.ParseWebMessage, whatsmeow's own documented conversion from a
// WebMessageInfo to an *events.Message) — so first-link backfill lands in
// the store identically to live messages, per this plan's own action
// text.
func (p *SourcePlugin) handleHistorySync(e *events.HistorySync) {
	if e.Data == nil {
		return
	}

	// Merge this payload's own top-level Pushnames list BEFORE
	// processing any message below, so the very first replayed message
	// from a newly-seen sender can already benefit from it.
	p.pushNames.merge(pushnamesFromProto(e.Data.GetPushnames()))

	count := 0
	for _, conv := range e.Data.GetConversations() {
		chatJID, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		for _, historyMsg := range conv.GetMessages() {
			webMsg := historyMsg.GetMessage()
			if webMsg == nil {
				continue
			}
			msgEvt, err := p.client.ParseWebMessage(chatJID, webMsg)
			if err != nil {
				continue
			}
			p.handleMessageEvent(msgEvt)
			count++
		}
	}
	fmt.Fprintf(p.logOut, "%s: history sync: processed %d message(s) across %d chat(s)\n", pluginName, count, len(e.Data.GetConversations()))
}

// handleGroupInfoEvent refreshes a group chat's cached subject — the ONLY
// path that ever writes a chat's name (T-08-01's mitigation: never a
// message sender's self-asserted push name).
func (p *SourcePlugin) handleGroupInfoEvent(e *events.GroupInfo) {
	if e.Name == nil {
		return
	}
	chatJID := e.JID.String()
	if err := p.store.UpsertChatName(chatJID, true, e.Name.Name, e.Timestamp.UnixMilli()); err != nil {
		fmt.Fprintf(p.logOut, "%s: upsert chat name: %v\n", pluginName, err)
	}
}

// handleContactEvent refreshes a 1:1 chat's cached address-book contact
// name when the user's contact list changes from ANOTHER linked device
// (e.g. they save/rename the contact on their phone with this plugin
// already running) — the live-update counterpart to handleMessageEvent's
// own per-message resolveContactName call, mirroring
// handleGroupInfoEvent's identical "refresh on the dedicated event, not
// just at message-capture time" shape for groups.
func (p *SourcePlugin) handleContactEvent(e *events.Contact) {
	chatJID := e.JID.String()
	name := p.resolveContactName(e.JID)
	if err := p.store.UpsertContactName(chatJID, name, e.Timestamp.UnixMilli()); err != nil {
		fmt.Fprintf(p.logOut, "%s: upsert contact name (contact event): %v\n", pluginName, err)
	}
}

// resolveContactName returns the ADDRESS-BOOK/system name whatsmeow's own
// local contact store carries for jid — the ONLY 1:1 match-candidate name
// source this plugin ever writes to messagestore.go's contact_name column
// (D-06's mitigation). types.ContactInfo also carries PushName and
// BusinessName fields — the contact's OWN self-chosen names — deliberately
// NEVER read here: a contact must not be able to pull themselves into a
// webspace by renaming their own profile. Prefers FullName, falls back to
// FirstName (WhatsApp's own address-book sync sometimes carries only a
// first name), and returns "" when the contact store has no saved name at
// all for jid — the exact empty value D-07 relies on to make an unsaved
// contact's chat unmatchable, with no phone-number fallback of any kind.
// This is a LOCAL read against whatsmeow's own sqlstore-backed contact
// store (populated automatically as WhatsApp delivers app-state contact
// sync — no network round trip happens here), so context.Background() is
// sufficient; never called before p.client is set (connect.go constructs
// the client, including its Store.Contacts, before AddEventHandler ever
// fires).
func (p *SourcePlugin) resolveContactName(jid types.JID) string {
	if p.client == nil || p.client.Store == nil || p.client.Store.Contacts == nil {
		return ""
	}
	info, err := p.client.Store.Contacts.GetContact(context.Background(), jid)
	if err != nil {
		fmt.Fprintf(p.logOut, "%s: resolve contact name: %v\n", pluginName, err)
		return ""
	}
	if info.FullName != "" {
		return info.FullName
	}
	return info.FirstName
}

// pushnamesFromProto converts one HistorySync payload's own top-level
// Pushnames list (a JID->pushname map WhatsApp delivers once per history
// sync, distinct from any individual message's own PushName field — see
// pushNameCache's own doc comment) into a plain map for pushNameCache.merge.
func pushnamesFromProto(list []*waHistorySync.Pushname) map[string]string {
	out := make(map[string]string, len(list))
	for _, pn := range list {
		id := pn.GetID()
		if id == "" {
			continue
		}
		out[id] = pn.GetPushname()
	}
	return out
}

// extractMessageText returns msg's plaintext body (plain text or extended
// text), or attachmentPlaceholder for a known non-text message type
// (image/video/audio/document/sticker/contact/location), or "" for
// anything else this plugin does not capture at all (e.g. a poll,
// button-reply, or unrecognised payload shape).
func extractMessageText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if c := msg.GetConversation(); c != "" {
		return c
	}
	if et := msg.GetExtendedTextMessage(); et != nil && et.GetText() != "" {
		return et.GetText()
	}
	switch {
	case msg.GetImageMessage() != nil,
		msg.GetVideoMessage() != nil,
		msg.GetAudioMessage() != nil,
		msg.GetDocumentMessage() != nil,
		msg.GetStickerMessage() != nil,
		msg.GetContactMessage() != nil,
		msg.GetLocationMessage() != nil:
		return attachmentPlaceholder
	default:
		return ""
	}
}
