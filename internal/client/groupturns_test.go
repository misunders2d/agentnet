package client

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

func groupTurnsFixture(t *testing.T, beforeRun ...func(*Agent)) (*world, *Agent, GroupContext, map[*Agent]func()) {
	t.Helper()
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	carol := proofReader(t, w, "carol")
	person, _, err := carol.store.selfPerson(carol.Address)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := carol.SignGroupAdmission(p.Root, 1, p.State.Hash(), nil)
	if err != nil {
		t.Fatal(err)
	}
	next := p
	next.Proof = nil
	next.State.Seq = 1
	next.State.Prev = p.State.Hash()
	next.State.Title = "Three current people"
	next.State.Members = slices.Clone(p.State.Members)
	for i := range next.State.Members {
		if next.State.Members[i].Person != p.State.Actor {
			next.State.Members[i].Admin = false
		}
	}
	next.State.Members = append(next.State.Members, protocol.GroupMember{ConvMember: protocol.ConvMember{Person: person.roster.Person, Roster: person.roster.Hash()}, Admission: admission})
	slices.SortFunc(next.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, next); err != nil {
		t.Fatal(err)
	}
	stops := map[*Agent]func(){}
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		for _, setup := range beforeRun {
			setup(a)
		}
		stops[a] = runAgent(t, a)
		publishGroupFixtureCaps(t, a, true)
	}
	for _, a := range []*Agent{w.bob, carol} {
		eventually(t, "ordinary member current context", func() bool {
			got, e := a.GroupContext(next.State.Conv)
			return e == nil && got.State.Hash() == next.State.Hash()
		})
	}
	return w, carol, next, stops
}

func groupTurns(t *testing.T, a *Agent, conv string) []ConvMessage {
	t.Helper()
	rows, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func groupTurnEnvelope(t *testing.T, a *Agent, id string) envelope.Envelope {
	t.Helper()
	var raw []byte
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestGroupTurnsT1ThreePeopleAndLinkedCopies(t *testing.T) {
	w, carol, p, _ := groupTurnsFixture(t)
	people := []*Agent{w.alice, w.bob, carol}
	all := slices.Clone(people)
	for _, a := range people {
		phone, await, _ := linkPhone(t, a, "phone")
		request := pendingLink(t, a)
		if err := a.DecideLink(tctx(t), request.ID, true); err != nil {
			t.Fatal(err)
		}
		if out := <-await; out.err != nil {
			t.Fatal(out.err)
		}
		runAgent(t, phone)
		publishGroupFixtureCaps(t, phone, true)
		all = append(all, phone)
	}
	// A new exact batch forwards only current membership to the linked devices.
	copies, err := w.alice.groupDeliveryCopies(tctx(t), p)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.addConvOutbox(copies, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		eventually(t, "linked current context", func() bool {
			got, e := a.GroupContext(p.State.Conv)
			return e == nil && got.State.Hash() == p.State.Hash()
		})
	}
	for i, a := range people {
		res, e := a.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: []string{"Alice message", "Bob message", "Carol message"}[i]})
		if e != nil {
			t.Fatal(e)
		}
		if len(res.Copies) != 5 {
			t.Fatalf("fixed audience has %d copies", len(res.Copies))
		}
		for _, copy := range res.Copies {
			env := groupTurnEnvelope(t, a, copy.ID)
			var fp string
			a.store.db.QueryRow(`SELECT recipient_fp FROM outbox WHERE id=?`, copy.ID).Scan(&fp)
			if !protocol.ValidFingerprint(fp) {
				t.Fatal("exact recipient key absent")
			}
			if env.To == a.Address {
				t.Fatal("sender self copy")
			}
		}
	}
	for _, a := range all {
		eventually(t, "three ordinary logical turns", func() bool { rows, e := a.ConversationMessages(p.State.Conv); return e == nil && len(rows) == 3 })
		views, e := a.Conversations()
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, v := range views {
			if v.ID == p.State.Conv {
				found = true
				if v.Kind != protocol.ConvKindGroup || v.Title != p.State.Title || len(v.Members) != 3 || v.Role != "member" || v.Frozen != "" || v.Peer.Person != "" {
					t.Fatalf("group view %+v", v)
				}
			}
		}
		if !found {
			t.Fatal("group projection missing")
		}
		unread, e := a.ConvUnread()
		if e != nil || len(unread[p.State.Conv]) != 2 {
			t.Fatalf("own replica unread %+v %v", unread, e)
		}
		if parts, err := a.Participations(p.State.Conv); err != nil || len(parts) != 0 {
			t.Fatalf("ordinary-only group participations %+v %v", parts, err)
		}
		var jobs int
		a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE kind IN ('question','task') OR state!=''`).Scan(&jobs)
		if jobs != 0 {
			t.Fatalf("group execution jobs %d", jobs)
		}
	}
}

func TestGroupTurnsT2LogicalRepliesOrderingAndRestart(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	stops[carol]()
	parent, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "Bob physical parent copy", func() bool { return len(groupTurns(t, w.bob, p.State.Conv)) == 1 })
	bobParent := groupTurns(t, w.bob, p.State.Conv)[0]
	reply, err := w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "reply", ReplyTo: bobParent.ID})
	if err != nil {
		t.Fatal(err)
	}
	var parentEnv, replyEnv envelope.Envelope
	for _, copy := range parent.Copies {
		if copy.To == carol.Address {
			parentEnv = groupTurnEnvelope(t, w.alice, copy.ID)
		}
	}
	for _, copy := range reply.Copies {
		if copy.To == carol.Address {
			replyEnv = groupTurnEnvelope(t, w.bob, copy.ID)
		}
	}
	if err = w.bob.hub.do(tctx(t), "POST", "/v1/messages", replyEnv, nil); err != nil {
		t.Fatal(err)
	}
	if err = carol.accept(tctx(t), replyEnv); err != nil {
		t.Fatal(err)
	}
	missing := groupTurns(t, carol, p.State.Conv)
	if len(missing) != 1 || missing[0].Body != "reply" || missing[0].ReplyTo != parent.LID {
		t.Fatalf("unresolved exact LID was not preserved: %+v", missing)
	}
	if _, err = groupReplyLID(carol.store.db, p.State.Conv, parent.LID); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("missing parent acquired authority: %v", err)
	}
	if err = w.alice.hub.do(tctx(t), "POST", "/v1/messages", parentEnv, nil); err != nil {
		t.Fatal(err)
	}
	if err = carol.accept(tctx(t), parentEnv); err != nil {
		t.Fatal(err)
	}
	if lid, err := groupReplyLID(carol.store.db, p.State.Conv, parent.LID); err != nil || lid != parent.LID {
		t.Fatalf("later exact scoped LID did not resolve: %q %v", lid, err)
	}
	home := carol.home
	carol.Close()
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	runAgent(t, reopened)
	publishGroupFixtureCaps(t, reopened, true)
	eventually(t, "reordered reply survives restart", func() bool { return len(groupTurns(t, reopened, p.State.Conv)) == 2 })
	for _, row := range groupTurns(t, reopened, p.State.Conv) {
		if row.Body == "reply" && row.ReplyTo != parent.LID {
			t.Fatalf("physical reply guess %q", row.ReplyTo)
		}
	}
	if err = reopened.accept(tctx(t), replyEnv); err != nil {
		t.Fatal(err)
	}
	if len(groupTurns(t, reopened, p.State.Conv)) != 2 {
		t.Fatal("duplicate reply")
	}
	dm := newDM(t, w.bob, w.alice)
	other, err := w.bob.SendConv(tctx(t), dm, ConvOutgoing{Body: "other conversation"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "wrong parent", ReplyTo: other.ID}); err == nil {
		t.Fatal("cross-conversation parent accepted")
	}
	// A logical reference with conflicting physical candidates cannot pick
	// whichever matching row happens to be returned first.
	if _, err = w.bob.store.db.Exec(`UPDATE outbox SET lid=? WHERE id=?`, parent.LID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "ambiguous parent", ReplyTo: parent.LID}); err == nil {
		t.Fatal("conflicting logical parent accepted")
	}
}

func TestGroupTurnsT3FilesResumeAcrossSenderRestart(t *testing.T) {
	var bobFaults *faults
	w, carol, p, stops := groupTurnsFixture(t, func(a *Agent) {
		if a.Address == "bob/laptop" {
			bobFaults = injectFaults(a)
		}
	})
	stops[w.alice]()
	path, data := writeFile(t, t.TempDir(), "group-file.bin", 6<<20)
	injectFaults(w.alice).addAfter("PUT", "/v1/blobs/", 1, 1, false)
	bobFaults.addAfter("GET", "/v1/blobs/", 1, 1, false)
	res, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Files: []OutgoingFile{{Path: path, Name: "group-file.bin"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Copies) != 2 {
		t.Fatal("file did not use exact two-recipient batch")
	}
	home := w.alice.home
	w.alice.Close()
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	runAgent(t, reopened)
	publishGroupFixtureCaps(t, reopened, true)
	if err = reopened.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.bob, carol} {
		eventually(t, "ordinary file turn received once", func() bool { return len(groupTurns(t, a, p.State.Conv)) == 1 })
		row := groupTurns(t, a, p.State.Conv)[0]
		paths, e := a.Download(tctx(t), row.ID, t.TempDir(), false)
		if e != nil {
			t.Fatal(e)
		}
		got, e := os.ReadFile(paths[0])
		if e != nil || !bytes.Equal(got, data) {
			t.Fatalf("download differs %v", e)
		}
	}
	text, err := reopened.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "text and file", Files: []OutgoingFile{{Path: path, Name: "group-file.bin"}}})
	if err != nil || len(text.Copies) != 2 {
		t.Fatalf("text+file %v", err)
	}
	for _, a := range []*Agent{w.bob, carol} {
		eventually(t, "two file turns", func() bool { return len(groupTurns(t, a, p.State.Conv)) == 2 })
	}
	if files, _ := filepath.Glob(filepath.Join(home, "staging", "upload-*")); len(files) != 0 {
		t.Fatal("leftover plaintext staging")
	}
}

// With the Hub out of reach a group message is kept, as a DM is: its copies
// wait, files spooled, with the true cause, and go out once the Hub is back
// and each recipient's current record shows it may read them.
func TestGroupTurnWaitsWhileHubUnreachable(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	stops[w.alice]()
	home := w.alice.home
	w.alice.Close()
	alice, err := Open(home) // a new command: its first request checks the workspace identity
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	base := alice.hub.http.Transport
	alice.hub.http.Transport = failingHub(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	})
	path, data := writeFile(t, t.TempDir(), "tiny.csv", 2000)
	sent, err := alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "while the Hub is down", Files: []OutgoingFile{{Path: path, Name: "tiny.csv"}}})
	if err != nil || sent.State != stateConvWaiting || len(sent.Copies) != 2 || !strings.HasPrefix(sent.Detail, WaitServerUnavailable+"cannot reach the Hub") || strings.Contains(sent.Detail, "workspace identity") {
		t.Fatalf("group send without the Hub: %+v %v", sent, err)
	}
	if r, err := alice.Cleanup(false); err != nil || r.SpoolFiles != 0 {
		t.Fatalf("cleanup = %+v, %v", r, err)
	}
	alice.hub.http.Transport = base
	runAgent(t, alice)
	publishGroupFixtureCaps(t, alice, true)
	for _, a := range []*Agent{w.bob, carol} {
		eventually(t, "the kept group turn", func() bool { return len(groupTurns(t, a, p.State.Conv)) == 1 })
		row := groupTurns(t, a, p.State.Conv)[0]
		if row.Body != "while the Hub is down" || row.LID != sent.LID {
			t.Fatalf("received %+v", row)
		}
		paths, err := a.Download(tctx(t), row.ID, t.TempDir(), false)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(paths[0]); err != nil || !bytes.Equal(got, data) {
			t.Fatalf("file differs %v", err)
		}
	}
}

// A group message kept while the Hub was out of reach was made for the
// devices pinned then; a device its person removed meanwhile never gets its
// copy: the copy is released and sent only on current evidence.
func TestGroupTurnKeptOfflineNotSentToRemovedDevice(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	phone, await, _ := linkPhone(t, w.bob, "phone")
	request := pendingLink(t, w.bob)
	if err := w.bob.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	publishGroupFixtureCaps(t, phone, true)
	bob, _, _ := w.bob.Person()
	if _, err := w.alice.refreshPerson(tctx(t), bob.Person, false); err != nil { // alice knows the phone
		t.Fatal(err)
	}
	if _, err := w.alice.sendKey(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	// Alice goes offline before Bob removes the phone, so the removal cannot
	// reach her running daemon first: she still has it pinned when she sends.
	stops[w.alice]()
	if err := w.bob.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	home := w.alice.home
	w.alice.Close()
	alice, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	base := alice.hub.http.Transport
	alice.hub.http.Transport = failingHub(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	})
	sent, err := alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "kept while offline"})
	if err != nil || sent.State != stateConvWaiting || len(sent.Copies) != 3 {
		t.Fatalf("group send without the Hub: %+v %v", sent, err)
	}
	removed := ""
	for _, c := range sent.Copies {
		if c.To == phone.Address {
			removed = c.ID
		}
	}
	if removed == "" {
		t.Fatalf("no copy for the device pinned here: %+v", sent.Copies)
	}
	alice.hub.http.Transport = base
	runAgent(t, alice)
	publishGroupFixtureCaps(t, alice, true)
	for _, a := range []*Agent{w.bob, carol} {
		eventually(t, "the kept group turn", func() bool { return len(groupTurns(t, a, p.State.Conv)) == 1 })
	}
	if err := alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var state, why string
	alice.store.db.QueryRow(`SELECT state, coalesce(error, '') FROM outbox WHERE id = ?`, removed).Scan(&state, &why)
	if state != stateConvWaiting && state != stateNotDelivered {
		t.Fatalf("the removed device's copy is %s (%s)", state, why)
	}
	t.Logf("the removed device's copy: %s (%s)", state, why)
}

// A group turn without the Hub is kept only for devices whose keys are known
// here. A member's device whose key this installation never fetched (his
// person linked it meanwhile; the roster pinned here lists it) fails the
// send with that cause, and nothing is kept: no part of the group gets it.
func TestGroupTurnOfflineUnknownKeyNamesCause(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	phone, await, _ := linkPhone(t, w.bob, "phone")
	request := pendingLink(t, w.bob)
	if err := w.bob.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	bob, _, _ := w.bob.Person()
	if _, err := w.alice.refreshPerson(tctx(t), bob.Person, false); err != nil { // lists the phone; its key is not fetched
		t.Fatal(err)
	}
	stops[w.alice]()
	home := w.alice.home
	w.alice.Close()
	alice, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	alice.hub.http.Transport = failingHub(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	})
	outbox := count(t, alice, "outbox")
	path, _ := writeFile(t, t.TempDir(), "tiny.csv", 2000)
	sent, err := alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "offline", Files: []OutgoingFile{{Path: path, Name: "tiny.csv"}}})
	if err == nil || !hubUnreachable(err) || !strings.Contains(err.Error(), "the key of "+phone.Address+" is not known here yet") {
		t.Fatalf("group send without the Hub or a member device's key: %+v %v", sent, err)
	}
	if n := count(t, alice, "outbox"); n != outbox {
		t.Fatalf("outbox %d -> %d after a refused send", outbox, n)
	}
	if entries, _ := os.ReadDir(filepath.Join(alice.home, "spool")); len(entries) != 0 {
		t.Fatalf("spool after a refused send: %d files", len(entries))
	}
}

// removedWhileOffline: alice pins bob's laptop and his linked phone, then
// is away; bob links a tablet from the phone and removes both from it. The
// Hub revokes the phone (it joined by a link) but not the laptop (it joined
// by invitation): the laptop stays a member, with no person. alice comes
// back as a new command that cannot reach the Hub; online gives it the Hub
// back and runs its daemon.
func removedWhileOffline(t *testing.T) (w *world, carol *Agent, p GroupContext, dm string, alice, tablet *Agent, online func()) {
	t.Helper()
	w, carol, p, stops := groupTurnsFixture(t)
	dm = newDM(t, w.alice, w.bob)
	phone, await, _ := linkPhone(t, w.bob, "phone")
	request := pendingLink(t, w.bob)
	if err := w.bob.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	publishGroupFixtureCaps(t, phone, true)
	bob, _, _ := w.bob.Person()
	if _, err := w.alice.refreshPerson(tctx(t), bob.Person, false); err != nil { // alice pins the phone
		t.Fatal(err)
	}
	if _, err := w.alice.sendKey(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	stops[w.alice]() // away before the removals: she never sees them
	home := w.alice.home
	w.alice.Close()
	tablet, await, _ = linkPhone(t, phone, "tablet")
	request = pendingLink(t, phone)
	if err := phone.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	for _, removed := range []string{w.bob.Address, phone.Address} {
		if err := tablet.RemoveDevice(tctx(t), removed); err != nil {
			t.Fatal(err)
		}
	}
	alice, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	base := alice.hub.http.Transport
	alice.hub.http.Transport = failingHub(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	})
	online = func() {
		alice.hub.http.Transport = base
		runAgent(t, alice)
		publishGroupFixtureCaps(t, alice, true)
	}
	return w, carol, p, dm, alice, tablet, online
}

// keptCopyTo returns the ID of the copy for address a send kept waiting.
func keptCopyTo(t *testing.T, sent ConvSent, err error, address string) string {
	t.Helper()
	if err != nil || sent.State != stateConvWaiting {
		t.Fatalf("send without the Hub: %+v %v", sent, err)
	}
	for _, c := range sent.Copies {
		if c.To == address {
			return c.ID
		}
	}
	t.Fatalf("no copy for %s, a device pinned here: %+v", address, sent.Copies)
	return ""
}

// notHandedOver fails unless the copy id is still waiting or not delivered.
func notHandedOver(t *testing.T, a *Agent, id string) {
	t.Helper()
	if err := a.FlushOutbox(tctx(t)); err != nil {
		t.Logf("flush: %v", err)
	}
	var state, why string
	a.store.db.QueryRow(`SELECT state, coalesce(error, '') FROM outbox WHERE id = ?`, id).Scan(&state, &why)
	if state != stateConvWaiting && state != stateNotDelivered {
		t.Fatalf("a removed device's copy was handed over: %s (%s)", state, why)
	}
	t.Logf("the removed device's copy: %s (%s)", state, why)
}

// A group turn kept while the Hub was out of reach goes to a device only if
// its person's current roster lists it: an invite-joined device removed
// meanwhile, which the Hub does not revoke, never gets its copy, though the
// roster pinned when the copy was made listed it.
func TestGroupTurnKeptOfflineNotSentToUnrevokedRemovedDevice(t *testing.T) {
	w, carol, p, _, alice, _, online := removedWhileOffline(t)
	sent, err := alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "after both removals"})
	laptop := keptCopyTo(t, sent, err, w.bob.Address)
	online()
	eventually(t, "carol gets the kept group turn", func() bool { return len(groupTurns(t, carol, p.State.Conv)) == 1 })
	notHandedOver(t, alice, laptop)
}

// The same for a DM kept while the Hub was out of reach.
func TestDMKeptOfflineNotSentToUnrevokedRemovedDevice(t *testing.T) {
	w, _, _, dm, alice, tablet, online := removedWhileOffline(t)
	sent, err := alice.SendConv(tctx(t), dm, ConvOutgoing{Body: "after both removals"})
	laptop := keptCopyTo(t, sent, err, w.bob.Address)
	bob, _, _ := tablet.Person()
	online()
	eventually(t, "alice reads bob's current roster", func() bool {
		p, ok, err := alice.store.personByID(bob.Person)
		return err == nil && ok && p.info.Roster == bob.Roster
	})
	notHandedOver(t, alice, laptop)
}

func TestGroupTurnsT4AtomicHeadsRemovalWithdrawalAndExactKey(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	stops[w.alice]()
	next := p
	next.State.Seq++
	next.State.Prev = p.State.Hash()
	next.State.Title = "Current successor"
	var err error
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	beforeOutbox = func() {
		w.alice.NoteGroupHead(protocol.GroupHead{Conv: p.State.Conv, Bootstrap: p.Root.Creator.Fingerprint, Seq: next.State.Seq, Hash: next.State.Hash()})
	}
	_, err = w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "blocked at transaction"})
	beforeOutbox = func() {}
	if !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("head changed but batch installed %v", err)
	}
	var count int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE body='blocked at transaction'`).Scan(&count)
	if count != 0 {
		t.Fatal("partial batch")
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, next); err != nil {
		t.Fatal(err)
	}
	injectFaults(w.alice).add("POST", "/v1/messages", 2, false)
	queued, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "fixed old batch"})
	if err != nil {
		t.Fatal(err)
	}
	var carolEnv, bobEnv envelope.Envelope
	for _, copy := range queued.Copies {
		if copy.To == carol.Address {
			carolEnv = groupTurnEnvelope(t, w.alice, copy.ID)
		} else if copy.To == w.bob.Address {
			bobEnv = groupTurnEnvelope(t, w.alice, copy.ID)
		}
	}
	// An unavailable newer head holds old admission rather than substituting
	// immutable bootstrap members as authority.
	if err = carol.NoteGroupHead(protocol.GroupHead{Conv: p.State.Conv, Bootstrap: p.Root.Creator.Fingerprint, Seq: next.State.Seq + 1, Hash: strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
	if err = carol.accept(tctx(t), carolEnv); err != nil {
		t.Fatal(err)
	}
	var reason string
	carol.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, carolEnv.ID).Scan(&reason)
	if reason != reasonProof {
		t.Fatalf("stale receive authorized %q", reason)
	}
	removed := next
	removed.State.Seq++
	removed.State.Prev = next.State.Hash()
	removed.State.Members = nil
	carolPerson, _, _ := carol.store.selfPerson(carol.Address)
	for _, member := range next.State.Members {
		if member.Person != carolPerson.roster.Person {
			removed.State.Members = append(removed.State.Members, member)
		}
	}
	removed, err = w.alice.SignGroupState(tctx(t), removed)
	if err != nil {
		t.Fatal(err)
	}
	commit, err = w.alice.BuildGroupCommit(tctx(t), removed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, removed); err != nil {
		t.Fatal(err)
	}
	handled, allowed, err := w.alice.mayDeliverGroupTurn(carolEnv)
	if !handled || allowed || err != nil {
		t.Fatalf("removed recipient %v %v %v", handled, allowed, err)
	}
	// Original exact-key snapshot survives restart and is never replaced by
	// another current device's fingerprint for the same routing address.
	if _, err = w.alice.store.db.Exec(`UPDATE outbox SET recipient_fp=? WHERE id=?`, w.alice.Self().Fingerprint(), bobEnv.ID); err != nil {
		t.Fatal(err)
	}
	home := w.alice.home
	w.alice.Close()
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	handled, allowed, err = reopened.mayDeliverGroupTurn(bobEnv)
	if !handled || allowed || err != nil {
		t.Fatalf("restart guessed current key %v %v %v", handled, allowed, err)
	}
	// NULL legacy group records stay held; no today's-roster backfill.
	reopened.store.db.Exec(`UPDATE outbox SET recipient_fp=NULL,state=? WHERE id=?`, stateQueued, bobEnv.ID)
	_, allowed, err = reopened.mayDeliverGroupTurn(bobEnv)
	if allowed || err != nil {
		t.Fatalf("NULL binding inferred %v %v", allowed, err)
	}
	withdrawal, err := w.bob.SignGroupWithdrawal(tctx(t), p.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.AcceptGroupWithdrawal(tctx(t), withdrawal); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "after own leave"}); err == nil {
		t.Fatal("withdrawn sender authorized")
	}
}

func TestGroupTurnsT5StaleFanAndUnsupportedOperations(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	stops[w.alice]()
	aliceFaults := injectFaults(w.alice)
	aliceFaults.add("POST", "/v1/messages", 2, false)
	path, data := writeFile(t, t.TempDir(), "stale-file.txt", 12345)
	old, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "live turn before device link", Files: []OutgoingFile{{Path: path, Name: "stale-file.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	phone, await, _ := linkPhone(t, w.bob, "stale-phone")
	request := pendingLink(t, w.bob)
	if err = w.bob.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	stopPhone := runAgent(t, phone)
	publishGroupFixtureCaps(t, phone, true)
	copies, err := w.alice.groupDeliveryCopies(tctx(t), p)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.addConvOutbox(copies, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stale current own-device forwarding", func() bool { return len(groupTurns(t, phone, p.State.Conv)) == 1 })
	row := groupTurns(t, phone, p.State.Conv)[0]
	if !row.History || row.LID != old.LID || row.AgentID != "" || row.Job != "" {
		t.Fatalf("stale history authority %+v", row)
	}
	if err = phone.RequestFile(tctx(t), row.ID, 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "current own-device history file offer", func() bool {
		got := groupTurns(t, phone, p.State.Conv)
		return len(got) == 1 && len(got[0].Attachments) == 1 && !strings.HasPrefix(got[0].Attachments[0].BlobID, historyBlob)
	})
	paths, err := phone.Download(tctx(t), row.ID, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("linked file differs %v", err)
	}
	for _, m := range []ConvOutgoing{{Kind: envelope.KindQuestion, Body: "Q"}, {Kind: envelope.KindTask, Body: "T"}, {Body: "PID", PID: protocol.NewID()}, {Body: "output", Kind: envelope.KindAnswer, AgentID: protocol.NewID()}, {Body: "agent origin", Origin: "agent:fixture"}} {
		if _, err = w.alice.SendConv(tctx(t), p.State.Conv, m); err == nil {
			t.Fatal("group execution fields accepted")
		}
	}
	// Signed invalid replica, unrelated Fan person and foreign root are
	// refused by actual receive; no job or control is admitted.
	var bobCopy envelope.Envelope
	for _, copy := range old.Copies {
		if copy.To == w.bob.Address {
			bobCopy = groupTurnEnvelope(t, w.alice, copy.ID)
		}
	}
	original, err := envelope.Open(bobCopy, w.bob.id, w.bob.Address, w.alice.Self())
	if err != nil {
		t.Fatal(err)
	}
	item := itemOf(original, w.bob.Self().Fingerprint(), 1)
	item.ID, item.LID = protocol.NewID(), protocol.NewID()
	raw, _ := json.Marshal(p.Root)
	forged, err := w.bob.historyCopy(phone.Self(), p.State.Conv, raw, item)
	if err == nil {
		t.Fatal("wrong original history key source sealed unstamped history")
	}
	forged, err = w.bob.groupHistoryCarrier(phone.Self(), p, item)
	if err != nil {
		t.Fatal(err)
	}
	if err = phone.accept(tctx(t), forged.env); err != nil {
		t.Fatal(err)
	}
	var historyReason string
	phone.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, forged.env.ID).Scan(&historyReason)
	if historyReason != reasonInvalid {
		t.Fatalf("wrong original history key authorized: %q", historyReason)
	}
	for _, mode := range []string{"replica", "fan", "root", "root signature"} {
		bad := original
		bad.ID = protocol.NewID()
		bad.LID = protocol.NewID()
		bad.Fan = slices.Clone(original.Fan)
		switch mode {
		case "replica":
			bad.Replica = true
		case "fan":
			bad.Fan = []envelope.Fan{{Person: protocol.NewID(), Roster: strings.Repeat("f", 64)}}
		case "root":
			r := p.Root
			r.Nonce = protocol.NewID()
			r.Sign(w.alice.id.Sign)
			bad.Root, _ = json.Marshal(r)
			bad.Conv = r.ID()
		case "root signature":
			r := p.Root
			r.Sig = slices.Clone(r.Sig)
			r.Sig[0] ^= 1
			bad.Root, _ = json.Marshal(r)
		}
		key, _ := w.bob.Self().Recipient()
		env, e := envelope.Seal(bad, w.alice.id.Sign, key)
		if e != nil {
			t.Fatal(e)
		}
		if e = w.bob.accept(tctx(t), env); e != nil {
			t.Fatal(e)
		}
		var reason string
		w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, env.ID).Scan(&reason)
		if reason == "" {
			t.Fatal("forged group turn admitted")
		}
	}
	for _, a := range []*Agent{w.alice, w.bob, carol, phone} {
		var jobs int
		a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE kind IN ('question','task') OR state!=''`).Scan(&jobs)
		if jobs != 0 {
			t.Fatal("group granted execution")
		}
	}
	// Seal for the current linked device, then remove it using its person's
	// signed roster transition. Restart must retain the original recipient
	// key and refuse that copy while an unaffected recipient remains valid.
	stopPhone()
	aliceFaults.add("POST", "/v1/messages", 3, false)
	queued, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "before linked-device removal"})
	if err != nil || len(queued.Copies) != 3 {
		t.Fatalf("linked exact batch %v %d", err, len(queued.Copies))
	}
	var removedCopy, unaffectedCopy envelope.Envelope
	for _, copy := range queued.Copies {
		switch copy.To {
		case phone.Address:
			removedCopy = groupTurnEnvelope(t, w.alice, copy.ID)
		case w.bob.Address:
			unaffectedCopy = groupTurnEnvelope(t, w.alice, copy.ID)
		}
	}
	if err = w.bob.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	bobPerson, _, _ := w.bob.store.selfPerson(w.bob.Address)
	if _, err = w.alice.refreshPerson(tctx(t), bobPerson.roster.Person, false); err != nil {
		t.Fatal(err)
	}
	home := w.alice.home
	w.alice.Close()
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var originalKey string
	if err = reopened.store.db.QueryRow(`SELECT recipient_fp FROM outbox WHERE id=?`, removedCopy.ID).Scan(&originalKey); err != nil || originalKey != phone.Self().Fingerprint() {
		t.Fatalf("original key changed: %q %v", originalKey, err)
	}
	handled, allowed, err := reopened.mayDeliverGroupTurn(removedCopy)
	if !handled || allowed || err != nil {
		t.Fatalf("removed linked device retry %v %v %v", handled, allowed, err)
	}
	handled, allowed, err = reopened.mayDeliverGroupTurn(unaffectedCopy)
	if !handled || !allowed || err != nil {
		t.Fatalf("unaffected recipient retry %v %v %v", handled, allowed, err)
	}
}

func TestGroupTurnsMigrationPreservesUnboundLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sqlitedb.Open(path, schema[:len(schema)-1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at)VALUES('legacy','bob/laptop','exact old text','exact old envelope','queued',1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	current, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.db.Close()
	var body, env string
	var key sql.NullString
	if err = current.db.QueryRow(`SELECT body,envelope,recipient_fp FROM outbox WHERE id='legacy'`).Scan(&body, &env, &key); err != nil {
		t.Fatal(err)
	}
	if body != "exact old text" || env != "exact old envelope" || key.Valid {
		t.Fatal("migration rewrote/inferred old record")
	}
}
