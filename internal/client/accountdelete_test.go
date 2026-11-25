package client

import (
	"slices"
	"strings"
	"testing"
)

// A person deletes their own account from the app: the relay revokes every
// device of theirs, agent hosts included, and refuses them afterwards. The
// workspace's last admin is refused, and no one names another person.
func TestDeleteAccountRevokesOwnDevices(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.bob)
	worker := linkedVia(t, w.bob, "worker", w.bob.ApproveAgentLink) // an own agent host
	alice, _, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	bob, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.DeleteAccount(tctx(t), alice.info.Person); err == nil {
		t.Fatal("deleted another person's account")
	}
	if _, err := w.alice.DeleteAccount(tctx(t), alice.info.Person); err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("the last admin deleted their account: %v", err)
	}
	deleted, err := w.bob.DeleteAccount(tctx(t), bob.info.Person)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{w.bob.Address, phone.Address, worker.Address}
	slices.Sort(want)
	if !slices.Equal(deleted, want) {
		t.Fatalf("deleted %v, want %v", deleted, want)
	}
	for _, d := range []*Agent{w.bob, phone} {
		if _, err := d.DeleteAccount(tctx(t), bob.info.Person); err == nil || !strings.Contains(err.Error(), "this account was deleted") {
			t.Fatalf("%s still answered after deletion: %v", d.Address, err)
		}
	}
}
