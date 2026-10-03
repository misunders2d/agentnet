package client

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/identity"
)

// convCopiesOf counts the outbox copies of a sent turn by its text.
func convCopiesOf(t *testing.T, a *Agent, body string) int {
	t.Helper()
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE body = ?`, body).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// BUG-11: a DM turn that no device of the other member can get fails with
// the real cause (revoked), as a device send does; it is never stored for
// the sender's own devices only and reported as sent. A revoked device of
// a member who has another still current is left out, and the turn goes.
func TestDMToRevokedMemberFails(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	linked(t, w.alice) // the sender's own other device still gets a copy
	bobPhone := linked(t, w.bob)
	conv := newDM(t, w.alice, w.bob)
	if err := w.alice.Revoke(tctx(t), bobPhone.Address); err != nil {
		t.Fatal(err)
	}
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "to the laptop"})
	if err != nil {
		t.Fatal(err)
	}
	to := map[string]bool{}
	for _, c := range sent.Copies {
		to[c.To] = true
	}
	if !to[w.bob.Address] || to[bobPhone.Address] {
		t.Fatalf("copies %+v", sent.Copies)
	}
	if err := w.alice.Revoke(tctx(t), w.bob.Address); err != nil {
		t.Fatal(err)
	}
	sent, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "to nobody"})
	if !errors.Is(err, ErrPeerRevoked) || !strings.Contains(err.Error(), w.bob.Address) {
		t.Fatalf("send to a revoked member: %+v %v", sent, err)
	}
	if n := convCopiesOf(t, w.alice, "to nobody"); n != 0 {
		t.Fatalf("%d copies stored", n)
	}
}

// BUG-11: a changed key of any device of the other member blocks a DM
// turn until it is trusted, as it blocks a device send; the turn is not
// sent to the member's other devices with that one silently left out.
func TestDMToChangedKeyFails(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	linked(t, w.alice)
	linked(t, w.bob) // a current device of bob's that would still get a copy
	conv := newDM(t, w.alice, w.bob)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "before"}); err != nil {
		t.Fatal(err)
	}
	other, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pinned, _ := json.Marshal(other.Public(w.bob.Address)) // as if the directory now offered a new key
	if _, err := w.alice.store.db.Exec(`UPDATE peers SET public = ? WHERE address = ?`, string(pinned), w.bob.Address); err != nil {
		t.Fatal(err)
	}
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "after"})
	var changed *KeyChangedError
	if !errors.As(err, &changed) || changed.Address != w.bob.Address || !strings.Contains(err.Error(), "agentnet trust") {
		t.Fatalf("send past a changed key: %+v %v", sent, err)
	}
	if n := convCopiesOf(t, w.alice, "after"); n != 0 {
		t.Fatalf("%d copies stored", n)
	}
}
