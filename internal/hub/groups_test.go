package hub

import (
	"bytes"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupJournalCASAuthorityAndDurability(t *testing.T) {
	h, _, _ := testHub(t)
	var n int
	if err := h.store.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='group_heads'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		if _, err := h.store.db.Exec(GroupHubSchema); err != nil {
			t.Fatal(err)
		}
	}
	a, b, c := joinMember(t, h, "ga"), joinMember(t, h, "gb"), joinMember(t, h, "gc")
	ar, br, _ := personOf(t, h, a), personOf(t, h, b), personOf(t, h, c)
	admins := []string{ar.Person, br.Person}
	if admins[0] > admins[1] {
		admins[0], admins[1] = admins[1], admins[0]
	}
	realm := protocol.NewID()
	commit := func(m member, r protocol.PersonRoster, seq int64, prev string, who []string) protocol.GroupCommit {
		v := protocol.GroupCommit{Bootstrap: a.id.Public(a.addr).Fingerprint(), V: 1, Conv: ar.Hash(), Realm: realm, Seq: seq, Prev: prev, Hash: protocol.PersonRoster{Person: protocol.NewID()}.Hash(), Admins: who, Writer: m.addr, Actor: r.Person, ActorRoster: r.Hash(), Ciphertext: []byte("encrypted durable state")}
		v.Sign(m.id.Sign)
		return v
	}
	root := commit(a, ar, 0, "", admins)
	if _, err := h.store.putGroupCommit(a.id.Public(a.addr), root, realm); err != nil {
		t.Fatal(err)
	}
	poison := commit(c, firstRoster(c, "Vitalii"), 1, root.Hash, admins)
	if _, err := h.store.putGroupCommit(c.id.Public(c.addr), poison, realm); !errors.Is(err, errGroupForbidden) {
		t.Fatalf("nonadmin append %v", err)
	}
	one, two := commit(a, ar, 1, root.Hash, admins), commit(b, br, 1, root.Hash, admins)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, v := range []struct {
		m member
		c protocol.GroupCommit
	}{{a, one}, {b, two}} {
		wg.Add(1)
		go func(v struct {
			m member
			c protocol.GroupCommit
		}) {
			defer wg.Done()
			_, err := h.store.putGroupCommit(v.m.id.Public(v.m.addr), v.c, realm)
			errs <- err
		}(v)
	}
	wg.Wait()
	close(errs)
	wins, stales := 0, 0
	for err := range errs {
		if err == nil {
			wins++
		} else if errors.Is(err, errGroupStale) {
			stales++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || stales != 1 {
		t.Fatalf("CAS winners %d stale %d", wins, stales)
	}
	page, err := h.store.groupChain(a.addr, root.Conv, root.Bootstrap, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 2 || !bytes.Equal(page.Records[0].Ciphertext, root.Ciphertext) {
		t.Fatal("journal orphaned encrypted payload")
	}
	// Open an independent database handle after the commit and before fanout.
	// Full ciphertext and ordered authority remain recoverable from disk.
	reopened, reopenErr := openStore(filepath.Join(h.cfg.DataDir, "hub.db"))
	if reopenErr != nil {
		t.Fatal(reopenErr)
	}
	defer reopened.db.Close()
	recovered, recoverErr := reopened.groupChain(a.addr, root.Conv, root.Bootstrap, -1)
	if recoverErr != nil {
		t.Fatal(recoverErr)
	}
	if len(recovered.Records) != 2 || !bytes.Equal(recovered.Records[1].Ciphertext, page.Records[1].Ciphertext) {
		t.Fatal("reopen lost publication before fanout")
	}
	latest := page.Records[1]
	rebased := commit(a, ar, 2, latest.Hash, []string{br.Person})
	if _, err = h.store.putGroupCommit(a.id.Public(a.addr), rebased, realm); err != nil {
		t.Fatal(err)
	}
	removed := commit(a, ar, 3, rebased.Hash, []string{br.Person})
	if _, err = h.store.putGroupCommit(a.id.Public(a.addr), removed, realm); !errors.Is(err, errGroupForbidden) {
		t.Fatalf("removed admin %v", err)
	}
	forged := commit(b, br, 3, rebased.Hash, []string{br.Person})
	forged.Ciphertext[0] ^= 1
	if _, err = h.store.putGroupCommit(b.id.Public(b.addr), forged, realm); err == nil {
		t.Fatal("forged ciphertext accepted")
	}
	if _, err = h.store.db.Exec("UPDATE agents SET revoked_at=1 WHERE address=?", b.addr); err != nil {
		t.Fatal(err)
	}
	live := commit(b, br, 3, rebased.Hash, []string{br.Person})
	if _, err = h.store.putGroupCommit(b.id.Public(b.addr), live, realm); !errors.Is(err, errGroupForbidden) {
		t.Fatalf("revoked admin device %v", err)
	}
}
