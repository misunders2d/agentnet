package client

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An agent's needs_human result is its chat message on the request: it is
// unread like any other arrival (badge), travels to the person's devices by
// read sync, and reading it clears it there too (MEL-693). A Stop that won
// raises nothing.
func TestNeedsHumanResultIsUnreadAttention(t *testing.T) {
	w, phone, conv, _ := historyCatchupFixture(t, 0)
	a := w.alice
	_, root := rootOf(t, w.bob, conv)
	ask := func(body string) string {
		env := craft(t, w.bob, a, envelope.Inner{Kind: envelope.KindQuestion, Conv: conv, Root: root, LID: protocol.NewID(), Body: body, Origin: envelope.OriginUI})
		if err := a.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if err := a.MarkRead([]string{env.ID}); err != nil {
			t.Fatal(err)
		}
		if attentionMessage(t, a, conv, env.ID).Unread {
			t.Fatal("fixture: the read request is still unread")
		}
		if _, err := a.store.db.Exec(`UPDATE inbox SET state=?, attempts=1 WHERE id=?`, stateRunning, env.ID); err != nil {
			t.Fatal(err)
		}
		return env.ID
	}
	id := ask("Which carrier?")
	_, mark, err := a.UnreadDirtySince(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.endJob(id, stateNeedHuman, "Which carrier should I book?", 1); err != nil {
		t.Fatal(err)
	}
	m := attentionMessage(t, a, conv, id)
	if !m.Unread || m.Attention == nil {
		t.Fatalf("needs_human result is not unread attention: unread=%v attention=%v", m.Unread, m.Attention)
	}
	keys, _, err := a.UnreadDirtySince(mark)
	if err != nil {
		t.Fatal(err)
	}
	dirty := false
	for _, k := range keys {
		dirty = dirty || k == "c:"+conv
	}
	if !dirty {
		t.Fatalf("badge recount not marked for the chat: %v", keys)
	}
	if _, err := a.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	var carried int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM attention_mark_copies WHERE recipient_fp=? AND conv=? AND lid=? AND revision=?`,
		phone.Self().Fingerprint(), conv, m.LID, m.Attention.Rev).Scan(&carried); err != nil || carried != 1 {
		t.Fatalf("the unread generation is not carried to the person's other device: %d %v", carried, err)
	}
	if err := a.MarkReadWithAttention([]string{id}, map[string]protocol.AttentionGeneration{id: *m.Attention}); err != nil {
		t.Fatal(err)
	}
	if attentionMessage(t, a, conv, id).Unread {
		t.Fatal("reading the needs_human result did not clear it")
	}

	stopped := ask("Book it?")
	if _, err := a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelReq, stopped); err != nil {
		t.Fatal(err)
	}
	if err := a.endJob(stopped, stateNeedHuman, "Which date?", 1); err != nil {
		t.Fatal(err)
	}
	if attentionMessage(t, a, conv, stopped).Unread {
		t.Fatal("a Stop that won still raised needs_human attention")
	}
}

// The owner's case: the agent runs on a host with no human key (headless),
// and someone asks it from their own device. The agent's needs_human result
// is posted to the conversation as its reply, tagging the asker: at the
// asker's device it is unread with a normal chat alert. The host keeps the
// request at needs_human, adds no second attention of its own, and never
// posts it twice.
func TestNeedsHumanResultPostedToAsker(t *testing.T) {
	old := alertGrace
	alertGrace = time.Hour // keep the queued alert visible to the test
	t.Cleanup(func() { alertGrace = old })
	st := installAgentStub(t)
	st.mode("needs")
	w, conv, lids, _ := agentWorld(t)
	headless(t, w.bob, w.alice.Self().Fingerprint())
	if err := w.alice.SetAlertPrefs(AlertPrefs{Enabled: true, Senders: []protocol.NotifySender{{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint()}}}); err != nil {
		t.Fatal(err)
	}
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, lids[:1], nil)
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "book the shipment")
	if err != nil {
		t.Fatal(err)
	}
	ans := replyAt(t, w.alice, conv, q.ID)
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	tag := "](agentnet:person/" + me.info.Person + ")"
	if ans.Kind != envelope.KindAnswer || ans.PID != pid || ans.From != w.bob.Address || !strings.HasPrefix(ans.Body, "[@") ||
		!strings.Contains(ans.Body, tag) || !strings.HasSuffix(ans.Body, "Which carrier should I book?") {
		t.Fatalf("the needs_human result is not the agent's reply tagging the asker: %+v", ans)
	}
	if !ans.Unread {
		t.Fatal("the agent's needs_human reply is not unread at the asker's device")
	}
	if n := inboxlessCount(t, w.alice, "alerts"); n != 1 {
		t.Fatalf("asker's device queued %d chat alerts, want 1", n)
	}
	var state, result string
	if err := w.bob.store.db.QueryRow(`SELECT state, coalesce(result_id,'') FROM inbox WHERE id=?`, q.ID).Scan(&state, &result); err != nil || state != stateNeedHuman || result == "" {
		t.Fatalf("host request: state=%s result=%q %v", state, result, err)
	}
	var marks int
	if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM attention_marks`).Scan(&marks); err != nil || marks != 0 {
		t.Fatalf("host added its own attention on top of the delivered reply: %d %v", marks, err)
	}
	if w.bob.runNext(tctx(t), nil) || st.runs() != 1 {
		t.Fatal("the needs_human request ran again")
	}
	if _, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.ReplyTo == q.ID || m.ReplyTo == q.LID }); n != 1 {
		t.Fatalf("asker holds %d replies, want exactly 1", n)
	}
	if err := w.alice.MarkRead([]string{ans.ID}); err != nil {
		t.Fatal(err)
	}
	if m, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.ID == ans.ID }); m.Unread {
		t.Fatal("reading the reply did not clear it")
	}
}

// headless leaves the device's own person record without a human key for it:
// an agent host (readsync_test's agent-host-only role).
func headless(t *testing.T, a *Agent, human string) {
	t.Helper()
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		t.Fatal(err)
	}
	me.roster.HumanKeys = []string{human}
	record, _ := json.Marshal(me.roster)
	if _, err := a.store.db.Exec(`UPDATE persons SET record=? WHERE state='self'`, record); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := a.store.selfPerson(a.Address); again.roster.Human(a.Self().Fingerprint()) {
		t.Fatal("fixture: the host still has a human key")
	}
}

// The owner asks their own agent on another of their devices: that device's
// rows are listed as sent (own device), yet the agent's needs_human reply
// tagging the owner is an arrival for them. Their own messages, untagged
// replies and ordinary (final) answers stay read.
func TestOwnAgentReplyTaggingOwnerIsUnread(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	tagged := mentionAnchor("person", owner, "Alice") + "\n\nWhich carrier should I book?"
	m := ConvMessage{Via: "admin/bezos", PID: "p", Origin: envelope.OriginAgentPrefix + "pi", Kind: envelope.KindAnswer, Body: tagged, Unread: true, status: envelope.StatusNeedsHuman}
	if !ownAgentTagsOwner(m, owner) {
		t.Fatal("own agent's needs_human reply tagging the owner is not an arrival")
	}
	for name, other := range map[string]ConvMessage{
		"untagged":     {Via: m.Via, PID: m.PID, Origin: m.Origin, Kind: m.Kind, Body: "Done.", status: m.status},
		"own message":  {Via: m.Via, Origin: envelope.OriginUI, Kind: envelope.KindMessage, Body: tagged, status: m.status},
		"other tag":    {Via: m.Via, PID: m.PID, Origin: m.Origin, Kind: m.Kind, Body: mentionAnchor("person", "someone-else", "Bob"), status: m.status},
		"not own":      {PID: m.PID, Origin: m.Origin, Kind: m.Kind, Body: tagged, status: m.status},
		"final answer": {Via: m.Via, PID: m.PID, Origin: m.Origin, Kind: m.Kind, Body: tagged, status: envelope.StatusDone},
		"no status":    {Via: m.Via, PID: m.PID, Origin: m.Origin, Kind: m.Kind, Body: tagged},
	} {
		if ownAgentTagsOwner(other, owner) {
			t.Errorf("%s became an arrival", name)
		}
	}
}

// v0.11.2 regression (owner report 2026-11-10): v0.11.1 began counting every
// past answer of the person's own agent on another of their devices that
// tagged them as unread. Those rows had always been listed as sent, so no
// device had ever read them or held a read mark for them: on upgrade, weeks
// of already-seen answers came back unread on every device at once. Only the
// posted needs_human result is an arrival; it clears by reading and the read
// becomes a mark the person's other devices apply.
func TestOwnAgentPastAnswerStaysReadAfterUpgrade(t *testing.T) {
	w, phone, conv, _ := historyCatchupFixture(t, 0)
	a := w.alice
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		t.Fatal(err)
	}
	tagged := mentionAnchor("person", me.info.Person, "Alice") + "\n\nDone: the shipment is booked."
	own := func(status string) string {
		id, lid := protocol.NewID(), protocol.NewID()
		now := time.Now()
		if _, err := a.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,received_ms,conv,lid,verified_by,origin,pid,status)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, phone.Address, now.Unix(), envelope.KindAnswer, tagged, now.Unix(), now.UnixMilli(), conv, lid,
			phone.Self().Fingerprint(), envelope.OriginAgentPrefix+"pi", protocol.NewID(), status); err != nil {
			t.Fatal(err)
		}
		return id
	}
	past := own(envelope.StatusDone)
	if m := attentionMessage(t, a, conv, past); m.Dir != "out" || m.Via != phone.Address {
		t.Fatalf("fixture: own device's answer is not listed as sent from it: dir=%s via=%s", m.Dir, m.Via)
	} else if m.Unread {
		t.Fatal("an own agent's past answer tagging the owner came back unread")
	}
	asked := own(envelope.StatusNeedsHuman)
	m := attentionMessage(t, a, conv, asked)
	if !m.Unread {
		t.Fatal("an own agent's needs_human reply tagging the owner is not unread")
	}
	if err := a.MarkRead([]string{asked}); err != nil {
		t.Fatal(err)
	}
	if attentionMessage(t, a, conv, asked).Unread {
		t.Fatal("reading the needs_human reply did not clear it")
	}
	var marks int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM read_marks WHERE conv=? AND fingerprint=? AND lid=?`, conv, phone.Self().Fingerprint(), m.LID).Scan(&marks); err != nil || marks != 1 {
		t.Fatalf("the read is not a mark the person's other devices apply: %d %v", marks, err)
	}
}
