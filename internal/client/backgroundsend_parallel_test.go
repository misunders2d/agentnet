package client

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

type slowAttachmentRecipient struct {
	base    http.RoundTripper
	mu      sync.Mutex
	to      string
	started chan string
	release <-chan struct{}
}

func (p *slowAttachmentRecipient) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "POST" && r.URL.Path == "/v1/blobs" {
		body, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		var reserve protocol.BlobReserve
		err = json.NewDecoder(body).Decode(&reserve)
		body.Close()
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		first := p.to == ""
		if first {
			p.to = reserve.Recipient
		}
		slow := p.to == reserve.Recipient
		p.mu.Unlock()
		if first {
			p.started <- reserve.Recipient
		}
		if slow {
			select {
			case <-p.release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
	}
	return p.base.RoundTrip(r)
}

func TestQueuedGroupAttachmentSlowRecipientDoesNotBlockOthers(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	stops[w.alice]()
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	p := &slowAttachmentRecipient{base: w.alice.hub.http.Transport, started: make(chan string, 1), release: release}
	w.alice.hub.http.Transport = p
	path, _ := writeFile(t, t.TempDir(), "group-picture.jpg", 641<<10)
	start := time.Now()
	sent, err := w.alice.SendConv(WithQueuedSend(tctx(t), protocol.NewID()), packet.State.Conv, ConvOutgoing{Files: []OutgoingFile{{Path: path, Name: "group-picture.jpg"}}})
	if err != nil {
		t.Fatal(err)
	}
	queued := time.Since(start)
	var slow string
	select {
	case slow = <-p.started:
	case <-time.After(time.Second):
		t.Fatal("background attachment upload never began")
	}
	var healthy string
	for _, cp := range sent.Copies {
		if cp.To != slow {
			healthy = cp.ID
			break
		}
	}
	if healthy == "" {
		t.Fatal("fixture has no independent group recipient")
	}
	deadline := time.Now().Add(time.Second)
	for {
		state := outboxState(t, w.alice, healthy)
		if state == protocol.StateCustody || state == protocol.StateDelivered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slow recipient %s blocked independent copy in %s; queued after %s", slow, state, queued)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// An unrelated new message must also use a free slot while the older
	// recipient upload is still stalled, rather than await the whole snapshot.
	var healthyTo string
	for _, cp := range sent.Copies {
		if cp.ID == healthy {
			healthyTo = cp.To
		}
	}
	text, err := w.alice.Send(WithQueuedSend(tctx(t), protocol.NewID()), healthyTo, "concurrent live message", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for {
		state := outboxState(t, w.alice, text.ID)
		if state == protocol.StateCustody || state == protocol.StateDelivered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new independent messaging waited for stalled upload", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Logf("641KiB group file prepared+queued=%s healthy-recipient-custody=%s while another upload remains blocked", queued, time.Since(start))
	once.Do(func() { close(release) })
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var copies int
	if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND conv=?`, sent.LID, packet.State.Conv).Scan(&copies); err != nil || copies != len(sent.Copies) {
		t.Fatal("group retry changed physical copies", copies, err)
	}
}
