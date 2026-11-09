package client

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Another process's write, through its own connection to the same file.
func TestContinuityWriterProcess(t *testing.T) {
	path := os.Getenv("AGENTNET_TEST_CONTINUITY_DB")
	if path == "" {
		t.Skip("helper for TestLogicalPersonsNeverOutliveTheirInputs")
	}
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if _, err = s.db.Exec(os.Getenv("AGENTNET_TEST_CONTINUITY_SQL")); err != nil {
		t.Fatal(err)
	}
}

// MEL-748: the shared result must equal a fresh resolve after every change that
// can move it, whoever wrote it: this store, another connection, another process.
func TestLogicalPersonsNeverOutliveTheirInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	seedFixtureStore(t, filepath.Dir(path))
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	other, err := openStore(path) // its own connection to the same file
	if err != nil {
		t.Fatal(err)
	}
	defer other.db.Close()
	check := func(stage string, changed bool) map[string]string {
		t.Helper()
		before := continuityResolves.Load()
		got, err := logicalPersons(s.db)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if ran := continuityResolves.Load() - before; changed && ran == 0 {
			t.Fatalf("%s: the old result was reused", stage)
		} else if !changed && ran != 0 {
			t.Fatalf("%s: an unchanged state computed again", stage)
		}
		fresh, err := resolveLogicalPersons(s.db)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if !maps.Equal(got, fresh) {
			t.Fatalf("%s: shared result %v, current inputs say %v", stage, got, fresh)
		}
		return got
	}
	process := func(sql string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestContinuityWriterProcess$")
		cmd.Env = append(os.Environ(), "AGENTNET_TEST_CONTINUITY_DB="+path, "AGENTNET_TEST_CONTINUITY_SQL="+sql)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("writer process: %v\n%s", err, out)
		}
	}
	lost := mel736OneRoster(t, "faeb3e71bb3aac661d198083239baa6f", "lost")
	second := mel736OneRoster(t, "59c89e536113a047e24569888bcc106a", "second")
	third := mel736OneRoster(t, "7c0d9e536113a047e24569888bcc1070", "third")

	mel736OnePin(t, s, lost)
	mel736OneLeave(t, s, lost, true)
	mel736OnePin(t, s, second)
	if got := check("first recovery", true); got[lost.Person] != second.Person {
		t.Fatalf("first recovery: %v", got)
	}
	check("nothing changed", false)

	// Same-person recovery again, written through another connection.
	mel736OneLeave(t, other, second, true)
	mel736OnePin(t, other, third)
	if got := check("second recovery from another connection", true); got[lost.Person] != third.Person || got[second.Person] != third.Person {
		t.Fatalf("second recovery: %v", got)
	}
	check("nothing changed", false)

	// A person frozen elsewhere stops being followed at once.
	if _, err = other.db.Exec(`UPDATE persons SET state='conflict',conflict='x' WHERE person=?`, third.Person); err != nil {
		t.Fatal(err)
	}
	if got := check("freeze from another connection", true); got[lost.Person] == third.Person {
		t.Fatalf("followed a frozen person: %v", got)
	}
	if _, err = other.db.Exec(`UPDATE persons SET state='pinned',conflict=NULL WHERE person=?`, third.Person); err != nil {
		t.Fatal(err)
	}
	check("unfrozen", true)

	// Another process: a revocation and a changed roster row.
	process(`INSERT OR REPLACE INTO config(k,v) VALUES('revoked-exact-device/bohdan/third/` + third.Devices[0].Fingerprint() + `','bohdan/third')`)
	check("revocation from another process", true)
	process(`DELETE FROM config WHERE k='` + continuityHeadPrefix + second.Person + `'`)
	if got := check("continuity record from another process", true); got[second.Person] == third.Person {
		t.Fatalf("followed a withdrawn continuity record: %v", got)
	}
	// A write nothing here reads changes nothing, from anywhere.
	process(`INSERT OR REPLACE INTO config(k,v) VALUES('unrelated','1')`)
	check("unrelated write from another process", false)
}

// A remembered signature check is about bytes, never about who may act now. A
// verified key that lost its place stays refused, and any different byte in a
// roster is checked for real.
func TestRememberedSignatureGrantsNoAuthority(t *testing.T) {
	w, phone, conv, _ := historyCatchupFixture(t, 1)
	a := w.alice
	a.stopBackgroundPosts()
	a.stopArchivePosts()
	_, root := rootOf(t, a, conv)
	key := phone.Self()
	remembered := func() {
		t.Helper()
		if err := key.Verify(); err != nil {
			t.Fatal(err)
		}
	}
	remembered()
	if room, err := syncWindowRoom(a.store.db, key); err != nil || room == 0 {
		t.Fatalf("a current device has no room: %d %v", room, err)
	}
	if err := a.Revoke(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	m, err := a.Members(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	a.onMembers(raw)
	if err = a.reconcileRecipientLifecycle(tctx(t)); err != nil {
		t.Fatal(err)
	}
	remembered() // the signature is still good: that is all this says
	if room, err := syncWindowRoom(a.store.db, key); err != nil || room != 0 {
		t.Fatalf("a revoked device still has room: %d %v", room, err)
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = a.checkHistoryCopies(tx, []outCopy{{env: envelope.Envelope{To: phone.Address}, in: envelope.Inner{Root: root}, recipientFP: key.Fingerprint()}})
	tx.Rollback()
	if !errors.Is(err, ErrPeerRevoked) {
		t.Fatalf("a revoked device passed: %v", err)
	}

	// A frozen person: the good signature changes nothing about sending to them.
	b := w.bob
	bobKey := b.Self()
	if err = bobKey.Verify(); err != nil {
		t.Fatal(err)
	}
	dm := newDM(t, a, b)
	freeze(t, a, b)
	if err = bobKey.Verify(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.SendConv(WithQueuedSend(tctx(t), protocol.NewID()), dm, ConvOutgoing{Kind: envelope.KindMessage, Origin: envelope.OriginUI, Body: "to a frozen person"}); err == nil {
		t.Fatal("a remembered signature let a message through to a frozen person")
	}

	// Different bytes under an address already checked get a real check.
	r := mel736OneRoster(t, "faeb3e71bb3aac661d198083239baa6f", "lost")
	good, _ := json.Marshal(r)
	if _, err = protocol.ParsePersonRoster(good); err != nil {
		t.Fatal(err)
	}
	if err = r.Devices[0].Verify(); err != nil {
		t.Fatal(err)
	}
	tampered := r
	tampered.Devices = []identity.Public{r.Devices[0]}
	sig := append([]byte(nil), r.Devices[0].BoxSig...)
	sig[0] ^= 1
	tampered.Devices[0].BoxSig = sig
	bad, _ := json.Marshal(tampered)
	if _, err = protocol.ParsePersonRoster(bad); err == nil {
		t.Fatal("a changed signature passed on the strength of the earlier one")
	}
	fresh, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	swapped := r
	swapped.Devices = []identity.Public{fresh.Public(r.Devices[0].Address)}
	// A new key is well formed on its own: only the roster's own signature says
	// whose it is, and that was made over the old key.
	if err = swapped.VerifyFirst(); err == nil {
		t.Fatal("a changed key passed under the old roster signature")
	}
	if err = r.VerifyFirst(); err != nil {
		t.Fatal(err)
	}
}
