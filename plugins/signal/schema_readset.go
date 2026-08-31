package main

// readSetColumns is the committed, exact declaration of every table and
// column this plugin's shipped SQL depends on — the expectation a schema
// check (live_schema_test.go's TestLiveSchemaReadSet, and any future
// tooling built on the pending verify-and-accept todo) diffs against.
//
// This map MUST be updated in the same commit as any change to the SQL in
// plugin.go's readConversations/readOwnAci/readAttachments/readReactions/
// readMessages. It is deliberately a non-test file (not declared in a
// _test.go) so a future tooling pass — a subcommand, a diff formatter —
// can import and reuse it directly without lifting it out of a test
// binary.
//
// Columns that appear ONLY in a WHERE or ORDER BY clause, never in a
// SELECT list, still belong here: message_attachments' conversationId,
// editHistoryIndex and orderInMessage, and reactions' conversationId and
// timestamp, are exactly this case (260805-lry-PLAN.md's own read_set
// table). A read set built from SELECT lists alone would silently miss
// them and let a breaking rename through undetected.
var readSetColumns = map[string][]string{
	"conversations": {
		"id", "type", "name", "profileName", "profileFamilyName",
		"e164", "serviceId", "json",
	},
	"items": {
		"id", "json",
	},
	"messages": {
		"id", "conversationId", "sent_at", "type", "sourceServiceId",
		"body", "isErased", "json",
	},
	"message_attachments": {
		"messageId", "fileName", "contentType", "attachmentType",
		// WHERE / ORDER BY only — never in a SELECT list.
		"conversationId", "editHistoryIndex", "orderInMessage",
	},
	"reactions": {
		"messageId", "emoji", "fromId",
		// WHERE / ORDER BY only — never in a SELECT list.
		"conversationId", "timestamp",
	},
}
