package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Append-only storage for the optional signed conversation topic reference.
const chatTopicSchema = `ALTER TABLE inbox ADD COLUMN topic TEXT; ALTER TABLE outbox ADD COLUMN topic TEXT;
ALTER TABLE inbox ADD COLUMN topic_event TEXT; ALTER TABLE outbox ADD COLUMN topic_event TEXT;`

func topicEventJSON(e *envelope.TopicEvent) string {
	if e == nil {
		return ""
	}
	b, _ := json.Marshal(e)
	return string(b)
}

// ChatTopicAssignments keeps main-flow turns unassigned. Explicit references
// win; a promotion seeds one message and its reply descendants. Cycles and
// unknown parents cannot join unrelated messages. IDs are logical, never copies.
func ChatTopicAssignments(msgs []ConvMessage) map[string]string {
	by := map[string]ConvMessage{}
	promoted := map[string]bool{}
	aliases := map[string]string{}
	for _, m := range msgs {
		if m.ID != "" {
			aliases[m.ID] = m.LID
		}
		for _, copy := range m.Copies {
			if copy.ID != "" {
				aliases[copy.ID] = m.LID
			}
		}
	}
	for _, m := range msgs {
		if m.Sub == "" {
			by[m.LID] = m
		}
		if m.TopicEvent != nil && m.TopicEvent.Action == "create" {
			promoted[m.Topic] = true
		}
	}
	assigned := map[string]string{}
	for _, m := range msgs {
		id := m.LID
		seen := map[string]bool{}
		cur := id
		for cur != "" && !seen[cur] {
			seen[cur] = true
			if lid := aliases[cur]; lid != "" {
				cur = lid
			}
			r, ok := by[cur]
			if !ok {
				break
			}
			if r.Topic != "" {
				assigned[id] = r.Topic
				break
			}
			if promoted[cur] {
				assigned[id] = cur
				break
			}
			cur = r.ReplyTo
		}
	}
	applyTopicOrganization(msgs, assigned)
	return assigned
}

// Stable signed order makes done/open converge regardless of delivery order.
func chatOrder(a, b ConvMessage) bool {
	if a.Sent != b.Sent {
		return a.Sent < b.Sent
	}
	return a.LID < b.LID
}
func (a *Agent) ChatTopics(conv string) ([]ThreadSummary, error) {
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		return nil, err
	}
	return a.ChatTopicsForMessages(conv, msgs)
}

// ChatTopicsForMessages derives the topic view from the same accepted messages
// the caller is displaying. It avoids loading and decorating the full history a
// second time for one chat response; local visibility/read state is still fresh.
func (a *Agent) ChatTopicsForMessages(conv string, msgs []ConvMessage, precomputed ...map[string]string) ([]ThreadSummary, error) {
	locals, err := a.store.topicLocals(conv)
	if err != nil {
		return nil, err
	}
	assigned := topicAssignmentMap(msgs, precomputed)
	ts := summarizeChatTopics(conv, msgs, locals, storeNow().Unix(), assigned)
	counts := make(map[string]int)
	for _, m := range msgs {
		if m.TopicEvent == nil && m.Unread {
			counts[assigned[m.LID]]++
		}
	}
	for i := range ts {
		ts[i].Unread = counts[ts[i].ID]
	}
	if err := a.projectSharedTopicTitles(conv, ts); err != nil {
		return nil, err
	}
	return ts, nil
}

// Read only the already selected conversation/topic set in one batch. Shared
// presence is applied after private labels; an empty reset restores automatic.
func (a *Agent) projectSharedTopicTitles(conv string, topics []ThreadSummary) error {
	if len(topics) == 0 {
		return nil
	}
	ids := make([]string, 0, len(topics))
	by := make(map[string]int, len(topics))
	for i, t := range topics {
		if protocol.ValidID(t.ID) {
			ids = append(ids, t.ID)
			by[t.ID] = i
		}
	}
	if len(ids) == 0 {
		return nil
	}
	selected, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	rows, err := a.store.db.Query(`SELECT topic,title FROM topic_shared_titles WHERE conv=? AND topic IN (SELECT value FROM json_each(?)) LIMIT ?`, conv, string(selected), len(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, title string
		if err = rows.Scan(&id, &title); err != nil {
			return err
		}
		i, ok := by[id]
		if !ok {
			continue
		}
		t := &topics[i]
		auto := t.Title
		if t.Renamed {
			auto = t.AutoTitle
		}
		t.Title = title
		t.AutoTitle = auto
		t.Renamed = title != ""
		if title == "" {
			t.Title = auto
			t.AutoTitle = ""
		}
	}
	return rows.Err()
}
func summarizeChatTopics(conv string, msgs []ConvMessage, locals map[string]topicLocal, now int64, precomputed ...map[string]string) []ThreadSummary {
	return summarizeChatTopicView(conv, msgs, locals, now, false, precomputed...)
}

func topicAssignmentMap(msgs []ConvMessage, precomputed []map[string]string) map[string]string {
	if len(precomputed) > 0 && precomputed[0] != nil {
		return precomputed[0]
	}
	return ChatTopicAssignments(msgs)
}

func summarizeChatTopicView(conv string, msgs []ConvMessage, locals map[string]topicLocal, now int64, main bool, precomputed ...map[string]string) []ThreadSummary {
	assigned := topicAssignmentMap(msgs, precomputed)
	// A display move keeps each existing destination's original title anchor.
	// Derive it from signed original placement, never another person's private
	// label or the earlier source messages now displayed in the destination.
	originals := msgs
	if !main {
		originals = make([]ConvMessage, 0, len(msgs))
		for _, m := range msgs {
			if !envelope.TopicOrganization(m.TopicEvent) {
				originals = append(originals, m)
			}
		}
	}
	var anchors map[string]ConvMessage
	if !main && len(originals) != len(msgs) {
		anchors = map[string]ConvMessage{}
		originalAssigned := ChatTopicAssignments(originals)
		originalGroups := map[string][]ConvMessage{}
		for _, m := range originals {
			if topic := originalAssigned[m.LID]; topic != "" && m.Sub == "" && m.TopicEvent == nil {
				originalGroups[topic] = append(originalGroups[topic], m)
			}
		}
		for topic, g := range originalGroups {
			sortChatTurns(g)
			anchors[topic] = g[0]
		}
	}
	groups := map[string][]ConvMessage{}
	events := map[string][]ConvMessage{}
	for _, m := range msgs {
		if m.Sub != "" {
			continue
		}
		if m.TopicEvent != nil {
			if !main {
				events[m.Topic] = append(events[m.Topic], m)
			}
			continue
		}
		if topic := assigned[m.LID]; main && topic == "" || !main && topic != "" {
			groups[topic] = append(groups[topic], m)
		}
	}
	out := []ThreadSummary{}
	for id, g := range groups {
		sortChatTurns(g)
		first, last := g[0], g[len(g)-1]
		if anchor, ok := anchors[id]; ok {
			first = anchor
		}
		aliases := map[string]string{}
		for _, m := range g {
			if m.ID != "" {
				aliases[m.ID] = m.LID
			}
			for _, c := range m.Copies {
				if c.ID != "" {
					aliases[c.ID] = m.LID
				}
			}
		}
		facts := make([]threadRow, len(g))
		for i, m := range g {
			if parent := aliases[m.ReplyTo]; parent != "" {
				m.ReplyTo = parent
			}
			state := m.Job
			incoming := state != "" && m.Dir == "in"
			if state == "" {
				state = m.State
			}
			execState, execStale := "", false
			if m.Exec != nil {
				execState, execStale = m.Exec.State, m.Exec.Stale
			}
			facts[i] = threadRow{link: link{id: m.LID, replyTo: m.ReplyTo, at: m.Sent}, kind: m.Kind, status: m.status, topicDone: m.TopicDone, in: incoming || m.Kind == envelope.KindAnswer || m.Kind == envelope.KindResult, state: state, localJob: m.Job != "", execState: execState, execStale: execStale}
		}
		local := locals[id]
		shared := topicLocal{}
		by := ""
		ev := events[id]
		sortChatEvents(ev)
		autoTitle := BodyPreview(first.Body)
		var namedBy ConvMessage
		hasName := false
		for _, m := range ev {
			// These are stored admitted events, including retained history.
			// Signed order, rather than arrival order, fixes the first name.
			if envelope.AgentTopicOrganization(envelope.Inner{TopicEvent: m.TopicEvent, PID: m.PID, Origin: m.Origin}) && (!hasName || chatOrder(m, namedBy)) {
				if title := topicOrganizationNoticeTitle(id, m.Body); title != "" {
					autoTitle, namedBy, hasName = title, m, true
				}
			}
			if m.TopicEvent.Action != "done" && m.TopicEvent.Action != "open" {
				continue
			}
			seen := map[string]bool{}
			for _, lid := range m.TopicEvent.Seen {
				seen[lid] = true
			}
			covered := true
			for _, r := range g {
				covered = covered && seen[r.LID]
			}
			if covered {
				shared = topicLocal{Mark: m.TopicEvent.Action, MarkAt: m.Sent, MarkCount: len(g)}
				by = m.From
			} else {
				shared = topicLocal{}
				by = ""
			}
		}
		// Shared closure wins over local state. Local archive never hides pending work.
		l := shared
		if local.Mark == "archived" && local.MarkCount >= len(g)+len(events[id]) && local.MarkAt >= shared.MarkAt {
			l = local
		}

		if main {
			l = local
		}

		activity := last.Sent
		for _, event := range events[id] {
			if event.Sent > activity {
				activity = event.Sent
			}
		}
		v := deriveTopic(facts, l, now)
		if activity > v.QuietSince {
			v.QuietSince = activity
		}
		if v.State == TopicArchived && l.Mark != "archived" && now-v.QuietSince < int64(TopicArchiveAfter/time.Second) {
			v.State = TopicActive
			if v.DoneBy != "" {
				v.State = TopicDone
			}
		}

		t := ThreadSummary{ID: id, Conv: conv, Title: autoTitle, Last: BodyPreview(last.Body), LastAt: time.Unix(activity, 0), Count: len(g), State: v.State, DoneBy: v.DoneBy, Pending: v.Pending, QuietSince: time.Unix(v.QuietSince, 0)}
		pending := pendingTopicRows(facts)
		for _, i := range pending {
			// The logical id matches replies; the stored copy id opens the
			// exact request and its existing local actions.
			t.PendingIDs = append(t.PendingIDs, g[i].ID)
		}
		// Pending alone is no evidence anything waits: a request with no
		// reply and no current word from its executor is unconfirmed.
		t.Waiting, t.Unconfirmed = topicWaiting(facts, pending)
		if shared.Mark == "done" && v.DoneBy == DoneByYou {
			t.DoneBy = "person"
			t.ConcludedBy = by
		}
		for _, m := range g {
			if m.LID == v.Conclusion {
				t.Conclusion = BodyPreview(m.Body)
				t.ConcludedBy = m.From
			}
			if m.Job == stateRunning {
				t.Running++
			}
			if m.Job == stateHeld || m.Job == stateNeedHuman {
				t.Review++
			}
		}
		if local.Title != "" {
			t.AutoTitle, t.Title, t.Renamed = t.Title, local.Title, true
		}
		out = append(out, t)
	}
	if !main {
		var previous []ThreadSummary
		for source, destination := range ChatTopicRedirects(msgs) {
			if previous == nil {
				previous = summarizeChatTopics(conv, originals, locals, now)
			}
			found := false
			for i := range out {
				if out[i].ID == source {
					out[i].Redirect, found = destination, true
					for _, old := range previous {
						if old.ID == source {
							out[i].Title, out[i].AutoTitle, out[i].Renamed = old.Title, old.AutoTitle, old.Renamed
						}
					}
				}
			}
			if found {
				continue
			}
			for _, old := range previous {
				if old.ID == source {
					old.Redirect, old.State, old.Count = destination, TopicDone, 0
					old.Review, old.Running, old.Unread = 0, 0, 0
					old.Pending, old.Waiting, old.PendingIDs, old.Unconfirmed = false, false, nil, 0
					out = append(out, old)
					break
				}
			}
		}
	}
	sortTopics(out)
	return out
}

func (a *Agent) outgoingTopic(conv, topic, reply string) (string, error) {
	if topic == "new" {
		return protocol.NewID(), nil
	}
	if topic != "" {
		if !protocol.ValidID(topic) {
			return "", errors.New("invalid topic")
		}
		return topic, nil
	}
	if reply == "" {
		return "", nil
	}
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		return "", err
	}
	// A reply without an explicit topic keeps its signed original placement;
	// display organization never expands old chains or task scope.
	originals := make([]ConvMessage, 0, len(msgs))
	for _, m := range msgs {
		if !envelope.TopicOrganization(m.TopicEvent) {
			originals = append(originals, m)
		}
	}
	assigned := ChatTopicAssignments(originals)
	for _, m := range msgs {
		if m.ID == reply || m.LID == reply {
			return assigned[m.LID], nil
		}
	}
	return "", nil
}

func (a *Agent) ChangeChatTopic(ctx context.Context, conv, id, what, title string, count int) (bool, error) {
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		return false, err
	}
	ts, err := a.ChatTopicsForMessages(conv, msgs)
	if err != nil {
		return false, err
	}
	var t *ThreadSummary
	for i := range ts {
		if ts[i].ID == id {
			t = &ts[i]
		}
	}
	if what == "create" {
		found := false
		for _, m := range msgs {
			if m.LID == id && m.Sub == "" && m.TopicEvent == nil {
				found = true
			}
		}
		if !found {
			return false, ErrNoMessage
		}
	} else if t == nil {
		return false, ErrNoMessage
	}
	switch what {
	case "create", "done", "reopen":
		if what == "done" && count > 0 && count != t.Count {
			return false, nil
		}
		action := what
		if action == "reopen" {
			action = "open"
		}
		seen := []string{}
		assigned := ChatTopicAssignments(msgs)
		for _, m := range msgs {
			if assigned[m.LID] == id && m.TopicEvent == nil {
				seen = append(seen, m.LID)
			}
		}
		sort.Strings(seen)
		text := map[string]string{"create": "Made a topic.", "done": "Marked this topic done.", "reopen": "Reopened this topic."}[what]
		prior := []ConvMessage{}
		for _, m := range msgs {
			if m.Topic == id && m.TopicEvent != nil {
				prior = append(prior, m)
			}
		}
		sortChatEvents(prior)
		parent := ""
		if len(prior) > 0 {
			parent = prior[len(prior)-1].LID
		}
		_, err = a.SendConv(ctx, conv, ConvOutgoing{Body: text, Topic: id, ReplyTo: parent, TopicEvent: &envelope.TopicEvent{Action: action, Seen: seen}})
	case "rename":
		_, err = a.RenameSharedTopic(ctx, conv, id, title)
	case "archive":
		count := t.Count
		for _, m := range msgs {
			if m.Topic == id && m.TopicEvent != nil {
				count++
			}
		}
		err = a.changeTopicMark(conv, id, protocol.TopicMarkArchived, count)
	case "delete":
		err = a.deleteChatTopic(ctx, conv, id, msgs)
	default:
		return false, errors.New("unknown topic action")
	}
	return err == nil, err
}

func (a *Agent) deleteChatTopic(ctx context.Context, conv, id string, msgs []ConvMessage) error {
	assigned := ChatTopicAssignments(msgs)
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	deletion := protocol.NewID()
	self := a.Self().Fingerprint()
	for _, m := range msgs {
		if assigned[m.LID] != id {
			continue
		}
		key := m.Key
		if m.History {
			key = m.Claimed
		}
		if m.Dir == "out" && m.Via == "" {
			key = self
		}
		if key == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO conv_erased(conv,key,lid,deletion,shared) VALUES(?,?,?,?,0)`, conv, key, m.LID, deletion); err != nil {
			return err
		}
	}
	cleared := ""
	if id == "" { // the erasure reaches own devices; so does the cleared Main flow mark
		if err = tx.QueryRow(`SELECT coalesce(mark,'') FROM topic_state WHERE peer=? AND topic=?`, mainTopicScope(conv), mainTopicKey).Scan(&cleared); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if cleared != "" {
			if _, err = a.setTopicMark(tx, mainTopicScope(conv), mainTopicKey, protocol.TopicMarkNone, 0); err != nil {
				return err
			}
		}
	}
	if err = eraseCoveredIn(tx, conv, self); err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err != nil {
		return err
	}
	if cleared != "" {
		a.topicTitlesChanged()
	}
	a.dropErasedFiles(conv)
	_, err = a.shareErased(ctx)
	a.NoteChange()
	return err
}

func (a *Agent) ArchiveTopic(peer, id string) error {
	return a.setTopic(peer, id, func(l *topicLocal, count int) { l.Mark, l.MarkAt, l.MarkCount = "archived", storeNow().Unix(), count })
}

func chatTopicPage(all []ThreadSummary, q TopicQuery) (TopicPage, error) {
	if q.State != "" && q.State != TopicActive && q.State != TopicDone && q.State != TopicArchived {
		return TopicPage{}, ErrTopicQuery
	}
	limit := q.Limit
	if limit == 0 {
		limit = TopicPageDefault
	}
	if limit < 1 || limit > TopicPageMax {
		return TopicPage{}, ErrTopicQuery
	}
	after, err := parseTopicPageCursor(q)
	if err != nil {
		return TopicPage{}, err
	}
	words := strings.Fields(strings.ToLower(q.Query))
	matched := make([]ThreadSummary, 0, len(all))
	for _, t := range all {
		if (q.State == "" || q.State == t.State) && topicMatches(t, words) {
			matched = append(matched, t)
		}
	}
	sortUnreadTopics(matched)
	start := 0
	if after != nil {
		start = sort.Search(len(matched), func(i int) bool { return unreadTopicBefore(*after, matched[i]) })
	}
	end := min(start+limit, len(matched))
	page := TopicPage{Topics: append([]ThreadSummary{}, matched[start:end]...), Matched: len(matched)}
	if end < len(matched) {
		page.Next = topicPageCursor(matched[end-1])
	}
	return page, nil
}

// A cursor is a position, never an exact-row lookup. Keep its unread bucket
// even if that topic was read, moved or deleted between requests. Pre-u1
// chronological cursors are deliberately refused: reload from the first page.
func topicPageCursor(t ThreadSummary) string {
	bucket := "0"
	if t.Unread > 0 {
		bucket = "1"
	}
	return strings.Join([]string{"u1", bucket, strconv.FormatInt(t.LastAt.Unix(), 10), t.Conv, t.Peer, t.ID}, "|")
}
func parseTopicPageCursor(q TopicQuery) (*ThreadSummary, error) {
	if q.Before == "" {
		return nil, nil
	}
	p := strings.Split(q.Before, "|")
	if len(q.Before) > 1024 || len(p) != 6 || p[0] != "u1" || (p[1] != "0" && p[1] != "1") || p[3] != q.Conv || p[5] == "" || (q.Peer != "" && p[4] != q.Peer) {
		return nil, ErrTopicQuery
	}
	for _, c := range p[2] {
		if c < '0' || c > '9' {
			return nil, ErrTopicQuery
		}
	}
	sec, err := strconv.ParseInt(p[2], 10, 64)
	if err != nil || sec < 0 || sec > 8640000000000 {
		return nil, ErrTopicQuery
	}
	unread := 0
	if p[1] == "1" {
		unread = 1
	}
	return &ThreadSummary{Conv: p[3], Peer: p[4], ID: p[5], LastAt: time.Unix(sec, 0), Unread: unread}, nil
}
func unreadTopicBefore(a, b ThreadSummary) bool {
	if (a.Unread > 0) != (b.Unread > 0) {
		return a.Unread > 0
	}
	return newer(a, b)
}
func sortUnreadTopics(ts []ThreadSummary) {
	sort.Slice(ts, func(i, j int) bool { return unreadTopicBefore(ts[i], ts[j]) })
}

// Within equal signed seconds, parents precede replies. Shared actions are
// causally ordered even across clock skew: every new action names the last
// action this author held. Concurrent branches have a stable signed tie.
func chatDepths(g []ConvMessage) map[string]int {
	by := map[string]ConvMessage{}
	aliases := map[string]string{}
	for _, m := range g {
		if m.ID != "" {
			aliases[m.ID] = m.LID
		}
		for _, c := range m.Copies {
			if c.ID != "" {
				aliases[c.ID] = m.LID
			}
		}
	}
	for _, m := range g {
		if parent := aliases[m.ReplyTo]; parent != "" {
			m.ReplyTo = parent
		}
		by[m.LID] = m
	}
	depths := map[string]int{}
	var depth func(string, map[string]bool) int
	depth = func(id string, seen map[string]bool) int {
		if d, ok := depths[id]; ok {
			return d
		}
		m, ok := by[id]
		if !ok || seen[id] {
			return 0
		}
		seen[id] = true
		d := 0
		if _, ok := by[m.ReplyTo]; ok {
			d = 1 + depth(m.ReplyTo, seen)
		}
		delete(seen, id)
		depths[id] = d
		return d
	}
	for _, m := range g {
		depth(m.LID, map[string]bool{})
	}
	return depths
}
func sortChatTurns(g []ConvMessage) {
	depth := chatDepths(g)
	sort.Slice(g, func(i, j int) bool {
		if g[i].Sent == g[j].Sent && depth[g[i].LID] != depth[g[j].LID] {
			return depth[g[i].LID] < depth[g[j].LID]
		}
		return chatOrder(g[i], g[j])
	})
}
func sortChatEvents(g []ConvMessage) {
	depth := chatDepths(g)
	sort.Slice(g, func(i, j int) bool {
		if depth[g[i].LID] != depth[g[j].LID] {
			return depth[g[i].LID] < depth[g[j].LID]
		}
		return chatOrder(g[i], g[j])
	})
}
func (a *Agent) chatTopicHead(conv, topic string) (string, error) {
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		return "", err
	}
	assigned := ChatTopicAssignments(msgs)
	g := []ConvMessage{}
	for _, m := range msgs {
		if assigned[m.LID] == topic && m.Sub == "" && m.TopicEvent == nil {
			g = append(g, m)
		}
	}
	sortChatTurns(g)
	if len(g) == 0 {
		return "", nil
	}
	return g[len(g)-1].LID, nil
}

// Main-flow names use the existing own-device preference channel with a stable
// private scope. This sentinel is never a signed conversation topic ID.
const mainTopicKey = "00000000000000000000000000000000"

func mainTopicScope(conv string) string {
	h := sha256.Sum256([]byte("agentnet/main-flow-preference/v1\x00" + conv))
	return hex.EncodeToString(h[:])
}
func mainChatMessages(msgs []ConvMessage) []ConvMessage {
	assigned := ChatTopicAssignments(msgs)
	out := make([]ConvMessage, 0)
	for _, m := range msgs {
		if m.Sub == "" && m.TopicEvent == nil && assigned[m.LID] == "" {
			out = append(out, m)
		}
	}
	return out
}
func (a *Agent) ChatMainTopicForMessages(conv string, msgs []ConvMessage) (*ThreadSummary, error) {
	locals, err := a.store.topicLocals(mainTopicScope(conv))
	if err != nil {
		return nil, err
	}
	ts := summarizeChatTopicView(conv, msgs, map[string]topicLocal{"": locals[mainTopicKey]}, storeNow().Unix(), true)
	assigned := ChatTopicAssignments(msgs)
	if len(ts) > 0 {
		for _, m := range msgs {
			if m.Unread && assigned[m.LID] == "" {
				ts[0].Unread++
			}
		}
	}
	if len(ts) == 0 {
		return nil, nil
	}
	return &ts[0], nil
}
func (a *Agent) ChangeChatMainTopic(ctx context.Context, conv, what, title string, seen int) (bool, error) {
	if !protocol.ValidHash(conv) || seen < 0 {
		return false, ErrTopicQuery
	}
	switch what {
	case "rename", "archive", "reopen", "delete":
	default:
		return false, ErrTopicQuery
	}
	if _, _, ok, err := a.store.conversation(conv); err != nil {
		return false, err
	} else if !ok {
		return false, ErrNoMessage
	}
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		return false, err
	}
	main := mainChatMessages(msgs)
	if len(main) == 0 {
		return false, ErrNoMessage
	}
	scope := mainTopicScope(conv)
	if what == "delete" {
		err = a.deleteChatTopic(ctx, conv, "", main)
		return err == nil, err
	}
	if what == "rename" {
		title = strings.Join(strings.Fields(title), " ")
		if utf8.RuneCountInString(title) > TopicTitleMax {
			return false, ErrTopicTitle
		}
		err = a.setTopicTitle(scope, mainTopicKey, title)
		return err == nil, err
	}
	count := len(main)
	if seen > 0 && seen < count {
		count = seen
	}
	mark := protocol.TopicMarkArchived
	if what == "reopen" {
		mark = topicMarkOpen
	}
	err = a.changeTopicMark(scope, mainTopicKey, mark, count)
	return count == len(main), err
}
