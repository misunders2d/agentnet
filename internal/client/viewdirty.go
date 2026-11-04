package client

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
