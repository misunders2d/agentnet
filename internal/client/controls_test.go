package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// legacyView is the thread with peer as a's messenger shows it, by id.
func legacyView(t *testing.T, a *Agent, id string) ConversationMessage {
	t.Helper()
	c, err := a.Conversation(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range c.Messages {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("%s not in %s's thread", id, a.Address)
	return ConversationMessage{}
}

// react keeps trying until the peer's capabilities are published (its
// daemon just connected), then returns.
func react(t *testing.T, a *Agent, ref ControlRef, emoji string, remove bool) ControlSent {
	t.Helper()
	var sent ControlSent
	eventually(t, "controls accepted by "+ref.ID, func() bool {
		var err error
		sent, err = a.React(tctx(t), ref, emoji, remove)
		return err == nil
	})
	return sent
}

// Controls on an existing device chat: a reaction and its removal converge
// on both sides, only the author edits or deletes, a deletion hides the
// text but keeps the row, and the controls themselves never show up as
// messages, decisions or jobs.
func TestControlsInDeviceChat(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	hello, err := w.bob.Send(tctx(t), w.alice.Address, "hello there", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to hold it", func() bool { return inboxCount(t, w.alice, `id = ?`, hello.ID) == 1 })
	ref, err := w.alice.RefOf("", hello.ID, "in")
	if err != nil || ref.Fingerprint != w.bob.Self().Fingerprint() || ref.Conv != "" {
		t.Fatalf("ref = %+v %v", ref, err)
	}
	react(t, w.alice, ref, "👍", false)
	if m := legacyView(t, w.alice, hello.ID); len(m.Reactions) != 1 || m.Reactions[0].Emoji != "👍" || !m.Reactions[0].Mine || m.Reactions[0].By[0].ID != w.alice.Address {
		t.Fatalf("alice's own reaction: %+v", m.Controls)
	}
	eventually(t, "bob sees the reaction", func() bool {
		m := legacyView(t, w.bob, hello.ID)
		return len(m.Reactions) == 1 && m.Reactions[0].Emoji == "👍" && !m.Reactions[0].Mine && m.Reactions[0].By[0].ID == w.alice.Address
	})
	// Bob's own view offers edit and delete; alice's offers only a reaction.
	if m := legacyView(t, w.bob, hello.ID); strings.Join(m.Can, ",") != "react,edit,delete" {
		t.Fatalf("bob may: %v", m.Can)
	}
	if m := legacyView(t, w.alice, hello.ID); strings.Join(m.Can, ",") != "react" {
		t.Fatalf("alice may: %v", m.Can)
	}
	react(t, w.alice, ref, "👍", true)
	eventually(t, "the reaction gone at bob", func() bool { return len(legacyView(t, w.bob, hello.ID).Reactions) == 0 })

	// Only the author edits: alice cannot, bob can; alice sees the edit and
	// the original stays the body.
	if _, err := w.alice.Revise(tctx(t), ref, "hijacked"); err == nil || !strings.Contains(err.Error(), "only the sender") {
		t.Fatalf("alice editing bob's message: %v", err)
	}
	bobRef, err := w.bob.RefOf("", hello.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.Revise(tctx(t), bobRef, "hello there, corrected"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice sees the edit", func() bool {
		m := legacyView(t, w.alice, hello.ID)
		return m.Edited && m.Revision == 1 && m.Text == "hello there, corrected" && m.Body == "hello there"
	})
	// A forged edit: alice claims bob's message as her own (ref under her
	// key). Bob holds it for a target that never exists and shows nothing.
	forged := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: envelope.SubRevision, Body: `{"rev":9,"text":"forged"}`, Ref: &envelope.Ref{ID: hello.ID, Fingerprint: w.alice.Self().Fingerprint()}}
	key, _ := w.alice.sendKey(tctx(t), w.bob.Address)
	rcpt, _ := key.Recipient()
	env, err := envelope.Seal(forged, w.alice.id.Sign, rcpt)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.alice.store.addControlOutbox(env, forged); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.deliver(tctx(t), env, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds the forgery aside", func() bool {
		var n int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id = ? AND reason = ?`, forged.ID, reasonProof).Scan(&n)
		return n == 1
	})
	if m := legacyView(t, w.bob, hello.ID); m.Text != "hello there, corrected" || m.Revision != 1 || m.Deleted {
		t.Fatalf("the forgery applied: %+v", m.Controls)
	}
	// Bob deletes: alice's view is a tombstone; the row, its reactions'
	// rows and the receipts stay; nothing is listed as a message or decision.
	if _, err := w.bob.Retract(tctx(t), bobRef, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice sees the deletion", func() bool { return legacyView(t, w.alice, hello.ID).Deleted })
	if m := legacyView(t, w.alice, hello.ID); m.Shown(m.Body) != "" || len(m.Can) != 0 {
		t.Fatalf("a deleted message still shows: %+v", m)
	}
	// The row stays (ids, receipts, links); an ordinary message's text does not.
	if msgs, _ := w.alice.Inbox(false, false); len(msgs) != 1 || msgs[0].ID != hello.ID || msgs[0].Body != "" {
		t.Fatalf("inbox lists controls, or kept the deleted text: %+v", msgs)
	}
	if review, _ := w.alice.Review(); len(review) != 0 {
		t.Fatalf("a control waits for a decision: %+v", review)
	}
	if c, _ := w.alice.Conversation(hello.ID, 0, 0); len(c.Messages) != 1 {
		t.Fatalf("controls in the thread: %d messages", len(c.Messages))
	}
}

// Editing a question never changes what the recipient's agent runs: the
// job takes the admitted text even when the edit arrived before the job
// ran, the edit is shown beside it, and nothing runs twice.
func TestEditNeverChangesAdmittedRequest(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	stopBob := runAgent(t, w.bob) // publishes bob's capabilities
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "what is the ORIGINAL question", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	stopBob()
	ref, err := w.alice.RefOf("", q.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.Revise(tctx(t), ref, "what is the EDITED question"); err != nil {
		t.Fatal(err)
	}
	// A second question, edited while bob is offline: the edit is queued
	// behind it and both arrive together when bob returns.
	q2, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "second ORIGINAL", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	ref2, _ := w.alice.RefOf("", q2.ID, "out")
	if _, err := w.alice.Revise(tctx(t), ref2, "second EDITED"); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.bob)
	waitState(t, w.bob, q2.ID, stateAnswered)
	eventually(t, "bob shows both edits", func() bool {
		return legacyView(t, w.bob, q.ID).Edited && legacyView(t, w.bob, q2.ID).Edited
	})
	for _, id := range []string{q.ID, q2.ID} {
		if row := inboxRow(t, w.bob, id); !strings.Contains(row.Body, "ORIGINAL") || row.State != stateAnswered {
			t.Fatalf("the admitted request changed: %+v", row)
		}
	}
	if stdin, _ := os.ReadFile(st.log + ".stdin"); !strings.Contains(string(stdin), "second ORIGINAL") || strings.Contains(string(stdin), "EDITED") {
		t.Fatalf("the agent ran on: %q", stdin)
	}
	if st.count() != 2 {
		t.Fatalf("the agent ran %d time(s)", st.count())
	}
	m := legacyView(t, w.bob, q2.ID)
	if m.Text != "second EDITED" || m.Body != "second ORIGINAL" || m.Kind != envelope.KindQuestion {
		t.Fatalf("bob's view: %+v", m)
	}
}

// Controls in a conversation: reactions merge per person and converge
// whatever order they arrive in; a deletion drops this device's cached
// file ciphertext (never a saved file) and is never fetched again; a
// linked device gets it all as history and its own reactions count as
// the same person's.
func TestControlsInConversationConverge(t *testing.T) {
	w, conv, _ := dmFiles(t)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opening"}); err != nil { // bob learns the conversation
		t.Fatal(err)
	}
	eventually(t, "bob holds the conversation", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	path, data := writeFile(t, t.TempDir(), "chart.png", 20000)
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "see the chart", Files: []OutgoingFile{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	var got ConvMessage
	eventually(t, "alice holds the turn and its file", func() bool {
		msgs, _ := w.alice.ConversationMessages(conv)
		for _, m := range msgs {
			if m.LID == turn.LID && m.Dir == "in" {
				got = m
				_, err := os.Stat(w.alice.downloadPath(m.Attachments[0].BlobID))
				return err == nil
			}
		}
		return false
	})
	saved, err := w.alice.Download(tctx(t), got.ID, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := w.alice.RefOf(conv, got.ID, "in")
	if err != nil || ref.ID != turn.LID || ref.Fingerprint != w.bob.Self().Fingerprint() {
		t.Fatalf("ref = %+v %v", ref, err)
	}
	react(t, w.alice, ref, "❤️", false)
	eventually(t, "bob sees alice's heart", func() bool {
		m := convMsgByID(t, w.bob, conv, turn.ID)
		return len(m.Reactions) == 1 && m.Reactions[0].Emoji == "❤️" && m.Reactions[0].By[0].Label == "Person of "+w.alice.Address && !m.Reactions[0].Mine
	})
	// Out of order: bob's "remove" (n=2) reaches alice before his "add"
	// (n=1). The newer counter wins: no reaction from bob.
	bobFP := w.bob.Self().Fingerprint()
	for _, c := range []envelope.Reaction{{Emoji: "❤️", Op: "remove", N: 2}, {Emoji: "❤️", Op: "add", N: 1}} {
		body, _ := json.Marshal(c)
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
			Kind: envelope.KindMessage, Sub: envelope.SubReaction, Body: string(body), Conv: conv, LID: protocol.NewID(), Ref: &envelope.Ref{ID: turn.LID, Fingerprint: bobFP}}
		if _, err := w.alice.store.addConvInbox(in, bobFP, "", false, nil); err != nil {
			t.Fatal(err)
		}
	}
	m := convMsgByID(t, w.alice, conv, got.ID)
	if len(m.Reactions) != 1 || len(m.Reactions[0].By) != 1 || !m.Reactions[0].Mine {
		t.Fatalf("after out-of-order controls: %+v", m.Controls)
	}
	// Only bob's person edits bob's turn.
	if _, err := w.alice.Revise(tctx(t), ref, "not mine"); err == nil {
		t.Fatal("alice edited bob's turn")
	}
	bobRef, _ := w.bob.RefOf(conv, turn.ID, "out")
	if _, err := w.bob.Revise(tctx(t), bobRef, "see the CORRECTED chart"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice sees the edit", func() bool {
		m := convMsgByID(t, w.alice, conv, got.ID)
		return m.Edited && m.Text == "see the CORRECTED chart" && m.Body == "see the chart"
	})
	// A linked device gets the turn, the reaction and the edit as history,
	// and its own reaction merges into its person's.
	phone := linked(t, w.alice)
	var pm ConvMessage
	eventually(t, "the phone's history shows the controls", func() bool {
		msgs, _ := phone.ConversationMessages(conv)
		for _, m := range msgs {
			if m.LID == turn.LID {
				pm = m
				return m.Edited && len(m.Reactions) == 1 && m.Reactions[0].Mine
			}
		}
		return false
	})
	pref, err := phone.RefOf(conv, pm.ID, "in")
	if err != nil {
		t.Fatal(err)
	}
	react(t, phone, pref, "👍", false)
	eventually(t, "the laptop counts the phone's reaction as its person's", func() bool {
		m := convMsgByID(t, w.alice, conv, got.ID)
		return len(m.Reactions) == 2 && m.Reactions[1].Emoji == "👍" && m.Reactions[1].Mine && len(m.Reactions[1].By) == 1
	})
	// Bob deletes: alice's cached ciphertext goes, the saved file and the
	// manifest row stay, nothing is fetched again, and the phone converges.
	if _, err := w.bob.Retract(tctx(t), bobRef, "wrong chart"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice's cache dropped", func() bool {
		_, err := os.Stat(w.alice.downloadPath(got.Attachments[0].BlobID))
		return errors.Is(err, os.ErrNotExist) && convMsgByID(t, w.alice, conv, got.ID).Deleted
	})
	if b, _ := os.ReadFile(saved[0]); string(b) != string(data) {
		t.Fatal("a saved file was touched")
	}
	if n := inboxCount(t, w.alice, `id = ?`, got.ID); n != 1 {
		t.Fatal("the deleted turn's row is gone")
	}
	var atts int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM attachments WHERE message_id = ?`, got.ID).Scan(&atts)
	if atts != 1 {
		t.Fatal("the manifest row is gone")
	}
	w.alice.convWork.due(convFetch)
	w.alice.kickNow()
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(w.alice.downloadPath(got.Attachments[0].BlobID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a deleted turn's file was fetched again")
	}
	eventually(t, "the phone sees the deletion", func() bool {
		msgs, _ := phone.ConversationMessages(conv)
		for _, m := range msgs {
			if m.LID == turn.LID {
				return m.Deleted
			}
		}
		return false
	})
	if _, _, err := w.alice.OpenFileFrom(tctx(t), "in", got.ID, 0); err == nil {
		t.Fatal("a deleted turn's file opened") // its ciphertext is gone; the Hub still holds it, but nothing here refetches for a tombstone
	}
}

// A peer whose program cannot read controls is refused honestly; nothing
// older is sent instead.
func TestControlsRefusedWithoutCapability(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	hello, err := w.bob.Send(tctx(t), w.alice.Address, "from an older program", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to hold it", func() bool { return inboxCount(t, w.alice, `id = ?`, hello.ID) == 1 })
	ref, _ := w.alice.RefOf("", hello.ID, "in")
	_, err = w.alice.React(tctx(t), ref, "👍", false)
	if !errors.Is(err, ErrNoControls) {
		t.Fatalf("reacting to a peer without controls: %v", err)
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d message(s) sent instead", n)
	}
	if _, err := w.alice.React(tctx(t), ref, "not-an-emoji", false); err == nil {
		t.Fatal("a word was accepted as a reaction")
	}
}

// controlRows counts the rows a control left in a's store for lid.
func controlRows(t *testing.T, a *Agent, lid string) int {
	t.Helper()
	var n int
	if err := a.store.db.QueryRow(`SELECT (SELECT count(*) FROM outbox WHERE lid = ?) + (SELECT count(*) FROM inbox WHERE lid = ?)`, lid, lid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// findLID is the message with logical id lid in a's view of conv.
func findLID(t *testing.T, a *Agent, conv, lid string) (ConvMessage, bool) {
	t.Helper()
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.LID == lid {
			return m, true
		}
	}
	return ConvMessage{}, false
}

// A sender that still holds an old roster of the other person sends its
// controls to the devices it knows; the device that receives them forwards
// them to the person's newer devices (the signed fan says which roster the
// sender used), so a linked phone converges without the sender ever
// learning about it.
func TestControlsReachDevicesTheSenderDoesNotKnow(t *testing.T) {
	w, conv, stopBob := dmFiles(t)
	opening, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opening"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds the conversation", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "bob's turn"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice holds bob's turn", func() bool { _, ok := findLID(t, w.alice, conv, turn.LID); return ok })
	stopBob() // bob's daemon is off: he learns nothing about alice's new device
	phone := linked(t, w.alice)
	eventually(t, "the phone has the history", func() bool { _, ok := findLID(t, phone, conv, turn.LID); return ok })
	if p, _, _ := w.bob.store.personByID(func() string {
		me, _, _ := w.alice.store.selfPerson(w.alice.Address)
		return me.info.Person
	}()); len(p.roster.Devices) != 1 {
		t.Fatalf("bob already knows %d devices of alice; the test needs his roster stale", len(p.roster.Devices))
	}
	aliceRef, err := w.bob.RefOf(conv, func() string { m, _ := findLID(t, w.bob, conv, opening.LID); return m.ID }(), "in")
	if err != nil {
		t.Fatal(err)
	}
	ownRef, _ := w.bob.RefOf(conv, turn.ID, "out")
	reaction, err := w.bob.React(tctx(t), aliceRef, "🎉", false)
	if err != nil || len(reaction.Skipped) != 0 {
		t.Fatalf("react: %+v %v", reaction, err)
	}
	if _, err := w.bob.Revise(tctx(t), ownRef, "bob's turn, edited"); err != nil {
		t.Fatal(err)
	}
	// Bob sent one copy each (to alice's laptop): the phone was not his to know.
	var copies int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv = ? AND ref_id IS NOT NULL`, conv).Scan(&copies)
	if copies != 2 {
		t.Fatalf("bob sent %d control copies; expected one per control to the one device he knows", copies)
	}
	eventually(t, "the phone shows the reaction and the edit, forwarded by the laptop", func() bool {
		o, ok1 := findLID(t, phone, conv, opening.LID)
		b, ok2 := findLID(t, phone, conv, turn.LID)
		return ok1 && ok2 && len(o.Reactions) == 1 && o.Reactions[0].Emoji == "🎉" && !o.Reactions[0].Mine && b.Edited && b.Text == "bob's turn, edited"
	})
	if _, err := w.bob.Retract(tctx(t), ownRef, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the phone shows the deletion", func() bool { b, ok := findLID(t, phone, conv, turn.LID); return ok && b.Deleted && b.Body == "" })
	// Raw rows, not the view: the phone keeps neither the deleted text nor the edit's text.
	var raw string
	phone.store.db.QueryRow(`SELECT coalesce(group_concat(body, '|'), '') FROM inbox WHERE conv = ? AND (lid = ? OR (ref_id = ? AND sub = 'revision'))`, conv, turn.LID, turn.LID).Scan(&raw)
	if strings.Contains(raw, "bob's turn") {
		t.Fatalf("the phone still stores deleted text: %q", raw)
	}
	// Forwarded as history: the phone's copies came from the laptop, and
	// carry no alert or job.
	var viaLaptop int
	phone.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv = ? AND ref_id IS NOT NULL AND via = ?`, conv, w.alice.Address).Scan(&viaLaptop)
	if viaLaptop != 3 {
		t.Fatalf("%d control(s) reached the phone through the laptop; expected 3", viaLaptop)
	}
	if review, _ := phone.Review(); len(review) != 0 {
		t.Fatalf("a forwarded control waits for a decision: %+v", review)
	}
}

// A retraction or edit from anyone but the sender's person is refused
// before anything is stored or removed: a member's forged retraction of a
// turn one of my devices sent leaves my cached file, its text and its view
// untouched; the same forgery in a device thread is refused too. The real
// sender's retraction then applies, and deletes the ordinary text here.
func TestForeignControlNeverStoresOrDropsAnything(t *testing.T) {
	w, conv, _ := dmFiles(t)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opening"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds the conversation", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	phone := linked(t, w.alice)
	eventually(t, "the phone has the conversation", func() bool { return len(convBodies(t, phone, conv)) == 1 })
	path, _ := writeFile(t, t.TempDir(), "from-phone.bin", 12000)
	turn, err := phone.SendConv(tctx(t), conv, ConvOutgoing{Body: "typed on the phone", Files: []OutgoingFile{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	var onLaptop ConvMessage
	eventually(t, "the laptop keeps the phone's file", func() bool {
		m, ok := findLID(t, w.alice, conv, turn.LID)
		if !ok || len(m.Attachments) != 1 {
			return false
		}
		onLaptop = m
		_, err := os.Stat(w.alice.downloadPath(m.Attachments[0].BlobID))
		return err == nil
	})
	eventually(t, "bob holds it", func() bool { _, ok := findLID(t, w.bob, conv, turn.LID); return ok })
	// Bob forges a retraction of the phone's turn (a member, not the author).
	phoneFP := phone.Self().Fingerprint()
	forged := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: envelope.SubRetraction, Body: `{"reason":"forged"}`, Conv: conv, LID: protocol.NewID(),
		Ref: &envelope.Ref{ID: turn.LID, Fingerprint: phoneFP}}
	key, _ := w.bob.sendKey(tctx(t), w.alice.Address)
	rcpt, _ := key.Recipient()
	env, err := envelope.Seal(forged, w.bob.id.Sign, rcpt)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.addConvOutbox([]outCopy{{env: env, in: forged, state: stateQueued}}, forged, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.deliver(tctx(t), env, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the laptop refuses the forgery", func() bool {
		var n int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id = ? AND reason = ?`, forged.ID, reasonInvalid).Scan(&n)
		return n == 1
	})
	if controlRows(t, w.alice, forged.LID) != 0 {
		t.Fatal("the forgery was stored")
	}
	if _, err := os.Stat(w.alice.downloadPath(onLaptop.Attachments[0].BlobID)); err != nil {
		t.Fatal("the forgery removed the cached file")
	}
	if m, _ := findLID(t, w.alice, conv, turn.LID); m.Deleted || m.Body != "typed on the phone" {
		t.Fatalf("the forgery changed the view: %+v", m)
	}
	// In a device thread too: bob cannot delete alice's message under her key.
	mine, err := w.alice.Send(tctx(t), w.bob.Address, "device-thread text", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds it", func() bool { return inboxCount(t, w.bob, `id = ?`, mine.ID) == 1 })
	forgedV1 := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: envelope.SubRetraction, Body: `{}`, Ref: &envelope.Ref{ID: mine.ID, Fingerprint: w.alice.Self().Fingerprint()}}
	env, _ = envelope.Seal(forgedV1, w.bob.id.Sign, rcpt)
	if err := w.bob.store.addControlOutbox(env, forgedV1); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.deliver(tctx(t), env, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the laptop refuses the device-thread forgery", func() bool {
		var n int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id = ? AND reason = ?`, forgedV1.ID, reasonInvalid).Scan(&n)
		return n == 1
	})
	if m := legacyView(t, w.alice, mine.ID); m.Deleted || m.Body != "device-thread text" {
		t.Fatalf("the device-thread forgery applied: %+v", m)
	}
	// The phone (the author's person) deletes its own turn: the laptop's
	// cache goes, the ordinary text is gone from its rows, the manifest stays.
	pref, _ := phone.RefOf(conv, turn.ID, "out")
	if _, err := phone.Retract(tctx(t), pref, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the laptop applies the author's deletion", func() bool {
		m, ok := findLID(t, w.alice, conv, turn.LID)
		_, err := os.Stat(w.alice.downloadPath(onLaptop.Attachments[0].BlobID))
		return ok && m.Deleted && m.Body == "" && errors.Is(err, os.ErrNotExist)
	})
	var body string
	w.alice.store.db.QueryRow(`SELECT body FROM inbox WHERE id = ?`, onLaptop.ID).Scan(&body)
	if body != "" {
		t.Fatalf("the deleted text is still stored: %q", body)
	}
	var atts int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM attachments WHERE message_id = ?`, onLaptop.ID).Scan(&atts)
	if atts != 1 {
		t.Fatal("the manifest row is gone")
	}
	eventually(t, "the phone's own rows are redacted too", func() bool {
		var kept int
		phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid = ? AND body != ''`, turn.LID).Scan(&kept)
		return kept == 0
	})
}

// Deleting an ordinary message removes its text and the text of its edits
// from both sides' stores and views; deleting a question keeps its admitted
// text (what an agent ran or would run), shown as deleted.
func TestRetractionRedactsOrdinaryTextAndKeepsRequests(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	msg, err := w.alice.Send(tctx(t), w.bob.Address, "secret plan", "")
	if err != nil {
		t.Fatal(err)
	}
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "ORIGINAL question", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds both", func() bool { return inboxCount(t, w.bob, `id IN (?, ?)`, msg.ID, q.ID) == 2 })
	mref, _ := w.alice.RefOf("", msg.ID, "out")
	qref, _ := w.alice.RefOf("", q.ID, "out")
	react(t, w.bob, func() ControlRef { r, _ := w.bob.RefOf("", msg.ID, "in"); return r }(), "👀", false) // bob's capabilities are up
	if _, err := w.alice.Revise(tctx(t), mref, "secret plan v2"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob sees the edit", func() bool { return legacyView(t, w.bob, msg.ID).Text == "secret plan v2" })
	for _, ref := range []ControlRef{mref, qref} {
		if _, err := w.alice.Retract(tctx(t), ref, ""); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "bob sees both deletions", func() bool {
		return legacyView(t, w.bob, msg.ID).Deleted && legacyView(t, w.bob, q.ID).Deleted
	})
	var bodies []string
	rows, _ := w.bob.store.db.Query(`SELECT body FROM inbox WHERE id = ? OR (ref_id = ? AND sub = 'revision') OR id = ? ORDER BY id`, msg.ID, msg.ID, q.ID)
	for rows.Next() {
		var b string
		rows.Scan(&b)
		bodies = append(bodies, b)
	}
	rows.Close()
	kept := strings.Join(bodies, "|")
	if strings.Contains(kept, "secret plan") || !strings.Contains(kept, "ORIGINAL question") {
		t.Fatalf("bob's stored bodies after deletion: %q", kept)
	}
	if m := legacyView(t, w.bob, msg.ID); m.Body != "" || m.Text != "" || !m.Deleted {
		t.Fatalf("deleted message in the view: %+v", m)
	}
	if m := legacyView(t, w.bob, q.ID); m.Body != "ORIGINAL question" || !m.Deleted {
		t.Fatalf("deleted question in the view: %+v", m)
	}
	var aliceBodies string
	w.alice.store.db.QueryRow(`SELECT group_concat(body, '|') FROM outbox WHERE id = ? OR (ref_id = ? AND sub = 'revision')`, msg.ID, msg.ID).Scan(&aliceBodies)
	if strings.Contains(aliceBodies, "secret plan") {
		t.Fatalf("alice kept the deleted text: %q", aliceBodies)
	}
	if body := inboxRow(t, w.bob, q.ID).Body; body != "ORIGINAL question" {
		t.Fatalf("the admitted question changed: %q", body)
	}
}

// A file shared by two sent messages keeps its kept copy until the last
// message that is not retracted is retracted too; a retraction in one
// conversation never touches an equal logical id elsewhere.
func TestRetractionKeptAndCacheScope(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	path, data := writeFile(t, t.TempDir(), "shared.bin", 4000)
	first, err := w.alice.Send(tctx(t), w.bob.Address, "first", "", path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.alice.Send(tctx(t), w.bob.Address, "second", "", path)
	if err != nil {
		t.Fatal(err)
	}
	kept := w.alice.keptPath(sha(data))
	eventually(t, "bob holds both", func() bool { return inboxCount(t, w.bob, `id IN (?, ?)`, first.ID, second.ID) == 2 })
	r1, _ := w.alice.RefOf("", first.ID, "out")
	react(t, w.bob, func() ControlRef { r, _ := w.bob.RefOf("", first.ID, "in"); return r }(), "👍", false) // wait for capabilities
	if _, err := w.alice.Retract(tctx(t), r1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatal("the kept copy went while another sent message still shares it")
	}
	r2, _ := w.alice.RefOf("", second.ID, "out")
	if _, err := w.alice.Retract(tctx(t), r2, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the kept copy stayed after its last message was retracted")
	}

	// Exact scope: bob's turn in conversation A, and a row with the same
	// logical id under another key in another conversation B on alice's
	// device (one key and logical id never name two messages here: the
	// store admits one, so the second differs by key).
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opening"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds the conversation", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	fpath, _ := writeFile(t, t.TempDir(), "in-a.bin", 3000)
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "in A", Files: []OutgoingFile{{Path: fpath}}})
	if err != nil {
		t.Fatal(err)
	}
	var inA ConvMessage
	eventually(t, "alice keeps the file of A", func() bool {
		m, ok := findLID(t, w.alice, conv, turn.LID)
		if !ok || len(m.Attachments) != 1 {
			return false
		}
		inA = m
		_, err := os.Stat(w.alice.downloadPath(m.Attachments[0].BlobID))
		return err == nil
	})
	otherConv := strings.Repeat("b", 64)
	otherKey := strings.Repeat("e", 64)
	other := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Body: "in B, same lid", Conv: otherConv, LID: turn.LID,
		Attachments: []envelope.Attachment{{Blob: envelope.Blob{ID: protocol.NewID(), Size: 100, SHA256: strings.Repeat("c", 64)}, Name: "b.bin", Size: 50, SHA256: strings.Repeat("d", 64)}}}
	if res, err := w.alice.store.addConvInbox(other, otherKey, "", false, nil); err != nil || res != admitted {
		t.Fatalf("the other conversation's row: %s %v", res, err)
	}
	otherCache := w.alice.downloadPath(other.Attachments[0].Blob.ID)
	os.MkdirAll(filepath.Dir(otherCache), 0o700)
	os.WriteFile(otherCache, []byte("cached elsewhere"), 0o600)
	bref, _ := w.bob.RefOf(conv, turn.ID, "out")
	if _, err := w.bob.Retract(tctx(t), bref, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice drops A's cache", func() bool {
		_, err := os.Stat(w.alice.downloadPath(inA.Attachments[0].BlobID))
		return errors.Is(err, os.ErrNotExist)
	})
	if _, err := os.Stat(otherCache); err != nil {
		t.Fatal("a retraction in conversation A removed conversation B's cache")
	}
	var otherBody string
	w.alice.store.db.QueryRow(`SELECT body FROM inbox WHERE id = ?`, other.ID).Scan(&otherBody)
	if otherBody != "in B, same lid" {
		t.Fatalf("conversation B's text changed: %q", otherBody)
	}
}

// A control carried as history is held to the same authority as a direct
// one: a device of this person forwarding a retraction of another person's
// turn under this person's key stores nothing on the phone, blocks no file,
// and deletes nothing.
func TestHistoryCarriedForeignRetractionRefused(t *testing.T) {
	w, conv, _ := dmFiles(t)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opening"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds the conversation", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	path, _ := writeFile(t, t.TempDir(), "bobs.bin", 2500)
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "bob's file turn", Files: []OutgoingFile{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice holds it", func() bool { _, ok := findLID(t, w.alice, conv, turn.LID); return ok })
	phone := linked(t, w.alice)
	eventually(t, "the phone has it", func() bool { _, ok := findLID(t, phone, conv, turn.LID); return ok })
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	var phoneDev identity.Public
	for _, d := range me.roster.Devices {
		if d.Address == phone.Address {
			phoneDev = d
		}
	}
	_, raw, _, _ := w.alice.store.conversation(conv)
	// The laptop "forwards" a retraction of bob's turn authored by itself.
	forged := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
		Sub: envelope.SubRetraction, Body: `{"reason":"forged via history"}`, Conv: conv, LID: protocol.NewID(),
		Ref: &envelope.Ref{ID: turn.LID, Fingerprint: w.bob.Self().Fingerprint()}}
	c, err := w.alice.historyCopy(phoneDev, conv, raw, itemOf(forged, w.alice.Self().Fingerprint(), time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := w.alice.store.db.Begin()
	if err := insertCopies(tx, []outCopy{c}); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if _, err := w.alice.deliver(tctx(t), c.env, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the phone holds the carrier aside", func() bool {
		var n int
		phone.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id = ? AND reason = ?`, c.env.ID, reasonInvalid).Scan(&n)
		return n == 1
	})
	if controlRows(t, phone, forged.LID) != 0 {
		t.Fatal("the forged history control was stored")
	}
	pm, _ := findLID(t, phone, conv, turn.LID)
	if pm.Deleted || pm.Body != "bob's file turn" || len(pm.Attachments) != 1 || pm.Attachments[0].Availability != "requestable" {
		t.Fatalf("the forgery changed the phone's view: %+v", pm)
	}
	// The file can still be asked for: nothing blocks it.
	if err := phone.RequestFile(tctx(t), pm.ID, 0); err != nil {
		t.Fatalf("a legitimate file request refused: %v", err)
	}
}

// Ids are the sender's choice: a received message can carry the id (or
// logical id) of one this device sent. The sender's retraction of its own
// message deletes only that one; this device's own text is untouched.
func TestRetractionSameIDOppositeDirection(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	mine, err := w.alice.Send(tctx(t), w.bob.Address, "mine, sent", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds it", func() bool { return inboxCount(t, w.bob, `id = ?`, mine.ID) == 1 })
	// A message from bob stored here under the same id as alice's own.
	bobFP := w.bob.Self().Fingerprint()
	theirs := envelope.Inner{V: 1, ID: mine.ID, From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "theirs, received"}
	if err := w.alice.store.addInbox(theirs, bobFP); err != nil {
		t.Fatal(err)
	}
	ret := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: envelope.SubRetraction, Body: `{}`, Ref: &envelope.Ref{ID: mine.ID, Fingerprint: bobFP}}
	key, _ := w.bob.sendKey(tctx(t), w.alice.Address)
	rcpt, _ := key.Recipient()
	env, _ := envelope.Seal(ret, w.bob.id.Sign, rcpt)
	if err := w.bob.store.addControlOutbox(env, ret); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.deliver(tctx(t), env, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice applies bob's retraction to bob's message", func() bool {
		var body string
		w.alice.store.db.QueryRow(`SELECT body FROM inbox WHERE id = ? AND sender = ?`, mine.ID, w.bob.Address).Scan(&body)
		return body == ""
	})
	var own string
	w.alice.store.db.QueryRow(`SELECT body FROM outbox WHERE id = ?`, mine.ID).Scan(&own)
	if own != "mine, sent" {
		t.Fatalf("alice's own sent text changed: %q", own)
	}
	if m := legacyView(t, w.alice, mine.ID); m.Dir != "out" || m.Deleted || m.Body != "mine, sent" {
		t.Fatalf("alice's own message in her view: %+v", m)
	}
	// The same in a conversation: bob's turn and an own row with its logical id.
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opening"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds the conversation", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "bob's turn"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice holds bob's turn", func() bool { _, ok := findLID(t, w.alice, conv, turn.LID); return ok })
	if _, err := w.alice.store.db.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, created_at, conv, lid, kind, created_ms)
		VALUES(?, ?, 'alice text under the same lid', '{}', 'custody', unixepoch(), ?, ?, 'message', ?)`,
		protocol.NewID(), w.bob.Address, conv, turn.LID, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	bref, _ := w.bob.RefOf(conv, turn.ID, "out")
	if _, err := w.bob.Retract(tctx(t), bref, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice applies it to bob's turn", func() bool { m, ok := findLID(t, w.alice, conv, turn.LID); return ok && m.Deleted })
	var ownConv string
	w.alice.store.db.QueryRow(`SELECT body FROM outbox WHERE conv = ? AND lid = ?`, conv, turn.LID).Scan(&ownConv)
	if ownConv != "alice text under the same lid" {
		t.Fatalf("alice's own conversation row changed: %q", ownConv)
	}
}

// Once a retraction is pinned, no later arrival of the same message (its
// original, a history copy, a direct copy replacing history) or of an edit
// of it stores ordinary text here, whatever the order; a question keeps
// its admitted text.
func TestRetractionSurvivesReorderedArrivals(t *testing.T) {
	w, conv, _ := dmFiles(t)
	bobFP := w.bob.Self().Fingerprint()
	lid := protocol.NewID()
	ret := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: envelope.SubRetraction, Body: `{}`, Conv: conv, LID: protocol.NewID(), Ref: &envelope.Ref{ID: lid, Fingerprint: bobFP}}
	if res, err := w.alice.store.addConvInbox(ret, bobFP, "", false, nil); err != nil || res != admitted {
		t.Fatalf("retraction first: %s %v", res, err)
	}
	// Then the original: a history copy, then the direct copy replacing it.
	orig := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Body: "late plaintext", Conv: conv, LID: lid}
	if res, err := w.alice.store.addHistoryInbox(orig, time.Now().UnixMilli(), bobFP, "admin/phone", protocol.NewID(), false, nil); err != nil || res != admitted {
		t.Fatalf("history copy: %s %v", res, err)
	}
	if res, err := w.alice.store.addConvInbox(orig, bobFP, "", false, nil); err != nil || res != admitted {
		t.Fatalf("direct copy: %s %v", res, err)
	}
	rev := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: envelope.SubRevision, Body: `{"rev":1,"text":"late edit"}`, Conv: conv, LID: protocol.NewID(), Ref: &envelope.Ref{ID: lid, Fingerprint: bobFP}}
	if res, err := w.alice.store.addConvInbox(rev, bobFP, "", false, nil); err != nil || res != admitted {
		t.Fatalf("late revision: %s %v", res, err)
	}
	var bodies string
	if err := w.alice.store.db.QueryRow(`SELECT coalesce(group_concat(body, '|'), '') FROM inbox WHERE conv = ? AND (lid = ? OR ref_id = ?)`, conv, lid, lid).Scan(&bodies); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bodies, "late") {
		t.Fatalf("deleted text stored after the retraction: %q", bodies)
	}
	var rows int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv = ? AND lid = ?`, conv, lid).Scan(&rows)
	if rows != 1 {
		t.Fatalf("%d rows for the turn (want the direct copy alone)", rows)
	}
	if m, ok := findLID(t, w.alice, conv, lid); !ok || !m.Deleted || m.Body != "" {
		t.Fatalf("view: %+v %v", m, ok)
	}
	// A question keeps its admitted text through the same order.
	qlid := protocol.NewID()
	qret := ret
	qret.ID, qret.LID, qret.Ref = protocol.NewID(), protocol.NewID(), &envelope.Ref{ID: qlid, Fingerprint: bobFP}
	if _, err := w.alice.store.addConvInbox(qret, bobFP, "", false, nil); err != nil {
		t.Fatal(err)
	}
	q := orig
	q.ID, q.LID, q.Kind, q.Body = protocol.NewID(), qlid, envelope.KindQuestion, "ORIGINAL question"
	if _, err := w.alice.store.addConvInbox(q, bobFP, stateConvHeld, false, nil); err != nil {
		t.Fatal(err)
	}
	var qbody string
	if err := w.alice.store.db.QueryRow(`SELECT body FROM inbox WHERE id = ?`, q.ID).Scan(&qbody); err != nil || qbody != "ORIGINAL question" {
		t.Fatalf("a question's admitted text: %q %v", qbody, err)
	}
	// Device thread: an edit arriving after the retraction keeps no text.
	hello, err := w.bob.Send(tctx(t), w.alice.Address, "device text", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice holds it", func() bool { return inboxCount(t, w.alice, `id = ?`, hello.ID) == 1 })
	dret := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: envelope.SubRetraction, Body: `{}`, Ref: &envelope.Ref{ID: hello.ID, Fingerprint: bobFP}}
	if err := w.alice.store.addControlInbox(dret, bobFP, false); err != nil {
		t.Fatal(err)
	}
	drev := dret
	drev.ID, drev.Sub, drev.Body = protocol.NewID(), envelope.SubRevision, `{"rev":1,"text":"device edit"}`
	if err := w.alice.store.addControlInbox(drev, bobFP, false); err != nil {
		t.Fatal(err)
	}
	var dbody string
	if err := w.alice.store.db.QueryRow(`SELECT body FROM inbox WHERE id = ?`, drev.ID).Scan(&dbody); err != nil || dbody != "" {
		t.Fatalf("a device-thread edit after deletion kept text: %q %v", dbody, err)
	}
}

// A device linked after a deletion gets the conversation's history
// without the deleted text, and shows the deletion.
func TestLateLinkedDeviceGetsNoDeletedText(t *testing.T) {
	w, conv, _ := dmFiles(t)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opening"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds the conversation", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "to be deleted before the phone exists"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice holds it", func() bool { _, ok := findLID(t, w.alice, conv, turn.LID); return ok })
	bref, _ := w.bob.RefOf(conv, turn.ID, "out")
	if _, err := w.bob.Revise(tctx(t), bref, "edited before deletion"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.Retract(tctx(t), bref, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice sees the deletion", func() bool { m, ok := findLID(t, w.alice, conv, turn.LID); return ok && m.Deleted })
	phone := linked(t, w.alice)
	eventually(t, "the late phone has the history with the deletion", func() bool {
		m, ok := findLID(t, phone, conv, turn.LID)
		return ok && m.Deleted && m.Body == ""
	})
	var raw string
	if err := phone.store.db.QueryRow(`SELECT coalesce(group_concat(body, '|'), '') FROM inbox WHERE conv = ? AND (lid = ? OR ref_id = ?)`, conv, turn.LID, turn.LID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "deleted") || strings.Contains(raw, "edited before") {
		t.Fatalf("the late phone stores deleted text: %q", raw)
	}
}

// A reaction composed here is one emoji: a currency or math sign, another
// script's character or a row of emoji is refused before anything is sent,
// while sequences that render as one emoji (skin tone, flag, family) go.
func TestReactionIsOneEmoji(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "react to me"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to hold the turn", func() bool { return inboxCount(t, w.alice, `id = ?`, turn.ID) == 1 })
	ref, err := w.alice.RefOf(conv, turn.ID, "in")
	if err != nil {
		t.Fatal(err)
	}
	react(t, w.alice, ref, "👍🏽", false) // also waits for bob's capabilities
	for _, bad := range []string{"$", "+", "€", "𠀀", strings.Repeat("👍", 12), "👍👍", "🇱🇻🇺🇸"} {
		if _, err := w.alice.React(tctx(t), ref, bad, false); err == nil {
			t.Errorf("%q accepted as a reaction", bad)
		}
	}
	for _, good := range []string{"🇱🇻", "👨‍👩‍👧‍👦", "1️⃣", "❤️", "✓"} {
		if _, err := w.alice.React(tctx(t), ref, good, false); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(DISTINCT lid) FROM outbox WHERE sub = ?`, envelope.SubReaction).Scan(&n)
	if n != 6 {
		t.Fatalf("%d reactions sent, want 6", n)
	}
}

// Taking off a reaction that is not there says so instead of reporting it
// removed, and sends nothing.
func TestRemovingAbsentReactionRefused(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "react to me"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to hold the turn", func() bool { return inboxCount(t, w.alice, `id = ?`, turn.ID) == 1 })
	ref, err := w.alice.RefOf(conv, turn.ID, "in")
	if err != nil {
		t.Fatal(err)
	}
	react(t, w.alice, ref, "👍", false) // also waits for bob's capabilities
	bobRef, err := w.bob.RefOf(conv, turn.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	react(t, w.bob, bobRef, "👀", false) // bob's own reaction is not alice's to take off
	eventually(t, "alice to show bob's reaction", func() bool { return len(convMsgByID(t, w.alice, conv, turn.ID).Reactions) == 2 })
	sent := func() int {
		var n int
		w.alice.store.db.QueryRow(`SELECT count(DISTINCT lid) FROM outbox WHERE sub = ?`, envelope.SubReaction).Scan(&n)
		return n
	}
	before := sent()
	for _, emoji := range []string{"🎉", "👀"} {
		if _, err := w.alice.React(tctx(t), ref, emoji, true); err == nil || !strings.Contains(err.Error(), "no "+emoji+" reaction of yours") {
			t.Errorf("removing %s alice never added: %v", emoji, err)
		}
	}
	if sent() != before {
		t.Fatal("a removal of nothing was sent")
	}
	if _, err := w.alice.React(tctx(t), ref, "👍", true); err != nil {
		t.Fatalf("removing her own reaction: %v", err)
	}
	if _, err := w.alice.React(tctx(t), ref, "👍", true); err == nil {
		t.Fatal("the same reaction removed twice")
	}
}

// A reaction of one's own added under the older rule (any ValidEmoji, as an
// older version or another device still sends) shows as one's own, so it
// can be taken off, though such a reaction is no longer composed here.
func TestOwnOlderRuleReactionRemovable(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "react to me"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to hold the turn", func() bool { return inboxCount(t, w.alice, `id = ?`, turn.ID) == 1 })
	ref, err := w.alice.RefOf(conv, turn.ID, "in")
	if err != nil {
		t.Fatal(err)
	}
	react(t, w.alice, ref, "👍", false) // also waits for bob's capabilities
	older := []string{"$", "👍👍"}
	for _, emoji := range older { // as the older rule let them be sent
		n, err := w.alice.store.nextCounter(ref, envelope.SubReaction, emoji)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(envelope.Reaction{Emoji: emoji, Op: "add", N: n})
		if _, err := w.alice.sendControl(tctx(t), ref, envelope.SubReaction, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	mine := func(emoji string) bool {
		return slices.ContainsFunc(convMsgByID(t, w.alice, conv, turn.ID).Reactions, func(v ReactionView) bool { return v.Emoji == emoji && v.Mine })
	}
	for _, emoji := range older {
		if !mine(emoji) {
			t.Fatalf("%q is not shown as alice's own", emoji)
		}
		if _, err := w.alice.React(tctx(t), ref, emoji, false); err == nil {
			t.Errorf("%q added again under the one-emoji rule", emoji)
		}
		if _, err := w.alice.React(tctx(t), ref, emoji, true); err != nil {
			t.Errorf("removing her own %q: %v", emoji, err)
		} else if mine(emoji) {
			t.Errorf("%q still shown after its removal", emoji)
		}
	}
}

// A deleted message takes no more controls: its author can neither edit
// nor delete it again, and no one can react to it. Each is refused before
// anything is stored or sent, in a conversation and in a device thread.
func TestControlsRefusedOnDeletedMessage(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	turn, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "wrong chat"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold the turn", func() bool { return inboxCount(t, w.bob, `id = ?`, turn.ID) == 1 })
	ref, err := w.alice.RefOf(conv, turn.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the deletion sent", func() bool {
		_, err := w.alice.Retract(tctx(t), ref, "")
		return err == nil
	})
	eventually(t, "bob to show the deletion", func() bool { return convMsgByID(t, w.bob, conv, turn.ID).Deleted })
	bobRef, err := w.bob.RefOf(conv, turn.ID, "in")
	if err != nil {
		t.Fatal(err)
	}
	hello, err := w.bob.Send(tctx(t), w.alice.Address, "device hello", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to hold the device message", func() bool { return inboxCount(t, w.alice, `id = ?`, hello.ID) == 1 })
	threadRef, err := w.bob.RefOf("", hello.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the device message deleted", func() bool {
		_, err := w.bob.Retract(tctx(t), threadRef, "")
		return err == nil
	})
	controls := func(a *Agent) int {
		var n int
		a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE ref_id IS NOT NULL`).Scan(&n)
		return n
	}
	before := map[*Agent]int{w.alice: controls(w.alice), w.bob: controls(w.bob)}
	for name, try := range map[string]func() (ControlSent, error){
		"edit":                func() (ControlSent, error) { return w.alice.Revise(tctx(t), ref, "resurrected") },
		"delete again":        func() (ControlSent, error) { return w.alice.Retract(tctx(t), ref, "") },
		"own reaction":        func() (ControlSent, error) { return w.alice.React(tctx(t), ref, "👍", false) },
		"peer reaction":       func() (ControlSent, error) { return w.bob.React(tctx(t), bobRef, "👍", false) },
		"thread edit":         func() (ControlSent, error) { return w.bob.Revise(tctx(t), threadRef, "resurrected") },
		"thread delete again": func() (ControlSent, error) { return w.bob.Retract(tctx(t), threadRef, "") },
	} {
		if _, err := try(); err == nil || !strings.Contains(err.Error(), "deleted") {
			t.Errorf("%s on a deleted message: %v", name, err)
		}
	}
	for a, n := range before {
		if got := controls(a); got != n {
			t.Errorf("%s stored %d more control(s) for a deleted message", a.Address, got-n)
		}
	}
	if m := convMsgByID(t, w.alice, conv, turn.ID); m.Edited || m.Text != "" || len(m.Reactions) != 0 {
		t.Fatalf("the deleted turn at alice: %+v", m.Controls)
	}
}

// Two devices of one person edit, or add and take off a reaction, at once
// with the same counter. Each device holds its own copy of each control,
// under an id of that copy, so the tie is broken on the control's logical
// id, the one value every copy shares: every device shows the same text
// and the same reactions, whichever copy ids it holds.
func TestConcurrentControlsResolveAlikeOnEveryDevice(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the laptop to hold the turn", func() bool { return inboxCount(t, w.alice, `id = ?`, turn.ID) == 1 })
	phone := linked(t, w.alice)
	shown := func(a *Agent) (ConvMessage, bool) {
		msgs, _ := a.ConversationMessages(conv)
		for _, m := range msgs {
			if m.LID == turn.LID {
				return m, true
			}
		}
		return ConvMessage{}, false
	}
	eventually(t, "the phone to hold the turn", func() bool { _, ok := shown(phone); return ok })
	bobFP := w.bob.Self().Fingerprint()
	id := func(c string) string { return strings.Repeat(c, 32) }
	// Logical ids: the second control's is the greater. Copy ids: the
	// laptop's copy of the first control has the greater id, the phone's
	// the lesser, so a tie broken on copy ids differs between them.
	type ctl struct{ copyID, lid, sub, body string }
	first := []ctl{{id("f"), id("1"), envelope.SubRevision, `{"rev":1,"text":"from the desk"}`}, {id("3"), id("4"), envelope.SubReaction, `{"emoji":"👍","op":"add","n":1}`}}
	second := []ctl{{id("2"), id("e"), envelope.SubRevision, `{"rev":1,"text":"from the phone"}`}, {id("5"), id("d"), envelope.SubReaction, `{"emoji":"👍","op":"remove","n":1}`}}
	onPhone := map[string]string{id("f"): id("6"), id("3"): id("c"), id("2"): id("9"), id("5"): id("7")}
	for _, a := range []*Agent{w.alice, phone} {
		for _, c := range append(append([]ctl{}, first...), second...) {
			copyID := c.copyID
			if a == phone {
				copyID = onPhone[copyID]
			}
			in := envelope.Inner{V: envelope.Version3, ID: copyID, From: w.bob.Address, To: a.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
				Sub: c.sub, Body: c.body, Conv: conv, LID: c.lid, Ref: &envelope.Ref{ID: turn.LID, Fingerprint: bobFP}}
			if res, err := a.store.addConvInbox(in, bobFP, "", false, nil); err != nil || res != admitted {
				t.Fatalf("%s storing %s: %s %v", a.Address, c.sub, res, err)
			}
		}
	}
	for _, a := range []*Agent{w.alice, phone} {
		m, _ := shown(a)
		if !m.Edited || m.Text != "from the phone" || len(m.Reactions) != 0 {
			t.Errorf("%s shows %q (edited %v), reactions %+v: want the control with the greater logical id", a.Address, m.Text, m.Edited, m.Reactions)
		}
	}
}
