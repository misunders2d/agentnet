package client

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// linked links a new device "phone" to a's person and runs its daemon.
func linked(t *testing.T, a *Agent) *Agent {
	t.Helper()
	phone, awaited, _ := linkPhone(t, a, "phone")
	req := pendingLink(t, a)
	if err := a.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	return phone
}

// T2 (history): a newly linked device gets the person's existing chats,
// as history: both directions, files as manifests, shown as synced; none of
// it runs or alerts, and a later message comes directly.
func TestHistoryToLinkedDevice(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	path, _ := writeFile(t, t.TempDir(), "notes.txt", 100)
	for _, m := range []struct {
		from *Agent
		out  ConvOutgoing
	}{
		{w.bob, ConvOutgoing{Body: "hello alice"}},
		{w.alice, ConvOutgoing{Body: "hi bob", Files: []OutgoingFile{{Path: path}}}},
		{w.bob, ConvOutgoing{Kind: envelope.KindQuestion, Body: "lunch?"}},
	} {
		if _, err := m.from.SendConv(tctx(t), conv, m.out); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the laptop to have the conversation", func() bool { return len(convBodies(t, w.alice, conv)) > 0 })
	}
	eventually(t, "the laptop to hold all three", func() bool { return len(convBodies(t, w.alice, conv)) == 3 })
	phone := linked(t, w.alice)
	eventually(t, "the history on the phone", func() bool {
		return strings.Join(convBodies(t, phone, conv), "|") == "in:hello alice|out:hi bob|in:lunch?"
	})
	msgs, _ := phone.ConversationMessages(conv)
	for _, m := range msgs {
		if !m.History || m.SyncedFrom != w.alice.Address || m.Key != "" || m.Job != "" || m.State != "" {
			t.Fatalf("history message %+v", m)
		}
	}
	if len(msgs[1].Attachments) != 1 || msgs[1].Attachments[0].Name != "notes.txt" || msgs[1].Via != w.alice.Address {
		t.Fatalf("the file message: %+v", msgs[1])
	}
	eventually(t, "the snapshot done", func() bool {
		jobs, _ := w.alice.HistoryProgress()
		return len(jobs) == 1 && jobs[0].State == "done" && jobs[0].ConvsDone == 1 && jobs[0].Name == "phone"
	})
	sentBoth, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "now to both"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sent %+v", sentBoth.Copies)
	deadline := time.Now().Add(15 * time.Second)
	for {
		msgs, _ := phone.ConversationMessages(conv)
		if len(msgs) == 4 && !msgs[3].History && msgs[3].Key == w.bob.Self().Fingerprint() {
			break
		}
		if time.Now().After(deadline) {
			for _, m := range msgs {
				t.Logf("%s %s hist=%v key=%s", m.Dir, m.Body, m.History, m.Key)
			}
			t.Fatal("no direct message on the phone")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A message sent to an older roster of the person (the sender did not know
// the new device yet) is forwarded by the device that got it; a copy that
// arrives directly later takes its place.
func TestHistoryForwardsStaleFan(t *testing.T) {
	t.Parallel()
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "first"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first message", func() bool { return len(convBodies(t, w.alice, conv)) == 1 })
	old, _, _ := w.alice.Person()
	phone := linked(t, w.alice)
	eventually(t, "the first message on the phone", func() bool { return len(convBodies(t, phone, conv)) == 1 })
	bobMe, _, _ := w.bob.Person()
	_, raw := rootOf(t, w.alice, conv)
	stale := envelope.Inner{Kind: envelope.KindMessage, Body: "sent to the old roster", Conv: conv, LID: protocol.NewID(), Root: raw,
		Origin: envelope.OriginUI, Fan: []envelope.Fan{{Person: bobMe.Person, Roster: bobMe.Roster}, {Person: old.Person, Roster: old.Roster}}}
	env := craft(t, w.bob, w.alice, stale)
	if err := w.alice.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if n := count(t, w.alice, "outbox WHERE sub = 'history' AND recipient = '"+phone.Address+"'"); n < 2 {
		var errs []string
		rows, _ := w.alice.store.db.Query(`SELECT id, state, coalesce(error,'') FROM outbox WHERE sub = 'history'`)
		for rows.Next() {
			var a, b, c string
			rows.Scan(&a, &b, &c)
			errs = append(errs, a+" "+b+" "+c)
		}
		rows.Close()
		t.Fatalf("history copies at the laptop: %d %v", n, errs)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		msgs, _ := phone.ConversationMessages(conv)
		if len(msgs) == 2 && msgs[1].Body == "sent to the old roster" && msgs[1].History {
			break
		}
		if time.Now().After(deadline) {
			var info []string
			rows, _ := w.alice.store.db.Query(`SELECT id, state, coalesce(error,'') FROM outbox WHERE sub = 'history'`)
			for rows.Next() {
				var a, b, c string
				rows.Scan(&a, &b, &c)
				info = append(info, "out "+a+" "+b+" "+c)
			}
			rows.Close()
			rows, _ = phone.store.db.Query(`SELECT id, reason FROM quarantine`)
			for rows.Next() {
				var a, b string
				rows.Scan(&a, &b)
				info = append(info, "held "+a+" "+b)
			}
			rows.Close()
			for _, m := range msgs {
				info = append(info, "msg "+m.Body)
			}
			t.Fatalf("no forwarded copy: %v", info)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The same message directly, later: it takes the history copy's place.
	direct := stale
	direct.ID = protocol.NewID()
	if err := phone.verifyAndStore(tctx(t), craft(t, w.bob, phone, direct)); err != nil {
		t.Fatal(err)
	}
	msgs, _ := phone.ConversationMessages(conv)
	if len(msgs) != 2 || msgs[1].History || msgs[1].Key != w.bob.Self().Fingerprint() {
		t.Fatalf("after the direct copy: %+v", msgs)
	}
	if n := inboxCount(t, phone, `lid = ?`, stale.LID); n != 1 {
		t.Fatalf("%d rows for one message", n)
	}
}

// A request to this device's agent received directly creates its job
// whether a history copy of it came first or comes later; a history copy
// alone never does.
func TestHistoryNeverSwallowsDirectRequest(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	_, raw := rootOf(t, w.bob, conv)
	for _, historyFirst := range []bool{true, false} {
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion,
			Body: "run this", Conv: conv, LID: protocol.NewID(), Root: raw, PID: protocol.NewID(),
			Target: &envelope.Target{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}}
		key := w.bob.Self().Fingerprint()
		hist := func() {
			h := itemOf(in, key, 0).inner(conv)
			if _, err := w.alice.store.addHistoryInbox(h, 0, key, "admin/laptop", protocol.NewID(), false, nil); err != nil {
				t.Fatal(err)
			}
		}
		if historyFirst {
			hist()
			if n := inboxCount(t, w.alice, `lid = ? AND state = ?`, in.LID, stateAgentWaiting); n != 0 {
				t.Fatal("history made a job")
			}
		}
		if _, err := w.alice.store.addConvInbox(in, key, stateAgentWaiting, false, nil); err != nil {
			t.Fatal(err)
		}
		if !historyFirst {
			hist()
		}
		if n := inboxCount(t, w.alice, `lid = ?`, in.LID); n != 1 {
			t.Fatalf("history first %v: %d rows", historyFirst, n)
		}
		if n := inboxCount(t, w.alice, `lid = ? AND state = ? AND verified_by = ? AND claimed_fp IS NULL`, in.LID, stateAgentWaiting, key); n != 1 {
			t.Fatalf("history first %v: the direct request has no job", historyFirst)
		}
	}
}

// Files in history: the new device asks for them. A file the old device
// sent (its source deleted, the daemon restarted) comes from its kept
// copy; one it received from what it fetched; one it no longer holds is
// answered as unavailable.
func TestHistoryFilesOnRequest(t *testing.T) {
	w := newWorld(t, "")
	stopAlice := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "open"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the laptop to have the conversation", func() bool { return len(convBodies(t, w.alice, conv)) == 1 })
	dir := t.TempDir()
	sentPath, sentData := writeFile(t, dir, "sent.bin", 3000)
	gonePath, _ := writeFile(t, dir, "gone.bin", 2000)
	recvPath, recvData := writeFile(t, dir, "received.bin", 4000)
	for _, s := range []struct {
		from *Agent
		path string
	}{{w.alice, sentPath}, {w.alice, gonePath}, {w.bob, recvPath}} {
		if _, err := s.from.SendConv(tctx(t), conv, ConvOutgoing{Body: filepath.Base(s.path), Files: []OutgoingFile{{Path: s.path}}}); err != nil {
			t.Fatal(err)
		}
	}
	os.Remove(sentPath) // the laptop answers from its kept copy
	eventually(t, "the laptop to hold all, and keep the received file", func() bool {
		msgs, _ := w.alice.ConversationMessages(conv)
		if len(msgs) != 4 {
			return false
		}
		_, err := os.Stat(w.alice.downloadPath(msgs[3].Attachments[0].BlobID))
		return err == nil
	})
	gone, _ := os.ReadFile(gonePath)
	os.Remove(w.alice.keptPath(sha(gone)))
	stopAlice()
	runAgent(t, w.alice)
	phone := linked(t, w.alice)
	var msgs []ConvMessage
	eventually(t, "the history with its files", func() bool {
		msgs, _ = phone.ConversationMessages(conv)
		return len(msgs) == 4 && len(msgs[3].Attachments) == 1 && msgs[3].Attachments[0].Availability == "requestable"
	})
	if _, _, err := phone.OpenAttachment(tctx(t), msgs[1].ID, 0); err == nil {
		t.Fatal("a history file opened before it was asked for")
	} else if !strings.Contains(err.Error(), "request it from "+w.alice.Address+" first") || strings.Contains(err.Error(), "RequestFile") {
		t.Fatalf("the refusal does not say where to ask, in plain words: %v", err)
	}
	for _, i := range []int{1, 2, 3} {
		if err := phone.RequestFile(tctx(t), msgs[i].ID, 0); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "the answers", func() bool {
		m, _ := phone.ConversationMessages(conv)
		return m[1].Attachments[0].Availability == "" && m[2].Attachments[0].Availability == "unavailable" && m[3].Attachments[0].Availability == ""
	})
	for i, want := range map[int][]byte{1: sentData, 3: recvData} {
		r, f, err := phone.OpenAttachment(tctx(t), msgs[i].ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(r)
		r.Close()
		if !bytes.Equal(got, want) || f.SHA256 != sha(want) {
			t.Fatalf("file %d: %d bytes", i, len(got))
		}
	}
}

// Saving a history turn whose files are not all here yet saves the ones
// that are and names each one still to be asked for, instead of a false
// integrity failure: nothing is fetched or locked for a file never asked
// for, and a lock an earlier version left for one goes.
func TestDownloadHistoryFileNotRequestedYet(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	dir := t.TempDir()
	first, _ := writeFile(t, dir, "first.bin", 3000)
	second, secondData := writeFile(t, dir, "second.bin", 4000)
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "two files", Files: []OutgoingFile{{Path: first}, {Path: second}}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the laptop to keep both files", func() bool {
		msgs, _ := w.alice.ConversationMessages(conv)
		if len(msgs) != 1 || len(msgs[0].Attachments) != 2 {
			return false
		}
		for _, f := range msgs[0].Attachments {
			if _, err := os.Stat(w.alice.downloadPath(f.BlobID)); err != nil {
				return false
			}
		}
		return true
	})
	phone := linked(t, w.alice)
	var m ConvMessage
	eventually(t, "the phone's history with both files", func() bool {
		msgs, _ := phone.ConversationMessages(conv)
		if len(msgs) != 1 || len(msgs[0].Attachments) != 2 {
			return false
		}
		m = msgs[0]
		return true
	})
	stale := phone.downloadPath(m.Attachments[0].BlobID) + ".lock"
	if !strings.HasPrefix(m.Attachments[0].BlobID, historyBlob) || os.MkdirAll(filepath.Dir(stale), 0o700) != nil || os.WriteFile(stale, nil, 0o600) != nil {
		t.Fatalf("history placeholder %+v", m.Attachments[0])
	}
	if err := phone.RequestFile(tctx(t), m.ID, 1); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second file here", func() bool {
		msgs, _ := phone.ConversationMessages(conv)
		return len(msgs) == 1 && msgs[0].Attachments[1].Availability == "" && !strings.HasPrefix(msgs[0].Attachments[1].BlobID, historyBlob)
	})
	out := t.TempDir()
	saved, err := phone.Download(tctx(t), m.ID, out, false)
	if err == nil || !strings.Contains(err.Error(), "first.bin: this file came with the conversation's history") || strings.Contains(err.Error(), "integrity") {
		t.Fatalf("download before the first file was asked for: %v", err)
	}
	if len(saved) != 1 || filepath.Base(saved[0]) != "second.bin" {
		t.Fatalf("saved %v", saved)
	}
	if got, _ := os.ReadFile(saved[0]); !bytes.Equal(got, secondData) {
		t.Fatal("the second file differs")
	}
	assertOnlyFiles(t, out, "second.bin")
	if left, _ := filepath.Glob(filepath.Join(phone.home, "downloads", historyBlob+"*")); len(left) != 0 {
		t.Fatalf("left for a file never asked for: %v", left)
	}
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// The page's history progress must not wait on itself: the store has one
// connection, so counting a job's position while its rows are still being
// read never gets one. Here a job with a position (a running or ended one).
func TestHistoryProgressWithOneConnection(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{Address: "alice/laptop", store: s}
	for _, id := range []string{"conv-a", "conv-b"} {
		if _, err := s.db.Exec(`INSERT INTO conversations(id, root, kind, peer, pinned_at) VALUES(?, '', 'dm', '', 0)`, id); err != nil {
			t.Fatal(err)
		}
	}
	pos, _ := json.Marshal(historyPos{Conv: "conv-b"})
	if _, err := s.db.Exec(`INSERT INTO history_jobs(device, fingerprint, pos, convs_total, state, created_at, updated_at) VALUES('alice/phone', 'fp', ?, 2, 'ended', 1, 1)`, string(pos)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		jobs []HistoryJob
		err  error
	}
	got := make(chan result, 1)
	go func() { jobs, err := a.HistoryProgress(); got <- result{jobs, err} }()
	select {
	case r := <-got:
		if r.err != nil || len(r.jobs) != 1 || r.jobs[0].Name != "phone" || r.jobs[0].State != "ended" || r.jobs[0].ConvsDone != 1 || r.jobs[0].ConvsTotal != 2 {
			t.Fatalf("progress = %+v, %v", r.jobs, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HistoryProgress waits for a second connection while its own rows hold the only one")
	}
}
