package hub

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func sendMessage(t *testing.T, h *Hub, from, to member) string {
	t.Helper()
	r, _ := to.id.Public(to.addr).Recipient()
	env, err := envelope.Seal(envelope.Inner{ID: protocol.NewID(), From: from.addr, To: to.addr, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "x"}, from.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if c, b := from.call(t, h, "POST", "/v1/messages", env); c != http.StatusAccepted {
		t.Fatalf("post: %d %s", c, b)
	}
	return env.ID
}

func waitState(t *testing.T, h *Hub, m member, id, timeout string) (int, string) {
	c, body := m.call(t, h, "GET", "/v1/messages/"+id+"/wait?timeout="+timeout, nil)
	var r protocol.Receipt
	json.Unmarshal(body, &r)
	return c, r.State
}

func TestReceiptWait(t *testing.T) {
	h, alice, bob, carol := blobHub(t, 1<<30)

	id := sendMessage(t, h, alice, bob)
	start := time.Now()
	if c, s := waitState(t, h, alice, id, "200ms"); c != 200 || s != protocol.StateCustody || time.Since(start) < 200*time.Millisecond {
		t.Fatalf("timed-out wait: %d %s after %s", c, s, time.Since(start))
	}
	if c, _ := waitState(t, h, carol, id, "1s"); c != http.StatusNotFound {
		t.Fatalf("foreign agent waited on the message: %d", c)
	}
	if c, _ := waitState(t, h, alice, id, "soon"); c != http.StatusBadRequest {
		t.Fatalf("bad timeout: %d", c)
	}

	// The receipt wakes the waiter; acknowledging right after the wait
	// starts must never be missed (subscribe before reading).
	for i := 0; i < 20; i++ {
		id := sendMessage(t, h, alice, bob)
		var wg sync.WaitGroup
		wg.Add(1)
		var state string
		var took time.Duration
		go func() {
			defer wg.Done()
			t0 := time.Now()
			_, state = waitState(t, h, alice, id, "10s")
			took = time.Since(t0)
		}()
		if c, _ := bob.call(t, h, "POST", "/v1/messages/"+id+"/ack", protocol.AckRequest{State: protocol.StateDelivered}); c != 200 {
			t.Fatalf("ack: %d", c)
		}
		wg.Wait()
		if state != protocol.StateDelivered || took > 5*time.Second {
			t.Fatalf("round %d: %s after %s", i, state, took)
		}
	}
	// Already delivered: answered at once, also to the recipient.
	bob.call(t, h, "POST", "/v1/messages/"+id+"/ack", protocol.AckRequest{State: protocol.StateDelivered})
	start = time.Now()
	if _, s := waitState(t, h, bob, id, "10s"); s != protocol.StateDelivered || time.Since(start) > time.Second {
		t.Fatalf("wait on a delivered message: %s after %s", s, time.Since(start))
	}
}
