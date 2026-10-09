package client

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
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
	locals, err := a.store.topicLocals(conv)
	if err != nil {
		return nil, err
	}
	ts := summarizeChatTopics(conv, msgs, locals, storeNow().Unix())
	unread, err := a.ConvUnread()
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, m := range unread[conv] {
		ids[m] = true
	}
	assigned := ChatTopicAssignments(msgs)
	for i := range ts {
		for _, m := range msgs {
			// Own-device copies are stored in inbox but shown as outgoing,
			// so they cannot be unread in the timeline or its topic badge.
			if m.Dir == "in" && assigned[m.LID] == ts[i].ID && m.TopicEvent == nil && ids[m.ID] {
				ts[i].Unread++
			}
		}
	}
	return ts, nil
}
func summarizeChatTopics(conv string, msgs []ConvMessage, locals map[string]topicLocal, now int64) []ThreadSummary {
	assigned := ChatTopicAssignments(msgs)
	groups := map[string][]ConvMessage{}
	events := map[string][]ConvMessage{}
	for _, m := range msgs {
		if m.Sub != "" {
			continue
		}
		if m.TopicEvent != nil {
			events[m.Topic] = append(events[m.Topic], m)
			continue
		}
		if topic := assigned[m.LID]; topic != "" {
			groups[topic] = append(groups[topic], m)
		}
	}
	out := []ThreadSummary{}
	for id, g := range groups {
		sortChatTurns(g)
		first, last := g[0], g[len(g)-1]
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
			facts[i] = threadRow{link: link{id: m.LID, replyTo: m.ReplyTo, at: m.Sent}, kind: m.Kind, status: m.status, topicDone: m.TopicDone, in: incoming || m.Kind == envelope.KindAnswer || m.Kind == envelope.KindResult, state: state}
		}
		local := locals[id]
		shared := topicLocal{}
		by := ""
		ev := events[id]
		sortChatEvents(ev)
		for _, m := range ev {
			if m.TopicEvent.Action == "create" {
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

		t := ThreadSummary{ID: id, Conv: conv, Title: firstLine(first.Body), Last: firstLine(last.Body), LastAt: time.Unix(activity, 0), Count: len(g), State: v.State, DoneBy: v.DoneBy, Pending: v.Pending, QuietSince: time.Unix(v.QuietSince, 0)}
		if shared.Mark == "done" && v.DoneBy == DoneByYou {
			t.DoneBy = "person"
			t.ConcludedBy = by
		}
		for _, m := range g {
			if m.LID == v.Conclusion {
				t.Conclusion = firstLine(m.Body)
				t.ConcludedBy = m.From
			}
			if m.Job == stateRunning {
				t.Running++
			}
			if m.Job == stateHeld || m.Job == stateNeedHuman {
				t.Review++
			}
		}
		t.Waiting = v.Pending
		if local.Title != "" {
			t.AutoTitle, t.Title, t.Renamed = t.Title, local.Title, true
		}
		out = append(out, t)
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
	assigned := ChatTopicAssignments(msgs)
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
	ts, err := a.ChatTopics(conv)
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
		title = strings.Join(strings.Fields(title), " ")
		if utf8.RuneCountInString(title) > TopicTitleMax {
			return false, ErrTopicTitle
		}
		err = a.setTopicTitle(conv, id, title)
	case "archive":
		local, err := a.store.topicLocals(conv)
		if err != nil {
			return false, err
		}
		l := local[id]
		l.Mark, l.MarkAt, l.MarkCount = "archived", storeNow().Unix(), t.Count
		for _, m := range msgs {
			if m.Topic == id && m.TopicEvent != nil {
				l.MarkCount++
			}
		}
		_, err = a.store.db.Exec(`INSERT INTO topic_state(peer,topic,title,mark,mark_at,mark_count,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(peer,topic) DO UPDATE SET mark=excluded.mark,mark_at=excluded.mark_at,mark_count=excluded.mark_count,updated_at=excluded.updated_at`, conv, id, l.Title, l.Mark, l.MarkAt, l.MarkCount, storeNow().Unix())
		if err == nil {
			a.NoteChange()
		}
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
	if err = eraseCoveredIn(tx, conv, self); err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err != nil {
		return err
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
	if limit < 0 || limit > TopicPageMax {
		return TopicPage{}, ErrTopicQuery
	}
	page := TopicPage{Topics: []ThreadSummary{}}
	words := strings.Fields(strings.ToLower(q.Query))
	start := q.Before == ""
	more := false
	for _, t := range all {
		if q.State != "" && q.State != t.State || !topicMatches(t, words) {
			continue
		}
		page.Matched++
		cursor := topicPageCursor(t)
		if !start {
			if cursor == q.Before {
				start = true
			}
			continue
		}
		if len(page.Topics) < limit {
			page.Topics = append(page.Topics, t)
		} else {
			more = true
		}
	}
	if !start {
		return TopicPage{}, ErrTopicQuery
	}
	if more && len(page.Topics) > 0 {
		page.Next = topicPageCursor(page.Topics[len(page.Topics)-1])
	}
	return page, nil
}
func topicPageCursor(t ThreadSummary) string {
	return t.LastAt.UTC().Format("20060102150405") + "|" + t.Conv + "|" + t.ID
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
