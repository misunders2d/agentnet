package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReceiptLogAtomicAndSenderOnly(t *testing.T) {
	h, a, b, c := blobHub(t, 1<<30)
	id := sendMessage(t, h, a, b)
	for _, state := range []string{"quarantined", "quarantined", "delivered", "quarantined"} {
		if code, body := b.call(t, h, "POST", "/v1/messages/"+id+"/ack", protocol.AckRequest{State: state}); code != 200 {
			t.Fatalf("%d %s", code, body)
		}
	}
	rows, err := h.store.receiptsFor(a.addr, 0)
	if err != nil || len(rows) != 2 || rows[0].State != "quarantined" || rows[1].State != "delivered" || rows[0].Seq >= rows[1].Seq {
		t.Fatalf("%+v %v", rows, err)
	}
	other, err := h.store.receiptsFor(c.addr, 0)
	if err != nil || len(other) != 0 {
		t.Fatalf("foreign receipts: %+v %v", other, err)
	}
	// Session expiry logs every message with its own sequence, exactly once.
	ids := []string{sendMessage(t, h, a, b), sendMessage(t, h, a, b), sendMessage(t, h, c, b)}
	for _, id := range ids {
		if _, err = h.store.db.Exec("UPDATE messages SET session='ended',fallback=0 WHERE id=?", id); err != nil {
			t.Fatal(err)
		}
	}
	senders, err := h.store.expireSession(b.addr, "ended")
	if err != nil || len(senders) != 2 {
		t.Fatalf("%v %v", senders, err)
	}
	expired, err := h.store.receiptsFor(a.addr, 2)
	if err != nil || len(expired) != 2 || expired[0].Seq != 3 || expired[1].Seq != 4 {
		t.Fatalf("expiry sequence: %+v %v", expired, err)
	}
	max, _ := h.store.receiptMax(a.addr)
	if max != 4 {
		t.Fatalf("max=%d", max)
	}
	h.store.expireSession(b.addr, "ended")
	again, _ := h.store.receiptMax(a.addr)
	if again != max {
		t.Fatal("expiry replay appended duplicate receipts")
	}
	for i := 0; i < protocol.ReceiptBatch+3; i++ {
		if _, err = h.store.db.Exec("INSERT INTO receipts(sender,id,state,seq) SELECT ?,?,'delivered',coalesce(max(seq),0)+1 FROM receipts WHERE sender=?", a.addr, protocol.NewID(), a.addr); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := h.store.receiptsFor(a.addr, 0)
	if err != nil || len(batch) != protocol.ReceiptBatch {
		t.Fatalf("batch %d %v", len(batch), err)
	}
	rest, err := h.store.receiptsFor(a.addr, batch[len(batch)-1].Seq)
	if err != nil || len(rest) != 7 {
		t.Fatalf("rest %d %v", len(rest), err)
	}
}
func TestReceiptStreamOptInAndCursor(t *testing.T) {
	h, a, b, c := blobHub(t, 1<<30)
	// Another sender's activity must not affect this sender's clamp or sequence.
	for i := 0; i < 4; i++ {
		foreign := sendMessage(t, h, c, b)
		b.call(t, h, "POST", "/v1/messages/"+foreign+"/ack", protocol.AckRequest{State: "delivered"})
	}
	id := sendMessage(t, h, a, b)
	b.call(t, h, "POST", "/v1/messages/"+id+"/ack", protocol.AckRequest{State: "delivered"})
	srv := httptest.NewTLSServer(h.routes())
	defer srv.Close()
	for _, tc := range []struct {
		name   string
		member member
		query  string
		want   bool
		code   int
	}{{"sender", a, "&receipts=0", true, 200}, {"foreign", c, "&receipts=4", false, 200}, {"absent", a, "", false, 200}, {"restore clamp", a, "&receipts=999", true, 200}, {"sender clamp", a, "&receipts=2", true, 200}, {"negative", a, "&receipts=-1", false, 400}, {"empty", a, "&receipts=", false, 400}, {"non decimal", a, "&receipts=1.2", false, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
			defer cancel()
			ad := protocol.SessionAd{Address: tc.member.addr, Session: protocol.NewID()}
			protocol.SignAd(&ad, tc.member.id.Sign)
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/stream?ad="+ad.Encode()+tc.query, nil)
			protocol.SignRequest(req, tc.member.addr, tc.member.id.Sign, nil)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.code {
				t.Fatalf("code %d", resp.StatusCode)
			}
			if tc.code != 200 {
				return
			}
			sc := bufio.NewScanner(resp.Body)
			got := false
			event := ""
			for sc.Scan() {
				line := sc.Text()
				if strings.HasPrefix(line, "event: ") {
					event = strings.TrimPrefix(line, "event: ")
				}
				if event == "receipt" && strings.HasPrefix(line, "data: ") {
					var r protocol.ReceiptEvent
					if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &r); err != nil || r.ID != id || r.State != "delivered" || r.Seq != 1 {
						t.Fatalf("%+v %v", r, err)
					}
					got = true
					break
				}
			}
			if got != tc.want {
				t.Fatalf("receipt=%v want %v", got, tc.want)
			}
		})
	}
}

func TestReceiptSequencesArePrivatePerSender(t *testing.T) {
	h, a, b, c := blobHub(t, 1<<30)
	for _, sender := range []member{a, c, c, a} {
		id := sendMessage(t, h, sender, b)
		b.call(t, h, "POST", "/v1/messages/"+id+"/ack", protocol.AckRequest{State: "delivered"})
	}
	for _, sender := range []member{a, c} {
		rows, err := h.store.receiptsFor(sender.addr, 0)
		if err != nil || len(rows) != 2 || rows[0].Seq != 1 || rows[1].Seq != 2 {
			t.Fatalf("%s: %+v %v", sender.addr, rows, err)
		}
	}
}
