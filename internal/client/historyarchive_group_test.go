package client

import (
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestHistoryArchiveGroupStructuralJourney(t *testing.T) {
	_, a, phone, packet := groupHistoryLinkedFixture(t)
	conv := packet.State.Conv
	stopPhone := runAgent(t, phone)
	label, name, _ := protocol.SplitAddress(phone.Address)
	eventually(t, "registered phone archive capability", func() bool {
		var profile protocol.Profile
		err := phone.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile)
		return err == nil && profile.Supports(phone.Address, phone.Self().SignKey, protocol.CapHistoryArchive)
	})
	stopPhone()
	sent, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "archive immutable group original"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := a.RefOf(conv, sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Revise(tctx(t), ref, "archive final group revision"); err != nil {
		t.Fatal(err)
	}
	if err = phone.store.pin(a.Self()); err != nil {
		t.Fatal(err)
	}

	for page := 0; page < 10; page++ {
		more, e := a.historyCatchupPage(tctx(t), phone.Self())
		if e != nil {
			t.Fatal(e)
		}
		if !more {
			break
		}
		if page == 9 {
			t.Fatal("bounded group source did not converge")
		}
	}
	var structural int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND state=? AND sub IN ('group-proof','group-context')`, phone.Address, archiveStaged).Scan(&structural); err != nil {
		t.Fatal(err)
	}
	if structural < 2 {
		t.Fatalf("missing staged signed proof/context: %d", structural)
	}
	if _, err = a.Cleanup(false); err != nil {
		t.Fatal(err)
	}
	if err = a.archivePack(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Cleanup(false); err != nil {
		t.Fatal(err)
	}
	if _, err = a.archiveStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	env := archiveDescriptor(t, a)
	inner, err := envelope.Open(env, phone.id, phone.Address, a.Self())
	if err != nil {
		t.Fatal(err)
	}
	var manifest protocol.HistoryArchive
	if err = json.Unmarshal([]byte(inner.Body), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Count < structural+2 {
		t.Fatalf("archive omitted original/revision or prerequisites: %d", manifest.Count)
	}
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err = phone.storeReceived(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.archiveImportStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var done, held, childACK int
	if err = phone.store.db.QueryRow(`SELECT done FROM history_archive_jobs WHERE id=?`, env.ID).Scan(&done); err != nil || done != 1 {
		t.Fatalf("group archive incomplete: %d %v", done, err)
	}
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM quarantine`).Scan(&held); err != nil || held != 0 {
		t.Fatalf("group archive left held children: %d %v", held, err)
	}
	receipts, err := phone.store.unsentReceipts()
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range receipts {
		var imported int
		if err = phone.store.db.QueryRow(`SELECT count(*) FROM history_archive_children WHERE id=?`, receipt.id).Scan(&imported); err != nil {
			t.Fatal(err)
		}
		childACK += imported
	}
	if childACK != 0 {
		t.Fatalf("archive fabricated %d child relay receipts", childACK)
	}

	messages, err := phone.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range messages {
		if m.Body == "archive immutable group original" && m.Text == "archive final group revision" && m.Edited {
			found = true
			if !m.History || m.Exec != nil {
				t.Fatalf("historical original became live execution: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("fresh phone did not render exact group revision")
	}
	if err = phone.flushReceipts(tctx(t)); err != nil {
		t.Fatal(err)
	}
	status, err := a.Status(tctx(t), env.ID, 0)
	if err != nil || status.State != protocol.StateDelivered {
		t.Fatalf("descriptor retention unproven: %+v %v", status, err)
	}
	if err = a.store.applyReceipt(protocol.ReceiptEvent{ID: env.ID, State: status.State, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	var before, after int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub IN ('history','group-proof','group-context','history-archive')`, phone.Address).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for wake := 0; wake < 3; wake++ {
		members, e := a.Members(tctx(t))
		if e != nil {
			t.Fatal(e)
		}
		raw, e := json.Marshal(members)
		if e != nil {
			t.Fatal(e)
		}
		a.onMembers(raw)
		if err = a.reconcileHistory(); err != nil {
			t.Fatal(err)
		}
		if _, err = a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
			t.Fatal(err)
		}
		a.historyStep(tctx(t))
		if _, err = a.syncRoots(); err != nil {
			t.Fatal(err)
		}
		if err = a.recoverInvalidGroupHistory(); err != nil {
			t.Fatal(err)
		}
		if _, err = a.archiveStep(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub IN ('history','group-proof','group-context','history-archive')`, phone.Address).Scan(&after); err != nil || after != before {
		t.Fatalf("duplicate wakes created new group children: %d -> %d %v", before, after, err)
	}
}
