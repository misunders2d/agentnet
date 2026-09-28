package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

func attention(t *testing.T, a *Agent, session, event string) Attention {
	t.Helper()
	at, err := a.Attention(HookEvent{Harness: "claude", Session: session, Event: event})
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// shown returns what a hook call tells session and records it as shown.
func shown(t *testing.T, a *Agent, session, event string) string {
	t.Helper()
	at := attention(t, a, session, event)
	if err := at.Commit(); err != nil {
		t.Fatal(err)
	}
	return at.Text
}

// Two-way conversation with files, stored after the processes restart;
// foreign links, unrelated messages and cycles stay out; reading it changes
// nothing.
func TestConversationBothWays(t *testing.T) {
	w := newWorld(t, "")
	carol := mustJoin(t, filepath.Join(t.TempDir(), "carol"), w.aliceInvites("carol"), "desk")
	stopAlice, _ := runWith(t, w, w.alice, RunOptions{})
	runWith(t, w, w.bob, RunOptions{})
	file := filepath.Join(t.TempDir(), "plan.txt")
	os.WriteFile(file, []byte("the plan"), 0o600)

	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "1 question", Kind: envelope.KindQuestion, Files: []string{file}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "question at bob", func() bool { return hasInbox(w.bob, "1 question") })
	ids := []string{q.ID}
	last := q.ID
	for i, who := range []*Agent{w.bob, w.alice, w.bob, w.alice, w.bob} {
		r, err := who.Reply(tctx(t), last, []string{"2 answer", "3 thanks", "4 more", "5 ok", "6 done"}[i])
		if err != nil {
			t.Fatal(err)
		}
		other := w.alice
		if who == w.alice {
			other = w.bob
		}
		eventually(t, "reply stored", func() bool { m, _ := other.store.inboxMessage(r.ID); return m != nil })
		ids, last = append(ids, r.ID), r.ID
	}
	unrelated, _ := w.bob.Send(tctx(t), w.alice.Address, "unrelated", "")
	foreign, _ := carol.Send(tctx(t), w.alice.Address, "carol names alice's question", q.ID)
	eventually(t, "other messages", func() bool {
		return hasInbox(w.alice, "unrelated") && hasInbox(w.alice, "carol names alice's question")
	})
	eventually(t, "uploads released after custody", func() bool { return count(t, w.alice, "uploads") == 0 })

	// A peer can make its own messages point at each other.
	for _, pair := range [][2]string{{"cyc-a", "cyc-b"}, {"cyc-b", "cyc-a"}} {
		if err := w.alice.store.addInbox(envelope.Inner{ID: pair[0], From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
			Kind: envelope.KindMessage, Body: pair[0], ReplyTo: pair[1]}, ""); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := w.alice.Inbox(false, false)

	// Stop and reopen from disk: the history is durable.
	stopAlice()
	w.alice.Close()
	alice, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	for _, from := range []string{ids[0], ids[3], ids[5]} {
		c, err := alice.Conversation(from, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range c.Messages {
			got = append(got, m.ID)
		}
		if c.Total != 6 || strings.Join(got, ",") != strings.Join(ids, ",") || c.Peer != w.bob.Address {
			t.Fatalf("conversation from %s: total %d peer %s\n got %v\nwant %v", from, c.Total, c.Peer, got, ids)
		}
		if c.Messages[0].Dir != "out" || c.Messages[1].Dir != "in" || c.Messages[1].Kind != envelope.KindAnswer || c.Messages[0].Kind != envelope.KindQuestion {
			t.Fatalf("directions/kinds: %+v", c.Messages[:2])
		}
	}
	c, _ := alice.Conversation(q.ID, 0, 0)
	if f := c.Messages[0].Attachments; len(f) != 1 || f[0].Name != "plan.txt" || f[0].Size != 8 {
		t.Fatalf("sent file metadata after custody: %+v", f)
	}
	page, _ := alice.Conversation(q.ID, 2, 3)
	if page.Total != 6 || page.Offset != 2 || len(page.Messages) != 3 || page.Messages[0].ID != ids[2] {
		t.Fatalf("page: %+v", page)
	}
	if cyc, err := alice.Conversation("cyc-a", 0, 0); err != nil || cyc.Total != 2 {
		t.Fatalf("cycle: %v %+v", err, cyc)
	}
	for _, id := range []string{unrelated.ID, foreign.ID} {
		if c, _ := alice.Conversation(id, 0, 0); c.Total != 1 {
			t.Fatalf("%s pulled in %d messages", id, c.Total)
		}
	}
	if _, err := alice.Conversation("nope", 0, 0); err != ErrNoMessage {
		t.Fatalf("unknown id: %v", err)
	}
	after, _ := alice.Inbox(false, false)
	for i := range before {
		if before[i].Read != after[i].Read || before[i].State != after[i].State {
			t.Fatalf("conversation changed %s", before[i].ID)
		}
	}
}

// Each session has its own cursor: reading, answering or another session
// never hides an arrival, and an unshown batch is shown again.
func TestAttentionPerSession(t *testing.T) {
	w := newWorld(t, "")
	runWith(t, w, w.alice, RunOptions{})
	runWith(t, w, w.bob, RunOptions{})
	early, _ := w.bob.Send(tctx(t), w.alice.Address, "early", "")
	eventually(t, "early", func() bool { return hasInbox(w.alice, "early") })

	// A new session gets an overview including what arrived before it.
	a1 := shown(t, w.alice, "A", "SessionStart")
	if !strings.Contains(a1, "1 received message(s), 1 unread") || !strings.Contains(a1, early.ID) {
		t.Fatalf("overview: %s", a1)
	}
	if got := shown(t, w.alice, "A", "UserPromptSubmit"); got != "" {
		t.Fatalf("nothing new, got %q", got)
	}

	q, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "status?", Kind: envelope.KindQuestion})
	eventually(t, "q at bob", func() bool { return hasInbox(w.bob, "status?") })
	r1, _ := w.bob.Reply(tctx(t), q.ID, "SECRET-BODY green")
	r2, _ := w.bob.Send(tctx(t), w.alice.Address, "and another", q.ID)
	eventually(t, "replies", func() bool { return hasInbox(w.alice, "SECRET-BODY green") && hasInbox(w.alice, "and another") })
	w.alice.Inbox(false, true) // the human read everything: sessions must still be told

	// An unshown batch is not consumed.
	if at := attention(t, w.alice, "A", "PostToolUse"); !strings.Contains(at.Text, r1.ID) {
		t.Fatalf("first try: %q", at.Text)
	}
	got := shown(t, w.alice, "A", "PostToolUse")
	for _, want := range []string{r1.ID, r2.ID, "replying to your question " + q.ID, "answer from " + w.bob.Address, "agentnet conversation ID"} {
		if !strings.Contains(got, want) {
			t.Fatalf("A lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "SECRET-BODY") {
		t.Fatalf("message text in context: %s", got)
	}
	if again := shown(t, w.alice, "A", "PostToolUse"); again != "" {
		t.Fatalf("shown twice: %q", again)
	}
	// Session B starts later: overview, then only newer arrivals.
	if b := shown(t, w.alice, "B", "UserPromptSubmit"); !strings.Contains(b, "3 received message(s), 0 unread") {
		t.Fatalf("B overview: %s", b)
	}

	// A burst larger than one batch arrives within a second.
	var burst []string
	for range 10 {
		m, _ := w.bob.Send(tctx(t), w.alice.Address, "burst", "")
		burst = append(burst, m.ID)
	}
	eventually(t, "burst", func() bool { n, _ := w.alice.store.countArrivalsAfter(0); return n == 13 })
	a2 := shown(t, w.alice, "A", "UserPromptSubmit")
	if strings.Count(a2, "\n- ") != attentionItems || !strings.Contains(a2, "(2 more arrived") {
		t.Fatalf("A batch: %s", a2)
	}
	a3 := shown(t, w.alice, "A", "UserPromptSubmit")
	b := shown(t, w.alice, "B", "UserPromptSubmit") + shown(t, w.alice, "B", "PostToolUse")
	for _, id := range burst {
		if !strings.Contains(a2+a3, id) || !strings.Contains(b, id) {
			t.Fatalf("burst item %s lost", id)
		}
	}
}

// Stop asks once to check new arrivals; never while already continued, and
// the same item does not stop the turn again.
func TestAttentionStopOnce(t *testing.T) {
	w := newWorld(t, "")
	runWith(t, w, w.alice, RunOptions{})
	if at := attention(t, w.alice, "S", "Stop"); at.Text != "" {
		t.Fatalf("new session Stop: %q", at.Text)
	} else {
		at.Commit()
	}
	m, _ := w.bob.Send(tctx(t), w.alice.Address, "news", "")
	eventually(t, "news", func() bool { return hasInbox(w.alice, "news") })
	if at, _ := w.alice.Attention(HookEvent{Harness: "claude", Session: "S", Event: "Stop", StopActive: true}); at.Text != "" {
		t.Fatalf("blocked while already continued: %q", at.Text)
	}
	stop := shown(t, w.alice, "S", "Stop")
	if !strings.Contains(stop, m.ID) || !strings.Contains(stop, "Before finishing") {
		t.Fatalf("stop: %q", stop)
	}
	if again := shown(t, w.alice, "S", "Stop"); again != "" {
		t.Fatalf("stopped twice for the same item: %q", again)
	}
	// Codex sessions are separate even with the same id.
	at, _ := w.alice.Attention(HookEvent{Harness: "codex", Session: "S", Event: "UserPromptSubmit"})
	if !strings.Contains(at.Text, "1 received") {
		t.Fatalf("codex session: %q", at.Text)
	}
}

// Arrival numbers keep increasing after the newest row is deleted, and
// duplicates of a stored message get none.
func TestArrivalNeverReused(t *testing.T) {
	w := newWorld(t, "")
	in := func(id string) envelope.Inner {
		return envelope.Inner{ID: id, From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: id}
	}
	for _, id := range []string{"m1", "m2", "m2"} {
		if err := w.alice.store.addInbox(in(id), ""); err != nil {
			t.Fatal(err)
		}
	}
	if top, _ := w.alice.store.arrivalTop(); top != 2 {
		t.Fatalf("top %d", top)
	}
	w.alice.store.db.Exec(`DELETE FROM inbox WHERE id = 'm2'`)
	w.alice.store.addInbox(in("m3"), "")
	var n int64
	w.alice.store.db.QueryRow(`SELECT arrival FROM inbox WHERE id = 'm3'`).Scan(&n)
	if n != 3 {
		t.Fatalf("m3 arrival %d", n)
	}
}

// oldInsertInbox is the inbox insert of the release before arrival numbers:
// a daemon started from it keeps using it after a newer CLI or hook upgraded
// the database under it.
const oldInsertInbox = `INSERT OR IGNORE INTO inbox(id, sender, ts, kind, body, reply_to, received_at, session, status, state)
	VALUES(?, ?, ?, 'message', 'old writer', NULL, ?, NULL, NULL, '')`

// Upgrading a store from before arrival numbers numbers existing messages
// in order and continues from there.
func TestArrivalBackfillOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	old, err := sqlitedb.Open(path, schema[:6])
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "a"} {
		if _, err := old.Exec(oldInsertInbox, id, "x/y", 1, 1); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	var b, a int64
	st.db.QueryRow(`SELECT arrival FROM inbox WHERE id = 'b'`).Scan(&b)
	st.db.QueryRow(`SELECT arrival FROM inbox WHERE id = 'a'`).Scan(&a)
	if top, _ := st.arrivalTop(); b != 1 || a != 2 || top != 2 {
		t.Fatalf("arrivals b=%d a=%d top=%d", b, a, top)
	}
}

// Rows an older writer stored without a number after the step that added
// numbers are repaired once, in order, after the existing ones.
func TestArrivalRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	db, err := sqlitedb.Open(path, schema[:7])
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, arrival) VALUES('n1', 'x/y', 1, 'message', '', 1, 1)`)
	db.Exec(`UPDATE config SET v = '1' WHERE k = 'arrival'`)
	for _, id := range []string{"z", "y"} {
		if _, err := db.Exec(oldInsertInbox, id, "x/y", 1, 1); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	var z, y int64
	st.db.QueryRow(`SELECT arrival FROM inbox WHERE id = 'z'`).Scan(&z)
	st.db.QueryRow(`SELECT arrival FROM inbox WHERE id = 'y'`).Scan(&y)
	if top, _ := st.arrivalTop(); z != 2 || y != 3 || top != 3 {
		t.Fatalf("repaired z=%d y=%d top=%d", z, y, top)
	}
}

// A daemon still running the older release inserts without a number after
// the upgrade: the trigger numbers the row, sessions are told about it, and
// a duplicate id is neither stored nor numbered again.
func TestOldWriterAfterUpgradeIsSeen(t *testing.T) {
	w := newWorld(t, "")
	shown(t, w.alice, "S", "SessionStart")
	for range 2 {
		if _, err := w.alice.store.db.Exec(oldInsertInbox, "from-old-daemon", w.bob.Address, time.Now().Unix(), time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if top, _ := w.alice.store.arrivalTop(); top != 1 {
		t.Fatalf("counter %d after one new row and a duplicate", top)
	}
	if got := shown(t, w.alice, "S", "PostToolUse"); !strings.Contains(got, "from-old-daemon") {
		t.Fatalf("hook missed the old writer's row: %q", got)
	}
}

// A conversation page is found from the link indexes alone and loads only
// its own messages, however much else was exchanged with the peer.
func TestConversationPageIsBounded(t *testing.T) {
	w := newWorld(t, "")
	add := func(id, replyTo string, body string) {
		t.Helper()
		if err := w.alice.store.addInbox(envelope.Inner{ID: id, From: w.bob.Address, To: w.alice.Address, TS: 1,
			Kind: envelope.KindMessage, Body: body, ReplyTo: replyTo}, ""); err != nil {
			t.Fatal(err)
		}
	}
	big := strings.Repeat("x", 64<<10)
	for i := range 200 {
		add(fmt.Sprintf("unrelated-%03d", i), "", big)
	}
	prev := ""
	for i := range 120 {
		id := fmt.Sprintf("chain-%03d", i)
		add(id, prev, "small")
		prev = id
	}
	c, err := w.alice.Conversation("chain-060", 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 120 || len(c.Messages) != 3 || c.Messages[0].ID != "chain-100" || c.Messages[2].ID != "chain-102" {
		t.Fatalf("page: total %d, %d messages, first %v", c.Total, len(c.Messages), c.Messages)
	}
	rows, err := w.alice.store.db.Query(`EXPLAIN QUERY PLAN `+peerLinksQuery, w.bob.Address, w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		rows.Scan(&id, &parent, &unused, &detail)
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "; ")
	if !strings.Contains(joined, "COVERING INDEX inbox_links") || !strings.Contains(joined, "COVERING INDEX outbox_links") {
		t.Fatalf("link query reads message rows: %s", joined)
	}
}

// Pi shows the text after its hook returned: nothing is recorded until the
// acknowledgement, which only moves the cursor forward and never past the
// newest arrival; an unacknowledged notice is offered again.
func TestAttentionAckedLater(t *testing.T) {
	w := newWorld(t, "")
	in := func(id string) envelope.Inner {
		return envelope.Inner{ID: id, From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: id}
	}
	ask := func(event string) Attention {
		t.Helper()
		at, err := w.alice.Attention(HookEvent{Harness: "pi", Session: "P", Event: event})
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	start := ask("SessionStart")
	pos, hasPos, _ := start.Ack()
	if !hasPos || pos != 0 {
		t.Fatalf("start ack %d %v", pos, hasPos)
	}
	if err := w.alice.AckAttention("pi", "P", pos, hasPos, ""); err != nil {
		t.Fatal(err)
	}
	w.alice.store.addInbox(in("a1"), "")
	w.alice.store.addInbox(in("a2"), "")
	first := ask("Idle")
	if !strings.Contains(first.Text, "a1") || !strings.Contains(first.Text, "a2") {
		t.Fatalf("idle notice: %q", first.Text)
	}
	// Handed over and awaiting acknowledgement: not listed again meanwhile.
	pos, _, _ = first.Ack()
	if pending, _ := w.alice.Attention(HookEvent{Harness: "pi", Session: "P", Event: "Idle", After: pos}); pending.Text != "" {
		t.Fatalf("pending notice listed again: %q", pending.Text)
	}
	// Not acknowledged (not shown): offered again, nothing recorded.
	if again := ask("UserPromptSubmit"); again.Text == "" || !strings.Contains(again.Text, "a1") {
		t.Fatalf("unacknowledged notice not offered again: %q", again.Text)
	}
	pos, hasPos, _ = first.Ack()
	if err := w.alice.AckAttention("pi", "P", pos, hasPos, ""); err != nil {
		t.Fatal(err)
	}
	if after := ask("Idle"); after.Text != "" {
		t.Fatalf("acknowledged notice shown again: %q", after.Text)
	}
	// Forward only, and bounded by the newest arrival.
	if err := w.alice.AckAttention("pi", "P", 1, true, ""); err != nil {
		t.Fatal(err)
	}
	if cur, _, _ := w.alice.store.cursor("pi", "P"); cur != pos {
		t.Fatalf("cursor moved back to %d", cur)
	}
	if err := w.alice.AckAttention("pi", "P", pos+100, true, ""); err == nil {
		t.Fatal("a cursor past the newest arrival was accepted")
	}
	if err := w.alice.AckAttention("pi", "", pos, true, ""); err == nil {
		t.Fatal("ack without a session accepted")
	}
}
