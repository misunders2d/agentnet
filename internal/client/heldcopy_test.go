package client

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An older own-device producer re-sent one held history record under fresh
// envelope IDs on every wake: each copy became a notice and was rechecked
// and logged on every pass (1,467 notices, 864,704 journal lines). Copies
// now settle into one notice and one check; each keeps its own quarantined
// receipt, and all are admitted (delivered) once their evidence arrives.
func TestHeldResentRecordSettlesIntoOneNotice(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	var mu sync.Mutex
	var logged []string
	a.Logf = func(f string, args ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(f, args...))
		mu.Unlock()
	}
	holdLines := func() (n int) {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range logged {
			if strings.Contains(l, " held (") {
				n++
			}
		}
		return n
	}
	q, err := phone.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "the phone's own question"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = phone.Revise(tctx(t), ControlRef{ID: q.ID, Fingerprint: phone.Self().Fingerprint()}, "the phone's edit"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err = phone.deviceHistoryPage(a.Self()); err != nil {
			t.Fatal(err)
		}
	}
	bodies := map[string]string{}
	rows, err := phone.store.db.Query(`SELECT body FROM outbox WHERE recipient=? AND sub=?`, a.Address, envelope.SubDeviceHistory)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		dh, e := protocol.ParseDeviceHistory([]byte(body))
		if e != nil {
			t.Fatal(e)
		}
		var item HistoryItem
		if e = json.Unmarshal(dh.Item, &item); e != nil {
			t.Fatal(e)
		}
		bodies[item.Sub] = body
	}
	rows.Close()
	if bodies[""] == "" || bodies[envelope.SubRevision] == "" {
		t.Fatalf("own history carriers: %v", len(bodies))
	}
	carrier := func(body string) envelope.Envelope {
		return craft(t, phone, a, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubDeviceHistory, Replica: true, Body: body})
	}
	// The edit arrives before its original, as the same record five times.
	var copies []string
	for range 5 {
		env := carrier(bodies[envelope.SubRevision])
		if err = a.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		copies = append(copies, env.ID)
	}
	for _, id := range copies {
		if state, e := a.store.disposition(id); e != nil || state != protocol.StateQuarantined || heldReason(t, a, id) != reasonProof {
			t.Fatalf("copy %s: %s %s %v", id, state, heldReason(t, a, id), e)
		}
	}
	held, err := a.Quarantine()
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].ID != copies[0] || held[0].Copies != 4 || held[0].LastAt == nil {
		t.Fatalf("one record is one notice: %+v", held)
	}
	if envs, _, e := a.store.heldAfter(reasonProof, heldPos{}, proofPage); e != nil || len(envs) != 1 || envs[0].ID != copies[0] {
		t.Fatalf("one check per record: %d %v", len(envs), e)
	}
	if n := holdLines(); n != 5 {
		t.Fatalf("hold log lines for five arrivals: %d", n)
	}
	// Rows held before this step have no key: the next look settles them.
	if _, err = a.store.db.Exec(`UPDATE quarantine SET copy_key='',copy_of=''`); err != nil {
		t.Fatal(err)
	}
	if held, err = a.Quarantine(); err != nil || len(held) != 5 {
		t.Fatalf("legacy rows: %d %v", len(held), err)
	}
	a.convWork.due(convRetry)
	a.convSync(tctx(t))
	if held, err = a.Quarantine(); err != nil || len(held) != 1 || held[0].ID != copies[0] || held[0].Copies != 4 {
		t.Fatalf("legacy rows after one look: %+v %v", held, err)
	}
	for range 3 {
		a.convWork.due(convRetry)
		a.convSync(tctx(t))
	}
	if n := holdLines(); n != 5 {
		t.Fatalf("an unchanged recheck logged again: %d lines", n)
	}
	// Its original arrives: the held row and then each copy are admitted
	// through the existing duplicate path, each with a delivered receipt.
	if err = a.verifyAndStore(tctx(t), carrier(bodies[""])); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		a.convSync(tctx(t))
	}
	for _, id := range copies {
		if state, e := a.store.disposition(id); e != nil || state != protocol.StateDelivered {
			t.Fatalf("copy %s after its original: %s %s %v", id, state, heldReason(t, a, id), e)
		}
	}
	var revised int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE sub=? AND ref_id=?`, envelope.SubRevision, q.ID).Scan(&revised); err != nil || revised != 1 {
		t.Fatalf("one logical edit stored: %d %v", revised, err)
	}
	// A re-sent invalid record also stays one notice; an exact id still finds a copy.
	var dh protocol.DeviceHistory
	if err = json.Unmarshal([]byte(bodies[""]), &dh); err != nil {
		t.Fatal(err)
	}
	dh.Roster = strings.Repeat("c", 64) // no roster step of this person
	raw, _ := json.Marshal(dh)
	bad := string(raw)
	var invalid []string
	for range 3 {
		env := carrier(bad)
		if err = a.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		invalid = append(invalid, env.ID)
	}
	if heldReason(t, a, invalid[2]) != reasonInvalid {
		t.Fatalf("copy under an unknown roster: %q", heldReason(t, a, invalid[2]))
	}
	held, err = a.Quarantine()
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].ID != invalid[0] || held[0].Copies != 2 {
		t.Fatalf("one invalid record is one notice: %+v", held)
	}
	if page, e := a.InspectInboxNotices("held", InboxPageOptions{Limit: 10}); e != nil || len(page.Items) != 1 {
		t.Fatalf("held review section: %+v %v", page.Items, e)
	}
	if page, e := a.InspectInboxNotices("held", InboxPageOptions{Limit: 10, ID: invalid[2]}); e != nil || len(page.Items) != 1 {
		t.Fatalf("exact copy lookup: %+v %v", page.Items, e)
	}
}

// Every member list looks at held messages again: their evidence also
// arrives through local admissions that announce nothing (a root or roster
// sync after a device is linked), which a presence-only list used to cover
// in v0.8.16 and must still (CI: a linked phone's deletion stayed held).
func TestEveryMemberListRechecksHeld(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	for _, list := range []string{
		`{"members":[{"address":"vitalii/desk","presence":"connected","joined":5}]}`,
		`{"members":[{"address":"vitalii/desk","presence":"offline","joined":5}]}`,
		`{"members":[{"address":"vitalii/desk","presence":"offline","joined":5,"version":"v0.8.17","suspended":true}]}`,
	} {
		a.convWork.take()
		a.onMembers([]byte(list))
		if work := a.convWork.take(); work&(convRetry|convPersons|convRelease) != convRetry|convPersons|convRelease {
			t.Fatalf("list %s: work %b", list, work)
		}
	}
}
