package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

func readState(t *testing.T, a *Agent, conv, body string) (string, bool) {
	t.Helper()
	var id string
	var read bool
	if err := a.store.db.QueryRow(`SELECT id,read_at IS NOT NULL FROM inbox WHERE conv=? AND body=?`, conv, body).Scan(&id, &read); err != nil {
		t.Fatal(err)
	}
	return id, read
}
func TestReadSyncExactOwnHumanJourney(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.bob, w.alice)
	for _, body := range []string{"old read turn", "new unseen turn"} {
		if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "two originals on both own devices", func() bool { return len(convBodies(t, w.alice, conv)) == 2 && len(convBodies(t, phone, conv)) == 2 })
	old, _ := readState(t, w.alice, conv, "old read turn")
	if err := w.alice.MarkRead([]string{old}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "exact read converges", func() bool { _, read := readState(t, phone, conv, "old read turn"); return read })
	if _, read := readState(t, phone, conv, "new unseen turn"); read {
		t.Fatal("newer unseen turn cleared")
	}
	// A verified own-human marker arriving before its original persists safely.
	me, _, err := phone.store.selfPerson(phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	future := protocol.NewID()
	r := protocol.ReadSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, Refs: []protocol.ReadRef{{Conv: conv, Fingerprint: w.bob.Self().Fingerprint(), LID: future}}}
	raw, _ := json.Marshal(r)
	carrier := craft(t, phone, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(raw)})
	if err = w.alice.verifyAndStore(tctx(t), carrier); err != nil {
		t.Fatal(err)
	}
	_, root := rootOf(t, w.alice, conv)
	arrival := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: future, Body: "marked before arrival"})
	if err = w.alice.verifyAndStore(tctx(t), arrival); err != nil {
		t.Fatal(err)
	}
	if _, read := readState(t, w.alice, conv, "marked before arrival"); !read {
		t.Fatal("future exact arrival missed durable mark")
	}
	foreign := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(raw)})
	if err = w.alice.verifyAndStore(tctx(t), foreign); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, w.alice, foreign.ID) == "" {
		t.Fatal("another person's read state accepted")
	}
	// Current human authority, rather than historical roster inclusion, decides.
	if err = readSyncAuthority(w.alice.store.db, r, w.bob.Address, w.bob.Self().Fingerprint(), w.alice.Address, w.alice.Self().Fingerprint()); err == nil {
		t.Fatal("foreign sender authority")
	}
	original, _ := json.Marshal(me.roster)
	me.roster.HumanKeys = []string{phone.Self().Fingerprint()}
	record, _ := json.Marshal(me.roster)
	if _, err = phone.store.db.Exec(`UPDATE persons SET record=? WHERE state=?`, string(record), personSelf); err != nil {
		t.Fatal(err)
	}
	if err = readSyncAuthority(phone.store.db, r, w.alice.Address, w.alice.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); err == nil {
		t.Fatal("agent-host device supplied read state")
	}
	if _, err = phone.store.db.Exec(`UPDATE persons SET record=? WHERE state=?`, string(original), personSelf); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if err = readSyncAuthority(phone.store.db, r, w.alice.Address, w.alice.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); err == nil {
		t.Fatal("pending peer key accepted")
	}
	if _, err = phone.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.store.db.Exec(`UPDATE persons SET state=? WHERE state=?`, personConflict, personSelf); err != nil {
		t.Fatal(err)
	}
	if err = readSyncAuthority(phone.store.db, r, w.alice.Address, w.alice.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); err == nil {
		t.Fatal("frozen self person accepted")
	}
	if _, err = phone.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, personSelf, r.Person); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.store.db.Exec(`INSERT INTO config(k,v) VALUES(?,?)`, revokedDevicePrefix+w.alice.Address+"/"+w.alice.Self().Fingerprint(), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if err = readSyncAuthority(phone.store.db, r, w.alice.Address, w.alice.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); err == nil {
		t.Fatal("revoked human key accepted")
	}
	if len(ownCaps)+1 > protocol.MaxAdvertisedCaps {
		t.Fatal("cap bound exceeded")
	}
}
func TestReadSyncOlderReaderRestartAndKeyFence(t *testing.T) {
	for _, mode := range []string{"rd1", "no-record"} {
		t.Run("rd2-concurrent-"+mode, func(t *testing.T) { testReadSyncV2Waiting(t, mode) })
	}
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	conv := newDM(t, w.alice, w.bob)
	_, root := rootOf(t, w.alice, conv)
	item := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Body: "restart mark"})
	if err := w.alice.verifyAndStore(tctx(t), item); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.MarkRead([]string{item.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	var encoded string
	if err := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND recipient=? LIMIT 1`, envelope.SubReadSync, phone.Address).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(encoded), &env); err != nil {
		t.Fatal(err)
	}
	if env.Attn {
		t.Fatal("read marker requests notification")
	}
	var profile protocol.Profile
	label, device, _ := protocol.SplitAddress(phone.Address)
	if err := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile); err != nil {
		t.Fatal(err)
	}
	publish := func(ts int64, caps []string) {
		rec := protocol.CapsRecord{Address: phone.Address, Session: profile.Sessions[0], TS: ts, Caps: caps}
		rec.Sign(phone.id.Sign)
		if err := phone.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	publish(time.Now().Unix()+100, []string{protocol.CapEnv2, protocol.CapPerson, protocol.CapRoom})
	if result, err := w.alice.deliver(tctx(t), env, nil); err != nil || result.State != stateConvWaiting {
		t.Fatalf("legacy reader %+v %v", result, err)
	}
	if err := w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err = again.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, again, "outbox WHERE sub='read-sync'"); n != 1 {
		t.Fatalf("restart duplicated mark: %d", n)
	}
	publish(time.Now().Unix()+200, slices.Clone(ownCaps))
	features, err := again.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	again.releaseConv(tctx(t), features)
	if err = again.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var unchanged string
	if err = again.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&unchanged); err != nil || unchanged != encoded {
		t.Fatal("restoration changed captured envelope", err)
	}
	if err = again.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if err = again.store.setOutboxState(env.ID, stateQueued, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := again.mayDeliverReadSync(env); err != nil || ok {
		t.Fatalf("removed reader admitted: %v %v", ok, err)
	}
}

func TestReadSyncBoundedBatchRecovery(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	me, _, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < protocol.MaxReadRefs+1; i++ {
		if _, err = w.alice.store.db.Exec(`INSERT INTO read_marks(owner,conv,fingerprint,lid) VALUES(?,'',?,?)`, me.info.Person, w.bob.Self().Fingerprint(), protocol.NewID()); err != nil {
			t.Fatal(err)
		}
	}
	more, err := w.alice.syncReadMarks()
	if err != nil || !more {
		t.Fatalf("first page: %v %v", more, err)
	}
	more, err = w.alice.syncReadMarks()
	if err != nil || more {
		t.Fatalf("second page: %v %v", more, err)
	}
	if _, err = w.alice.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, w.alice, "outbox WHERE sub='read-sync'"); n != 2 {
		t.Fatalf("65 references require two durable carriers, got %d", n)
	}
	rows, err := w.alice.store.db.Query(`SELECT body,recipient FROM outbox WHERE sub='read-sync'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	refs := 0
	for rows.Next() {
		var raw, to string
		if err = rows.Scan(&raw, &to); err != nil {
			t.Fatal(err)
		}
		r, e := protocol.ParseReadSync([]byte(raw))
		if e != nil || to != phone.Address {
			t.Fatal("invalid batch", e)
		}
		refs += len(r.Refs)
	}
	if refs != protocol.MaxReadRefs+1 {
		t.Fatalf("batch lost exact references %d", refs)
	}
}

func TestReadSyncVisibleOwnSentQuestion(t *testing.T) {
	w, linux, _, _ := historyCatchupFixture(t, 0)
	zen := w.alice
	storeSend := func(from, to *Agent, in envelope.Inner) envelope.Envelope {
		t.Helper()
		env := sealTo(t, from, to, in)
		opened, err := envelope.Open(env, to.id, to.Address, from.Self())
		if err != nil {
			t.Fatal(err)
		}
		if err = from.store.addOutbox(env, opened, "", nil, boundOutgoing{fingerprint: to.Self().Fingerprint()}); err != nil {
			t.Fatal(err)
		}
		if err = to.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return env
	}
	q := storeSend(linux, zen, envelope.Inner{Kind: envelope.KindQuestion, Body: "what's your cpu?"})
	answer := storeSend(zen, linux, envelope.Inner{Kind: envelope.KindAnswer, Body: "Intel Core i7-1165G7", ReplyTo: q.ID})
	sibling := storeSend(linux, zen, envelope.Inner{Kind: envelope.KindQuestion, Body: "unseen sibling topic"})
	if _, err := zen.store.db.Exec(`UPDATE inbox SET state='answered',attempts=1 WHERE id=?`, q.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		a          *Agent
		qdir, adir string
	}{{linux, "out", "in"}, {zen, "in", "out"}} {
		c, err := tc.a.Conversation(q.ID, 0, 0)
		if err != nil || len(c.Messages) != 2 {
			t.Fatalf("thread: %+v %v", c, err)
		}
		if c.Messages[0].Dir != tc.qdir || c.Messages[1].Dir != tc.adir {
			t.Fatalf("directions: %+v", c.Messages)
		}
	}
	// Exact visible IDs from the human's Linux topic, including its sent question.
	if err := linux.MarkRead([]string{q.ID, answer.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := linux.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	deliverDeviceHistory(t, linux, zen, envelope.SubReadSync)
	var read bool
	if err := zen.store.db.QueryRow(`SELECT read_at IS NOT NULL FROM inbox WHERE id=?`, q.ID).Scan(&read); err != nil || !read {
		t.Fatalf("visible own sent question stays unread on recipient: read=%v err=%v", read, err)
	}
	if err := zen.store.db.QueryRow(`SELECT read_at IS NOT NULL FROM inbox WHERE id=?`, sibling.ID).Scan(&read); err != nil || read {
		t.Fatalf("unseen sibling read=%v err=%v", read, err)
	}

	// Idempotency and the duplicate self-answer inbox/outbox form use the same ledger.
	if err := linux.MarkRead([]string{q.ID, answer.ID}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := linux.store.db.QueryRow(`SELECT count(*) FROM read_marks WHERE fingerprint=? AND lid=?`, linux.Self().Fingerprint(), q.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate outgoing read: %d %v", n, err)
	}
	selfQ := storeSend(zen, zen, envelope.Inner{Kind: envelope.KindQuestion, Body: "self question"})
	selfA := storeSend(zen, zen, envelope.Inner{Kind: envelope.KindAnswer, Body: "self answer", ReplyTo: selfQ.ID})
	if err := zen.MarkRead([]string{selfA.ID, selfA.ID}); err != nil {
		t.Fatal(err)
	}
	if err := zen.store.db.QueryRow(`SELECT count(*) FROM read_marks WHERE fingerprint=? AND lid=?`, zen.Self().Fingerprint(), selfA.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate self answer: %d %v", n, err)
	}
	// A foreign signed envelope in an outbox never acquires the current key.
	foreign := sealTo(t, w.bob, zen, envelope.Inner{Kind: envelope.KindQuestion, Body: "foreign signer"})
	opened, err := envelope.Open(foreign, zen.id, zen.Address, w.bob.Self())
	if err != nil {
		t.Fatal(err)
	}
	if err = linux.store.addOutbox(foreign, opened, "", nil, boundOutgoing{fingerprint: zen.Self().Fingerprint()}); err != nil {
		t.Fatal(err)
	}
	if err = linux.MarkRead([]string{foreign.ID}); err != nil {
		t.Fatal(err)
	}
	if err = linux.store.db.QueryRow(`SELECT count(*) FROM read_marks WHERE lid=?`, foreign.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("foreign signer granted mark: %d %v", n, err)
	}
	// A current agent-host-only role or frozen owner cannot originate sent marks.
	owner, _, err := linux.store.selfPerson(linux.Address)
	if err != nil {
		t.Fatal(err)
	}
	originalRecord, _ := json.Marshal(owner.roster)
	owner.roster.HumanKeys = []string{zen.Self().Fingerprint()}
	hostRecord, _ := json.Marshal(owner.roster)
	gated := storeSend(linux, zen, envelope.Inner{Kind: envelope.KindQuestion, Body: "unviewed gated outgoing"})
	if _, err = linux.store.db.Exec(`UPDATE persons SET record=? WHERE state='self'`, hostRecord); err != nil {
		t.Fatal(err)
	}
	if err = linux.MarkRead([]string{gated.ID}); err != nil {
		t.Fatal(err)
	}
	if err = linux.store.db.QueryRow(`SELECT count(*) FROM read_marks WHERE lid=?`, gated.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("agent host granted mark: %d %v", n, err)
	}
	if _, err = linux.store.db.Exec(`UPDATE persons SET record=?,state=? WHERE state='self'`, originalRecord, personConflict); err != nil {
		t.Fatal(err)
	}
	if err = linux.MarkRead([]string{gated.ID}); err != nil {
		t.Fatal(err)
	}
	if err = linux.store.db.QueryRow(`SELECT count(*) FROM read_marks WHERE lid=?`, gated.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("frozen owner granted mark: %d %v", n, err)
	}
	if _, err = linux.store.db.Exec(`UPDATE persons SET state='self' WHERE person=?`, owner.info.Person); err != nil {
		t.Fatal(err)
	}
	var state string
	var attempts int
	if err := zen.store.db.QueryRow(`SELECT state,attempts FROM inbox WHERE id=?`, q.ID).Scan(&state, &attempts); err != nil || state != "answered" || attempts != 1 {
		t.Fatalf("read changed execution: %s %d %v", state, attempts, err)
	}
}

func attentionMessage(t *testing.T, a *Agent, conv, id string) ConvMessage {
	t.Helper()
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.ID == id || m.LID == id {
			return m
		}
	}
	t.Fatal("missing canonical attention message", id)
	return ConvMessage{}
}
func TestAttentionExactScopeCASAndRestart(t *testing.T) {
	w, _, conv, _ := historyCatchupFixture(t, 0)
	a := w.alice
	_, root := rootOf(t, w.bob, conv)
	add := func(body, topic string) string {
		in := envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Body: body, Topic: topic}
		env := craft(t, w.bob, a, in)
		if err := a.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return env.ID
	}
	first, second, sibling := add("first", ""), add("second", ""), add("other topic", protocol.NewID())
	own, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "my own turn"})
	if err != nil {
		t.Fatal(err)
	}
	var before string
	var read, acked bool
	if err = a.store.db.QueryRow(`SELECT state,read_at IS NOT NULL,acked FROM inbox WHERE id=?`, first).Scan(&before, &read, &acked); err != nil {
		t.Fatal(err)
	}
	if err = a.MarkUnread(first, []string{first, second, own.ID}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first, second, own.LID} {
		m := attentionMessage(t, a, conv, id)
		if !m.Unread || m.Attention == nil {
			t.Fatal("normal/outgoing deliberate unread absent", id, m)
		}
	}
	var after string
	var afterRead, afterAcked bool
	if err = a.store.db.QueryRow(`SELECT state,read_at IS NOT NULL,acked FROM inbox WHERE id=?`, first).Scan(&after, &afterRead, &afterAcked); err != nil || before != after || read != afterRead || acked != afterAcked {
		t.Fatal("unread changed execution/read/receipt", err)
	}
	generation := *attentionMessage(t, a, conv, first).Attention
	if err = a.MarkRead([]string{first, second, own.ID}); err != nil {
		t.Fatal(err)
	}
	if !attentionMessage(t, a, conv, first).Unread || !attentionMessage(t, a, conv, own.ID).Unread {
		t.Fatal("legacy read cleared deliberate state")
	}
	if err = a.MarkUnread(first, []string{first, sibling}); err == nil {
		t.Fatal("cross-topic unread accepted")
	}
	if got := *attentionMessage(t, a, conv, first).Attention; got != generation {
		t.Fatal("failed action partially changed register", got)
	}
	stale := generation
	stale.Writer = w.bob.Self().Fingerprint()
	if stale.Writer == generation.Writer {
		t.Fatal("fixture writers overlap")
	}
	if err = a.MarkReadWithAttention([]string{first}, map[string]protocol.AttentionGeneration{first: stale}); err != nil {
		t.Fatal(err)
	}
	if !attentionMessage(t, a, conv, first).Unread {
		t.Fatal("equal rev wrong writer cleared unread")
	}
	if err = a.MarkReadWithAttention([]string{first}, map[string]protocol.AttentionGeneration{first: generation}); err != nil {
		t.Fatal(err)
	}
	if attentionMessage(t, a, conv, first).Unread {
		t.Fatal("matching full generation did not clear")
	}
	if err = a.MarkUnread(second, []string{second}); err != nil {
		t.Fatal(err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !attentionMessage(t, reopened, conv, second).Unread {
		t.Fatal("restart lost deliberate unread")
	}
	if err = reopened.MarkUnread(protocol.NewID(), []string{protocol.NewID()}); err == nil {
		t.Fatal("unknown message mark accepted")
	}
	if _, err = reopened.DeleteConversation(tctx(t), conv); err != nil {
		t.Fatal(err)
	}
	if err = reopened.MarkUnread(second, []string{second}); err == nil {
		t.Fatal("erased history became eligible")
	}
	unread, err := reopened.ConvUnread()
	if err != nil || len(unread[conv]) != 0 {
		t.Fatal("erased history counted", unread, err)
	}
}

func TestAttentionSignedLinkedJourney(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.bob, w.alice)
	sent, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "linked deliberate mark"})
	if err != nil {
		t.Fatal(err)
	}
	var local, remote string
	eventually(t, "same admitted logical message on linked readers", func() bool {
		left, e := w.alice.ConversationMessages(conv)
		if e != nil {
			return false
		}
		right, e := phone.ConversationMessages(conv)
		if e != nil {
			return false
		}
		for _, m := range left {
			if m.LID == sent.LID {
				local = m.ID
			}
		}
		for _, m := range right {
			if m.LID == sent.LID {
				remote = m.ID
			}
		}
		return local != "" && remote != ""
	})
	eventually(t, "deliberate unread obtains a current snapshot", func() bool {
		// The running daemons may commit between canonical projection and
		// the write transaction. Only that explicit, mutation-free conflict
		// retries the same exact selection through a newly resolved scope.
		err = w.alice.MarkUnread(local, []string{local})
		if errors.Is(err, errAttentionScopeChanged) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return true
	})
	eventually(t, "signed V2 unread converges", func() bool { m := attentionMessage(t, phone, conv, remote); return m.Attention != nil && m.Unread })
	observed := *attentionMessage(t, phone, conv, remote).Attention
	if err = phone.MarkRead([]string{remote}); err != nil {
		t.Fatal(err)
	}
	if !attentionMessage(t, phone, conv, remote).Unread {
		t.Fatal("old read cleared V2 mark")
	}
	if err = phone.MarkReadWithAttention([]string{remote}, map[string]protocol.AttentionGeneration{remote: observed}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "signed read tombstone converges", func() bool { return !attentionMessage(t, w.alice, conv, local).Unread })
	me, _, err := phone.store.selfPerson(phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.ReadRef{Conv: conv, Fingerprint: w.bob.Self().Fingerprint(), LID: protocol.NewID()}
	mark := protocol.AttentionMark{ReadRef: ref, AttentionGeneration: protocol.AttentionGeneration{Rev: 4, Writer: phone.Self().Fingerprint()}, State: "unread"}
	r := protocol.ReadSync{V: 2, Person: me.info.Person, Roster: me.info.Roster, Marks: []protocol.AttentionMark{mark}}
	raw, _ := json.Marshal(r)
	carrier := craft(t, phone, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(raw)})
	if carrier.Attn {
		t.Fatal("attention carrier requests notification")
	}
	if err = w.alice.verifyAndStore(tctx(t), carrier); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.verifyAndStore(tctx(t), carrier); err != nil {
		t.Fatal("duplicate carrier", err)
	}
	if count(t, w.alice, "inbox WHERE lid='"+ref.LID+"'") != 0 {
		t.Fatal("unknown attention ref fetched/revealed content")
	}
	mark.State = "read"
	r.Marks = []protocol.AttentionMark{mark}
	raw, _ = json.Marshal(r)
	conflict := craft(t, phone, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(raw)})
	if err = w.alice.verifyAndStore(tctx(t), conflict); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, w.alice, conflict.ID) == "" {
		t.Fatal("exact-generation conflict not quarantined")
	}
	foreign := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(raw)})
	if err = w.alice.verifyAndStore(tctx(t), foreign); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, w.alice, foreign.ID) == "" {
		t.Fatal("foreign-owner attention accepted")
	}
}

func TestAttentionConcurrentMergeTombstonesAndCopies(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.ReadRef{Fingerprint: w.bob.Self().Fingerprint(), LID: protocol.NewID()}
	writers := []string{a.Self().Fingerprint(), phone.Self().Fingerprint()}
	slices.Sort(writers)
	low := protocol.AttentionMark{ReadRef: ref, AttentionGeneration: protocol.AttentionGeneration{Rev: 9, Writer: writers[0]}, State: "unread"}
	high := protocol.AttentionMark{ReadRef: ref, AttentionGeneration: protocol.AttentionGeneration{Rev: 9, Writer: writers[1]}, State: "read"}
	for _, order := range [][]protocol.AttentionMark{{low, high}, {high, low}} {
		tx, e := a.store.db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(`DELETE FROM attention_marks`); e != nil {
			t.Fatal(e)
		}
		for _, m := range order {
			if e = saveAttentionMark(tx, me.info.Person, m); e != nil {
				t.Fatal(e)
			}
		}
		if e = saveAttentionMark(tx, me.info.Person, high); e != nil {
			t.Fatal("duplicate", e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		got, e := loadAttentionMarks(a.store.db, me.info.Person, []protocol.ReadRef{ref})
		if e != nil || got[ref] != high {
			t.Fatal("concurrent order did not converge", got, e)
		}
	}
	if _, err = a.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	before := count(t, a, "outbox WHERE sub='read-sync'")
	if _, err = a.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	if after := count(t, a, "outbox WHERE sub='read-sync'"); after != before {
		t.Fatal("same generation resealed", before, after)
	}
	var cap string
	if err = a.store.db.QueryRow(`SELECT required_cap FROM outbox WHERE sub=? AND body LIKE '%"marks"%' LIMIT 1`, envelope.SubReadSync).Scan(&cap); err != nil || cap != protocol.CapReadSyncV2 {
		t.Fatal("mark carrier downgraded", cap, err)
	}
	if count(t, a, "attention_mark_copies") != 1 {
		t.Fatal("copy ledger not bounded per recipient/ref")
	}
	var carrierID string
	if err = a.store.db.QueryRow(`SELECT id FROM outbox WHERE required_cap=? LIMIT 1`, protocol.CapReadSyncV2).Scan(&carrierID); err != nil {
		t.Fatal(err)
	}
	if err = a.MarkUnread(carrierID, []string{carrierID}); err == nil {
		t.Fatal("quiet protocol carrier became an unread message")
	}
}

func TestAttentionOutgoingDirectSummary(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	env := sealTo(t, a, phone, envelope.Inner{Kind: envelope.KindMessage, Body: "my direct message"})
	opened, err := envelope.Open(env, phone.id, phone.Address, a.Self())
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.addOutbox(env, opened, "", nil, boundOutgoing{fingerprint: phone.Self().Fingerprint()}); err != nil {
		t.Fatal(err)
	}
	if err = a.MarkUnread(env.ID, []string{env.ID}); err != nil {
		t.Fatal(err)
	}
	view, err := a.Conversation(env.ID, 0, 0)
	if err != nil || len(view.Messages) != 1 || !view.Messages[0].Unread || view.Messages[0].Attention == nil {
		t.Fatal("outgoing direct projection", view, err)
	}
	summary, err := a.TopicOf(env.ID)
	if err != nil || summary.Unread != 1 {
		t.Fatal("outgoing direct summary", summary, err)
	}
}

func TestAttentionAppendOnlyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.db")
	old, err := sqlitedb.Open(path, schema[:len(schema)-1])
	if err != nil {
		t.Fatal(err)
	}
	fp := "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"
	owner, lid := protocol.NewID(), protocol.NewID()
	if _, err = old.Exec(`INSERT INTO config(k,v) VALUES('attention-migration-proof','retained')`); err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,read_at,verified_by) VALUES(?, 'owner/device', 1,'message','retained inbox',2,3,?)`, lid, fp); err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(`INSERT INTO read_marks(owner,conv,fingerprint,lid) VALUES(?,'',?,?)`, owner, fp, lid); err != nil {
		t.Fatal(err)
	}
	definitions := func(q dbq) map[string]string {
		t.Helper()
		rows, e := q.Query(`SELECT name,coalesce(sql,'') FROM sqlite_master ORDER BY name`)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var name, sql string
			if e = rows.Scan(&name, &sql); e != nil {
				t.Fatal(e)
			}
			out[name] = sql
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		return out
	}
	before := definitions(old)
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.db.Close()
	after := definitions(upgraded.db)
	for name, sql := range before {
		if after[name] != sql {
			t.Fatalf("shipped object %s changed", name)
		}
	}
	var version, read, marks, attention, copies int
	var value, body string
	if err = upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != len(schema) {
		t.Fatal(version, err)
	}
	if err = upgraded.db.QueryRow(`SELECT v FROM config WHERE k='attention-migration-proof'`).Scan(&value); err != nil || value != "retained" {
		t.Fatal(value, err)
	}
	if err = upgraded.db.QueryRow(`SELECT body,read_at FROM inbox WHERE id=?`, lid).Scan(&body, &read); err != nil || body != "retained inbox" || read != 3 {
		t.Fatal(body, read, err)
	}
	if err = upgraded.db.QueryRow(`SELECT count(*) FROM read_marks WHERE owner=? AND fingerprint=? AND lid=?`, owner, fp, lid).Scan(&marks); err != nil || marks != 1 {
		t.Fatal(marks, err)
	}
	if err = upgraded.db.QueryRow(`SELECT (SELECT count(*) FROM attention_marks),(SELECT count(*) FROM attention_mark_copies)`).Scan(&attention, &copies); err != nil || attention != 0 || copies != 0 {
		t.Fatal(attention, copies, err)
	}
}

func TestAttentionLargeActionAtomicAndBoundedCopies(t *testing.T) {
	w, phone, conv, _ := historyCatchupFixture(t, 0)
	a := w.alice
	runAgent(t, phone)
	label, device, _ := protocol.SplitAddress(phone.Address)
	eventually(t, "healthy bulk rd2 reader", func() bool {
		var p protocol.Profile
		return a.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &p) == nil && p.Supports(phone.Address, phone.Self().SignKey, protocol.CapReadSyncV2)
	})
	_, root := rootOf(t, w.bob, conv)
	ids := make([]string, 130)
	for i := range ids {
		env := craft(t, w.bob, a, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Body: "bulk unread"})
		if err := a.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		ids[i] = env.ID
	}
	// Before any deliberate action, ordinary reads add no attention work.
	if err := a.MarkRead(ids); err != nil {
		t.Fatal(err)
	}
	if count(t, a, "attention_marks") != 0 || count(t, a, "attention_mark_copies") != 0 {
		t.Fatal("normal reads amplified attention work")
	}
	if err := a.MarkUnread(ids[0], append(ids, ids[0])); err != nil {
		t.Fatal(err)
	}
	if count(t, a, "attention_marks") != 130 {
		t.Fatal("chunk/dedup changed exact action size")
	}
	for i := 0; i < 5; i++ {
		more, err := a.syncReadMarks()
		if err != nil {
			t.Fatal(err)
		}
		pending := ownCarriers(t, a, phone, envelope.SubReadSync)
		for _, carrier := range pending {
			var required, state string
			if err = a.store.db.QueryRow(`SELECT required_cap,state FROM outbox WHERE id=?`, carrier.ID).Scan(&required, &state); err != nil {
				t.Fatal(err)
			}
			if required == protocol.CapReadSyncV2 && state == stateQueued {
				before := count(t, a, "outbox WHERE required_cap='rd2'")
				if _, err = a.syncReadMarks(); err != nil {
					t.Fatal(err)
				}
				if count(t, a, "outbox WHERE required_cap='rd2'") != before {
					t.Fatal("bulk second pass duplicated pending carrier")
				}
				if result, e := a.deliver(tctx(t), carrier, nil); e != nil || result.State != protocol.StateCustody && result.State != protocol.StateDelivered {
					t.Fatal("healthy bulk carrier did not hand over", result.State, e)
				}
				eventually(t, "healthy bulk carrier delivered", func() bool {
					r, e := a.Status(tctx(t), carrier.ID, 0)
					return e == nil && r.State == protocol.StateDelivered
				})
			}
		}
		if !more {
			break
		}
	}
	if count(t, a, "attention_mark_copies") != 130 {
		t.Fatal("not all generations have bounded copies")
	}
	rows, err := a.store.db.Query(`SELECT body FROM outbox WHERE required_cap=? AND state='delivered'`, protocol.CapReadSyncV2)
	if err != nil {
		t.Fatal(err)
	}
	total, carriers := 0, 0
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		record, e := protocol.ParseReadSync([]byte(body))
		if e != nil || len(record.Marks) > 64 {
			t.Fatal("invalid bounded carrier", e)
		}
		total += len(record.Marks)
		carriers++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if total != 130 || carriers != 3 {
		t.Fatal("wire action was not batched 64/64/2", total, carriers)
	}
	if _, err = a.store.db.Exec(`UPDATE outbox SET state='quarantined' WHERE required_cap=?`, protocol.CapReadSyncV2); err != nil {
		t.Fatal(err)
	}
	before := count(t, a, "outbox WHERE required_cap='rd2'")
	if _, err = a.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	if count(t, a, "outbox WHERE required_cap='rd2'") != before {
		t.Fatal("quarantined generation resealed")
	}
	last := attentionMessage(t, a, conv, ids[len(ids)-1])
	if _, err = a.store.db.Exec(`UPDATE attention_marks SET revision=? WHERE conv=? AND lid=?`, protocol.MaxAttentionRevision, conv, last.LID); err != nil {
		t.Fatal(err)
	}
	if err = a.MarkUnread(ids[0], ids); err == nil {
		t.Fatal("overflow accepted")
	}
	if got := attentionMessage(t, a, conv, ids[0]); got.Attention == nil || got.Attention.Rev != 1 {
		t.Fatal("large failed action partially committed", got)
	}
	tokens := map[string]protocol.AttentionGeneration{}
	for _, m := range mustAttentionMessages(t, a, conv) {
		if m.Attention != nil {
			tokens[m.ID] = *m.Attention
		}
	}
	if err = a.MarkReadWithAttention(ids, tokens); err == nil {
		t.Fatal("clear overflow accepted")
	}
	if got := attentionMessage(t, a, conv, ids[0]); !got.Unread || got.Attention.Rev != 1 {
		t.Fatal("failed clear partially committed", got)
	}
	if _, err = a.store.db.Exec(`UPDATE attention_marks SET revision=1 WHERE conv=? AND lid=?`, conv, last.LID); err != nil {
		t.Fatal(err)
	}
	tokens[ids[len(ids)-1]] = protocol.AttentionGeneration{Rev: 1, Writer: a.Self().Fingerprint()}
	if err = a.MarkReadWithAttention(ids, tokens); err != nil {
		t.Fatal(err)
	}
	for _, m := range mustAttentionMessages(t, a, conv) {
		if m.Unread || m.Attention != nil {
			t.Fatal("large clear failed", m)
		}
	}
}

func mustAttentionMessages(t *testing.T, a *Agent, conv string) []ConvMessage {
	t.Helper()
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func TestAttentionScopeSnapshotRevalidatedBeforeWrite(t *testing.T) {
	w, _, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	calls := 0
	tx, scope, err := a.attentionActionScope(func() (map[string]protocol.ReadRef, error) {
		calls++
		if calls == 1 {
			_, e := a.store.db.Exec(`INSERT INTO config(k,v) VALUES('attention-scope-change','1')`)
			if e != nil {
				return nil, e
			}
		}
		return map[string]protocol.ReadRef{"snapshot": {LID: protocol.NewID()}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if calls != 2 || len(scope) != 1 {
		t.Fatal("stale canonical snapshot entered transaction", calls, scope)
	}
	var stable int
	if err = tx.QueryRow(`SELECT count(*) FROM config WHERE k='attention-scope-change'`).Scan(&stable); err != nil || stable != 1 {
		t.Fatal(stable, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var path string
	if err = a.store.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	other, err := sqlitedb.Open(path, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	calls = 0
	tx, _, err = a.attentionActionScope(func() (map[string]protocol.ReadRef, error) {
		calls++
		if calls == 1 {
			_, e := other.Exec(`INSERT INTO config(k,v) VALUES('attention-other-connection','1')`)
			if e != nil {
				return nil, e
			}
		}
		return map[string]protocol.ReadRef{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if calls != 2 {
		t.Fatal("external commit bypassed scope validation", calls)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	incoming := sealTo(t, w.bob, a, envelope.Inner{Kind: envelope.KindMessage, Body: "bounded contention incoming"})
	if err = a.verifyAndStore(tctx(t), incoming); err != nil {
		t.Fatal(err)
	}
	outgoing, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "bounded contention sent"})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.MarkUnread(incoming.ID, []string{incoming.ID}); err != nil {
		t.Fatal(err)
	}
	owner, err := attentionOwner(a.store.db)
	if err != nil {
		t.Fatal(err)
	}
	refs := []protocol.ReadRef{{Fingerprint: w.bob.Self().Fingerprint(), LID: incoming.ID}}
	before, err := loadAttentionMarks(a.store.db, owner, refs)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{incoming.ID, outgoing.ID}
	resetLegacy := func() {
		t.Helper()
		if _, e := a.store.db.Exec(`UPDATE inbox SET read_at=NULL WHERE id=?`, incoming.ID); e != nil {
			t.Fatal(e)
		}
		if _, e := a.store.db.Exec(`DELETE FROM read_marks WHERE lid IN (?,?)`, incoming.ID, outgoing.ID); e != nil {
			t.Fatal(e)
		}
	}
	checkLegacy := func(t *testing.T, want bool) {
		t.Helper()
		var read bool
		var sent int
		if e := a.store.db.QueryRow(`SELECT read_at IS NOT NULL FROM inbox WHERE id=?`, incoming.ID).Scan(&read); e != nil {
			t.Fatal(e)
		}
		if e := a.store.db.QueryRow(`SELECT count(*) FROM read_marks WHERE fingerprint=? AND lid=?`, a.Self().Fingerprint(), outgoing.ID).Scan(&sent); e != nil {
			t.Fatal(e)
		}
		if read != want || (sent == 1) != want {
			t.Fatalf("legacy incoming/signed-sent acknowledgement: read=%v sent=%d want=%v", read, sent, want)
		}
	}
	for _, writer := range []struct {
		name string
		db   *sql.DB
	}{{"same-connection", a.store.db}, {"external-connection", other}} {
		t.Run("continuous-"+writer.name, func(t *testing.T) {
			resetLegacy()
			calls := 0
			stop := errors.New("test stopped unbounded resolution at fourth call")
			tx, scope, err := a.attentionActionScope(func() (map[string]protocol.ReadRef, error) {
				calls++
				if calls == 4 {
					return nil, stop
				}
				scope, e := a.attentionScope(incoming.ID)
				if e != nil {
					return nil, e
				}
				_, e = writer.db.Exec(`INSERT INTO config(k,v) VALUES('attention-continuous',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, calls)
				return scope, e
			})
			if tx != nil {
				tx.Rollback()
			}
			if calls != 3 || tx != nil || scope != nil || !errors.Is(err, errAttentionScopeChanged) || errors.Is(err, stop) {
				t.Fatalf("snapshot contention must stop after three attempts without transaction: calls=%d tx=%v scope=%v err=%v", calls, tx, scope, err)
			}
			// An abandoned attempt must release the store's only connection.
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			proof, e := a.store.db.BeginTx(ctx, nil)
			if e == nil {
				e = proof.Rollback()
			}
			cancel()
			if e != nil {
				t.Fatal("contention leaked a transaction", e)
			}
			checkLegacy(t, false)
			// Execute the same exhaustion continuation used by MarkReadWithAttention,
			// with the real error from the write-invalidated scope above.
			if e = a.markReadAfterAttention(ids, err); e != nil {
				t.Fatal(e)
			}
			checkLegacy(t, true)
			after, e := loadAttentionMarks(a.store.db, owner, refs)
			if e != nil || !reflect.DeepEqual(after, before) {
				t.Fatal("contention changed the deliberate register", before, after, e)
			}
			t.Logf("%s: scopes=%d, no attention mutation/clear, no leaked transaction, incoming and signed-sent legacy read retained", writer.name, calls)
		})
	}
	resetLegacy()
	otherFailure := errors.New("scope resolver failure")
	_, _, scopeErr := a.attentionActionScope(func() (map[string]protocol.ReadRef, error) { return nil, otherFailure })
	if err = a.markReadAfterAttention(ids, scopeErr); !errors.Is(err, otherFailure) {
		t.Fatal("non-contention error changed", err)
	}
	checkLegacy(t, false)
	if err = a.MarkReadWithAttention(ids, map[string]protocol.AttentionGeneration{incoming.ID: {Rev: 0, Writer: a.Self().Fingerprint()}}); err == nil {
		t.Fatal("malformed generation reached legacy fallback")
	}
	checkLegacy(t, false)
}

func TestAttentionExactAuthorReferences(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	owner, err := attentionOwner(a.store.db)
	if err != nil {
		t.Fatal(err)
	}
	lid := protocol.NewID()
	refs := []protocol.ReadRef{{Fingerprint: a.Self().Fingerprint(), LID: lid}, {Fingerprint: phone.Self().Fingerprint(), LID: lid}}
	want := map[protocol.ReadRef]protocol.AttentionMark{}
	tx, err := a.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range refs {
		m := protocol.AttentionMark{ReadRef: ref, AttentionGeneration: protocol.AttentionGeneration{Rev: 1, Writer: a.Self().Fingerprint()}, State: []string{"read", "unread"}[i]}
		want[ref] = m
		if err = saveAttentionMark(tx, owner, m); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := loadAttentionMarks(a.store.db, owner, refs)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("bare LID collapsed exact authors", got, err)
	}
}

func TestAttentionLinkedOutgoingDeviceProjectionAndRetraction(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	sent, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "my linked outgoing turn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.deviceHistoryPage(phone.Self()); err != nil {
		t.Fatal(err)
	}
	if n := deliverDeviceHistory(t, a, phone, envelope.SubDeviceHistory); n != 1 {
		t.Fatal("history copies", n)
	}
	view, err := phone.Conversation(sent.ID, 0, 0)
	if err != nil || len(view.Messages) != 1 || view.Messages[0].Dir != "out" || view.Messages[0].FromKey != a.Self().Fingerprint() {
		t.Fatal("original linked author/direction", view, err)
	}
	if err = phone.MarkUnread(sent.ID, []string{sent.ID}); err != nil {
		t.Fatal(err)
	}
	summary, err := phone.TopicOf(sent.ID)
	if err != nil || summary.Unread != 1 {
		t.Fatal("linked outgoing summary", summary, err)
	}
	if err = a.MarkUnread(sent.ID, []string{sent.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Retract(tctx(t), ControlRef{ID: sent.ID, Fingerprint: a.Self().Fingerprint()}, "retracted"); err != nil {
		t.Fatal(err)
	}
	view, err = a.Conversation(sent.ID, 0, 0)
	if err != nil || len(view.Messages) != 1 || !view.Messages[0].Deleted || view.Messages[0].Unread || view.Messages[0].Attention != nil {
		t.Fatal("retraction left attention visible", view, err)
	}
	if err = a.MarkUnread(sent.ID, []string{sent.ID}); err == nil {
		t.Fatal("retracted target accepted")
	}
	summary, err = a.TopicOf(sent.ID)
	if err != nil || summary.Unread != 0 {
		t.Fatal("retracted target counted", summary, err)
	}
}

func TestAttentionNormalAgentAndFileOnlyRows(t *testing.T) {
	w, _, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	question := sealTo(t, w.bob, a, envelope.Inner{Kind: envelope.KindQuestion, Body: "normal request"})
	if err := a.verifyAndStore(tctx(t), question); err != nil {
		t.Fatal(err)
	}
	agentID := protocol.NewID()
	reply, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindAnswer, ReplyTo: question.ID, Body: "agent response", AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := writeFile(t, t.TempDir(), "attention-file-only.bin", 997)
	file, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, ReplyTo: reply.ID, Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{question.ID, reply.ID, file.ID}
	if err = a.MarkUnread(question.ID, ids); err != nil {
		t.Fatal(err)
	}
	view, err := a.Conversation(question.ID, 0, 0)
	if err != nil || len(view.Messages) != 3 {
		t.Fatal(view, err)
	}
	for _, m := range view.Messages {
		if !m.Unread || m.Attention == nil {
			t.Fatal("normal row omitted", m)
		}
		if m.ID == file.ID && (m.Body != "" || len(m.Attachments) != 1) {
			t.Fatal("file-only fixture", m)
		}
		if m.ID == reply.ID && m.AgentID != agentID {
			t.Fatal("agent fixture", m)
		}
	}
	summary, err := a.TopicOf(question.ID)
	if err != nil || summary.Unread != 3 {
		t.Fatal("normal rows did not share count", summary, err)
	}
}

type attentionQueryCounter struct {
	dbq
	sets, scalars int
}

func (q *attentionQueryCounter) Query(query string, args ...any) (*sql.Rows, error) {
	q.sets++
	return q.dbq.Query(query, args...)
}
func (q *attentionQueryCounter) QueryRow(query string, args ...any) *sql.Row {
	q.scalars++
	return q.dbq.QueryRow(query, args...)
}
func TestAttentionOperationLookupCost(t *testing.T) {
	w, _, _, _ := historyCatchupFixture(t, 0)
	q := &attentionQueryCounter{dbq: w.alice.store.db}
	owner, err := attentionOwner(q)
	if err != nil || owner == "" || q.scalars != 1 || q.sets != 0 {
		t.Fatal("owner lookup loaded roster/device rows", owner, q.scalars, q.sets, err)
	}
	refs := make([]protocol.ReadRef, 130)
	for i := range refs {
		refs[i] = protocol.ReadRef{Fingerprint: w.bob.Self().Fingerprint(), LID: protocol.NewID()}
	}
	marks, err := loadAttentionMarks(q, owner, refs)
	if err != nil || len(marks) != 0 || q.sets != 2 || q.scalars != 1 {
		t.Fatal("exact lookup cost depends on individual rows", q.scalars, q.sets, err)
	}
	t.Logf("130 references: %d scalar owner query, %d chunked exact mark queries, 0 per-row lookups", q.scalars, q.sets)
	if _, err = w.alice.store.db.Exec(`UPDATE persons SET state=? WHERE state=?`, personConflict, personSelf); err != nil {
		t.Fatal(err)
	}
	owner, err = attentionOwner(q)
	if err != nil || owner != "" {
		t.Fatal("missing owner compatibility", owner, err)
	}
}

// The generic delivery gate must retain an incompatible V2 carrier, rather
// than resealing it. The second signed stream is a real concurrent reader.
func testReadSyncV2Waiting(t *testing.T, mode string) {
	t.Helper()
	w, phone, conv, _ := historyCatchupFixture(t, 0)
	a := w.alice
	runAgent(t, phone)
	label, device, _ := protocol.SplitAddress(phone.Address)
	profilePath := "/v1/agents/" + label + "/" + device + "/profile"
	var profile protocol.Profile
	eventually(t, "current rd2 reader", func() bool {
		return a.hub.do(tctx(t), "GET", profilePath, nil, &profile) == nil && profile.Supports(phone.Address, phone.Self().SignKey, protocol.CapReadSyncV2)
	})
	ad := protocol.SessionAd{Address: phone.Address, Session: protocol.NewID()}
	protocol.SignAd(&ad, phone.id.Sign)
	req, err := phone.hub.request(t.Context(), "GET", "/v1/stream?ad="+ad.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := phone.hub.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() { defer close(drained); _, _ = io.Copy(io.Discard, response.Body) }()
	defer func() { response.Body.Close(); <-drained }()
	publish := func(caps []string, ts int64) {
		t.Helper()
		caps = slices.Clone(caps)
		slices.Sort(caps)
		c := protocol.CapsRecord{Address: phone.Address, Session: ad.Session, Caps: caps, TS: ts}
		c.Sign(phone.id.Sign)
		if err := phone.hub.do(tctx(t), "PUT", "/v1/caps", c, nil); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().Unix() + 100
	if mode == "rd1" {
		publish([]string{protocol.CapEnv2, protocol.CapPerson, protocol.CapReadSync, protocol.CapRoom}, stamp)
	}
	if err = a.hub.do(tctx(t), "GET", profilePath, nil, &profile); err != nil {
		t.Fatal(err)
	}
	if len(profile.Sessions) < 2 || profile.Supports(phone.Address, phone.Self().SignKey, protocol.CapReadSyncV2) {
		t.Fatal("true concurrent old reader did not hold rd2")
	}
	_, root := rootOf(t, w.bob, conv)
	logical := protocol.NewID()
	item := craft(t, w.bob, a, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: logical, Body: "deliberately unread while old reader remains"})
	if err = a.verifyAndStore(tctx(t), item); err != nil {
		t.Fatal(err)
	}
	if err = a.MarkUnread(item.ID, []string{item.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	var encoded, carrier string
	if err = a.store.db.QueryRow(`SELECT id,envelope FROM outbox WHERE recipient=? AND required_cap=? ORDER BY rowid DESC LIMIT 1`, phone.Address, protocol.CapReadSyncV2).Scan(&carrier, &encoded); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err = json.Unmarshal([]byte(encoded), &env); err != nil {
		t.Fatal(err)
	}
	// Close the queued-before-first-handover race as well as waiting churn.
	if err = a.MarkUnread(item.ID, []string{item.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, a, "outbox WHERE required_cap='rd2'"); n != 1 {
		t.Fatal("second producer duplicated queued carrier", n)
	}
	if result, e := a.deliver(tctx(t), env, nil); e != nil || result.State != stateConvWaiting {
		t.Fatalf("rd2 generic delivery must wait: state=%s err=%v", result.State, e)
	}
	other := craft(t, w.bob, a, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Body: "another latent ref"})
	if err = a.verifyAndStore(tctx(t), other); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if err = a.MarkUnread(item.ID, []string{item.ID, other.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err = a.syncReadMarks(); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, a, "outbox WHERE required_cap='rd2'"); n != 1 {
		t.Fatalf("60 waiting generations must coalesce to one carrier: %d", n)
	}
	if got := attentionMessage(t, a, conv, item.ID).Attention.Rev; got != 62 {
		t.Fatal("latest generation not canonical", got)
	}
	for i := 0; i < 4; i++ {
		more, e := a.syncReadMarks()
		if e != nil || more {
			t.Fatal("represented generation kept producing work", more, e)
		}
	}
	var copies int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM attention_mark_copies WHERE recipient_fp=? AND carrier=?`, phone.Self().Fingerprint(), carrier).Scan(&copies); err != nil || copies != 1 {
		t.Fatal("exact copy mapping lost", copies, err)
	}
	if n := count(t, a, "outbox WHERE required_cap='rd2'"); n != 1 {
		t.Fatal("rd2 resealed", n)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	a = again
	if more, e := a.syncReadMarks(); e != nil || more {
		t.Fatal("restart replayed represented generation", more, e)
	}
	var after string
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, carrier).Scan(&after); err != nil || after != encoded || count(t, a, "outbox WHERE required_cap='rd2'") != 1 {
		t.Fatal("restart changed exact waiting ciphertext", err)
	}
	if mode == "rd1" {
		ordinary := craft(t, w.bob, a, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Body: "ordinary V1 read beside waiting V2"})
		if err = a.verifyAndStore(tctx(t), ordinary); err != nil {
			t.Fatal(err)
		}
		if err = a.MarkRead([]string{ordinary.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err = a.syncReadMarks(); err != nil {
			t.Fatal(err)
		}
		var v1 string
		if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND required_cap=? ORDER BY rowid DESC LIMIT 1`, phone.Address, protocol.CapReadSync).Scan(&v1); err != nil {
			t.Fatal("waiting rd2 starved ordinary V1", err)
		}
		var legacy envelope.Envelope
		if err = json.Unmarshal([]byte(v1), &legacy); err != nil {
			t.Fatal(err)
		}
		if result, e := a.deliver(tctx(t), legacy, nil); e != nil || result.State != protocol.StateCustody && result.State != protocol.StateDelivered {
			t.Fatal("ordinary V1 did not deliver", result.State, e)
		}
	}
	publish(slices.Clone(ownCaps), stamp+100)
	features, err := a.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	a.releaseConv(tctx(t), features)
	if result, e := a.deliver(tctx(t), env, nil); e != nil || result.State != protocol.StateCustody && result.State != protocol.StateDelivered {
		t.Fatal("same rd2 carrier did not release", result.State, e)
	}
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, carrier).Scan(&after); err != nil || after != encoded {
		t.Fatal("release resealed ciphertext", err)
	}
	var receipt protocol.Receipt
	eventually(t, "original carrier acknowledged by real recipient", func() bool {
		return a.hub.do(tctx(t), "GET", "/v1/messages/"+carrier, nil, &receipt) == nil && receipt.State == protocol.StateDelivered
	})
	a.convWork.take() // isolate the ordinary receipt event from local-action wakes
	data, _ := json.Marshal(protocol.ReceiptEvent{ID: carrier, State: receipt.State, Seq: 1})
	if err = a.dispatch(tctx(t), "receipt", string(data)); err != nil {
		t.Fatal(err)
	}
	if a.convWork.bits.Load()&convHistory == 0 {
		t.Fatal("receipt did not wake latent attention")
	}
	a.convSync(tctx(t))
	var latestEncoded string
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE required_cap=? AND id!=? ORDER BY rowid DESC LIMIT 1`, protocol.CapReadSyncV2, carrier).Scan(&latestEncoded); err != nil {
		t.Fatal("receipt wake failed to produce latest generation", err)
	}
	var latest envelope.Envelope
	if err = json.Unmarshal([]byte(latestEncoded), &latest); err != nil {
		t.Fatal(err)
	}
	if result, e := a.deliver(tctx(t), latest, nil); e != nil || result.State != protocol.StateCustody && result.State != protocol.StateDelivered {
		t.Fatal("latest generation did not follow", result.State, e)
	}
	eventually(t, "latest generation admitted without new user action", func() bool {
		var rev int
		return phone.store.db.QueryRow(`SELECT revision FROM attention_marks WHERE fingerprint=? AND lid=?`, w.bob.Self().Fingerprint(), logical).Scan(&rev) == nil && rev == 62
	})
	// Fresh signed carriers/references exercise both actual admission orders,
	// rather than duplicate inbox IDs taking the idempotency shortcut.
	self, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		t.Fatal(err)
	}
	for _, reverse := range []bool{false, true} {
		ref := protocol.ReadRef{Conv: conv, Fingerprint: w.bob.Self().Fingerprint(), LID: protocol.NewID()}
		old := protocol.AttentionMark{ReadRef: ref, AttentionGeneration: protocol.AttentionGeneration{Rev: 1, Writer: a.Self().Fingerprint()}, State: "unread"}
		newest := old
		newest.Rev = 62
		newest.State = "read"
		order := []protocol.AttentionMark{old, newest}
		if reverse {
			slices.Reverse(order)
		}
		for _, mark := range order {
			body, _ := json.Marshal(protocol.ReadSync{V: 2, Person: self.info.Person, Roster: self.info.Roster, Marks: []protocol.AttentionMark{mark}})
			replay := craft(t, a, phone, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(body)})
			if err = phone.verifyAndStore(tctx(t), replay); err != nil {
				t.Fatal(err)
			}
		}
		var rev int
		var state string
		if err = phone.store.db.QueryRow(`SELECT revision,state FROM attention_marks WHERE fingerprint=? AND lid=?`, ref.Fingerprint, ref.LID).Scan(&rev, &state); err != nil || rev != 62 || state != "read" {
			t.Fatal("signed arrival order lost latest tombstone", reverse, rev, state, err)
		}
	}
	for _, terminal := range []string{stateFailed, protocol.StateExpired} {
		if err = a.store.setOutboxState(latest.ID, terminal, "test terminal transport", ""); err != nil {
			t.Fatal(err)
		}
		before := count(t, a, "outbox WHERE required_cap='rd2'")
		if _, err = a.syncReadMarks(); err != nil {
			t.Fatal(err)
		}
		if count(t, a, "outbox WHERE required_cap='rd2'") != before+1 {
			t.Fatal("terminal carrier blocked reseal", terminal)
		}
		if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE required_cap=? ORDER BY rowid DESC LIMIT 1`, protocol.CapReadSyncV2).Scan(&latestEncoded); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(latestEncoded), &latest); err != nil {
			t.Fatal(err)
		}
		if result, e := a.deliver(tctx(t), latest, nil); e != nil || result.State != protocol.StateCustody && result.State != protocol.StateDelivered {
			t.Fatal("terminal reseal delivery", terminal, result.State, e)
		}
	}
	if _, err = a.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	if err = a.store.setOutboxState(carrier, stateQueued, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, allowed, e := a.mayDeliverReadSync(env); e != nil || allowed {
		t.Fatal("changed reader key admitted", allowed, e)
	}
	if _, err = a.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	if err = a.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if err = a.store.setOutboxState(carrier, stateQueued, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, allowed, e := a.mayDeliverReadSync(env); e != nil || allowed {
		t.Fatal("revoked reader admitted", allowed, e)
	}
	t.Logf("rd2 mode=%s one exact waiting carrier/copy across producer+restart; V1 independent=%t; same cipher released; key/revocation fenced", mode, mode == "rd1")
}
