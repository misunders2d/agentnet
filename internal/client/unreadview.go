package client

import "github.com/misunders2d/agentnet/internal/envelope"

// UnreadConversationCandidates is a conservative prefilter, not an unread
// projection. The normal admitted message/control/read-generation projection
// must still decide which rows are unread. False positives are harmless;
// completely read conversations need no body, file or participation work.
func (a *Agent) UnreadConversationCandidates() (map[string]bool, error) {
	rows, err := a.store.db.Query(`SELECT DISTINCT conv FROM inbox
		WHERE conv IS NOT NULL AND local=0 AND ref_id IS NULL AND read_at IS NULL
		AND ((coalesce(sub,'')='' AND kind IN ('message','question','task','answer','result')) OR sub=?)
		UNION SELECT m.conv FROM attention_marks m JOIN persons p ON p.person=m.owner AND p.state=?
		WHERE m.conv<>'' AND m.state='unread'`, envelope.SubExcerpt, personSelf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var conv string
		if err := rows.Scan(&conv); err != nil {
			return nil, err
		}
		out[conv] = true
	}
	return out, rows.Err()
}

// UnreadDeviceRefs reuses the canonical thread/placement/read projection from
// each peer's kept summaries, and reads again only peers a write touched.
func (a *Agent) UnreadDeviceRefs() ([]DeviceUnreadRef, error) {
	cache := &a.topicCache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	peers, entries, err := a.keptPeerTopics(cache.off.Load())
	if err != nil {
		return nil, err
	}
	var out []DeviceUnreadRef
	for _, peer := range peers {
		out = append(out, entries[peer].unread...)
	}
	return out, nil
}

// Telegram keeps each chat's unread number and changes only the chat a
// message or read touched (TDLib set_dialog_last_read_inbox_message_id). The
// store's change feed says only "look again", so these triggers name the chat
// or direct peer each relevant write touched: "c:"+conv, "p:"+peer, "*" all
// (MEL-731). Columns that churn without changing unread (job state, delivery
// state, status_due) fire nothing. One row per key; each write gives its row
// a seq above every other, so readers follow a high-water mark and never
// write (a reader's write would fail attention actions' snapshot check).
var unreadDirtySchema = `
CREATE TABLE unread_dirty(key TEXT PRIMARY KEY,seq INTEGER NOT NULL) WITHOUT ROWID;
CREATE INDEX unread_dirty_seq ON unread_dirty(seq);
INSERT INTO unread_dirty(key,seq) VALUES('*',1);
` + unreadTrigger("inbox_in", "AFTER INSERT ON inbox", unreadRowKey("NEW", "sender")) +
	unreadTrigger("inbox_out", "AFTER DELETE ON inbox", unreadRowKey("OLD", "sender"), unreadHistoryKey("in", "OLD.id")) +
	unreadTrigger("inbox_set", "AFTER UPDATE OF read_at,conv,sender,kind,sub,local,ref_id,replica,via,verified_by,claimed_fp,lid,group_admission,status,topic_event ON inbox",
		unreadRowKey("OLD", "sender"), unreadRowKey("NEW", "sender"), unreadHistoryKey("in", "NEW.id")) +
	unreadTrigger("outbox_in", "AFTER INSERT ON outbox", unreadRowKey("NEW", "recipient")) +
	unreadTrigger("outbox_out", "AFTER DELETE ON outbox", unreadRowKey("OLD", "recipient"), unreadHistoryKey("out", "OLD.id")) +
	unreadTrigger("outbox_set", "AFTER UPDATE OF conv,recipient,kind,sub,ref_id,lid,group_admission,status,topic_event ON outbox",
		unreadRowKey("OLD", "recipient"), unreadRowKey("NEW", "recipient"), unreadHistoryKey("out", "NEW.id")) +
	unreadTrigger("history_in", "AFTER INSERT ON device_history_rows", `SELECT 'p:'||NEW.peer AS k`) +
	unreadTrigger("history_out", "AFTER DELETE ON device_history_rows", `SELECT 'p:'||OLD.peer AS k`) +
	unreadTrigger("history_set", "AFTER UPDATE ON device_history_rows", `SELECT 'p:'||OLD.peer AS k`, `SELECT 'p:'||NEW.peer AS k`) +
	unreadTrigger("mark_in", "AFTER INSERT ON attention_marks", unreadMarkKey("NEW")...) +
	unreadTrigger("mark_set", "AFTER UPDATE ON attention_marks", unreadMarkKey("NEW")...) +
	unreadTrigger("mark_out", "AFTER DELETE ON attention_marks", unreadMarkKey("OLD")...) +
	unreadTrigger("notice_in", "AFTER INSERT ON reply_receiver_inputs", unreadNoticeKey("NEW")...) +
	unreadTrigger("notice_set", "AFTER UPDATE ON reply_receiver_inputs", unreadNoticeKey("NEW")...) +
	unreadTrigger("notice_out", "AFTER DELETE ON reply_receiver_inputs", unreadNoticeKey("OLD")...) +
	unreadTrigger("conv_in", "AFTER INSERT ON conversations", `SELECT 'c:'||NEW.id AS k`) +
	unreadTrigger("conv_set", "AFTER UPDATE ON conversations", `SELECT 'c:'||NEW.id AS k`) +
	unreadTrigger("conv_out", "AFTER DELETE ON conversations", `SELECT 'c:'||OLD.id AS k`) +
	unreadTrigger("erased_in", "AFTER INSERT ON conv_erased", `SELECT 'c:'||NEW.conv AS k`) +
	unreadTrigger("erased_out", "AFTER DELETE ON conv_erased", `SELECT 'c:'||OLD.conv AS k`)

// unreadTrigger marks each key the selects yield (column k). An UPSERT, not
// OR REPLACE: a statement's own OR IGNORE overrides a trigger's conflict
// clause, which would keep the old seq.
func unreadTrigger(name, on string, selects ...string) string {
	out := "CREATE TRIGGER unread_" + name + " " + on + " BEGIN\n"
	for _, sel := range selects {
		out += " INSERT INTO unread_dirty(key,seq) SELECT k,(SELECT max(seq)+1 FROM unread_dirty) FROM (" + sel + ") WHERE k IS NOT NULL ON CONFLICT(key) DO UPDATE SET seq=excluded.seq;\n"
	}
	return out + "END;\n"
}

// A message row's chat, or its direct peer (device threads have no chat).
func unreadRowKey(row, peer string) string {
	return "SELECT CASE WHEN " + row + ".conv IS NULL THEN 'p:'||" + row + "." + peer + " ELSE 'c:'||" + row + ".conv END AS k"
}

// Imported device history names the thread's peer separately.
func unreadHistoryKey(storage, id string) string {
	return "SELECT 'p:'||peer AS k FROM device_history_rows WHERE storage='" + storage + "' AND id=" + id
}

// A direct message's mark names no chat: its row names the peer.
func unreadMarkKey(row string) []string {
	return []string{
		"SELECT 'c:'||" + row + ".conv AS k WHERE " + row + ".conv<>''",
		"SELECT 'p:'||sender AS k FROM inbox WHERE " + row + ".conv='' AND id=" + row + ".lid AND conv IS NULL",
		"SELECT 'p:'||recipient AS k FROM outbox WHERE " + row + ".conv='' AND id=" + row + ".lid AND conv IS NULL",
		"SELECT 'p:'||peer AS k FROM device_history_rows WHERE " + row + ".conv='' AND id=" + row + ".lid",
	}
}

// A reply receiver's input decides whether its message is a notice.
func unreadNoticeKey(row string) []string {
	return []string{
		"SELECT 'p:'||sender AS k FROM inbox WHERE id=" + row + ".inbox_id AND conv IS NULL",
		unreadHistoryKey("in", row+".inbox_id"),
	}
}

// UnreadDirtySince returns the chats and direct peers whose unread number
// may have changed after mark ("c:"+conv, "p:"+peer; "*" for all) and the
// next mark. It writes nothing; a later write moves its key past next.
func (a *Agent) UnreadDirtySince(mark int64) (keys []string, next int64, err error) {
	rows, err := a.store.db.Query(`SELECT seq,key FROM unread_dirty WHERE seq>? ORDER BY seq`, mark)
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

// UnreadPeers lists the direct peers with device threads.
func (a *Agent) UnreadPeers() ([]string, error) { return a.store.conversationPeers() }

// PeerUnread counts a direct peer's unread device-thread messages with the
// overview's projection: archived topics included, as its alert total.
func (a *Agent) PeerUnread(peer string) (int, error) {
	topics, err := a.peerTopics(peer, storeNow().Unix())
	n := 0
	for _, t := range topics {
		n += t.Unread
	}
	return n, err
}
