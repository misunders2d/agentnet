package client

import "strings"

// The chat list used to read every message of every chat again on each change:
// 1.4 s on a real store, and with it every new message or receipt. Telegram keeps
// each chat's summary and changes only the chat a write touched (TDLib
// set_dialog_last_message_id, MessagesManager.cpp at 4c6377aa; MEL-748). These
// triggers name the scope of each write the list's summaries read, as
// unread_dirty does for unread numbers: "c:"+conv, "p:"+peer for a direct peer's
// device threads, "h" for history copying's progress, "*" for all.
//
// Scoping is the only filter, and it leans one way: a table whose scope is not
// plain marks everything. A missed scope would show old state; an extra one only
// rebuilds. Copies between devices (viewCarrierSubs) are no chat's rows: a whole
// catch-up marks only "h".
const viewCarrierSubs = `('model-sync','device-history','device-file','topic-sync','topic-state-sync','direct-topic-sync','invitation-sync','reminder-sync','read-sync','root-sync','history-archive','drive-space')`

func viewCarrier(row string) string {
	return "(coalesce(" + row + ".sub,'') IN " + viewCarrierSubs + " OR (" + row + ".conv IS NOT NULL AND coalesce(" + row + ".sub,'') IN ('history','file')))"
}

// viewTrigger marks each key the selects yield (column k), as unreadTrigger does.
func viewTrigger(name, on, when string, selects ...string) string {
	out := "CREATE TRIGGER view_" + name + " " + on
	if when != "" {
		out += " WHEN " + when
	}
	out += " BEGIN\n"
	for _, sel := range selects {
		out += " INSERT INTO view_dirty(key,seq) SELECT k,(SELECT max(seq)+1 FROM view_dirty) FROM (" + sel + ") WHERE k IS NOT NULL ON CONFLICT(key) DO UPDATE SET seq=excluded.seq;\n"
	}
	return out + "END;\n"
}

// viewTable marks one scope for every write to a table; row is NEW or OLD in key.
func viewTable(name, table string, key func(row string) string) string {
	return viewTrigger(name+"_in", "AFTER INSERT ON "+table, "", key("NEW")) +
		viewTrigger(name+"_set", "AFTER UPDATE ON "+table, "", key("OLD"), key("NEW")) +
		viewTrigger(name+"_out", "AFTER DELETE ON "+table, "", key("OLD"))
}

func viewConvKey(col string) func(string) string {
	return func(row string) string { return "SELECT 'c:'||" + row + "." + col + " AS k" }
}

func viewFixedKey(key string) func(string) string {
	return func(string) string { return "SELECT '" + key + "' AS k" }
}

var viewDirtySchema = `
CREATE TABLE view_dirty(key TEXT PRIMARY KEY,seq INTEGER NOT NULL) WITHOUT ROWID;
CREATE INDEX view_dirty_seq ON view_dirty(seq);
INSERT INTO view_dirty(key,seq) VALUES('*',1);
` +
	viewTrigger("inbox_in", "AFTER INSERT ON inbox", "NOT "+viewCarrier("NEW"), unreadRowKey("NEW", "sender")) +
	viewTrigger("inbox_set", "AFTER UPDATE ON inbox", "NOT "+viewCarrier("NEW"), unreadRowKey("OLD", "sender"), unreadRowKey("NEW", "sender"), unreadHistoryKey("in", "NEW.id")) +
	viewTrigger("inbox_out", "AFTER DELETE ON inbox", "NOT "+viewCarrier("OLD"), unreadRowKey("OLD", "sender"), unreadHistoryKey("in", "OLD.id")) +
	viewTrigger("outbox_in", "AFTER INSERT ON outbox", "NOT "+viewCarrier("NEW"), unreadRowKey("NEW", "recipient")) +
	viewTrigger("outbox_set", "AFTER UPDATE ON outbox", "NOT "+viewCarrier("NEW"), unreadRowKey("OLD", "recipient"), unreadRowKey("NEW", "recipient"), unreadHistoryKey("out", "NEW.id")) +
	viewTrigger("outbox_out", "AFTER DELETE ON outbox", "NOT "+viewCarrier("OLD"), unreadRowKey("OLD", "recipient"), unreadHistoryKey("out", "OLD.id")) +
	viewTrigger("carrier_in", "AFTER INSERT ON outbox", viewCarrier("NEW"), `SELECT 'h' AS k`) +
	viewTrigger("carrier_set", "AFTER UPDATE ON outbox", viewCarrier("NEW"), `SELECT 'h' AS k`) +
	viewTrigger("carrier_out", "AFTER DELETE ON outbox", viewCarrier("OLD"), `SELECT 'h' AS k`) +
	viewTable("device_rows", "device_history_rows", func(row string) string { return "SELECT 'p:'||" + row + ".peer AS k" }) +
	viewTrigger("mark_in", "AFTER INSERT ON attention_marks", "", unreadMarkKey("NEW")...) +
	viewTrigger("mark_set", "AFTER UPDATE ON attention_marks", "", unreadMarkKey("NEW")...) +
	viewTrigger("mark_out", "AFTER DELETE ON attention_marks", "", unreadMarkKey("OLD")...) +
	viewTrigger("notice_in", "AFTER INSERT ON reply_receiver_inputs", "", unreadNoticeKey("NEW")...) +
	viewTrigger("notice_set", "AFTER UPDATE ON reply_receiver_inputs", "", unreadNoticeKey("NEW")...) +
	viewTrigger("notice_out", "AFTER DELETE ON reply_receiver_inputs", "", unreadNoticeKey("OLD")...) +
	viewTable("conversations", "conversations", viewConvKey("id")) +
	viewTable("erased", "conv_erased", func(row string) string {
		return "SELECT CASE WHEN " + row + ".conv='' THEN '*' ELSE 'c:'||" + row + ".conv END AS k"
	}) +
	viewTable("participation", "participation_events", viewConvKey("conv")) +
	viewTable("group_context", "group_context", viewConvKey("conv")) +
	viewTable("group_heads", "group_known_heads", viewConvKey("conv")) +
	viewTable("group_pending", "group_pending_withdrawals", viewConvKey("conv")) +
	viewTable("group_withdrawals", "group_withdrawals", viewConvKey("conv")) +
	viewTable("skipped", "skipped_copies", viewConvKey("conv")) +
	viewTable("shared_titles", "topic_shared_titles", viewConvKey("conv")) +
	viewTable("placements", "direct_topic_placements", func(row string) string { return "SELECT 'p:'||" + row + ".peer AS k" }) +
	// topic_state's peer names a device thread's peer or a chat.
	viewTrigger("topic_in", "AFTER INSERT ON topic_state", "", `SELECT 'p:'||NEW.peer AS k`, `SELECT 'c:'||id AS k FROM conversations WHERE id=NEW.peer`) +
	viewTrigger("topic_set", "AFTER UPDATE ON topic_state", "", `SELECT 'p:'||NEW.peer AS k`, `SELECT 'c:'||id AS k FROM conversations WHERE id=NEW.peer`) +
	viewTrigger("topic_out", "AFTER DELETE ON topic_state", "", `SELECT 'p:'||OLD.peer AS k`, `SELECT 'c:'||id AS k FROM conversations WHERE id=OLD.peer`) +
	viewTable("persons", "persons", viewFixedKey("*")) +
	viewTable("person_devices", "person_devices", viewFixedKey("*")) +
	viewTable("person_chain", "person_chain", viewFixedKey("*")) +
	viewTable("peers", "peers", viewFixedKey("*")) +
	viewTrigger("continuity", "AFTER UPDATE ON continuity_generation", "", `SELECT '*' AS k`) +
	viewTrigger("suspended_in", "AFTER INSERT ON config", "NEW.k='suspended_devices'", `SELECT '*' AS k`) +
	viewTrigger("suspended_set", "AFTER UPDATE ON config", "NEW.k='suspended_devices' AND OLD.v IS NOT NEW.v", `SELECT '*' AS k`) +
	viewTrigger("suspended_out", "AFTER DELETE ON config", "OLD.k='suspended_devices'", `SELECT '*' AS k`) +
	viewTrigger("arrival_in", "AFTER INSERT ON config", "NEW.k='arrival'", `SELECT 'h' AS k`) +
	viewTrigger("arrival_set", "AFTER UPDATE ON config", "NEW.k='arrival'", `SELECT 'h' AS k`) +
	viewTable("history_jobs", "history_jobs", viewFixedKey("h")) +
	viewTable("history_catchup", "history_catchup", viewFixedKey("h")) +
	viewTable("history_deferred", "history_deferred", viewFixedKey("h")) +
	viewTable("history_copies", "history_copies", viewFixedKey("h")) +
	viewTable("device_jobs", "device_history_jobs", viewFixedKey("h")) +
	viewTable("device_pending", "device_history_pending", viewFixedKey("h")) +
	viewTable("device_copies", "device_history_copies", viewFixedKey("h"))

// ViewDirtySince returns the scopes whose list summaries may have changed after
// mark, and the next mark. A summary read after this call is at least as new as
// next: a write meanwhile moves its key past next and is seen again, so a reader
// never loses a change. It writes nothing.
func (a *Agent) ViewDirtySince(mark int64) (keys []string, next int64, err error) {
	rows, err := a.store.db.Query(`SELECT seq,key FROM view_dirty WHERE seq>? ORDER BY seq`, mark)
	if err != nil {
		return nil, mark, err
	}
	defer rows.Close()
	next = mark
	for rows.Next() {
		var key string
		if err := rows.Scan(&next, &key); err != nil {
			return nil, mark, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, mark, err
	}
	return keys, next, nil
}

// NoteViewChange tells the page to look again, with nothing stored meanwhile.
func (a *Agent) NoteViewChange() { a.changes.bump() }

// SetViewCachesOff makes the device threads' list read everything afresh, leaving
// what it keeps as it was: a cached list can then be compared with a fresh one.
func (a *Agent) SetViewCachesOff(off bool) { a.topicCache.off.Store(off) }

// The list's keys name every write its summaries read. A chat's own view reads
// more tables: these triggers name the rest, so a change event can say which
// chats moved and the page reads again only those (MEL-748; TDLib sends one
// update per changed chat, updateChatLastMessage and the rest, td_api.tl at
// 03220f6b). The step is not appended in this branch: the send path's branch
// owns this phase's one schema step and appends ViewCoverSchema after
// proposalLookupSchema in store.go. Until it is there, ViewKeysScoped is false
// and every change reads every view again, as before. With it, the background
// flush's note after it stored rows (backgroundsend.go, also that branch's)
// must be noteStoredChange, not NoteChange: as NoteChange it makes every
// received message's change unscoped (measured: 0 of 20 events then spare
// the open chat, 20 of 20 with it).
//
// Each table names its chat, or marks everything: a missed key would show old
// state, an extra one only reads again. viewCoverSkip are the tables no view
// reads: copies queued for this person's other devices (their carriers mark
// "h" through outbox), the Hub's routes, sent reports, and the dirty tables.
var viewCoverSkip = []string{"sqlite_sequence", "unread_dirty", "view_dirty", "routes", "reported",
	"agent_model_copies", "attention_mark_copies", "direct_topic_copies", "group_invitation_copies", "own_invitation_copies",
	"read_mark_copies", "reminder_mark_copies", "topic_location_copies", "topic_mark_copies", "topic_title_copies"}

// viewMessageKey names the chat of the message whose id is col.
func viewMessageKey(col string) func(string) string {
	return func(row string) string {
		id := row + "." + col
		return "SELECT coalesce((SELECT 'c:'||conv FROM inbox WHERE id=" + id + " AND conv IS NOT NULL),(SELECT 'c:'||conv FROM outbox WHERE id=" + id + " AND conv IS NOT NULL),'*') AS k"
	}
}

// viewScopeKey names a topic's chat, or its direct peer's device thread.
func viewScopeKey(row string) string {
	return "SELECT CASE WHEN EXISTS(SELECT 1 FROM conversations WHERE id=" + row + ".scope) THEN 'c:'||" + row + ".scope ELSE 'p:'||" + row + ".scope END AS k"
}

var viewCover = []struct {
	table string
	key   func(string) string
}{
	{"alert_mutes", viewConvKey("conv")}, {"alerts", viewConvKey("conv")}, {"file_serves", viewConvKey("conv")},
	{"group_invitation_cancellations", viewConvKey("conv")}, {"group_invitations", viewConvKey("conv")},
	{"group_proof_records", viewConvKey("conv")}, {"group_proof_roots", viewConvKey("conv")}, {"group_publications", viewConvKey("conv")},
	{"proposal_choices", viewConvKey("conv")}, {"proposal_sends", viewConvKey("conv")}, {"read_marks", viewConvKey("conv")},
	{"reminder_marks", viewConvKey("conv")}, {"reminders", viewConvKey("conv")}, {"reply_receivers", viewConvKey("conv")},
	{"room_context", viewConvKey("conv")}, {"room_membership_events", viewConvKey("conv")}, {"room_turn_readers", viewConvKey("conv")},
	{"scheduled_sends", viewConvKey("conv")},
	{"attachments", viewMessageKey("message_id")}, {"file_requests", viewMessageKey("message_id")},
	{"sent_attachments", viewMessageKey("message_id")}, {"uploads", viewMessageKey("message_id")},
	{"request_continuations", viewMessageKey("request")}, {"request_parks", viewMessageKey("request")}, {"request_waits", viewMessageKey("request")},
	{"topic_titles", viewScopeKey}, {"topic_marks", viewScopeKey}, {"topic_locations", viewScopeKey},
	{"history_receipts", viewFixedKey("h")}, {"history_archive_children", viewFixedKey("h")}, {"history_archive_entries", viewFixedKey("h")},
	{"history_archive_export_errors", viewFixedKey("h")}, {"history_archive_jobs", viewFixedKey("h")}, {"device_history_sources", viewFixedKey("h")},
	{"agent_models", viewFixedKey("*")}, {"alert_senders", viewFixedKey("*")}, {"approvals", viewFixedKey("*")}, {"attention", viewFixedKey("*")},
	{"conversations_v1", viewFixedKey("*")}, {"device_admin_notices", viewFixedKey("*")}, {"device_links", viewFixedKey("*")},
	{"direct_blobs", viewFixedKey("*")}, {"direct_topic_operations", viewFixedKey("*")}, {"group_invitation_refreshes", viewFixedKey("*")},
	{"link_offers", viewFixedKey("*")}, {"native_requests", viewFixedKey("*")}, {"operator_persons", viewFixedKey("*")},
	{"operators", viewFixedKey("*")}, {"own_invitation_views", viewFixedKey("*")}, {"person_grants", viewFixedKey("*")},
	{"persons_v1", viewFixedKey("*")}, {"quarantine", viewFixedKey("*")}, {"reply_sessions", viewFixedKey("*")},
	{"task_grants", viewFixedKey("*")}, {"team_chain", viewFixedKey("*")}, {"teams", viewFixedKey("*")},
	{"topic_transfer_manifest", viewFixedKey("*")}, {"topic_transfer_plans", viewFixedKey("*")},
}

// ViewCoverSchema is the step that makes change events name their chats.
var ViewCoverSchema = func() string {
	var b strings.Builder
	for _, c := range viewCover {
		b.WriteString(viewTable("cover_"+c.table, c.table, c.key))
	}
	return b.String()
}()

// ViewKeysScoped reports whether every table a view reads names its scope in
// view_dirty: then a change's keys say which views moved.
func (a *Agent) ViewKeysScoped() bool {
	var n int
	err := a.store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'view\_cover\_%' ESCAPE '\'`).Scan(&n)
	return err == nil && n == 3*len(viewCover)
}

// ChatViewMark is what conv's view depends on in view_dirty: its own and its
// continuity family's keys, and the newest mark of those and of every key that
// is no one chat's ("*", "h", "p:"). The same mark means none of them moved.
func (a *Agent) ChatViewMark(conv string) (keys []string, mark int64, err error) {
	family, err := a.continuityDMs(conv)
	if err != nil {
		return nil, 0, err
	}
	keys = []string{"c:" + conv}
	for _, r := range family {
		if r.ID != conv {
			keys = append(keys, "c:"+r.ID)
		}
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	// "c:" keys sort between "c:" and "c;": the rest is two index ranges.
	err = a.store.db.QueryRow(`SELECT coalesce(max(seq),0) FROM view_dirty WHERE key IN (?`+strings.Repeat(",?", len(keys)-1)+`) OR key<'c:' OR key>='c;'`, args...).Scan(&mark)
	return keys, mark, err
}

// ViewScope returns the keys that moved after mark, as ViewDirtySince, when
// keys are scoped; ok is false when they say nothing. Past max keys it says
// "*": a burst that touched that many chats reads everything again anyway.
func (a *Agent) ViewScope(mark int64, max int) (keys []string, next int64, ok bool) {
	if !a.ViewKeysScoped() {
		return nil, mark, false
	}
	keys, next, err := a.ViewDirtySince(mark)
	if err != nil {
		return nil, mark, false
	}
	if len(keys) > max {
		keys = []string{"*"}
	}
	return keys, next, true
}

// ViewMark is the newest view_dirty mark: where a new listener starts.
func (a *Agent) ViewMark() (int64, error) {
	var mark int64
	return mark, a.store.db.QueryRow(`SELECT coalesce(max(seq),0) FROM view_dirty`).Scan(&mark)
}
