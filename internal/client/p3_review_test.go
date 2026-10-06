package client

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestReceiptReplayResetsRestoredCursor(t *testing.T) {
	w := newWorld(t, "")
	if err := w.alice.store.setConfig(map[string]string{"receipt_cursor": "100"}); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.store.applyReceipt(protocol.ReceiptEvent{ID: protocol.NewID(), State: "delivered", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.alice.store.config("receipt_cursor"); got != "1" {
		t.Fatal(got)
	}
}

func TestRestoredReceiptCursorReplaysDisconnectedDeliveries(t *testing.T) {
	w := newWorld(t, "")
	if err := w.alice.store.setConfig(map[string]string{"receipt_cursor": "4"}); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.bob)
	stop := runAgent(t, w.alice)
	first, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "after restore"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "restored stream applies its lower receipt", func() bool {
		state, _, _, _ := w.alice.store.outboxState(first.ID)
		cursor, _ := w.alice.store.config("receipt_cursor")
		return state == "delivered" && cursor == "1"
	})
	stop()
	var ids []string
	for i := 0; i < 4; i++ {
		sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "while sender disconnected"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sent.ID)
	}
	eventually(t, "recipient stored all disconnected copies", func() bool {
		for _, id := range ids {
			var acked int
			w.bob.store.db.QueryRow("SELECT acked FROM inbox WHERE id=?", id).Scan(&acked)
			if acked != 1 {
				return false
			}
		}
		return true
	})
	runAgent(t, w.alice)
	eventually(t, "every offline receipt is replayed", func() bool {
		for _, id := range ids {
			state, _, _, _ := w.alice.store.outboxState(id)
			if state != "delivered" {
				return false
			}
		}
		return true
	})
}

func TestReceiptCannotDowngradeTerminalDelivery(t *testing.T) {
	w := newWorld(t, "")
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "receipt order"})
	if err != nil {
		t.Fatal(err)
	}
	for _, terminal := range []string{"delivered", "expired", "quarantined"} {
		if _, err := w.alice.store.db.Exec("UPDATE outbox SET state=? WHERE id=?", terminal, sent.ID); err != nil {
			t.Fatal(err)
		}
		for _, stale := range []string{"queued", "custody", "waiting", "failed", "not_delivered", "quarantined", "expired"} {
			if err := w.alice.store.setOutboxState(sent.ID, stale, "stale response", ""); err != nil {
				t.Fatal(err)
			}
			if got, _, _, _ := w.alice.store.outboxState(sent.ID); got != terminal {
				t.Fatalf("%s -> %s by %s", terminal, got, stale)
			}
		}
	}
	if err := w.alice.store.applyReceipt(protocol.ReceiptEvent{ID: sent.ID, State: "delivered", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if got, _, _, _ := w.alice.store.outboxState(sent.ID); got != "delivered" {
		t.Fatal(got)
	}
}

func TestPlainDMQuoteResolvesOnLinkedDevice(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	phone, awaited, _ := linkPhone(t, w.alice, "quote-phone")
	request := pendingLink(t, w.alice)
	if err := w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	conv := newDM(t, w.alice, w.bob)
	first := sendConv(t, w.alice, conv, ConvOutgoing{Body: "quoted parent"})
	var local string
	eventually(t, "parent on peer", func() bool {
		rows, _ := w.bob.ConversationMessages(conv)
		for _, m := range rows {
			if m.LID == first.LID {
				local = m.ID
				return true
			}
		}
		return false
	})
	quoted := sendConv(t, w.bob, conv, ConvOutgoing{Body: "explicit quote", Quote: local})
	for _, a := range []*Agent{w.alice, phone} {
		eventually(t, "quote on "+a.Address, func() bool {
			rows, _ := a.ConversationMessages(conv)
			parent, quote := false, false
			for _, m := range rows {
				parent = parent || m.LID == first.LID
				quote = quote || m.LID == quoted.LID && m.Quote == first.LID
			}
			return parent && quote
		})
	}
	// The recipient person was captured at send time, before a roster drops it.
	if _, err := w.bob.store.db.Exec("UPDATE outbox SET state=CASE WHEN recipient=? THEN 'delivered' ELSE 'custody' END WHERE lid=?", w.alice.Address, quoted.LID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.store.db.Exec("DELETE FROM person_devices WHERE address=?", phone.Address); err != nil {
		t.Fatal(err)
	}
	rows, err := w.bob.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rows {
		if m.LID == quoted.LID {
			if m.Delivery != "delivered" {
				t.Fatalf("removed device changed delivery: %+v", m.Copies)
			}
			return
		}
	}
	t.Fatal("quoted message missing")
}

func TestHistoryAndExcerptQuoteValidation(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	root, _, _, err := w.bob.store.conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../ui/testdata/history_quote_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name   string
		Fields map[string]any
		Valid  bool
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			h := HistoryItem{V: 1, ID: strings.Repeat("1", 32), LID: protocol.NewID(), From: w.alice.Address, FromKey: w.alice.Self().Fingerprint(), TS: time.Now().Unix(), At: time.Now().UnixMilli(), Kind: "message", Body: "history quote", Quote: strings.Repeat("3", 32), Origin: "ui"}
			raw, _ := json.Marshal(h)
			var fields map[string]any
			json.Unmarshal(raw, &fields)
			for k, value := range v.Fields {
				fields[k] = value
			}
			raw, _ = json.Marshal(fields)
			json.Unmarshal(raw, &h)
			carrier := envelope.Inner{ID: protocol.NewID(), From: w.bob.Address, Conv: conv, Body: string(raw), Sub: envelope.SubHistory}
			held := ""
			err := w.bob.admitHistory(tctx(t), envelope.Envelope{ID: carrier.ID, From: carrier.From}, carrier, root, func(reason, detail string) error { held = reason; return errors.New(detail) }, false)
			if v.Valid && (err != nil || held != "") || !v.Valid && (err == nil || held != reasonInvalid) {
				t.Fatalf("history: held=%s err=%v", held, err)
			}
			info := ParticipationInfo{Grant: []protocol.GrantRef{{LID: h.LID, Fingerprint: h.FromKey}}}
			_, err = parseGrantedExcerpt(carrier, info)
			if (err == nil) != v.Valid {
				t.Fatalf("excerpt: valid=%v err=%v", v.Valid, err)
			}
		})
	}
}

func TestGuestCheckIgnoresOfflineMemberDevice(t *testing.T) {
	w, guest, conv, _, _ := humanWorld(t)
	phone, awaited, _ := linkPhone(t, w.alice, "support-phone")
	request := pendingLink(t, w.alice)
	if err := w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	stop := runAgent(t, phone)
	humanTestCaps(t, phone)
	stop()
	support, err := w.alice.HumanInviteSupport(tctx(t), conv, guest.Address)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range support {
		if p.State == "offline" {
			t.Fatalf("live host blocked by an offline member device: %+v", support)
		}
	}
}
