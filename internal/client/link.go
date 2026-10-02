package client

import (
	"bufio"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Linking another device to this installation's person (protocol/link.go).
//
// Here, the existing device E: NewDeviceLink makes an offer (a device invite
// from the Hub, and a secret only the code carries); the Hub's "link" event
// brings the new device's request, checked against the offer (MAC, consent)
// and kept as a pending link, consuming the offer; the person decides with
// DecideLink, and an approval publishes the roster step that admits the
// device (retried until the Hub answers, after restarts too).
//
// There, the new device N: JoinAndLink joins with the code's device invite
// and waits (AwaitLink, or the daemon) on its only stream until the Hub says
// it is linked; it then pins its person's chain, which must name it in the
// step the approver signed.

// Typed link outcomes.
var (
	ErrLinkForged      = errors.New("the device link request does not match the code (forged or damaged)")
	ErrLinkUsed        = errors.New("that device link code was used already")
	ErrLinkExpired     = errors.New("that device link code expired, or nobody approved the device in time")
	ErrLinkStale       = errors.New("that device link code is out of date: make a new one on your other device")
	ErrLinkCrossPerson = errors.New("this device already belongs to a person (or the code is for another person)")
	ErrLinkRefused     = errors.New("the device link was refused on the other device")
	ErrRosterStale     = errors.New("your person's devices changed meanwhile: try again")
)

// Link states (device_links.state, and LinkStatus.State on the new device).
const (
	LinkPending  = "pending"  // waits for the person's decision (E) or approval (N)
	LinkApproved = "approved" // approved; the roster step is being published (E)
	LinkLinked   = "linked"
	LinkRefused  = "refused"
	LinkExpired  = "expired"
	LinkStale    = "stale"
	LinkFailed   = "failed"
)

// DeviceLinkOffer is a code another device of this person can link with.
type DeviceLinkOffer struct {
	Code    string    `json:"code"` // protocol.LinkPrefix…; in a QR, as a URL fragment
	Expires time.Time `json:"expires"`
}

// NewDeviceLink makes a one-use code that lets a new device join this Hub
// as a device of this installation's person, once the person approves it
// here. The code carries a secret: show it only to the person.
func (a *Agent) NewDeviceLink(ctx context.Context) (DeviceLinkOffer, error) {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return DeviceLinkOffer{}, err
	}
	if !ok {
		return DeviceLinkOffer{}, errors.New("set up your person first (agentnet person create NAME)")
	}
	if !a.PersonPublished() {
		if err := a.publishPerson(ctx); err != nil {
			return DeviceLinkOffer{}, fmt.Errorf("%w: %v", ErrNotPublished, err)
		}
	}
	offer, expires := protocol.NewID(), time.Now().Add(protocol.MaxLinkTTL*time.Second).Unix()
	var inv protocol.DeviceInvite
	if err := a.hub.do(ctx, "POST", "/v1/person/device-invite", protocol.DeviceInviteRequest{Offer: offer, Expires: expires}, &inv); err != nil {
		return DeviceLinkOffer{}, err
	}
	secret := make([]byte, protocol.LinkSecretSize)
	if _, err := crand.Read(secret); err != nil {
		return DeviceLinkOffer{}, err
	}
	if _, err := a.store.db.Exec(`INSERT INTO link_offers(offer, secret, person, seq, roster, expires, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		offer, secret, me.info.Person, me.info.Seq, me.info.Roster, expires, time.Now().Unix()); err != nil {
		return DeviceLinkOffer{}, err
	}
	self := a.Self()
	o := protocol.LinkOffer{V: 2, Invite: inv.Code, Offer: offer, Expires: expires, Person: me.info.Person, Seq: me.info.Seq, Roster: me.info.Roster,
		Approver: protocol.LinkApprover{Address: a.Address, Fingerprint: self.Fingerprint()}, Secret: secret}
	return DeviceLinkOffer{Code: o.Encode(), Expires: time.Unix(expires, 0)}, nil
}

// offerRow is a stored offer, as the code it was shown in.
func (a *Agent) offerRow(q querier, offer string) (protocol.LinkOffer, bool, bool, error) {
	var o protocol.LinkOffer
	var used bool
	err := q.QueryRow(`SELECT offer, secret, person, seq, roster, expires, used FROM link_offers WHERE offer = ?`, offer).
		Scan(&o.Offer, &o.Secret, &o.Person, &o.Seq, &o.Roster, &o.Expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, false, nil
	}
	o.V, o.Approver = 2, protocol.LinkApprover{Address: a.Address, Fingerprint: a.Self().Fingerprint()}
	return o, true, used, err
}

// onLinkEvent takes the Hub's "link" event: a device joined with one of
// this device's offers. A request that matches its offer becomes a pending
// link and uses the offer up; anything else changes nothing (so nobody
// without the code can use an offer up). A redelivered event is a no-op.
func (a *Agent) onLinkEvent(data []byte) {
	var ev protocol.LinkEvent
	if err := json.Unmarshal(data, &ev); err != nil || ev.Device.Verify() != nil {
		a.Logf("device link event ignored: malformed")
		return
	}
	if err := a.takeLinkRequest(ev); err != nil {
		a.Logf("device link request from %s: %v", ev.Device.Address, err)
		return
	}
	a.Logf("%s asks to be linked to your person: approve or refuse it on this device", ev.Device.Address)
	a.wakeWorker()
}

func (a *Agent) takeLinkRequest(ev protocol.LinkEvent) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, found, used, err := a.offerRow(tx, ev.Offer)
	if err != nil {
		return err
	}
	if !found {
		return ErrLinkForged
	}
	if used {
		var addr, pub string
		if err := tx.QueryRow(`SELECT address, public FROM device_links WHERE offer = ?`, ev.Offer).Scan(&addr, &pub); err == nil {
			if d, _ := json.Marshal(ev.Device); addr == ev.Device.Address && pub == string(d) {
				return nil // the same request again
			}
		}
		return ErrLinkUsed
	}
	if !protocol.CheckLinkMAC(o, ev.Device, ev.Join, ev.MAC) ||
		!ed25519.Verify(ev.Device.SignKey, protocol.JoinBytes(o.Person, o.Seq+1, o.Roster, ev.Device), ev.Join) {
		return ErrLinkForged
	}
	if time.Now().Unix() >= o.Expires {
		return ErrLinkExpired
	}
	myLabel, _, _ := protocol.SplitAddress(a.Address)
	if label, _, err := protocol.SplitAddress(ev.Device.Address); err != nil || label != myLabel {
		return ErrLinkForged
	}
	state := LinkPending
	me, ok, err := personBySelfIn(tx, a.Address)
	if err != nil {
		return err
	}
	if !ok || me.info.Person != o.Person {
		return ErrLinkCrossPerson
	}
	if me.info.Seq != o.Seq || me.info.Roster != o.Roster {
		state = LinkStale // valid, but the roster moved on: shown, never approved
	}
	pub, _ := json.Marshal(ev.Device)
	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE link_offers SET used = 1 WHERE offer = ?`, ev.Offer); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO device_links(offer, address, public, join_sig, requested_at, expires, state, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.Offer, ev.Device.Address, string(pub), ev.Join, now, o.Expires, state, now); err != nil {
		return err
	}
	return a.store.done(tx.Commit())
}

// personBySelfIn is selfPerson within a transaction.
func personBySelfIn(q dbq, address string) (personRow, bool, error) {
	p, ok, err := scanPersonIn(q, `state = ?`, personSelf)
	return p.at(address), ok, err
}

// LinkRequest is a device asking to be linked to this person here.
type LinkRequest struct {
	ID          string `json:"id"` // the offer it answers
	Address     string `json:"address"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	RequestedAt int64  `json:"requested_at"`
	Expires     int64  `json:"expires"` // unix seconds: decide before
	State       string `json:"state"`
	Detail      string `json:"detail,omitempty"`
}

// PendingLinks lists the device link requests held here, newest first
// (decided ones too, with their outcome).
func (a *Agent) PendingLinks() ([]LinkRequest, error) {
	rows, err := a.store.db.Query(`SELECT offer, address, public, requested_at, expires, state, coalesce(detail, '') FROM device_links ORDER BY requested_at DESC, offer`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkRequest
	for rows.Next() {
		var l LinkRequest
		var pub string
		if err := rows.Scan(&l.ID, &l.Address, &pub, &l.RequestedAt, &l.Expires, &l.State, &l.Detail); err != nil {
			return nil, err
		}
		var d identity.Public
		json.Unmarshal([]byte(pub), &d)
		_, l.Name, _ = protocol.SplitAddress(l.Address)
		l.Fingerprint = d.Fingerprint()
		if l.State == LinkPending && time.Now().Unix() >= l.Expires {
			l.State = LinkExpired
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DecideLink approves (accept) or refuses the pending link request id.
// An approval checks again that the code has not expired and the person's
// devices have not changed, signs the roster step that adds the device and
// publishes it; if the Hub cannot be reached, it stays approved and is
// published when it can (never as a second, competing step).
func (a *Agent) DecideLink(ctx context.Context, id string, accept bool) error {
	var address, pub, state string
	var join []byte
	var expires int64
	err := a.store.db.QueryRow(`SELECT address, public, join_sig, expires, state FROM device_links WHERE offer = ?`, id).Scan(&address, &pub, &join, &expires, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no device link request %s here", id)
	}
	if err != nil {
		return err
	}
	switch {
	case state == LinkApproved && accept:
		return a.publishLink(ctx, id)
	case state != LinkPending:
		return fmt.Errorf("that request is %s already", state)
	case !accept:
		a.setLink(id, LinkRefused, "")
		if err := a.hub.do(ctx, "POST", "/v1/person/device-refuse", protocol.DeviceRefusal{Address: address}, nil); err != nil {
			a.Logf("refusing %s on the Hub: %v (it expires there anyway)", address, err) // the device was never admitted
		}
		return nil
	case time.Now().Unix() >= expires:
		a.setLink(id, LinkExpired, "")
		return ErrLinkExpired
	}
	o, _, _, err := a.offerRow(a.store.db, id)
	if err != nil {
		return err
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || me.info.Person != o.Person {
		a.setLink(id, LinkFailed, "this device no longer speaks for that person")
		return ErrLinkCrossPerson
	}
	if me.info.Seq != o.Seq || me.info.Roster != o.Roster {
		a.setLink(id, LinkStale, "")
		return ErrLinkStale
	}
	var dev identity.Public
	if err := json.Unmarshal([]byte(pub), &dev); err != nil {
		return err
	}
	r := protocol.PersonRoster{Person: me.info.Person, Label: me.info.Label, Seq: me.info.Seq + 1, Prev: me.info.Roster,
		Devices: append(append([]identity.Public(nil), me.roster.Devices...), dev), By: a.Self().Fingerprint(), Join: join}
	r.Sign(a.id.Sign)
	if _, err := r.VerifyNext(me.roster); err != nil {
		a.setLink(id, LinkFailed, err.Error())
		return err
	}
	raw, _ := json.Marshal(r)
	res, err := a.store.db.Exec(`UPDATE device_links SET state = ?, roster = ?, updated_at = ? WHERE offer = ? AND state = ?`, LinkApproved, string(raw), time.Now().Unix(), id, LinkPending)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("that request was decided meanwhile")
	}
	return a.publishLink(ctx, id)
}

func (a *Agent) setLink(id, state, detail string) {
	a.store.db.Exec(`UPDATE device_links SET state = ?, detail = nullif(?, ''), updated_at = ? WHERE offer = ?`, state, detail, time.Now().Unix(), id)
	a.store.changed()
}

// publishLink publishes an approved link's roster step and pins it here.
func (a *Agent) publishLink(ctx context.Context, id string) error {
	var raw string
	if err := a.store.db.QueryRow(`SELECT roster FROM device_links WHERE offer = ? AND state = ?`, id, LinkApproved).Scan(&raw); err != nil {
		return err
	}
	err := a.hub.doBytes(ctx, "PUT", "/v1/person", []byte(raw), nil)
	var he *HubError
	switch {
	case errors.As(err, &he) && he.Code == protocol.CodeRosterStale:
		a.setLink(id, LinkStale, "")
		return ErrLinkStale
	case errors.As(err, &he) && he.Code == protocol.CodeLinkExpired:
		a.setLink(id, LinkExpired, "")
		return ErrLinkExpired
	case err != nil && retryable(err):
		return fmt.Errorf("approved; the Hub has not taken it yet (it is sent again when it can): %w", err)
	case err != nil:
		a.setLink(id, LinkFailed, err.Error())
		return err
	}
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if _, err := a.store.pinChain(me.info.Person, [][]byte{[]byte(raw)}, a.Self(), false); err != nil {
		return err
	}
	var step protocol.PersonRoster
	json.Unmarshal([]byte(raw), &step)
	a.store.setConfig(map[string]string{"person_published": step.Hash()})
	a.setLink(id, LinkLinked, "")
	a.Logf("linked %s to your person", a.linkAddress(id))
	a.onLinked(id)
	return nil
}

func (a *Agent) linkAddress(id string) string {
	var address string
	a.store.db.QueryRow(`SELECT address FROM device_links WHERE offer = ?`, id).Scan(&address)
	return address
}

// onLinked starts sending this device's conversations to the device the
// link added (history.go).
func (a *Agent) onLinked(id string) {
	var pub string
	if err := a.store.db.QueryRow(`SELECT public FROM device_links WHERE offer = ?`, id).Scan(&pub); err != nil {
		return
	}
	var dev identity.Public
	if json.Unmarshal([]byte(pub), &dev) != nil {
		return
	}
	if err := a.startHistory(dev); err != nil {
		a.Logf("history for %s: %v", dev.Address, err)
	}
}

// retryApprovedLinks publishes approved links the Hub has not taken yet.
func (a *Agent) retryApprovedLinks(ctx context.Context) {
	rows, err := a.store.db.Query(`SELECT offer FROM device_links WHERE state = ?`, LinkApproved)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := a.publishLink(ctx, id); err != nil {
			a.Logf("device link: %v", err)
		}
	}
}

// RemoveDevice takes the device at address out of this installation's
// person (this device itself included, unless it is the last one). A
// device a link admitted is revoked on the Hub with it.
func (a *Agent) RemoveDevice(ctx context.Context, address string) error {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("this installation does not speak for a person")
	}
	var keep []identity.Public
	for _, d := range me.roster.Devices {
		if d.Address != address {
			keep = append(keep, d)
		}
	}
	switch {
	case len(keep) == len(me.roster.Devices):
		return fmt.Errorf("%s is not a device of your person", address)
	case len(keep) == 0:
		return errors.New("the last device of a person cannot be removed")
	}
	r := protocol.PersonRoster{Person: me.info.Person, Label: me.info.Label, Seq: me.info.Seq + 1, Prev: me.info.Roster, Devices: keep, By: a.Self().Fingerprint()}
	r.Sign(a.id.Sign)
	raw, _ := json.Marshal(r)
	err = a.hub.doBytes(ctx, "PUT", "/v1/person", raw, nil)
	var he *HubError
	if errors.As(err, &he) && he.Code == protocol.CodeRosterStale {
		a.refreshPerson(ctx, me.info.Person, false)
		return ErrRosterStale
	}
	if err != nil {
		return err
	}
	if _, err = a.store.pinChain(me.info.Person, [][]byte{raw}, a.Self(), false); err != nil {
		return err
	}
	return a.store.setConfig(map[string]string{"person_published": r.Hash()})
}

// LinkStatus is the new device's side of a link.
type LinkStatus struct {
	State       string `json:"state"` // "" (no link), pending, linked, refused, expired, stale, failed
	Detail      string `json:"detail,omitempty"`
	Person      string `json:"person,omitempty"`
	Approver    string `json:"approver,omitempty"` // the device that approves it
	Expires     int64  `json:"expires,omitempty"`
	ApproverKey string `json:"approver_key,omitempty"` // its key fingerprint
	Seq         int64  `json:"seq,omitempty"`          // the roster step that adds this device
}

// JoinAndLink joins the Hub as agentName with a device link code from an
// existing device of a person, asking to be linked to that person. The
// device is enrolled PENDING: it is no member until the person approves it
// on that device (AwaitLink, or the daemon, waits for that).
func JoinAndLink(ctx context.Context, home, code, agentName string) (*Agent, error) {
	o, err := protocol.DecodeLinkOffer(code)
	if err != nil {
		return nil, err
	}
	if time.Now().Unix() >= o.Expires {
		return nil, ErrLinkExpired
	}
	// The join carries this device's consent to be the step after the
	// offer's, and the MAC under the offer's secret; the pending link is
	// saved with the enrollment.
	return join(ctx, home, o.Invite, agentName, func(pub identity.Public, sign ed25519.PrivateKey) (*protocol.JoinLink, map[string]string) {
		consent := ed25519.Sign(sign, protocol.JoinBytes(o.Person, o.Seq+1, o.Roster, pub))
		state, _ := json.Marshal(LinkStatus{State: LinkPending, Person: o.Person, Approver: o.Approver.Address, Expires: o.Expires,
			ApproverKey: o.Approver.Fingerprint, Seq: o.Seq + 1})
		return &protocol.JoinLink{Offer: o.Offer, Join: consent, MAC: protocol.LinkMAC(o, pub, consent)}, map[string]string{"link": string(state)}
	})
}

// LinkState reports this device's link, if it joined with a link code.
func (a *Agent) LinkState() LinkStatus {
	var s LinkStatus
	if raw, _ := a.store.config("link"); raw != "" {
		json.Unmarshal([]byte(raw), &s)
	}
	return s
}

func (a *Agent) setLinkState(s LinkStatus) {
	raw, _ := json.Marshal(s)
	a.store.setConfig(map[string]string{"link": string(raw)})
	a.store.changed()
}

// AwaitLink waits, on this device's only stream, until its person approves
// the link (then pins the person's chain, which must name this device in
// the step the approver signed), refuses it, or it expires. A device that
// is not waiting returns its state at once.
func (a *Agent) AwaitLink(ctx context.Context) (LinkStatus, error) {
	ad := protocol.SessionAd{Address: a.Address, Session: protocol.NewID()}
	protocol.SignAd(&ad, a.id.Sign)
	return a.awaitLink(ctx, "?ad="+ad.Encode(), ad.Session)
}

// awaitLink is AwaitLink on the stream of session (query: its signed ad).
func (a *Agent) awaitLink(ctx context.Context, query, session string) (LinkStatus, error) {
	s := a.LinkState()
	if s.State != LinkPending {
		return s, nil
	}
	reconnect := newReconnectBackoff() // never reset: the pending stream is not a healthy connection
	reconnect.Reset()
	for {
		linked, err := a.pendingStream(ctx, query, session)
		if !linked && err == nil {
			// The stream ended: activated meanwhile, or ended? A member
			// request tells (a pending device is refused it).
			err = a.hub.do(ctx, "GET", "/v1/agents", nil, nil)
			linked = err == nil
		}
		var he *HubError
		switch {
		case linked:
			return a.finishLink(ctx)
		case errors.As(err, &he) && he.Code == protocol.CodeLinkRefused:
			s.State = LinkRefused
			a.setLinkState(s)
			return s, ErrLinkRefused
		case errors.As(err, &he) && (he.Code == protocol.CodeLinkExpired || he.Code == protocol.CodeRevoked):
			s.State = LinkExpired
			a.setLinkState(s)
			return s, ErrLinkExpired
		case ctx.Err() != nil:
			return s, ctx.Err()
		}
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-time.After(reconnect.NextBackOff()):
		}
	}
}

// pendingStream holds the pending device's stream: pings answered, until
// "linked" (true) or the stream ends.
func (a *Agent) pendingStream(ctx context.Context, query, session string) (linked bool, err error) {
	req, err := a.hub.request(ctx, "GET", "/v1/stream"+query, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := a.hub.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return false, err
	}
	if resp.Header.Get(protocol.MembersHeader) == "1" {
		// A member's stream: activated before this stream connected. The
		// Hub counts this session as live for a while: say what it reads,
		// as a daemon's session does, so nobody waits on it.
		rec := protocol.CapsRecord{Address: a.Address, Session: session, Caps: ownCaps, TS: time.Now().Unix()}
		rec.Sign(a.id.Sign)
		if err := a.hub.do(ctx, "PUT", "/v1/caps", rec, nil); err != nil {
			a.Logf("capabilities of the waiting session: %v", err)
		}
		return true, nil
	}
	sc := bufio.NewScanner(resp.Body)
	var event, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			switch event {
			case "linked":
				return true, nil
			case "ping":
				var ping protocol.PingAck
				if json.Unmarshal([]byte(data), &ping) == nil && ping.Conn != "" {
					a.hub.do(ctx, "POST", "/v1/stream/ack", ping, nil)
				}
			}
			event, data = "", ""
		case strings.HasPrefix(line, "event: "):
			event = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			data += line[len("data: "):]
		}
	}
	return false, sc.Err()
}

// finishLink pins the person's chain once the Hub admitted this device: the
// chain must name it in the step after the offer's, signed by the approver.
func (a *Agent) finishLink(ctx context.Context) (LinkStatus, error) {
	s := a.LinkState()
	res, err := a.refreshPerson(ctx, s.Person, true)
	if err != nil {
		s.State, s.Detail = LinkFailed, err.Error()
		a.setLinkState(s)
		return s, err
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return s, err
	}
	var step protocol.PersonRoster
	var raw string
	err = a.store.db.QueryRow(`SELECT record FROM person_chain WHERE person = ? AND seq = ?`, s.Person, s.Seq).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &step)
	}
	if !ok || me.info.Person != s.Person || err != nil || step.By != s.ApproverKey || !step.Has(a.Address, a.Self().Fingerprint()) {
		s.State, s.Detail = LinkFailed, "the person's roster does not name this device in the step its approver signed"
		a.setLinkState(s)
		return s, ErrLinkCrossPerson
	}
	_ = res
	s.State = LinkLinked
	a.setLinkState(s)
	return s, nil
}
