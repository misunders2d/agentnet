package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestDeviceHistoryFollowupRequestDirections(t *testing.T) {
	w, laptop, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	stop := runAgent(t, a)
	reader, awaited, _ := linkPhone(t, a, "followup-reader")
	pending := pendingLink(t, a)
	stop()
	if err := a.DecideLink(tctx(t), pending.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := <-awaited; r.err != nil {
		t.Fatal(r.err)
	}

	receive := func(kind, reply string) string {
		env := sealTo(t, laptop, a, envelope.Inner{Kind: kind, Body: "retained request", ReplyTo: reply})
		if err := a.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return env.ID
	}
	answer := func(reply string) string {
		sent, err := a.SendMessage(tctx(t), Outgoing{To: laptop.Address, Kind: envelope.KindAnswer, Body: "retained response", ReplyTo: reply})
		if err != nil {
			t.Fatal(err)
		}
		return sent.ID
	}
	first := receive(envelope.KindQuestion, "")
	response := answer(first)
	follow := receive(envelope.KindQuestion, response)
	final := answer(follow)
	task := receive(envelope.KindTask, final)
	peerRequest, err := a.SendMessage(tctx(t), Outgoing{To: laptop.Address, Kind: envelope.KindQuestion, Body: "peer counter-question", ReplyTo: task})
	if err != nil {
		t.Fatal(err)
	}
	peerTask, err := a.SendMessage(tctx(t), Outgoing{To: laptop.Address, Kind: envelope.KindTask, Body: "peer task", ReplyTo: peerRequest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.deviceHistoryPage(reader.Self()); err != nil {
		t.Fatal(err)
	}
	if n := deliverDeviceHistory(t, a, reader, envelope.SubDeviceHistory); n != 7 {
		t.Fatalf("copies=%d", n)
	}
	for _, id := range []string{follow, task} {
		var direction, fromKey, recipientKey string
		if err := reader.store.db.QueryRow(`SELECT direction,author_fp,recipient_fp FROM device_history_rows WHERE id=?`, id).Scan(&direction, &fromKey, &recipientKey); err != nil || direction != "out" || fromKey != laptop.Self().Fingerprint() || recipientKey != a.Self().Fingerprint() {
			t.Fatalf("fresh copy direction/key changed: %s %s %v", id, direction, err)
		}
	}
	check := func() {
		t.Helper()
		expected := map[string]string{first: "out", response: "in", follow: "out", final: "in", task: "out", peerRequest.ID: "in", peerTask.ID: "in"}
		replies := map[string]string{first: "", response: first, follow: response, final: follow, task: final, peerRequest.ID: task, peerTask.ID: peerRequest.ID}
		// Original history keeps all seven reply-linked records. Display topics
		// separately retain the host and the laptop's execution identities.
		const snapshot = `SELECT json_object('body',i.body,'reply_to',i.reply_to,'sender',i.sender,'kind',i.kind,'ts',i.ts,'target',i.target,'agent_id',i.agent_id,'recipient',h.recipient,'recipient_fp',h.recipient_fp,'peer',h.peer,'direction',h.direction,'author_fp',h.author_fp,'hash',h.hash,'reply',h.reply_to,'at',h.at) FROM inbox i JOIN device_history_rows h ON h.storage='in' AND h.id=i.id WHERE i.id=?`
		retained := map[string]string{}
		for id := range expected {
			var row string
			if err := reader.store.db.QueryRow(snapshot, id).Scan(&row); err != nil {
				t.Fatal(err)
			}
			retained[id] = row
		}
		thread, err := reader.Conversation(first, 0, 0)
		if err != nil || thread.Peer != a.Address || len(thread.Messages) != 7 {
			t.Fatalf("thread=%+v err=%v", thread, err)
		}
		originalIDs := map[string]bool{}
		for _, m := range thread.Messages {
			if originalIDs[m.ID] || m.Dir != expected[m.ID] || (!m.History || m.State != "") || m.ReplyTo != replies[m.ID] {
				t.Fatalf("id=%s dir=%s history=%v state=%s", m.ID, m.Dir, m.History, m.State)
			}
			originalIDs[m.ID] = true
			if m.From != laptop.Address && m.From != a.Address && m.From != reader.Address {
				t.Fatalf("original sender changed: %s", m.From)
			}
		}
		seen := map[string]bool{}
		for _, group := range []struct {
			anchor, host, key string
			ids               []string
		}{{first, a.Address, a.Self().Fingerprint(), []string{first, response, follow, final, task}}, {peerRequest.ID, laptop.Address, laptop.Self().Fingerprint(), []string{peerRequest.ID, peerTask.ID}}} {
			topic, err := reader.TopicOf(group.anchor)
			if err != nil || topic.AgentHost != group.host || topic.AgentKey != group.key || topic.AgentID != "" || !topic.AgentLegacy {
				t.Fatalf("original execution identity: %+v err=%v", topic, err)
			}
			display, err := reader.DisplayConversation(group.anchor, 0, 0)
			if err != nil || display.Peer != a.Address || len(display.Messages) != len(group.ids) {
				t.Fatalf("display=%+v err=%v", display, err)
			}
			wanted := map[string]bool{}
			for _, id := range group.ids {
				wanted[id] = true
			}
			for _, m := range display.Messages {
				if !wanted[m.ID] || seen[m.ID] || m.Dir != expected[m.ID] || !m.History || m.State != "" || m.ReplyTo != replies[m.ID] || m.SyncedFrom != a.Address {
					t.Fatalf("display changed original: %+v", m)
				}
				fromKey := a.Self().Fingerprint()
				if m.Dir == "out" {
					fromKey = laptop.Self().Fingerprint()
				}
				if m.FromKey != fromKey {
					t.Fatalf("original sender key changed: %s", m.ID)
				}
				seen[m.ID] = true
			}
		}
		if len(seen) != len(expected) || len(originalIDs) != len(expected) {
			t.Fatalf("missing originals: display=%v original=%v", seen, originalIDs)
		}
		for id, before := range retained {
			var after string
			if err := reader.store.db.QueryRow(snapshot, id).Scan(&after); err != nil || after != before {
				t.Fatalf("read changed retained original or history mapping: %s err=%v", id, err)
			}
		}
	}
	check()
	// Reproduce the previously shipped saved projection. Reading corrects it
	// without mutating the immutable original or the retained mapping.
	if _, err := reader.store.db.Exec(`UPDATE device_history_rows SET direction='in' WHERE id IN (?,?)`, follow, task); err != nil {
		t.Fatal(err)
	}
	check()
	var saved string
	if err := reader.store.db.QueryRow(`SELECT direction FROM device_history_rows WHERE id=?`, follow).Scan(&saved); err != nil || saved != "in" {
		t.Fatalf("read rewrote saved data: %s %v", saved, err)
	}
}
