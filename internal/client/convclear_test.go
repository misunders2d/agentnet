package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// withConvClear signs the clr1 capability for a's live sessions at a
// current timestamp, after each session's own publish (which would replace
// it). The Hub announces the change to connected devices as it does any
// capability publish (membersChanged), which makes their waiting copies
// look again.
func withConvClear(t *testing.T, agents ...*Agent) {
	t.Helper()
	caps := withCap(ownCaps, protocol.CapConvClear)
	for _, a := range agents {
		label, name, _ := protocol.SplitAddress(a.Address)
		eventually(t, a.Address+" to publish its own capabilities", func() bool {
			var prof protocol.Profile
			if a.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof) != nil || len(prof.Sessions) == 0 {
				return false
			}
			published := map[string]bool{}
			for _, raw := range prof.Caps {
				if r, err := protocol.ParseCapsRecord(raw); err == nil {
					published[r.Session] = true
				}
			}
			for _, s := range prof.Sessions {
				if !published[s] {
					return false
				}
			}
			return true
		})
		signCapsNow(t, a, caps)
	}
}

func convInfo(t *testing.T, a *Agent, conv string) ConversationInfo {
	t.Helper()
	convs, err := a.Conversations()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range convs {
		if c.ID == conv {
			return c
		}
	}
	t.Fatalf("no conversation %s on %s", conv, a.Address)
	return ConversationInfo{}
}

// sendConv sends one turn and fails the test on error.
func sendConv(t *testing.T, a *Agent, conv string, m ConvOutgoing) ConvSent {
	t.Helper()
	sent, err := a.SendConv(tctx(t), conv, m)
	if err != nil {
		t.Fatal(err)
	}
	return sent
}

// convFan is the fan of a turn between the persons of a and b now.
func convFan(t *testing.T, a, b *Agent) []envelope.Fan {
	t.Helper()
	ap, _, _ := a.Person()
	bp, _, _ := b.Person()
	return []envelope.Fan{{Person: ap.Person, Roster: ap.Roster}, {Person: bp.Person, Roster: bp.Roster}}
}

// queuedClears are the sealed deletion copies a queued for dev.
func queuedClears(t *testing.T, a *Agent, dev string) []envelope.Envelope {
	t.Helper()
	rows, err := a.store.db.Query(`SELECT envelope FROM outbox WHERE sub = 'clear' AND recipient = ? ORDER BY rowid`, dev)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []envelope.Envelope
	for rows.Next() {
		var raw string
		var env envelope.Envelope
		if err := rows.Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &env) != nil {
			t.Fatal("a queued deletion copy is unreadable")
		}
		out = append(out, env)
	}
	return out
}

// dumpClears logs, after a failure, where deletion copies stand on each
// device: queued copies with their state, and held envelopes.
func dumpClears(t *testing.T, agents ...*Agent) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, a := range agents {
			rows, _ := a.store.db.Query(`SELECT id, recipient, state, coalesce(error, ''), coalesce(required_cap, '') FROM outbox WHERE sub = 'clear'`)
			for rows != nil && rows.Next() {
				var id, to, state, why, req string
				rows.Scan(&id, &to, &state, &why, &req)
				t.Logf("%s clear copy %s to %s: %s %q need %s", a.Address, id, to, state, why, req)
			}
			if rows != nil {
				rows.Close()
			}
			rows, _ = a.store.db.Query(`SELECT id, sender, reason FROM quarantine`)
			for rows != nil && rows.Next() {
				var id, from, reason string
				rows.Scan(&id, &from, &reason)
				t.Logf("%s holds %s from %s: %s", a.Address, id, from, reason)
			}
			if rows != nil {
				rows.Close()
			}
		}
	})
}

func erasedHere(t *testing.T, a *Agent, conv, lid string) bool {
	t.Helper()
	var n int
	a.store.db.QueryRow(`SELECT count(*) FROM conv_erased WHERE conv = ? AND lid = ?`, conv, lid).Scan(&n)
	return n > 0
}

// D1/D2: deleting a conversation erases this person's copy on every own
// device (text and this device's file copies), leaves the other person's
// copy as it is, and a later turn reopens it with only that turn.
func TestDeleteConversationOnAllOwnDevices(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	dumpClears(t, w.alice, phone)
	conv := newDM(t, w.bob, w.alice)
	sendConv(t, w.bob, conv, ConvOutgoing{Body: "hello alice"})
	eventually(t, "the DM on both of alice's devices", func() bool {
		return len(convBodies(t, w.alice, conv)) == 1 && len(convBodies(t, phone, conv)) == 1
	})
	path, data := writeFile(t, t.TempDir(), "notes.txt", 100)
	sendConv(t, w.alice, conv, ConvOutgoing{Body: "hi bob", Files: []OutgoingFile{{Path: path}}})
	sendConv(t, w.bob, conv, ConvOutgoing{Body: "secret"})
	eventually(t, "three turns everywhere", func() bool {
		return len(convBodies(t, w.alice, conv)) == 3 && len(convBodies(t, phone, conv)) == 3 && len(convBodies(t, w.bob, conv)) == 3
	})
	sum := sha256.Sum256(data)
	kept := w.alice.keptPath(hex.EncodeToString(sum[:]))
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("the sent file's kept copy: %v", err)
	}
	withConvClear(t, w.alice, phone)

	done, err := w.alice.DeleteConversation(tctx(t), conv)
	if err != nil || done.Erased != 3 || done.Devices != 1 || done.Kept != 0 || done.ThisOnly {
		t.Fatalf("delete: %+v %v", done, err)
	}
	if b := convBodies(t, w.alice, conv); len(b) != 0 {
		t.Fatalf("still shown here: %v", b)
	}
	if n := inboxCount(t, w.alice, `conv = ? AND body <> '' AND coalesce(sub, '') = ''`, conv); n != 0 {
		t.Fatalf("%d received turns keep text", n)
	}
	if n := count(t, w.alice, "outbox WHERE conv = '"+conv+"' AND body <> '' AND coalesce(sub, '') = ''"); n != 0 {
		t.Fatalf("%d sent turns keep text", n)
	}
	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Fatalf("the deleted file's kept copy stays: %v", err)
	}
	if !convInfo(t, w.alice, conv).Deleted {
		t.Fatal("the deleted conversation is still listed")
	}
	if _, err := w.alice.DeleteConversation(tctx(t), conv); !errors.Is(err, ErrNothingToDelete) {
		t.Fatalf("a second deletion: %v", err)
	}
	eventually(t, "the phone to erase it too", func() bool {
		return len(convBodies(t, phone, conv)) == 0 && inboxCount(t, phone, `conv = ? AND body <> '' AND coalesce(sub, '') = ''`, conv) == 0
	})
	if !convInfo(t, phone, conv).Deleted {
		t.Fatal("the phone still lists it")
	}
	if b := strings.Join(convBodies(t, w.bob, conv), "|"); b != "out:hello alice|in:hi bob|out:secret" {
		t.Fatalf("the other person's copy changed: %s", b)
	}
	// Bob's later turn reopens it on both devices, alone.
	sendConv(t, w.bob, conv, ConvOutgoing{Body: "after"})
	eventually(t, "the later turn on both devices", func() bool {
		return strings.Join(convBodies(t, w.alice, conv), "|") == "in:after" && strings.Join(convBodies(t, phone, conv), "|") == "in:after"
	})
	if convInfo(t, w.alice, conv).Deleted || convInfo(t, phone, conv).Deleted {
		t.Fatal("a reopened conversation is still left out")
	}
}

// D2: an erased turn never comes back: not as a retried copy of the same
// turn, not on a device linked after the deletion (the snapshot leaves it
// out and the deletion is replayed), not as a stale history copy from a
// device that had not been told.
func TestDeletedConversationNeverComesBack(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	first := sendConv(t, w.bob, conv, ConvOutgoing{Body: "secret"})
	eventually(t, "the turn here", func() bool { return len(convBodies(t, w.alice, conv)) == 1 })
	var ts int64
	if err := w.alice.store.db.QueryRow(`SELECT ts FROM inbox WHERE lid = ?`, first.LID).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if done, err := w.alice.DeleteConversation(tctx(t), conv); err != nil || done.Erased != 1 || done.Devices != 0 {
		t.Fatalf("delete: %+v %v", done, err)
	}
	_, raw := rootOf(t, w.alice, conv)
	again := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindMessage, Body: "secret", Conv: conv, LID: first.LID, TS: ts, Root: raw,
		Origin: envelope.OriginUI, Fan: convFan(t, w.bob, w.alice)})
	w.alice.verifyAndStore(tctx(t), again)
	if b := convBodies(t, w.alice, conv); len(b) != 0 || inboxCount(t, w.alice, `lid = ? AND body <> ''`, first.LID) != 0 {
		t.Fatalf("a retried copy came back: %v", b)
	}

	phone := linked(t, w.alice)
	waitNamedAgentCaps(t, phone)
	dropCapSuccessor(t, phone, protocol.CapConvClear) // a device that cannot read deletions yet
	dumpClears(t, w.alice, phone)
	sendConv(t, w.bob, conv, ConvOutgoing{Body: "after"})
	eventually(t, "the later turn on the phone", func() bool { return strings.Join(convBodies(t, phone, conv), "|") == "in:after" })
	if n := len(queuedClears(t, w.alice, phone.Address)); n != 1 {
		t.Fatalf("the deletion replayed for the new device: %d copies", n)
	}
	if erasedHere(t, phone, conv, first.LID) {
		t.Fatal("a device that cannot read deletions applied one")
	}
	// The phone starts reading clr1: its signed capability publish is the
	// only event; the held copy goes out and is applied, no kick.
	withConvClear(t, phone)
	eventually(t, "the deletion replayed to the phone", func() bool { return erasedHere(t, phone, conv, first.LID) })
	item := HistoryItem{V: 1, From: w.bob.Address, FromKey: w.bob.Self().Fingerprint(), ID: protocol.NewID(), LID: first.LID, TS: ts,
		Kind: envelope.KindMessage, Body: "secret", Origin: envelope.OriginUI, At: time.Now().UnixMilli()}
	stale, err := w.alice.historyCopy(phone.id.Public(phone.Address), conv, raw, item)
	if err != nil {
		t.Fatal(err)
	}
	if err := phone.verifyAndStore(tctx(t), stale.env); err != nil {
		t.Fatal(err)
	}
	if b := strings.Join(convBodies(t, phone, conv), "|"); b != "in:after" || inboxCount(t, phone, `lid = ? AND body <> ''`, first.LID) != 0 {
		t.Fatalf("a stale history copy came back: %s", b)
	}
}

// Exact names only: the phone erases the turns the deleting laptop named
// and nothing else. A turn of bob's only the phone held stays (the laptop
// never had it); bob's turn sent after the deletion that reached the phone
// before an older, named turn of his stays too; nothing is told back, and
// the laptop shows that later turn when it arrives there.
func TestDeletionErasesExactlyTheNamedTurns(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	waitNamedAgentCaps(t, phone)
	dropCapSuccessor(t, phone, protocol.CapConvClear) // its deletion copies wait, to be handed over in order
	conv := newDM(t, w.bob, w.alice)
	sendConv(t, w.bob, conv, ConvOutgoing{Body: "m0"})
	eventually(t, "m0 on both", func() bool { return len(convBodies(t, w.alice, conv)) == 1 && len(convBodies(t, phone, conv)) == 1 })
	_, raw := rootOf(t, w.alice, conv)
	turn := func(body string) envelope.Inner {
		return envelope.Inner{Kind: envelope.KindMessage, Body: body, Conv: conv, LID: protocol.NewID(), Root: raw,
			Origin: envelope.OriginUI, Fan: convFan(t, w.bob, w.alice)}
	}
	unseen, older, newer := turn("only on the phone"), turn("older, late to the phone"), turn("sent after the deletion")
	if err := phone.verifyAndStore(tctx(t), craft(t, w.bob, phone, unseen)); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.verifyAndStore(tctx(t), craft(t, w.bob, w.alice, older)); err != nil {
		t.Fatal(err)
	}
	if done, err := w.alice.DeleteConversation(tctx(t), conv); err != nil || done.Erased != 2 {
		t.Fatalf("delete: %+v %v", done, err)
	}
	// Bob's later turn reaches the phone first, then his older named one.
	if err := phone.verifyAndStore(tctx(t), craft(t, w.bob, phone, newer)); err != nil {
		t.Fatal(err)
	}
	if err := phone.verifyAndStore(tctx(t), craft(t, w.bob, phone, older)); err != nil {
		t.Fatal(err)
	}
	parts := queuedClears(t, w.alice, phone.Address) // held: the phone does not read clr1
	if len(parts) != 1 {
		t.Fatalf("deletion copies for the phone: %d", len(parts))
	}
	if err := phone.verifyAndStore(tctx(t), parts[0]); err != nil {
		t.Fatal(err)
	}
	if b := strings.Join(convBodies(t, phone, conv), "|"); b != "in:only on the phone|in:sent after the deletion" {
		t.Fatalf("the phone after the deletion: %s", b)
	}
	if !erasedHere(t, phone, conv, older.LID) || erasedHere(t, phone, conv, newer.LID) || erasedHere(t, phone, conv, unseen.LID) {
		t.Fatal("the phone erased other than the named turns")
	}
	if n := len(queuedClears(t, phone, w.alice.Address)); n != 0 {
		t.Fatalf("the phone told the laptop more: %d copies", n)
	}
	if err := w.alice.verifyAndStore(tctx(t), craft(t, w.bob, w.alice, newer)); err != nil {
		t.Fatal(err)
	}
	if b := strings.Join(convBodies(t, w.alice, conv), "|"); b != "in:sent after the deletion" {
		t.Fatalf("the laptop after the later turn: %s", b)
	}
}

// D4: a deletion hides a request whose work is unfinished but keeps its
// text (and file) for that work: it is neither run nor dropped, and an
// unrelated change does not erase it. The change that ends the work, here
// the person declining it, erases what was kept: no sweep call, no
// reconnect.
func TestDeleteKeepsUnfinishedWorkUntilItEnds(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	path, _ := writeFile(t, t.TempDir(), "plan.txt", 100)
	task, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindTask, Body: "tidy the plan", Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	var blob string
	eventually(t, "the task awaiting here and its file fetched", func() bool {
		if inboxCount(t, w.alice, `id = ? AND state = ?`, task.ID, stateAwaiting) != 1 {
			return false
		}
		w.alice.store.db.QueryRow(`SELECT blob_id FROM attachments WHERE message_id = ?`, task.ID).Scan(&blob)
		_, err := os.Stat(w.alice.downloadPath(blob))
		return blob != "" && err == nil
	})
	done, err := w.alice.DeleteThread(w.bob.Address, task.ID)
	if err != nil || done.Erased != 1 || done.Kept != 1 {
		t.Fatalf("delete: %+v %v", done, err)
	}
	if ts, _ := w.alice.Threads(); len(ts) != 0 {
		t.Fatalf("shown after the deletion: %+v", ts)
	}
	w.alice.NoteChange() // an unrelated change: the eraser looks, keeps it
	time.Sleep(300 * time.Millisecond)
	if inboxCount(t, w.alice, `id = ? AND body = 'tidy the plan' AND state = ?`, task.ID, stateAwaiting) != 1 {
		t.Fatal("the unfinished request lost its text or state")
	}
	if _, err := os.Stat(w.alice.downloadPath(blob)); err != nil {
		t.Fatalf("the unfinished request's file: %v", err)
	}
	if _, err := w.alice.Decline(tctx(t), task.ID, "not now"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the declined task's text and file erased", func() bool {
		_, err := os.Stat(w.alice.downloadPath(blob))
		return inboxCount(t, w.alice, `id = ? AND body = '' AND state = ?`, task.ID, stateDeclined) == 1 && os.IsNotExist(err)
	})
}

// D4: a sent copy not yet handed over keeps its text after the deletion;
// the change that records its hand-over (as delivery records it) erases it.
func TestDeleteKeepsUnsentCopyUntilHandedOver(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	sendConv(t, w.bob, conv, ConvOutgoing{Body: "hello"})
	eventually(t, "the DM here", func() bool { return len(convBodies(t, w.alice, conv)) == 1 })
	sent := sendConv(t, w.alice, conv, ConvOutgoing{Body: "going out"})
	eventually(t, "the turn at bob", func() bool { return len(convBodies(t, w.bob, conv)) == 2 })
	// The copy as it stands before the Hub takes it.
	if _, err := w.alice.store.db.Exec(`UPDATE outbox SET state = ? WHERE lid = ?`, stateQueued, sent.LID); err != nil {
		t.Fatal(err)
	}
	if done, err := w.alice.DeleteConversation(tctx(t), conv); err != nil || done.Kept != 1 {
		t.Fatalf("delete: %+v %v", done, err)
	}
	if n := count(t, w.alice, "outbox WHERE lid = '"+sent.LID+"' AND body = 'going out'"); n == 0 {
		t.Fatal("the unsent copy lost its text")
	}
	if err := w.alice.store.setOutboxState(sent.ID, protocol.StateCustody, "", ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the handed-over copy's text erased", func() bool {
		return count(t, w.alice, "outbox WHERE lid = '"+sent.LID+"' AND body <> ''") == 0
	})
}

// D5: a device thread is deleted exactly: the selected reply-linked thread
// with that peer, on this device (its only copy), never another thread
// with the same peer; the peer keeps theirs; a later reply to it starts anew.
func TestDeleteThreadExactScope(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	start, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "thread one"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "thread two"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "both threads here", func() bool { ts, _ := w.alice.Threads(); return len(ts) == 2 })
	reply, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "reply in one", ReplyTo: start.ID})
	if err != nil {
		t.Fatal(err)
	}
	done, err := w.alice.DeleteThread(w.bob.Address, start.ID)
	if err != nil || done.Erased != 2 || !done.ThisOnly {
		t.Fatalf("delete thread: %+v %v", done, err)
	}
	ts, _ := w.alice.Threads()
	if len(ts) != 1 || ts[0].ID != other.ID {
		t.Fatalf("threads left: %+v", ts)
	}
	if _, err := w.alice.Conversation(start.ID, 0, 0); !errors.Is(err, ErrNoMessage) {
		t.Fatalf("the deleted thread opens: %v", err)
	}
	if inboxCount(t, w.alice, `id IN (?, ?) AND body <> ''`, start.ID, reply.ID) != 0 || count(t, w.alice, "outbox WHERE id = '"+reply.ID+"' AND body <> ''") != 0 {
		t.Fatal("the deleted thread keeps text")
	}
	if c, err := w.alice.Conversation(other.ID, 0, 0); err != nil || len(c.Messages) != 1 || c.Messages[0].Body != "thread two" {
		t.Fatalf("the other thread: %+v %v", c, err)
	}
	if bt, _ := w.bob.Threads(); len(bt) != 2 {
		t.Fatalf("the peer's threads: %+v", bt)
	}
	later, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "still there?", ReplyTo: reply.ID})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the later reply as a thread of its own", func() bool {
		c, err := w.alice.Conversation(later.ID, 0, 0)
		return err == nil && len(c.Messages) == 1 && c.Messages[0].Body == "still there?"
	})
}

// D4/D5: a group conversation is deleted the same way: this person's copy
// only. Membership and its proof stay (turns still flow both ways), the
// other members keep theirs, and a later turn reopens it.
func TestDeleteGroupConversationKeepsMembership(t *testing.T) {
	w, carol, p, _ := groupTurnsFixture(t)
	conv := p.State.Conv
	sendConv(t, w.bob, conv, ConvOutgoing{Body: "group hello"})
	eventually(t, "the first turn here", func() bool { return len(groupTurns(t, w.alice, conv)) == 1 })
	sendConv(t, carol, conv, ConvOutgoing{Body: "from carol"})
	eventually(t, "both turns here", func() bool { return len(groupTurns(t, w.alice, conv)) == 2 })
	for _, a := range []*Agent{w.bob, carol} {
		eventually(t, "both turns at "+a.Address, func() bool { return len(groupTurns(t, a, conv)) == 2 })
	}
	done, err := w.alice.DeleteConversation(tctx(t), conv)
	if err != nil || done.Erased != 2 || done.Devices != 0 {
		t.Fatalf("delete: %+v %v", done, err)
	}
	if turns := groupTurns(t, w.alice, conv); len(turns) != 0 || !convInfo(t, w.alice, conv).Deleted {
		t.Fatalf("the group here after the deletion: %+v", turns)
	}
	if got, err := w.alice.GroupContext(conv); err != nil || got.State.Hash() != p.State.Hash() {
		t.Fatalf("membership changed: %v", err)
	}
	for _, a := range []*Agent{w.bob, carol} {
		if n := len(groupTurns(t, a, conv)); n != 2 {
			t.Fatalf("%s's copy: %d turns", a.Address, n)
		}
	}
	sendConv(t, w.alice, conv, ConvOutgoing{Body: "back again"})
	eventually(t, "the member's later turn at bob", func() bool { return len(groupTurns(t, w.bob, conv)) == 3 })
	sendConv(t, w.bob, conv, ConvOutgoing{Body: "welcome back"})
	eventually(t, "the reopened group here", func() bool {
		return strings.Join(convBodies(t, w.alice, conv), "|") == "out:back again|in:welcome back"
	})
	if convInfo(t, w.alice, conv).Deleted {
		t.Fatal("the reopened group is still left out")
	}
}

// A deletion comes only from another current device of this person: the
// other person in the conversation cannot erase anything here.
func TestDeletionFromAnotherPersonRefused(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	sendConv(t, w.bob, conv, ConvOutgoing{Body: "hello"})
	eventually(t, "the DM here", func() bool { return len(convBodies(t, w.alice, conv)) == 1 })
	mine := sendConv(t, w.alice, conv, ConvOutgoing{Body: "keep this"})
	eventually(t, "the turn on bob's side", func() bool { return len(convBodies(t, w.bob, conv)) == 2 })
	bp, _, _ := w.bob.Person()
	ref := envelope.Ref{ID: mine.LID, Fingerprint: w.alice.Self().Fingerprint()}
	body, _ := json.Marshal(envelope.Clear{Deletion: protocol.NewID(), Part: 1, Parts: 1})
	in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: envelope.SubClear, Body: string(body), Ref: &ref, Conv: conv, LID: protocol.NewID(),
		Fan: []envelope.Fan{{Person: bp.Person, Roster: bp.Roster}}}
	r, _ := w.alice.id.Public(w.alice.Address).Recipient()
	env, err := envelope.Seal(in, w.bob.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	w.alice.verifyAndStore(tctx(t), env)
	if heldReason(t, w.alice, env.ID) != reasonInvalid || erasedHere(t, w.alice, conv, mine.LID) {
		t.Fatal("another person's deletion was applied")
	}
	if b := convBodies(t, w.alice, conv); len(b) != 2 {
		t.Fatalf("the turns here: %v", b)
	}
}
