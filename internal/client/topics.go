package client

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Topics: an agent's separate conversations (device threads, uiview.go),
// each its own reply chain and so its own agent session (owner decisions
// 2026-10-04, docs/plans/TOPICS.md).
//
// A topic is active, done or archived. Done comes from the agent (the
// topic's latest request got its final answer or result with status done,
// as the topic's last message, and nothing in it is pending) or from the
// person (Mark done). Archived is derived: quiet for TopicArchiveAfter with
// nothing pending. A new message ends any mark the person set, so the topic
// is active again unless the new message itself makes it done. Archiving
// hides a topic from the overview only: nothing is ever deleted by it.
//
// Private names sync between the person's current human devices. Legacy
// Mark done/Reopen remain local; they never grant permission to run work.
// The derived states are the same on every device that holds the same
// messages. The browser device (engine.mjs) derives exactly the same.

// Topic tunables. Owner (2026-10-04): "this should all be easily changed,
// if needed — not via settings, but with code". They are the one place to
// change how topics behave; engine.mjs keeps the same values (TOPICS
// there), pinned by the parity test (internal/ui topics_browser_test.go).
const (
	TopicArchiveAfter = 7 * 24 * time.Hour // quiet this long with nothing pending: archived
	TopicPageDefault  = 50                 // topics in one page of the All topics list
	TopicPageMax      = 200                // the most a page may ask for
	TopicTitleMax     = 120                // characters in a name the person gives a topic
	topicLineScan     = 200                // characters of a first line read from the store (firstLine keeps 120)
	topicLineChunk    = 500                // message ids in one query for first lines (SQLite's bound-parameter limit is far above)
)

// Topic states (ThreadSummary.State) and who made a topic done (DoneBy).
const (
	TopicActive   = "active"
	TopicDone     = "done"
	TopicArchived = "archived"
	DoneByAgent   = "agent"
	DoneByYou     = "you"
)

// Marks the person sets on a topic (topic_state.mark).
const (
	topicMarkDone = "done"
	topicMarkOpen = "open" // reopened: active even if the agent had finished
)

// topicStateSchema keeps what the person set on topics here: a name of
// their own and the latest Mark done or Reopen, with the topic's message
// count then (a later message ends the mark). A topic is named by its
// earliest message (ThreadSummary.ID); a row naming any message of a
// topic still applies, so one whose earlier parent arrives late keeps it.
const topicStateSchema = `
CREATE TABLE topic_state(
  peer TEXT NOT NULL,
  topic TEXT NOT NULL,
  title TEXT,
  mark TEXT,
  mark_at INTEGER,
  mark_count INTEGER,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(peer, topic));
`

// storeNow is the clock for the times this device stores on device-thread
// messages and topic marks, and for deriving topic states: time.Now.
// Two test-only seams move it: a build with the agentnet_testclock tag
// replaces storeClock at start (testclock.go: a disposable world's old
// history), and this package's own tests set testShift (seconds). Neither
// is ever set in a released program.
func storeNow() time.Time {
	return storeClock().Add(time.Duration(testShift.Load()) * time.Second)
}

var (
	storeClock = time.Now
	testShift  atomic.Int64
)

// topicLocal is what the person set on one topic here.
type topicLocal struct {
	Title     string `json:"title,omitempty"`
	Mark      string `json:"mark,omitempty"`
	MarkAt    int64  `json:"mark_at,omitempty"`
	MarkCount int    `json:"mark_count,omitempty"`
}

// topicVerdict is a topic's derived state.
type topicVerdict struct {
	State      string `json:"state"`
	DoneBy     string `json:"done_by,omitempty"`
	Conclusion string `json:"conclusion,omitempty"` // the id of the agent's final reply, when the agent made it done
	Pending    bool   `json:"pending"`
	QuietSince int64  `json:"quiet_since"`
}

// topicOpenIn are the states of a received message that still has work in
// it: a decision for the person, or a responder's run (or follow-up).
var topicOpenIn = map[string]bool{stateHeld: true, stateAwaiting: true, stateNeedHuman: true, statePending: true,
	stateAccepted: true, stateRunning: true, stateCancelReq: true, stateAgentWaiting: true}

// topicUndelivered are the sent states no reply can follow: such a request
// waits on nobody, so it never keeps its topic from being archived.
var topicUndelivered = map[string]bool{stateFailed: true, "expired": true, "quarantined": true}

// topicOpen reports whether one message keeps its topic pending.
func topicOpen(r threadRow, replied bool) bool {
	switch {
	case r.notice:
		return r.state == stateNeedHuman // an open review notice
	case r.in:
		// An interrupted request waits for the person to run it again or
		// close it (reviewStates): open, like one held for them.
		return !r.selected && (topicOpenIn[r.state] || r.state == stateInterrupt && (r.kind == envelope.KindQuestion || r.kind == envelope.KindTask))
	}
	return (r.kind == envelope.KindQuestion || r.kind == envelope.KindTask) && !replied && !topicUndelivered[r.state]
}

// deriveTopic is a topic's state from its messages (thread order, oldest
// first, at least one), what the person set on it here and the time now
// (unix seconds). engine.mjs deriveTopic is the same function.
func deriveTopic(g []threadRow, l topicLocal, now int64) topicVerdict {
	var v topicVerdict
	replied := map[string]bool{}
	for _, r := range g {
		if r.in && r.replyTo != "" && r.status != envelope.StatusProgress {
			replied[r.replyTo] = true
		}
	}
	for _, r := range g {
		if topicOpen(r, replied[r.id]) {
			v.Pending = true
		}
	}
	last := g[len(g)-1]
	v.QuietSince = last.at
	live := l.Mark != "" && len(g) <= l.MarkCount // a later message ends the mark
	if live && l.MarkAt > v.QuietSince {
		v.QuietSince = l.MarkAt
	}
	switch {
	case live && l.Mark == topicMarkDone:
		v.DoneBy = DoneByYou
	case live && l.Mark == topicMarkOpen:
	case !v.Pending && last.topicDone && last.status == envelope.StatusDone &&
		(last.kind == envelope.KindAnswer || last.kind == envelope.KindResult):
		v.DoneBy, v.Conclusion = DoneByAgent, last.id
	}
	v.State = TopicActive
	if v.DoneBy != "" {
		v.State = TopicDone
	}
	if !v.Pending && (live && l.Mark == "archived" || now-v.QuietSince >= int64(TopicArchiveAfter/time.Second)) {
		v.State = TopicArchived
	}
	return v
}

// topicLocals reads what the person set on peer's topics here, by the id
// each row names.
func (s *store) topicLocals(peer string) (map[string]topicLocal, error) {
	rows, err := s.db.Query(`SELECT topic, coalesce(title, ''), coalesce(mark, ''), coalesce(mark_at, 0), coalesce(mark_count, 0) FROM topic_state WHERE peer = ?`, peer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]topicLocal{}
	for rows.Next() {
		var id string
		var l topicLocal
		if err := rows.Scan(&id, &l.Title, &l.Mark, &l.MarkAt, &l.MarkCount); err != nil {
			return nil, err
		}
		out[id] = l
	}
	return out, rows.Err()
}

// localOf is the row for topic g: the one naming its earliest message, else
// one naming any of its messages.
func localOf(locals map[string]topicLocal, g []string) (topicLocal, string) {
	if l, ok := locals[g[0]]; ok {
		return l, g[0]
	}
	for _, id := range g[1:] {
		if l, ok := locals[id]; ok {
			return l, id
		}
	}
	return topicLocal{}, ""
}

// summarize is the summary of one topic g with peer, without its text
// (topicText fills it); self is this device's address.
func summarize(self, peer string, g []string, rows map[string]threadRow, l topicLocal, now int64) ThreadSummary {
	first, last := rows[g[0]], rows[g[len(g)-1]]
	t := ThreadSummary{ID: first.id, Peer: peer, Count: len(g), LastAt: time.Unix(last.at, 0), NoticeOnly: true, lastID: last.id, custom: l.Title}
	facts := make([]threadRow, len(g))
	for i, id := range g {
		r := rows[id]
		facts[i] = r
		if r.agent != "" { // oldest first: the latest named one stays
			t.AgentID = r.agent
		}
		t.NoticeOnly = t.NoticeOnly && r.notice
		switch {
		case r.notice:
			if r.state == stateNeedHuman {
				t.Notices++
			}
			continue
		case r.in && !r.selected && (r.state == stateHeld || r.state == stateAwaiting || r.state == stateNeedHuman ||
			r.state == stateInterrupt && (r.kind == envelope.KindQuestion || r.kind == envelope.KindTask)): // reviewStates
			t.Review++
		case r.in && !r.selected && (r.state == stateRunning || r.state == stateCancelReq):
			t.Running++
		}
		if r.in && r.unread {
			t.Unread++
		}
		if !r.in && (r.kind == envelope.KindQuestion || r.kind == envelope.KindTask) && !r.replied {
			t.Waiting = true
		}
	}
	v := deriveTopic(facts, l, now)
	t.State, t.DoneBy, t.Pending, t.conclusionID, t.QuietSince = v.State, v.DoneBy, v.Pending, v.Conclusion, time.Unix(v.QuietSince, 0)
	if v.Conclusion != "" {
		t.ConcludedBy = peer
		if !rows[v.Conclusion].in {
			t.ConcludedBy = self
		}
	}
	return t
}

// peerTopics summarizes every device thread with peer, without text.
func (a *Agent) peerTopics(peer string, now int64) ([]ThreadSummary, error) {
	groups, rows, err := a.peerThreadGroups(peer)
	if err != nil || len(groups) == 0 {
		return nil, err
	}
	locals, err := a.store.topicLocals(peer)
	if err != nil {
		return nil, err
	}
	out := make([]ThreadSummary, 0, len(groups))
	for _, g := range groups {
		l, _ := localOf(locals, g)
		out = append(out, summarize(a.Address, peer, g, rows, l, now))
	}
	return out, nil
}

// topicText fills the titles, last lines and conclusions of ts, all with
// peer, with one read of first lines (per topicLineChunk messages).
func (a *Agent) topicText(peer string, ts []*ThreadSummary) error {
	var ids []string
	for _, t := range ts {
		ids = append(ids, t.ID, t.lastID)
		if t.conclusionID != "" {
			ids = append(ids, t.conclusionID)
		}
	}
	lines, err := a.store.firstLines(peer, ids)
	if err != nil {
		return err
	}
	for _, t := range ts {
		t.Title, t.Last = lines[t.ID], lines[t.lastID]
		if t.conclusionID != "" {
			t.Conclusion = lines[t.conclusionID]
		}
		if t.custom != "" {
			t.AutoTitle, t.Title, t.Renamed = t.Title, t.custom, true
		}
	}
	return nil
}

// firstLines reads the first line of each message ids exchanged with peer,
// shortened as firstLine does, without reading whole bodies.
func (s *store) firstLines(peer string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	seen := map[string]bool{}
	var unique []string
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	line := `substr(body, 1, min(coalesce(nullif(instr(body, char(10)), 0) - 1, ` + strconv.Itoa(topicLineScan) + `), ` + strconv.Itoa(topicLineScan) + `))`
	for len(unique) > 0 {
		chunk := unique[:min(len(unique), topicLineChunk)]
		unique = unique[len(chunk):]
		marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := []any{peer}
		for _, id := range chunk {
			args = append(args, id)
		}
		args = append(args, peer)
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.db.Query(`SELECT id, `+line+` FROM inbox WHERE sender = ? AND conv IS NULL AND ref_id IS NULL AND id IN (`+marks+`)
			UNION ALL SELECT id, `+line+` FROM outbox WHERE recipient = ? AND conv IS NULL AND ref_id IS NULL AND id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, l string
			if err := rows.Scan(&id, &l); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = firstLine(l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PeerTopics is one peer's topics as the overview counts them: archived
// topics are not in the overview's threads, only counted here.
type PeerTopics struct {
	Peer           string        `json:"peer"`
	Total          int           `json:"total"`           // topics with this peer (a thread of review notices only is not one)
	Archived       int           `json:"archived"`        // of those, archived
	ArchivedUnread int           `json:"archived_unread"` // unread messages in archived topics
	Latest         ThreadSummary `json:"latest"`          // the most recently active topic, whatever its state
}

// TopicOverview is what the overview shows of device threads, newest
// first, and each peer's topic counts. A page that lists topics itself
// (the messenger: GET /api/overview?topics=1) asks without archived
// topics (listArchived false): they are only counted, and listed a page at
// a time by Topics. Every other page (the previous interface, installed
// skins) gets every thread, archived topics included, each with its state.
// Review-notice threads are always listed.
func (a *Agent) TopicOverview(listArchived bool) ([]ThreadSummary, []PeerTopics, error) {
	peers, err := a.store.conversationPeers()
	if err != nil {
		return nil, nil, err
	}
	now := storeNow().Unix()
	var out []ThreadSummary
	var counts []PeerTopics
	for _, peer := range peers {
		all, err := a.peerTopics(peer, now)
		if err != nil {
			return nil, nil, err
		}
		c := PeerTopics{Peer: peer}
		var shown []*ThreadSummary
		latest := -1
		for i := range all {
			t := &all[i]
			if !t.NoticeOnly {
				c.Total++
				if latest < 0 || newer(*t, all[latest]) {
					latest = i
				}
			}
			if t.State == TopicArchived && !t.NoticeOnly {
				c.Archived++
				c.ArchivedUnread += t.Unread
				if !listArchived {
					continue
				}
			}
			shown = append(shown, t)
		}
		text := shown
		if latest >= 0 && all[latest].State == TopicArchived && !listArchived {
			text = append(text, &all[latest])
		}
		if err := a.topicText(peer, text); err != nil {
			return nil, nil, err
		}
		for _, t := range shown {
			out = append(out, *t)
		}
		if c.Total > 0 {
			c.Latest = all[latest]
			counts = append(counts, c)
		}
	}
	sortTopics(out)
	sort.Slice(counts, func(i, j int) bool { return counts[i].Peer < counts[j].Peer })
	return out, counts, nil
}

// newer orders topics most recently active first (then by id, descending,
// then by peer).
func newer(a, b ThreadSummary) bool {
	if !a.LastAt.Equal(b.LastAt) {
		return a.LastAt.After(b.LastAt)
	}
	if a.ID != b.ID {
		return a.ID > b.ID
	}
	return a.Peer < b.Peer
}

func sortTopics(ts []ThreadSummary) {
	sort.Slice(ts, func(i, j int) bool { return newer(ts[i], ts[j]) })
}

// TopicQuery asks for one page of the All topics list.
type TopicQuery struct {
	Conv   string
	Peer   string // "" every peer
	State  string // "" every state, or TopicActive, TopicDone, TopicArchived
	Query  string // words in the title (the person's name and the automatic one) or the last line, any case
	Before string // TopicPage.Next of the page before
	Limit  int    // 0: TopicPageDefault
}

// TopicPage is one page of topics, most recently active first.
type TopicPage struct {
	Topics  []ThreadSummary `json:"topics"`
	Next    string          `json:"next,omitempty"` // Before for the next page; "" when this is the last
	Matched int             `json:"matched"`        // topics matching the query, on every page
}

// ErrTopicQuery refuses a page request that does not say what it asks for.
var ErrTopicQuery = errors.New("that topic list request is not valid")

// Topics lists one page of topics (never review-notice threads).
func (a *Agent) Topics(q TopicQuery) (TopicPage, error) {
	if q.Conv != "" {
		all, err := a.ChatTopics(q.Conv)
		if err != nil {
			return TopicPage{}, err
		}
		return chatTopicPage(all, q)
	}
	switch q.State {
	case "", TopicActive, TopicDone, TopicArchived:
	default:
		return TopicPage{}, ErrTopicQuery
	}
	limit := q.Limit
	if limit == 0 {
		limit = TopicPageDefault
	}
	if limit < 0 || limit > TopicPageMax {
		return TopicPage{}, ErrTopicQuery
	}
	var after *ThreadSummary
	if q.Before != "" {
		at, rest, ok := strings.Cut(q.Before, "|")
		peer, id, ok2 := strings.Cut(rest, "|")
		sec, err := strconv.ParseInt(at, 10, 64)
		if !ok || !ok2 || err != nil || id == "" {
			return TopicPage{}, ErrTopicQuery
		}
		after = &ThreadSummary{LastAt: time.Unix(sec, 0), Peer: peer, ID: id}
	}
	peers := []string{q.Peer}
	if q.Peer == "" {
		var err error
		if peers, err = a.store.conversationPeers(); err != nil {
			return TopicPage{}, err
		}
	}
	words := strings.Fields(strings.ToLower(q.Query))
	now := storeNow().Unix()
	var matched []ThreadSummary
	for _, peer := range peers {
		all, err := a.peerTopics(peer, now)
		if err != nil {
			return TopicPage{}, err
		}
		var keep []*ThreadSummary
		for i := range all {
			if t := &all[i]; !t.NoticeOnly && (q.State == "" || t.State == q.State) {
				keep = append(keep, t)
			}
		}
		if len(words) > 0 { // the text decides: read it for every candidate
			if err := a.topicText(peer, keep); err != nil {
				return TopicPage{}, err
			}
		}
		for _, t := range keep {
			if topicMatches(*t, words) {
				matched = append(matched, *t)
			}
		}
	}
	sortTopics(matched)
	p := TopicPage{Matched: len(matched), Topics: []ThreadSummary{}}
	start := 0
	if after != nil {
		start = sort.Search(len(matched), func(i int) bool { return newer(*after, matched[i]) })
	}
	end := min(start+limit, len(matched))
	page := matched[start:end]
	if len(words) == 0 {
		byPeer := map[string][]*ThreadSummary{}
		for i := range page {
			byPeer[page[i].Peer] = append(byPeer[page[i].Peer], &page[i])
		}
		for peer, ts := range byPeer {
			if err := a.topicText(peer, ts); err != nil {
				return TopicPage{}, err
			}
		}
	}
	p.Topics = append(p.Topics, page...)
	if end < len(matched) && end > start {
		last := page[len(page)-1]
		p.Next = fmt.Sprintf("%d|%s|%s", last.LastAt.Unix(), last.Peer, last.ID)
	}
	return p, nil
}

// topicMatches: every word is in the title, the automatic title or the
// last line.
func topicMatches(t ThreadSummary, words []string) bool {
	if len(words) == 0 {
		return true
	}
	text := strings.ToLower(t.Title + "\n" + t.AutoTitle + "\n" + t.Last)
	for _, w := range words {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

// TopicOf is the topic that message id belongs to.
func (a *Agent) TopicOf(id string) (ThreadSummary, error) {
	peer, err := a.store.peerOf(id)
	if err != nil {
		return ThreadSummary{}, err
	}
	groups, rows, err := a.peerThreadGroups(peer)
	if err != nil {
		return ThreadSummary{}, err
	}
	for _, g := range groups {
		for _, m := range g {
			if m != id {
				continue
			}
			locals, err := a.store.topicLocals(peer)
			if err != nil {
				return ThreadSummary{}, err
			}
			l, _ := localOf(locals, g)
			t := summarize(a.Address, peer, g, rows, l, storeNow().Unix())
			err = a.topicText(peer, []*ThreadSummary{&t})
			return t, err
		}
	}
	return ThreadSummary{}, ErrNoMessage
}

// ErrTopicTitle refuses a name too long for a topic.
var ErrTopicTitle = fmt.Errorf("a topic's name is at most %d characters", TopicTitleMax)

// RenameTopic gives topic id with peer a name of the person's own, here;
// an empty title gives it back its automatic one (its first line).
func (a *Agent) RenameTopic(peer, id, title string) error {
	title = strings.Join(strings.Fields(title), " ")
	if utf8.RuneCountInString(title) > TopicTitleMax {
		return ErrTopicTitle
	}
	return a.setTopic(peer, id, func(l *topicLocal, _ int) { l.Title = title }, true)
}

// MarkTopicDone marks topic id with peer done by the person, here, until
// a new message comes. seen is how many of its messages the person's
// screen showed (0: all it has now): a mark never covers a message the
// person has not seen, so one that came after does not let it hold, and
// covered says whether it holds now.
func (a *Agent) MarkTopicDone(peer, id string, seen int) (covered bool, err error) {
	return a.mark(peer, id, topicMarkDone, seen)
}

// ReopenTopic makes topic id with peer active again, here, until a new
// message comes: a done or archived topic goes back among the active ones.
// seen and covered are as for MarkTopicDone.
func (a *Agent) ReopenTopic(peer, id string, seen int) (covered bool, err error) {
	return a.mark(peer, id, topicMarkOpen, seen)
}

func (a *Agent) mark(peer, id, mark string, seen int) (covered bool, err error) {
	err = a.setTopic(peer, id, func(l *topicLocal, have int) {
		count := have
		if seen > 0 && seen < have {
			count = seen
		}
		covered = count == have
		l.Mark, l.MarkAt, l.MarkCount = mark, storeNow().Unix(), count
	})
	return covered, err
}

// setTopic changes what the person set on topic id (its earliest message)
// with peer and stores it under that id.
func (a *Agent) setTopic(peer, id string, change func(l *topicLocal, count int), renamed ...bool) error {
	groups, _, err := a.peerThreadGroups(peer)
	if err != nil {
		return err
	}
	var g []string
	for _, x := range groups {
		if x[0] == id {
			g = x
		}
	}
	if g == nil {
		return ErrNoMessage
	}
	locals, err := a.store.topicLocals(peer)
	if err != nil {
		return err
	}
	l, was := localOf(locals, g)
	change(&l, len(g))
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rename := len(renamed) > 0 && renamed[0]
	if was != "" && was != id {
		if _, err := tx.Exec(`DELETE FROM topic_state WHERE peer = ? AND topic = ?`, peer, was); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO topic_state(peer, topic, title, mark, mark_at, mark_count, updated_at) VALUES(?, ?, nullif(?, ''), nullif(?, ''), nullif(?, 0), nullif(?, 0), ?)
		ON CONFLICT(peer, topic) DO UPDATE SET title = CASE WHEN ? THEN excluded.title ELSE topic_state.title END, mark = excluded.mark, mark_at = excluded.mark_at, mark_count = excluded.mark_count, updated_at = excluded.updated_at`,
		peer, id, l.Title, l.Mark, l.MarkAt, l.MarkCount, storeNow().Unix(), rename); err != nil {
		return err
	}
	if rename {
		if err = a.recordTopicTitle(tx, peer, id, l.Title); err != nil {
			return err
		}
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return err
	}
	a.topicTitlesChanged()
	return nil
}

// forgetTopics removes what the person set on topics whose messages ids
// were deleted (DeleteThread).
func forgetTopics(tx *sql.Tx, peer string, ids []string) error {
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM topic_state WHERE peer = ? AND topic = ?`, peer, id); err != nil {
			return err
		}
	}
	return nil
}
