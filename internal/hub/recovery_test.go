package hub

import (
	"encoding/json"
	"fmt"
	"github.com/misunders2d/agentnet/internal/googleauth"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testgoogle"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type recoveryFixture struct {
	h        *Hub
	admin    member
	old, new protocol.PersonRoster
	key      *identity.Identity
	claims   googleauth.Claims
	req      protocol.RecoveryRequest
}

func mel736Fixture(t *testing.T, removed bool) recoveryFixture {
	t.Helper()
	h, _, _ := testHub(t)
	f := recoveryFixture{h: h, admin: enrollAdmin(t, h, "boss"), claims: googleauth.Claims{Email: "bohdan@mellanni.com", Domain: "mellanni.com", Subject: "fresh-bohdan", IssuedAt: time.Now().Unix()}}
	if _, e := h.store.changeGoogleAccess(protocol.GoogleAccessChange{Email: f.claims.Email}); e != nil {
		t.Fatal(e)
	}
	enroll := func(person, name string) (protocol.PersonRoster, *identity.Identity) {
		k, _ := identity.Generate()
		pub := k.Public(protocol.GoogleLabel(f.claims.Email) + "/" + name)
		r := protocol.PersonRoster{Person: person, Label: "Bohdan", Email: f.claims.Email, Devices: []identity.Public{pub}}
		r.Sign(k.Sign)
		if _, _, e := h.store.enrollGoogle(f.claims, protocol.GoogleRequest{Public: pub, First: &r}); e != nil {
			t.Fatal(e)
		}
		return r, k
	}
	f.old, _ = enroll("faeb3e71bb3aac661d198083239baa6f", "lost")
	if removed {
		if _, e := h.store.changeGoogleAccess(protocol.GoogleAccessChange{Email: f.claims.Email, Remove: true}); e != nil {
			t.Fatal(e)
		}
		if _, e := h.store.changeGoogleAccess(protocol.GoogleAccessChange{Email: f.claims.Email}); e != nil {
			t.Fatal(e)
		}
	} else {
		if _, e := h.store.db.Exec(`DELETE FROM google_people WHERE email=?`, f.claims.Email); e != nil {
			t.Fatal(e)
		}
	}
	f.new, f.key = enroll("59c89e536113a047e24569888bcc106a", "new")
	s := protocol.PersonSuccessor{Version: 1, Realm: h.realmID, OldPerson: f.old.Person, OldHead: f.old.Hash(), NewPerson: f.new.Person, NewGenesis: f.new.Hash(), Email: f.claims.Email, Created: time.Now().Unix(), Nonce: protocol.NewID(), Signer: f.new.Devices[0].Fingerprint()}
	s.Sign(f.key.Sign)
	f.req = protocol.RecoveryRequest{Successor: s, Google: protocol.GoogleRequest{Public: f.new.Devices[0], IDToken: "fixture"}}
	f.req.Google.Sign(f.key.Sign)
	return f
}
func (f recoveryFixture) request(t *testing.T) {
	t.Helper()
	if _, e := f.h.store.requestRecovery(f.h.realmID, f.new.Devices[0].Address, f.claims, f.req); e != nil {
		t.Fatal(e)
	}
}
func TestMEL736BohdanSplit(t *testing.T) {
	f := mel736Fixture(t, true)
	f.request(t)
	v, _, e := f.h.store.decideRecovery(f.h.realmID, f.admin.addr, f.req.Successor.Hash(), true, false)
	if e != nil || v.Status != "committed" {
		t.Fatalf("%+v %v", v, e)
	}
	for _, r := range []protocol.PersonRoster{f.old, f.new} {
		a, e := f.h.store.agent(r.Devices[0].Address)
		if e != nil || a.Revoked != (r.Person == f.old.Person) {
			t.Fatalf("revocation %s %+v %v", r.Person, a, e)
		}
	}
	var person string
	var denied bool
	if e = f.h.store.db.QueryRow(`SELECT person FROM google_people WHERE email=?`, f.claims.Email).Scan(&person); e != nil || person != f.new.Person {
		t.Fatalf("binding %s %v", person, e)
	}
	if e = f.h.store.db.QueryRow(`SELECT denied FROM google_emails WHERE email=?`, f.claims.Email).Scan(&denied); e != nil || denied {
		t.Fatal("email denied", e)
	}
	code, b := f.admin.call(t, f.h, "GET", "/v1/google/recovery/"+f.old.Person, nil)
	var got []protocol.RecoveryRecord
	if code != 200 || json.Unmarshal(b, &got) != nil || len(got) != 1 {
		t.Fatalf("catchup %d %s", code, b)
	}
}
func TestMEL736ActivePendingDetachedRevoked(t *testing.T) {
	f := mel736Fixture(t, false)
	// A pending old association and detached old key both share its email scope.
	for i := 0; i < 2; i++ {
		k, _ := identity.Generate()
		p := k.Public(protocol.GoogleLabel(f.claims.Email) + fmt.Sprintf("/extra%d", i))
		raw, _ := json.Marshal(p)
		var pending any
		if i == 0 {
			pending = f.old.Person
		}
		if _, e := f.h.store.db.Exec(`INSERT INTO agents(address,label,public,admin,created_at,pending_person,pending_until) VALUES(?,?,?,0,?,?,?)`, p.Address, protocol.GoogleLabel(f.claims.Email), string(raw), time.Now().Unix(), pending, time.Now().Unix()+1000); e != nil {
			t.Fatal(e)
		}
	}
	f.request(t)
	_, a, e := f.h.store.decideRecovery(f.h.realmID, f.admin.addr, f.req.Successor.Hash(), true, false)
	if e != nil || len(a) != 3 {
		t.Fatalf("revoked %v %v", a, e)
	}
}
func TestMEL736DuplicateConcurrentApproval(t *testing.T) {
	f := mel736Fixture(t, false)
	f.request(t)
	f.request(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := f.h.store.decideRecovery(f.h.realmID, f.admin.addr, f.req.Successor.Hash(), true, false)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var n int
	f.h.store.db.QueryRow(`SELECT count(*) FROM person_recoveries WHERE status='committed'`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
func TestMEL736InvalidRequests(t *testing.T) {
	for _, field := range []string{"old", "head", "realm", "email", "consent"} {
		t.Run(field, func(t *testing.T) {
			f := mel736Fixture(t, false)
			s := &f.req.Successor
			switch field {
			case "old":
				s.OldPerson = ""
			case "head":
				s.OldHead = f.new.Hash()
			case "realm":
				s.Realm = "other"
			case "email":
				s.Email = "someone@mellanni.com"
			case "consent":
				s.Sig[0] ^= 1
			}
			if field != "consent" {
				s.Sign(f.key.Sign)
			}
			if _, e := f.h.store.requestRecovery(f.h.realmID, f.new.Devices[0].Address, f.claims, f.req); e == nil {
				t.Fatal("accepted")
			}
		})
	}
}
func TestMEL736SelfApprovalAndOperator(t *testing.T) {
	f := mel736Fixture(t, false)
	f.request(t)
	if _, e := f.h.store.db.Exec(`UPDATE agents SET admin=1 WHERE address=?`, f.new.Devices[0].Address); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.h.store.decideRecovery(f.h.realmID, f.new.Devices[0].Address, f.req.Successor.Hash(), true, false); e == nil {
		t.Fatal("self approved")
	}
	v, _, e := f.h.store.decideRecovery(f.h.realmID, "hub-operator", f.req.Successor.Hash(), true, true)
	if e != nil || v.Actor != "hub-operator" {
		t.Fatal(v, e)
	}
}
func TestMEL736AtomicRollback(t *testing.T) {
	f := mel736Fixture(t, false)
	f.request(t)
	if _, e := f.h.store.db.Exec(`CREATE TRIGGER fail_recovery BEFORE UPDATE OF status ON person_recoveries WHEN NEW.status='committed' BEGIN SELECT RAISE(ABORT,'injected crash'); END`); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.h.store.decideRecovery(f.h.realmID, f.admin.addr, f.req.Successor.Hash(), true, false); e == nil {
		t.Fatal("injection failed")
	}
	v, e := recoveryIn(f.h.store.db, f.req.Successor.Hash())
	if e != nil || v.Status != "pending" {
		t.Fatal(v, e)
	}
	a, e := f.h.store.agent(f.old.Devices[0].Address)
	if e != nil || a.Revoked {
		t.Fatal("partial revocation", e)
	}
	if _, e = f.h.store.db.Exec(`DROP TRIGGER fail_recovery`); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.h.store.decideRecovery(f.h.realmID, f.admin.addr, f.req.Successor.Hash(), true, false); e != nil {
		t.Fatal(e)
	}
}
func TestMEL736ConflictingTarget(t *testing.T) {
	f := mel736Fixture(t, false)
	f.request(t)
	s := f.req.Successor
	s.Nonce = protocol.NewID()
	s.Sign(f.key.Sign)
	f.req.Successor = s
	if _, e := f.h.store.requestRecovery(f.h.realmID, f.new.Devices[0].Address, f.claims, f.req); e == nil {
		t.Fatal("conflicting record accepted")
	}
}

func TestMEL736SignedHTTPJourney(t *testing.T) {
	f := mel736Fixture(t, true)
	issuer := testgoogle.New(t)
	var e error
	f.h.google, e = googleauth.New(issuer.Client, []string{testgoogle.ClientID})
	if e != nil {
		t.Fatal(e)
	}
	f.req.Google.IDToken = issuer.IDToken(t, f.req.Google.Public, f.claims.Email, map[string]any{"hd": f.claims.Domain})
	f.req.Google.Sign(f.key.Sign)
	requester := member{f.key, f.new.Devices[0].Address}
	gen := f.h.againGen.Load()
	if code, b := requester.call(t, f.h, "POST", "/v1/google/recovery", f.req); code != 201 {
		t.Fatalf("request %d %s", code, b)
	}
	if code, b := f.admin.call(t, f.h, "GET", "/v1/google/recovery", nil); code != 200 {
		t.Fatalf("list %d %s", code, b)
	}
	if code, _ := requester.call(t, f.h, "POST", "/v1/google/recovery/"+f.req.Successor.Hash()+"/approve", struct{}{}); code != 403 {
		t.Fatal("non-admin approved", code)
	}
	if code, b := f.admin.call(t, f.h, "POST", "/v1/google/recovery/"+f.req.Successor.Hash()+"/approve", struct{}{}); code != 200 {
		t.Fatalf("approve %d %s", code, b)
	}
	if f.h.againGen.Load() <= gen {
		t.Fatal("no push change generation")
	}
	if code, b := requester.call(t, f.h, "GET", "/v1/google/recovery/"+f.new.Person, nil); code != 200 {
		t.Fatalf("catchup %d %s", code, b)
	}
}
func TestMEL736DenyAndFreshRetry(t *testing.T) {
	f := mel736Fixture(t, false)
	f.request(t)
	id := f.req.Successor.Hash()
	if _, _, e := f.h.store.decideRecovery(f.h.realmID, f.admin.addr, id, false, false); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.h.store.decideRecovery(f.h.realmID, f.admin.addr, id, true, false); e == nil {
		t.Fatal("denied approved")
	}
	f.req.Successor.Nonce = protocol.NewID()
	f.req.Successor.Sign(f.key.Sign)
	f.request(t)
}
func TestMEL736RestartAndOperatorMaintenance(t *testing.T) {
	f := mel736Fixture(t, false)
	f.request(t)
	dir := f.h.cfg.DataDir
	if e := f.h.Close(); e != nil {
		t.Fatal(e)
	}
	m, e := OpenMaintenance(dir)
	if e != nil {
		t.Fatal(e)
	}
	v, e := m.ApproveRecovery(f.req.Successor.Hash())
	if e != nil || v.Status != "committed" {
		t.Fatal(v, e)
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	s, e := openStore(filepath.Join(dir, "hub.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	v, e = recoveryIn(s.db, f.req.Successor.Hash())
	if e != nil || v.Status != "committed" || v.Actor != "hub-operator" {
		t.Fatal(v, e)
	}
}
func TestMEL736SecondRecoveryAndCycle(t *testing.T) {
	f := mel736Fixture(t, false)
	f.request(t)
	if _, _, e := f.h.store.decideRecovery(f.h.realmID, f.admin.addr, f.req.Successor.Hash(), true, false); e != nil {
		t.Fatal(e)
	}
	// A third enrolled identity represents the second lost-every-device event.
	if _, e := f.h.store.db.Exec(`DELETE FROM google_people WHERE email=?`, f.claims.Email); e != nil {
		t.Fatal(e)
	}
	k, _ := identity.Generate()
	pub := k.Public(protocol.GoogleLabel(f.claims.Email) + "/third")
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Bohdan", Email: f.claims.Email, Devices: []identity.Public{pub}}
	r.Sign(k.Sign)
	if _, _, e := f.h.store.enrollGoogle(f.claims, protocol.GoogleRequest{Public: pub, First: &r}); e != nil {
		t.Fatal(e)
	}
	s := f.req.Successor
	s.OldPerson = f.new.Person
	s.OldHead = f.new.Hash()
	s.NewPerson = r.Person
	s.NewGenesis = r.Hash()
	s.Signer = pub.Fingerprint()
	s.Nonce = protocol.NewID()
	s.Sign(k.Sign)
	req := protocol.RecoveryRequest{Successor: s, Google: protocol.GoogleRequest{Public: pub, IDToken: "fixture"}}
	req.Google.Sign(k.Sign)
	if _, e := f.h.store.requestRecovery(f.h.realmID, pub.Address, f.claims, req); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.h.store.decideRecovery(f.h.realmID, f.admin.addr, s.Hash(), true, false); e != nil {
		t.Fatal(e)
	}
	s.OldPerson = r.Person
	s.OldHead = r.Hash()
	s.NewPerson = f.old.Person
	s.NewGenesis = f.old.Hash()
	if e := validateRecovery(f.h.store.db, f.h.realmID, s); e == nil {
		t.Fatal("cycle accepted")
	}
}
func TestMEL736StaleGoogleProof(t *testing.T) {
	f := mel736Fixture(t, false)
	f.claims.IssuedAt = time.Now().Unix() - 301
	if _, e := f.h.store.requestRecovery(f.h.realmID, f.new.Devices[0].Address, f.claims, f.req); e == nil {
		t.Fatal("stale proof accepted")
	}
}

func TestMEL736DifferentTargetRejected(t *testing.T) {
	f := mel736Fixture(t, false)
	f.request(t)
	if _, e := f.h.store.db.Exec(`DELETE FROM google_people WHERE email=?`, f.claims.Email); e != nil {
		t.Fatal(e)
	}
	k, _ := identity.Generate()
	pub := k.Public(protocol.GoogleLabel(f.claims.Email) + "/conflict")
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Bohdan", Email: f.claims.Email, Devices: []identity.Public{pub}}
	r.Sign(k.Sign)
	if _, _, e := f.h.store.enrollGoogle(f.claims, protocol.GoogleRequest{Public: pub, First: &r}); e != nil {
		t.Fatal(e)
	}
	s := f.req.Successor
	s.NewPerson = r.Person
	s.NewGenesis = r.Hash()
	s.Signer = pub.Fingerprint()
	s.Nonce = protocol.NewID()
	s.Sign(k.Sign)
	req := protocol.RecoveryRequest{Successor: s, Google: protocol.GoogleRequest{Public: pub, IDToken: "fixture"}}
	req.Google.Sign(k.Sign)
	if _, e := f.h.store.requestRecovery(f.h.realmID, pub.Address, f.claims, req); e == nil {
		t.Fatal("second target accepted")
	}
}
func TestMEL736ChangedHeadOrRemovedSuccessorBlocksApproval(t *testing.T) {
	for _, change := range []string{"head", "revoked"} {
		t.Run(change, func(t *testing.T) {
			f := mel736Fixture(t, false)
			f.request(t)
			var e error
			if change == "head" {
				_, e = f.h.store.db.Exec(`UPDATE persons SET hash=? WHERE person=?`, f.new.Hash(), f.old.Person)
			} else {
				_, e = f.h.store.db.Exec(`UPDATE agents SET revoked_at=? WHERE address=?`, time.Now().Unix(), f.new.Devices[0].Address)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = f.h.store.decideRecovery(f.h.realmID, f.admin.addr, f.req.Successor.Hash(), true, false); e == nil {
				t.Fatal("stale state approved")
			}
		})
	}
}

func TestMEL736DetachedOldAdminCannotSelfApprove(t *testing.T) {
	f := mel736Fixture(t, false)
	f.request(t)
	caller := f.old.Devices[0].Address
	if _, e := f.h.store.db.Exec(`UPDATE agents SET person_id=NULL,linked=0,admin=1 WHERE address=?`, caller); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.h.store.decideRecovery(f.h.realmID, caller, f.req.Successor.Hash(), true, false); e == nil {
		t.Fatal("detached old self approval accepted")
	}
}

func TestMEL736CycleAdmission(t *testing.T) {
	f := mel736Fixture(t, false)
	// Model a previously committed target -> old edge. The otherwise valid
	// old -> target consent must fail specifically at the graph admission gate.
	if _, e := f.h.store.db.Exec(`INSERT INTO person_recoveries(id,realm,old_person,new_person,record,subject,status) VALUES(?,?,?,?,?,?,'committed')`, protocol.NewID(), f.h.realmID, f.new.Person, f.old.Person, "{}", f.claims.Subject); e != nil {
		t.Fatal(e)
	}
	if _, e := f.h.store.requestRecovery(f.h.realmID, f.new.Devices[0].Address, f.claims, f.req); e == nil {
		t.Fatal("cycle accepted")
	}
}

func TestMEL736SameEmailSplitCannotSelfApprove(t *testing.T) {
	f := mel736Fixture(t, false)
	f.request(t)
	a, e := f.h.store.agent(f.admin.addr)
	if e != nil {
		t.Fatal(e)
	}
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Bohdan", Email: f.claims.Email, Devices: []identity.Public{a.Public}}
	r.Sign(f.admin.id.Sign)
	raw, _ := json.Marshal(r)
	if _, e = f.h.store.db.Exec(`INSERT INTO persons(person,seq,hash,record) VALUES(?,0,?,?)`, r.Person, r.Hash(), string(raw)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.h.store.db.Exec(`UPDATE agents SET person_id=? WHERE address=?`, r.Person, f.admin.addr); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.h.store.decideRecovery(f.h.realmID, f.admin.addr, f.req.Successor.Hash(), true, false); e == nil {
		t.Fatal("same-email historical split approved own recovery")
	}
}

func TestMEL736DurableRequestRetryAfterAdmissionWindow(t *testing.T) {
	f := mel736Fixture(t, false)
	s := &f.req.Successor
	s.Created = time.Now().Unix() - 1000
	s.Sign(f.key.Sign)
	if _, e := f.h.store.requestRecovery(f.h.realmID, f.new.Devices[0].Address, f.claims, f.req); e == nil {
		t.Fatal("new stale request admitted")
	}
	// Restore an earlier admitted request as found after restart/offline time.
	raw, _ := json.Marshal(s)
	if _, e := f.h.store.db.Exec(`INSERT INTO person_recoveries(id,realm,old_person,new_person,record,subject,status) VALUES(?,?,?,?,?,?,'pending')`, s.Hash(), s.Realm, s.OldPerson, s.NewPerson, string(raw), f.claims.Subject); e != nil {
		t.Fatal(e)
	}
	v, e := f.h.store.requestRecovery(f.h.realmID, f.new.Devices[0].Address, f.claims, f.req)
	if e != nil || v.ID != s.Hash() || v.Status != "pending" {
		t.Fatal(v, e)
	}
}
