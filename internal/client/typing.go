package client

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type TypingPreferences struct {
	Send bool `json:"send"`
	Show bool `json:"show"`
}
type TypingEntry struct {
	Person  string    `json:"person,omitempty"`
	Address string    `json:"address"`
	Label   string    `json:"label"`
	Expires time.Time `json:"expires"`
}
type TypingView struct {
	Scope       protocol.TypingScope `json:"scope"`
	Preferences TypingPreferences    `json:"preferences"`
	Supported   bool                 `json:"supported"`
	Current     bool                 `json:"current"`
	Entries     []TypingEntry        `json:"entries"`
}
type TypingResult struct {
	Submitted int  `json:"submitted"` // live fanout accepted, never delivery proof
	Skipped   int  `json:"skipped"`
	Throttled bool `json:"throttled"`
}
type typingSeen struct {
	ts      int64
	active  bool
	expires time.Time
	entry   TypingEntry
	scope   protocol.TypingScope
}
type typingSent struct {
	at     time.Time
	ts     int64
	active bool
}
type typingRuntime struct {
	mu        sync.Mutex
	connected bool
	supported bool
	seen      map[string]typingSeen
	replay    protocol.ReplayWindow
	sent      map[string]typingSent
	timer     *time.Timer
	// Group core installs its verified EFFECTIVE membership resolver;
	// nil refuses groups. Never substitute frozen root membership.
	groupMembers func(string) ([]protocol.ConvMember, error)
}

var ErrTypingScope = errors.New("typing requires a known exact conversation or peer/thread with verified current membership")

func (a *Agent) TypingPreferences() (TypingPreferences, error) {
	role, err := a.Role()
	if err != nil {
		return TypingPreferences{}, err
	}
	p := TypingPreferences{Send: role == "person", Show: role == "person"}
	for key, target := range map[string]*bool{"typing_send": &p.Send, "typing_show": &p.Show} {
		v, e := a.store.config(key)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return p, e
		}
		*target = v == "1"
	}
	return p, nil
}
func (a *Agent) SetTypingPreferences(p TypingPreferences) error {
	val := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	if err := a.store.setConfig(map[string]string{"typing_send": val(p.Send), "typing_show": val(p.Show)}); err != nil {
		return err
	}
	if !p.Show {
		a.typing.mu.Lock()
		for k, v := range a.typing.seen {
			v.active = false
			a.typing.seen[k] = v
		}
		a.typing.mu.Unlock()
	}
	a.changes.bump()
	return nil
}
func scopeKey(s protocol.TypingScope) string { return s.Conv + "\x00" + s.Peer + "\x00" + s.Thread }
func (a *Agent) typingMember(address string) bool {
	v := a.MemberView()
	if !v.Current {
		return false
	}
	for _, m := range v.Members.Members {
		if m.Address == address {
			return m.Presence != protocol.PresenceOffline
		}
	}
	return false
}

// Current pinned peer or pinned person-chain device. Never fetch or pin
// contacts, infer permission, or recover a changed key for a live signal.
func (a *Agent) typingKey(address string) (identity.Public, personRow, bool) {
	pub, pending, found, err := a.store.peer(address)
	if err != nil || pending != nil {
		return identity.Public{}, personRow{}, false
	}
	p, has, err := a.store.personByAddress(address)
	if err != nil || has && p.info.State == personConflict {
		return identity.Public{}, p, false
	}
	if has {
		dev, ok := p.device(address)
		if !ok || found && !sameKeys(pub, dev) {
			return identity.Public{}, p, false
		}
		for _, m := range a.MemberView().Members.Members {
			if m.Address == address && m.Person != nil && (m.Person.ID != p.info.Person || m.Person.Hash != p.info.Roster) {
				return identity.Public{}, p, false
			}
		}
		return dev, p, true
	}
	return pub, p, found
}
func (a *Agent) typingMembers(scope protocol.TypingScope) ([]protocol.ConvMember, error) {
	root, _, found, err := a.store.conversation(scope.Conv)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrTypingScope
	}
	if root.Kind == protocol.ConvKindDM && root.V == protocol.ConvRootVersion {
		return root.Members, nil
	}
	if root.Kind == protocol.ConvKindGroup {
		a.typing.mu.Lock()
		resolve := a.typing.groupMembers
		a.typing.mu.Unlock()
		if resolve != nil {
			return resolve(scope.Conv)
		}
	}
	return nil, ErrTypingScope
}
func (a *Agent) typingScopeAllows(scope protocol.TypingScope, address string, p personRow) bool {
	if scope.Validate() != nil || !a.typingMember(address) {
		return false
	}
	if scope.Conv == "" {
		if scope.Peer != address {
			return false
		}
		threads, err := a.peerThreads(address)
		if err != nil {
			return false
		}
		for _, t := range threads {
			if t.ID == scope.Thread && !t.NoticeOnly {
				return true
			}
		}
		return false
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok || p.info.Person == "" {
		return false
	}
	members, err := a.typingMembers(scope)
	if err != nil {
		return false
	}
	self, peer := false, false
	for _, m := range members {
		self = self || m.Person == me.info.Person
		peer = peer || m.Person == p.info.Person
	}
	if !self || !peer { // an accepted human guest shares the DM's typing as its turns
		dev, ok := p.device(address)
		self = self || a.typingGuest(scope.Conv, a.Address, a.Self().Fingerprint())
		peer = peer || ok && a.typingGuest(scope.Conv, address, dev.Fingerprint())
	}
	return self && peer
}

// typingGuest reports whether address with key fp is the exact host of a
// human participation of DM conv that is accepted now: never a pending,
// declined or ended one, another device of that person, or a changed key.
func (a *Agent) typingGuest(conv, address, fp string) bool {
	ok, err := humanEndReader(a.store.db, conv, address, fp)
	return err == nil && ok
}

// typingGuestHosts are the exact accepted human guest devices of DM conv.
func (a *Agent) typingGuestHosts(conv string) []protocol.ParticipationHost {
	m, err := a.dmMembers(conv)
	if err != nil || !externalDM(m.root) {
		return nil
	}
	infos, err := a.Participations(conv)
	if err != nil {
		return nil
	}
	var out []protocol.ParticipationHost
	for _, p := range infos {
		if p.HumanActive() {
			out = append(out, protocol.ParticipationHost{Person: p.Host.Person, Address: p.Host.Address, Fingerprint: p.Host.Fingerprint})
		}
	}
	return out
}
func (a *Agent) typingTargets(scope protocol.TypingScope) ([]identity.Public, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if scope.Conv == "" {
		pub, p, ok := a.typingKey(scope.Peer)
		if !ok || !a.typingScopeAllows(scope, scope.Peer, p) {
			return nil, ErrTypingScope
		}
		return []identity.Public{pub}, nil
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrTypingScope
	}
	members, err := a.typingMembers(scope)
	if err != nil {
		return nil, err
	}
	self := false
	for _, m := range members {
		self = self || m.Person == me.info.Person
	}
	if !self && !a.typingGuest(scope.Conv, a.Address, a.Self().Fingerprint()) {
		return nil, ErrTypingScope
	}
	var out []identity.Public
	seen := map[string]bool{}
	for _, m := range members {
		if m.Person == me.info.Person {
			continue
		}
		p, ok, err := a.store.personByID(m.Person)
		if err != nil {
			return nil, err
		}
		if !ok || p.info.State == personConflict {
			return nil, ErrTypingScope
		}
		for _, d := range p.roster.Devices {
			key, person, ok := a.typingKey(d.Address)
			if ok && a.typingScopeAllows(scope, d.Address, person) && !seen[key.Address] {
				out = append(out, key)
				seen[key.Address] = true
			}
		}
	}
	for _, g := range a.typingGuestHosts(scope.Conv) {
		if g.Address == a.Address || seen[g.Address] {
			continue
		}
		key, person, ok := a.typingKey(g.Address)
		if ok && key.Fingerprint() == g.Fingerprint && a.typingScopeAllows(scope, g.Address, person) {
			out = append(out, key)
			seen[g.Address] = true
		}
	}
	return out, nil
}
func (a *Agent) SendTyping(ctx context.Context, scope protocol.TypingScope, active bool) (TypingResult, error) {
	var result TypingResult
	prefs, err := a.TypingPreferences()
	if err != nil {
		return result, err
	}
	if !prefs.Send {
		return result, nil
	}
	a.typing.mu.Lock()
	connected := a.typing.connected && a.typing.supported
	a.typing.mu.Unlock()
	if !connected {
		return result, nil
	}
	realm, err := a.RealmID()
	if err != nil {
		return result, err
	}
	keys, err := a.typingTargets(scope)
	if err != nil {
		return result, err
	}
	now := time.Now()
	key := scopeKey(scope)
	a.typing.mu.Lock()
	if a.typing.sent == nil {
		a.typing.sent = map[string]typingSent{}
	}
	for k, s := range a.typing.sent {
		if now.Sub(s.at) > protocol.SignalTTL {
			delete(a.typing.sent, k)
		}
	}
	prev := a.typing.sent[key]
	if active && prev.active && now.Sub(prev.at) < protocol.TypingThrottle {
		a.typing.mu.Unlock()
		result.Throttled = true
		return result, nil
	}
	if !active && !prev.active {
		a.typing.mu.Unlock()
		return result, nil
	}
	if len(a.typing.sent) >= 256 && prev.at.IsZero() {
		a.typing.mu.Unlock()
		return result, nil
	}
	ts := now.UnixMilli()
	if ts <= prev.ts {
		ts = prev.ts + 1
	}
	a.typing.sent[key] = typingSent{at: now, ts: ts, active: active}
	a.typing.mu.Unlock()
	for _, pub := range keys {
		label, name, _ := protocol.SplitAddress(pub.Address)
		var prof protocol.Profile
		if e := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); e != nil || !prof.Live || !prof.Supports(pub.Address, pub.SignKey, protocol.CapTyping) {
			result.Skipped++
			continue
		}
		current, p, ok := a.typingKey(pub.Address)
		if !ok || !sameKeys(current, pub) || !a.typingScopeAllows(scope, pub.Address, p) {
			result.Skipped++
			continue
		}
		for _, session := range prof.Sessions {
			in := protocol.TypingPlain{V: 1, ID: protocol.NewID(), From: a.Address, To: pub.Address, TS: ts, Session: session, Realm: realm, Conv: scope.Conv, Thread: scope.Thread, Origin: "human", Active: active}
			v, e := protocol.SealTyping(in, a.id.Sign, pub)
			if e != nil {
				return result, e
			}
			if e = a.hub.do(ctx, "POST", "/v1/signal", v, nil); e != nil {
				result.Skipped++
				continue
			}
			result.Submitted++
		}
	}
	return result, nil
}
func (a *Agent) typingConnected(headers http.Header) {
	a.typing.mu.Lock()
	a.typing.connected = true
	a.typing.supported = headers.Get(protocol.SignalsHeader) == "1"
	a.typing.mu.Unlock()
	a.changes.bump()
}
func (a *Agent) typingDisconnected() {
	a.typing.mu.Lock()
	a.typing.connected = false
	for k, v := range a.typing.seen {
		v.active = false
		a.typing.seen[k] = v
	}
	if a.typing.timer != nil {
		a.typing.timer.Stop()
		a.typing.timer = nil
	}
	a.typing.mu.Unlock()
	a.changes.bump()
}
func (a *Agent) typingMembershipChanged() { a.changes.bump() } // views recheck current membership and keys
func (a *Agent) onSignal(raw []byte) {
	prefs, e := a.TypingPreferences()
	if e != nil || !prefs.Show {
		return
	}
	v, e := protocol.ParseSignal(raw)
	if e != nil || v.Session != a.session {
		return
	}
	a.typing.mu.Lock()
	connected := a.typing.connected && a.typing.supported
	a.typing.mu.Unlock()
	if !connected {
		return
	}
	pub, p, ok := a.typingKey(v.From)
	if !ok {
		return
	}
	realm, e := a.RealmID()
	if e != nil {
		return
	}
	now := time.Now()
	in, e := protocol.OpenTyping(v, a.id, a.Address, pub, realm, now)
	if e != nil {
		return
	}
	scope := protocol.TypingScope{Conv: in.Conv, Peer: in.From, Thread: in.Thread}
	if in.Conv != "" {
		scope.Peer = ""
	}
	if !a.typingScopeAllows(scope, in.From, p) {
		return
	}
	if me, ok, _ := a.Person(); ok && me.Person == p.info.Person {
		return
	}
	label := in.From
	if p.info.Person != "" {
		label = p.info.Label
	}
	expires := time.UnixMilli(in.TS).Add(protocol.SignalTTL)
	if bound := now.Add(protocol.SignalTTL); expires.After(bound) {
		expires = bound
	}
	key := scopeKey(scope) + "\x00" + pub.Fingerprint()
	a.typing.mu.Lock()
	if !a.typing.connected || !a.typing.supported {
		a.typing.mu.Unlock()
		return
	}
	if a.typing.seen == nil {
		a.typing.seen = map[string]typingSeen{}
		a.typing.replay = protocol.ReplayWindow{}
	}
	a.pruneTypingLocked(now)
	if !a.typing.replay.Accept(in.From+"\x00"+in.ID, time.UnixMilli(in.TS).Add(protocol.SignalTTL), now, 2048) {
		a.typing.mu.Unlock()
		return
	}
	old, found := a.typing.seen[key]
	// STOP wins equal signed milliseconds, independent of arrival and ID.
	if found && (old.ts > in.TS || old.ts == in.TS && (!old.active || in.Active)) {
		a.typing.mu.Unlock()
		return
	}
	if len(a.typing.seen) >= 256 && !found {
		a.typing.mu.Unlock()
		return
	}
	a.typing.seen[key] = typingSeen{ts: in.TS, active: in.Active, expires: expires, scope: scope, entry: TypingEntry{Person: p.info.Person, Address: in.From, Label: label, Expires: expires}}
	a.armTypingLocked(now)
	a.typing.mu.Unlock()
	a.changes.bump()
}
func (a *Agent) pruneTypingLocked(now time.Time) bool {
	changed := false
	for key, v := range a.typing.seen {
		if !v.expires.After(now) {
			changed = changed || v.active
			delete(a.typing.seen, key)
		}
	}
	return changed
}
func (a *Agent) armTypingLocked(now time.Time) {
	if a.typing.timer != nil {
		a.typing.timer.Stop()
		a.typing.timer = nil
	}
	if !a.typing.connected {
		return
	}
	var next time.Time
	for _, v := range a.typing.seen {
		if next.IsZero() || v.expires.Before(next) {
			next = v.expires
		}
	}
	if next.IsZero() {
		return
	}
	a.typing.timer = time.AfterFunc(next.Sub(now), func() {
		a.typing.mu.Lock()
		changed := a.pruneTypingLocked(time.Now())
		a.armTypingLocked(time.Now())
		a.typing.mu.Unlock()
		if changed {
			a.changes.bump()
		}
	})
}
func (a *Agent) Typing(scope protocol.TypingScope) (TypingView, error) {
	view := TypingView{Scope: scope, Entries: []TypingEntry{}}
	prefs, e := a.TypingPreferences()
	if e != nil {
		return view, e
	}
	view.Preferences = prefs
	if scope.Conv != "" || scope.Peer != "" || scope.Thread != "" {
		if e := scope.Validate(); e != nil {
			return view, e
		}
	}
	a.typing.mu.Lock()
	a.pruneTypingLocked(time.Now())
	view.Supported = a.typing.supported
	view.Current = a.typing.connected && a.MemberView().Current
	var candidates []typingSeen
	for _, v := range a.typing.seen {
		if v.active && scopeKey(v.scope) == scopeKey(scope) {
			candidates = append(candidates, v)
		}
	}
	a.typing.mu.Unlock()
	if !prefs.Show || !view.Current {
		return view, nil
	}
	byPerson := map[string]TypingEntry{}
	for _, v := range candidates {
		_, p, ok := a.typingKey(v.entry.Address)
		if !ok || !a.typingScopeAllows(scope, v.entry.Address, p) {
			continue
		}
		id := v.entry.Person
		if id == "" {
			id = v.entry.Address
		}
		if prev, ok := byPerson[id]; !ok || v.entry.Expires.After(prev.Expires) {
			byPerson[id] = v.entry
		}
	}
	for _, entry := range byPerson {
		view.Entries = append(view.Entries, entry)
	}
	sort.Slice(view.Entries, func(i, j int) bool { return view.Entries[i].Address < view.Entries[j].Address })
	return view, nil
}
