package hub

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A decision can commit after authentication read Pending but before the
// stream subscribes. Its notification then has no subscriber to wake.
func TestPendingStreamSeesDecisionBeforeSubscription(t *testing.T) {
	for _, decision := range []string{"refused", "linked"} {
		t.Run(decision, func(t *testing.T) {
			h, owner, _, _ := blobHub(t, 1<<30)
			head := personOf(t, h, owner)
			offer := protocol.NewID()
			secret := deviceInvite(t, h, owner, offer)
			id, err := identity.Generate()
			if err != nil {
				t.Fatal(err)
			}
			label, _, _ := protocol.SplitAddress(owner.addr)
			phone := member{id: id, addr: label + "/phone"}
			if c, e := joinLinked(t, h, secret, phone.addr, offer, head, id); c != http.StatusCreated {
				t.Fatalf("join: %d %+v", c, e)
			}
			if decision == "refused" {
				if c, b := owner.call(t, h, "POST", "/v1/person/device-refuse", protocol.DeviceRefusal{Address: phone.addr}); c != http.StatusNoContent {
					t.Fatalf("refuse: %d %s", c, b)
				}
			} else {
				r := step(head, owner, &phone, owner.id.Public(owner.addr), phone.id.Public(phone.addr))
				if c, e := put(t, h, owner, r); c != http.StatusNoContent {
					t.Fatalf("activate: %d %+v", c, e)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sub := h.streams.add(phone.addr, cancel)
			defer h.streams.remove(phone.addr, sub)
			ping := time.NewTicker(time.Hour) // a decision must not wait for a heartbeat
			defer ping.Stop()
			done := make(chan bool, 1)
			go func() {
				linked := false
				h.holdPending(ctx, phone.addr, sub, ping, 2*time.Hour, func(format string, _ ...any) bool {
					linked = format == "event: linked\ndata: {}\n\n"
					return true
				})
				done <- linked
			}()
			select {
			case linked := <-done:
				if linked != (decision == "linked") {
					t.Fatalf("decision %s, linked event %v", decision, linked)
				}
			case <-time.After(2 * time.Second):
				cancel()
				<-done
				t.Fatal("decision before subscription was lost until the heartbeat")
			}
		})
	}
}
