package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Persons (roster version 2): this installation's own person and the
// persons pinned here, each as a verified chain of roster steps
// (protocol/person.go). A person is pinned on first use from the whole
// chain the Hub serves (trust on first use of its first step); later steps
// are pinned only if each follows the one before it. A different step at a
// seq already pinned freezes the person. The Hub is not trusted for any of
// it: it can withhold a step, never forge one.
//
// Tables (client schema step 20): persons (the newest pinned step of each),
// person_chain (every pinned step, for roots and events bound to earlier
// ones and for the keys of removed devices), person_devices (the current
// devices, by address).

// Person states.
const (
	personSelf     = "self"     // the person this installation speaks for
	personPinned   = "pinned"   // verified and pinned
	personConflict = "conflict" // a different step was seen for it: frozen
)

var errPersonConflict = errors.New("this person's record conflicts with the one pinned here; it is frozen")

// ErrService refuses a person on an installation set up as a service.
var ErrService = errors.New("this installation is a service: it speaks as itself, not for a person")

// PersonInfo is a person as this installation knows it.
type PersonInfo struct {
	Person string `json:"person"`
	Label  string `json:"label"` // the person's own claim, not verified
	// Address and Fingerprint are the one device this view is about: this
	// installation for its own person, the host or author device of a
	// participation, otherwise the person's first current device.
	Address     string       `json:"address"`
	Fingerprint string       `json:"fingerprint"`
	Roster      string       `json:"roster"` // hash of the newest pinned roster step
	Seq         int64        `json:"seq"`
	State       string       `json:"state"` // self, pinned or conflict
	Devices     []DeviceInfo `json:"devices,omitempty"`
}

// DeviceInfo is one current device of a person.
type DeviceInfo struct {
	Address     string `json:"address"`
	Name        string `json:"name"` // the device's name, the part after the "/"
	Fingerprint string `json:"fingerprint"`
	This        bool   `json:"this,omitempty"` // this installation
	Added       int64  `json:"added"`          // the roster step it was added at
}

type personRow struct {
	info   PersonInfo
	roster protocol.PersonRoster // the newest pinned step
	raw    []byte
}

// has reports whether the device at address with fingerprint fp is a
// current device of p.
func (p personRow) has(address, fp string) bool { return p.roster.Has(address, fp) }

// device returns p's current device at address.
func (p personRow) device(address string) (identity.Public, bool) {
	i := slices.IndexFunc(p.roster.Devices, func(d identity.Public) bool { return d.Address == address })
	if i < 0 {
		return identity.Public{}, false
	}
	return p.roster.Devices[i], true
}

// at returns p viewed through its device at address (for PersonInfo's
// Address and Fingerprint).
func (p personRow) at(address string) personRow {
	if d, ok := p.device(address); ok {
		p.info.Address, p.info.Fingerprint = d.Address, d.Fingerprint()
	}
	return p
}

const personCols = `person, label, seq, hash, record, state`

func scanPersonIn(q dbq, where string, args ...any) (personRow, bool, error) {
	var p personRow
	var raw string
	err := q.QueryRow(`SELECT `+personCols+` FROM persons WHERE `+where, args...).
		Scan(&p.info.Person, &p.info.Label, &p.info.Seq, &p.info.Roster, &raw, &p.info.State)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	p.raw = []byte(raw)
	if err := json.Unmarshal(p.raw, &p.roster); err != nil {
		return p, false, err
	}
	added := map[string]int64{}
	if rows, err := q.Query(`SELECT address, added FROM person_devices WHERE person = ?`, p.info.Person); err == nil {
		for rows.Next() {
			var a string
			var s int64
			if rows.Scan(&a, &s) == nil {
				added[a] = s
			}
		}
		rows.Close()
	}
	for _, d := range p.roster.Devices {
		_, name, _ := protocol.SplitAddress(d.Address)
		p.info.Devices = append(p.info.Devices, DeviceInfo{Address: d.Address, Name: name, Fingerprint: d.Fingerprint(), Added: added[d.Address]})
	}
	if len(p.roster.Devices) > 0 {
		p = p.at(p.roster.Devices[0].Address)
	}
	return p, true, nil
}

// selfPerson returns the person this installation (at address) speaks for,
// viewed through this device.
func (s *store) selfPerson(address string) (personRow, bool, error) {
	p, ok, err := scanPersonIn(s.db, `state = ?`, personSelf)
	if !ok || err != nil {
		return p, ok, err
	}
	p = p.at(address)
	for i := range p.info.Devices {
		p.info.Devices[i].This = p.info.Devices[i].Address == address
	}
	return p, true, nil
}

func (s *store) personByID(id string) (personRow, bool, error) { return personByIDIn(s.db, id) }

func personByIDIn(q dbq, id string) (personRow, bool, error) {
	return scanPersonIn(q, `person = ?`, id)
}

// personByAddress returns the person whose current device address is,
// viewed through that device.
func (s *store) personByAddress(address string) (personRow, bool, error) {
	var id string
	err := s.db.QueryRow(`SELECT person FROM person_devices WHERE address = ?`, address).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return personRow{}, false, nil
	}
	if err != nil {
		return personRow{}, false, err
	}
	p, ok, err := s.personByID(id)
	return p.at(address), ok, err
}

// inChain reports whether hash is a step of person's pinned chain.
func inChainIn(q querier, person, hash string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM person_chain WHERE person = ? AND hash = ?`, person, hash).Scan(&n)
	return n > 0, err
}

func (s *store) inChain(person, hash string) bool {
	ok, err := inChainIn(s.db, person, hash)
	return err == nil && ok
}

// chainStep returns person's pinned step with hash.
func (s *store) chainStep(person, hash string) (protocol.PersonRoster, bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT record FROM person_chain WHERE person = ? AND hash = ?`, person, hash).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.PersonRoster{}, false, nil
	}
	if err != nil {
		return protocol.PersonRoster{}, false, err
	}
	var r protocol.PersonRoster
	return r, true, json.Unmarshal([]byte(raw), &r)
}

// pinResult is what pinning steps changed.
type pinResult struct {
	changed bool // a newer step was pinned
	left    bool // this installation's own person no longer lists this device
	joined  bool // this installation now speaks for the person (adopt)
}

// pinChain verifies raws, the steps of person oldest first, as following
// what is pinned for it (from seq 0 when nothing is), and pins the newest.
// Steps already pinned are skipped; a different one at a pinned seq, or a
// device another pinned person already lists, freezes the person
// (errPersonConflict). me is this installation's device; with adopt, a
// person listing it becomes this installation's own (a device link).
func (s *store) pinChain(person string, raws [][]byte, me identity.Public, adopt bool) (pinResult, error) {
	var res pinResult
	tx, err := s.db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	head, pinned, err := personByIDIn(tx, person)
	if err != nil {
		return res, err
	}
	if pinned && head.info.State == personConflict {
		return res, errPersonConflict
	}
	// freeze freezes the pinned person other (a verified step conflicts
	// with it) and refuses the steps; this installation's own person is
	// never frozen, only the step refused.
	freeze := func(other string, raw []byte) (pinResult, error) {
		tx.Rollback()
		if _, err := s.db.Exec(`UPDATE persons SET state = ?, conflict = coalesce(conflict, ?) WHERE person = ? AND state = ?`,
			personConflict, string(raw), other, personPinned); err != nil {
			return res, err
		}
		s.changed()
		return res, errPersonConflict
	}
	cur, have := head.roster, pinned
	for _, raw := range raws {
		r, err := protocol.ParsePersonRoster(raw)
		if err != nil || r.Person != person {
			return res, fmt.Errorf("%w: %v", errPersonRecord, err)
		}
		if have && r.Seq <= cur.Seq {
			if ok, err := inChainIn(tx, person, r.Hash()); err != nil {
				return res, err
			} else if ok {
				continue
			}
			// Another step at a pinned seq: a conflict only if it verifies
			// (against the pinned step before it); otherwise it proves
			// nothing and changes nothing.
			if err := verifyAt(tx, person, r); err != nil {
				return res, fmt.Errorf("%w: %v", errPersonRecord, err)
			}
			return freeze(person, raw)
		}
		switch {
		case !have:
			err = r.VerifyFirst()
		case r.Seq != cur.Seq+1:
			err = errors.New("a step of the chain is missing")
		default:
			_, err = r.VerifyNext(cur)
		}
		if err != nil {
			return res, fmt.Errorf("%w: %v", errPersonRecord, err)
		}
		if _, err := tx.Exec(`INSERT INTO person_chain(person, seq, hash, record) VALUES(?, ?, ?, ?)`, person, r.Seq, r.Hash(), string(raw)); err != nil {
			return res, err
		}
		cur, have, res.changed = r, true, true
	}
	if !res.changed {
		return res, nil
	}
	raw, _ := json.Marshal(cur)
	lists := cur.Has(me.Address, me.Fingerprint())
	_, haveSelf, err := scanPersonIn(tx, `state = ?`, personSelf)
	if err != nil {
		return res, err
	}
	state := head.info.State
	switch {
	case !pinned && lists && adopt && !haveSelf:
		state, res.joined = personSelf, true
	case lists && state != personSelf:
		return res, errPersonConflict // a chain claims this device without this installation joining it
	case !pinned:
		state = personPinned
	case state == personSelf && !lists:
		state, res.left = personPinned, true
	}
	if _, err := tx.Exec(`INSERT INTO persons(person, label, seq, hash, record, state, pinned_at) VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(person) DO UPDATE SET label = excluded.label, seq = excluded.seq, hash = excluded.hash, record = excluded.record, state = excluded.state`,
		person, cur.Label, cur.Seq, cur.Hash(), string(raw), state, time.Now().Unix()); err != nil {
		return res, err
	}
	old := map[string]int64{}
	rows, err := tx.Query(`SELECT address, added FROM person_devices WHERE person = ?`, person)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var a string
		var s int64
		if err := rows.Scan(&a, &s); err != nil {
			rows.Close()
			return res, err
		}
		old[a] = s
	}
	rows.Close()
	if _, err := tx.Exec(`DELETE FROM person_devices WHERE person = ?`, person); err != nil {
		return res, err
	}
	for _, d := range cur.Devices {
		added, ok := old[d.Address]
		if !ok {
			added = cur.Seq
		}
		var other string
		switch err := tx.QueryRow(`SELECT person FROM person_devices WHERE address = ?`, d.Address).Scan(&other); {
		case err == nil:
			return freeze(other, raw) // another pinned person lists this device: it equivocates
		case !errors.Is(err, sql.ErrNoRows):
			return res, err
		}
		if _, err := tx.Exec(`INSERT INTO person_devices(address, person, fingerprint, added) VALUES(?, ?, ?, ?)`, d.Address, person, d.Fingerprint(), added); err != nil {
			return res, err
		}
	}
	return res, s.done(tx.Commit())
}

// verifyAt verifies r against the step of person's pinned chain before it
// (seq 0: on its own).
func verifyAt(q dbq, person string, r protocol.PersonRoster) error {
	if r.Seq == 0 {
		return r.VerifyFirst()
	}
	var raw string
	if err := q.QueryRow(`SELECT record FROM person_chain WHERE person = ? AND seq = ?`, person, r.Seq-1).Scan(&raw); err != nil {
		return err
	}
	var prev protocol.PersonRoster
	if err := json.Unmarshal([]byte(raw), &prev); err != nil {
		return err
	}
	_, err := r.VerifyNext(prev)
	return err
}

// ErrNoPerson means the device has published no person record.
var ErrNoPerson = errors.New("that device has no person yet (its owner must create one: agentnet person create NAME)")

// errPersonRecord means a published person record did not verify.
var errPersonRecord = errors.New("person record does not verify")

// fetchChain returns person's chain steps after seq, oldest first, from the
// Hub (paged).
func (a *Agent) fetchChain(ctx context.Context, person string, after int64) ([][]byte, error) {
	var out [][]byte
	for {
		var page protocol.PersonChain
		if err := a.hub.do(ctx, "GET", fmt.Sprintf("/v1/persons/%s/chain?after=%d", person, after), nil, &page); err != nil {
			return nil, err
		}
		for _, r := range page.Records {
			out = append(out, r)
		}
		if !page.More || len(page.Records) == 0 {
			return out, nil
		}
		var last protocol.PersonRoster
		if err := json.Unmarshal(page.Records[len(page.Records)-1], &last); err != nil {
			return nil, err
		}
		after = last.Seq
	}
}

// refreshPerson pins the steps of person newer than what is pinned here.
func (a *Agent) refreshPerson(ctx context.Context, person string, adopt bool) (pinResult, error) {
	after := int64(-1)
	if p, ok, err := a.store.personByID(person); err != nil {
		return pinResult{}, err
	} else if ok {
		if p.info.State == personConflict {
			return pinResult{}, errPersonConflict
		}
		after = p.info.Seq
	}
	raws, err := a.fetchChain(ctx, person, after)
	if err != nil {
		return pinResult{}, err
	}
	res, err := a.store.pinChain(person, raws, a.Self(), adopt)
	if err == nil && res.left {
		a.Logf("this device is no longer a device of its person")
		a.store.setConfig(map[string]string{"role": ""})
	}
	if err == nil && res.changed && stewardOf(a.store.db, person) {
		// A steward's newer roster: a device it added gets the waiting
		// requests by name now, one it removed stops deciding (MEL-532).
		a.wakeWorker()
	}
	return res, err
}

// personOfKey returns the person that the device at address, with key, its
// verified key, speaks for now, pinning its chain first if needed.
func (a *Agent) personOfKey(ctx context.Context, address string, key identity.Public) (personRow, error) {
	p, ok, err := a.store.personByAddress(address)
	if err != nil {
		return p, err
	}
	if ok {
		if p.info.State == personConflict {
			return p, errPersonConflict
		}
		if p.info.Fingerprint != key.Fingerprint() {
			return p, fmt.Errorf("%w: %s: its person lists another key for it", errPersonRecord, address)
		}
		return p, nil
	}
	label, name, err := protocol.SplitAddress(address)
	if err != nil {
		return p, err
	}
	var prof protocol.Profile
	err = a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof)
	var he *HubError
	if errors.As(err, &he) && he.Status == 404 {
		return p, ErrNoPerson
	}
	if err != nil {
		return p, err
	}
	if len(prof.Person) == 0 {
		return p, ErrNoPerson
	}
	r, err := protocol.ParsePersonRoster(prof.Person)
	if err != nil {
		return p, fmt.Errorf("%w: %s: %v", errPersonRecord, address, err)
	}
	if _, err := a.refreshPerson(ctx, r.Person, false); err != nil {
		return p, err
	}
	p, ok, err = a.store.personByAddress(address)
	if err != nil {
		return p, err
	}
	if !ok || p.info.Fingerprint != key.Fingerprint() {
		return p, fmt.Errorf("%w: %s: its person does not list this device's key", errPersonRecord, address)
	}
	if p.info.State == personConflict {
		return p, errPersonConflict
	}
	return p, nil
}

// observeRef compares a member list's reference to a person with what is
// pinned: a newer step of a person pinned (or this installation's own) is
// fetched and pinned; a person not pinned here is left alone (listing
// someone never pins or trusts them).
func (a *Agent) observeRef(ctx context.Context, ref *protocol.PersonRef) {
	if ref == nil {
		return
	}
	p, ok, err := a.store.personByID(ref.ID)
	if err != nil || !ok || p.info.State == personConflict {
		return
	}
	if ref.Seq < p.info.Seq || ref.Seq == p.info.Seq && ref.Hash == p.info.Roster {
		return
	}
	if _, err := a.refreshPerson(ctx, ref.ID, false); errors.Is(err, errPersonConflict) {
		a.Logf("the Hub serves a different roster for %q than the one pinned here: frozen (its conversations hold)", p.info.Label)
	} else if err != nil {
		a.Logf("person %s: %v", ref.ID, err)
	}
}

// checkPersons brings pinned persons up to the steps the member list
// names, and reads the rosters of persons it lists that are not pinned
// here into the listed cache (for their names; never pinned or trusted).
func (a *Agent) checkPersons(ctx context.Context, ms protocol.Members) {
	seen, wanted := map[string]bool{}, map[string]bool{}
	for _, m := range ms.Members {
		if m.Person == nil || seen[m.Person.ID] {
			continue
		}
		seen[m.Person.ID] = true
		if _, ok, err := a.store.personByID(m.Person.ID); err == nil && ok {
			a.observeRef(ctx, m.Person)
			continue
		}
		wanted[m.Person.Hash] = true
		if a.listed.has(m.Person.Hash) {
			continue
		}
		label, name, _ := protocol.SplitAddress(m.Address)
		var prof protocol.Profile
		if err := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
			continue
		}
		if r, err := protocol.ParsePersonRoster(prof.Person); err == nil && r.Hash() == m.Person.Hash {
			a.listed.put(r)
		}
	}
	a.listed.keep(wanted)
}

// listedCache holds, by roster hash, the rosters the Hub lists for persons
// not pinned here: unverified, for showing their claimed names only.
type listedCache struct {
	mu     sync.Mutex
	byHash map[string]protocol.PersonRoster
}

func (c *listedCache) has(hash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.byHash[hash]
	return ok
}

func (c *listedCache) put(r protocol.PersonRoster) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byHash == nil {
		c.byHash = map[string]protocol.PersonRoster{}
	}
	if len(c.byHash) < protocol.MaxMembers {
		c.byHash[r.Hash()] = r
	}
}

// keep drops the rosters the member list no longer names.
func (c *listedCache) keep(wanted map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for h := range c.byHash {
		if !wanted[h] {
			delete(c.byHash, h)
		}
	}
}

// ListedPersons returns the persons the Hub's member list names that are
// not pinned here, as far as their rosters were read already (a member
// whose roster was not read yet is left out; nothing waits on the
// network). Each is unverified: State "listed", the claimed label, its
// devices as the roster names them (Address: the first); never pinned or
// trusted by this.
func (a *Agent) ListedPersons() ([]PersonInfo, error) {
	a.listed.mu.Lock()
	defer a.listed.mu.Unlock()
	seen := map[string]bool{}
	var out []PersonInfo
	for _, m := range a.MemberView().Members.Members {
		if m.Person == nil || seen[m.Person.ID] {
			continue
		}
		r, ok := a.listed.byHash[m.Person.Hash]
		if !ok {
			continue
		}
		if _, pinned, err := a.store.personByID(r.Person); err != nil {
			return nil, err
		} else if pinned {
			continue
		}
		seen[r.Person] = true
		p := PersonInfo{Person: r.Person, Label: r.Label, Seq: r.Seq, Roster: r.Hash(), State: "listed"}
		for _, d := range r.Devices {
			_, name, _ := protocol.SplitAddress(d.Address)
			p.Devices = append(p.Devices, DeviceInfo{Address: d.Address, Name: name, Fingerprint: d.Fingerprint()})
		}
		p.Address, p.Fingerprint = p.Devices[0].Address, p.Devices[0].Fingerprint
		out = append(out, p)
	}
	slices.SortFunc(out, func(x, y PersonInfo) int { return strings.Compare(x.Label+x.Person, y.Label+y.Person) })
	return out, nil
}

// Role is what this installation is: "person" (it speaks for a person),
// "service" (it speaks as itself), or "" (not chosen yet).
func (a *Agent) Role() (string, error) {
	if _, ok, err := a.store.selfPerson(a.Address); err != nil {
		return "", err
	} else if ok {
		return "person", nil
	}
	switch r, err := a.store.config("role"); {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil // not chosen yet
	case err != nil:
		return "", err
	case r == "service":
		return r, nil
	}
	return "", nil
}

// betweenSetupChecks lets tests finish a device link between the two
// checks that refuse setting this device up a second way.
var betweenSetupChecks = func() {}

// SetService records that this installation is a service: it is not asked
// to set up a person again, and none is created or linked here.
func (a *Agent) SetService() error {
	if a.LinkState().State == LinkPending { // read first: a link pins its person before it is linked
		return ErrLinkWaiting
	}
	betweenSetupChecks()
	if _, ok, err := a.store.selfPerson(a.Address); err != nil {
		return err
	} else if ok {
		return errors.New("this installation already speaks for a person")
	}
	return a.store.done(a.store.setConfig(map[string]string{"role": "service"}))
}

// Person returns this installation's person, if it speaks for one.
func (a *Agent) Person() (PersonInfo, bool, error) {
	me, ok, err := a.store.selfPerson(a.Address)
	return me.info, ok, err
}

// CreatePerson creates this installation's person, with label as the name
// it shows (its own claim), and publishes its first roster. It is the only
// way a person comes to exist here besides a device link: never from an
// enrollment, address, label or migration. A returned ErrNotPublished means
// the person exists but the Hub does not hold it yet.
func (a *Agent) CreatePerson(ctx context.Context, label string) (PersonInfo, error) {
	// Its person comes with the approval. The link is read first: a link
	// pins its person before it is marked linked, so a link finished after
	// this read shows its person below.
	if a.LinkState().State == LinkPending {
		return PersonInfo{}, ErrLinkWaiting
	}
	betweenSetupChecks()
	if me, ok, err := a.store.selfPerson(a.Address); err != nil {
		return PersonInfo{}, err
	} else if ok {
		return me.info, fmt.Errorf("this installation already speaks for %q (%s); a second person is not created", me.info.Label, me.info.Person)
	}
	if role, _ := a.store.config("role"); role == "service" {
		return PersonInfo{}, ErrService
	}
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: label, Devices: []identity.Public{a.Self()}}
	if err := r.Validate(); err != nil {
		return PersonInfo{}, err
	}
	r.Sign(a.id.Sign)
	raw, _ := json.Marshal(r)
	if len(raw) > protocol.MaxPersonRecord {
		return PersonInfo{}, errors.New("person: the record is too large; use a shorter label")
	}
	if _, err := a.store.pinChain(r.Person, [][]byte{raw}, a.Self(), true); err != nil {
		return PersonInfo{}, err
	}
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		return PersonInfo{}, err
	}
	feats, err := a.relayFeatures(ctx)
	if err == nil && !slices.Contains(feats, protocol.FeaturePerson) {
		err = errors.New("the Hub does not hold persons of this version (it needs an update)")
	}
	if err == nil {
		err = a.publishPerson(ctx)
	}
	if err != nil {
		return me.info, fmt.Errorf("%w: %v", ErrNotPublished, err)
	}
	return me.info, nil
}

// publishPerson sends this installation's own newest step to the Hub if
// this device signed it (the Hub takes a step only from its signer) and it
// was not sent yet.
func (a *Agent) publishPerson(ctx context.Context) error {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		return err
	}
	if done, _ := a.store.config("person_published"); done == me.info.Roster {
		return nil
	}
	if !(me.roster.Seq == 0 && me.roster.Devices[0].Address == a.Address || me.roster.By == a.Self().Fingerprint()) {
		return nil // another device of the person signed it, and published it
	}
	if err := a.hub.doBytes(ctx, "PUT", "/v1/person", me.raw, nil); err != nil {
		return err
	}
	return a.store.setConfig(map[string]string{"person_published": me.info.Roster})
}

// PersonPublished reports whether this installation's person, as it is
// now, is known to be held by the Hub.
func (a *Agent) PersonPublished() bool {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		return false
	}
	if done, _ := a.store.config("person_published"); done == me.info.Roster {
		return true
	}
	return me.roster.Seq > 0 && me.roster.By != a.Self().Fingerprint() // published by the device that signed it
}

// KnownPersons lists the persons pinned here, other than this
// installation's own.
func (a *Agent) KnownPersons() ([]PersonInfo, error) {
	rows, err := a.store.db.Query(`SELECT person FROM persons WHERE state != ? ORDER BY label, person`, personSelf)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var out []PersonInfo
	for _, id := range ids {
		if p, ok, err := a.store.personByID(id); err != nil {
			return nil, err
		} else if ok {
			out = append(out, p.info)
		}
	}
	return out, nil
}

// deviceKey returns the key of person's device address with fingerprint
// fp from any pinned step of its chain (a removed device's key stays
// verifiable).
func (s *store) deviceKey(person, address, fp string) (identity.Public, bool) {
	rows, err := s.db.Query(`SELECT record FROM person_chain WHERE person = ? ORDER BY seq DESC`, person)
	if err != nil {
		return identity.Public{}, false
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var r protocol.PersonRoster
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &r) != nil {
			continue
		}
		if d, ok := r.Device(fp); ok && d.Address == address {
			return d, true
		}
	}
	return identity.Public{}, false
}
