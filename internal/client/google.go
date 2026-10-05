package client

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// GoogleDevice creates/loads this installation's ordinary device keys
// before sign-in, so the Google nonce can bind the login to those keys.
func GoogleDevice(home, name string) (identity.Public, error) {
	if !protocol.ValidName(name) {
		return identity.Public{}, errors.New("invalid device name")
	}
	if err := secfile.EnsureDir(home); err != nil {
		return identity.Public{}, err
	}
	p := filepath.Join(home, "identity.json")
	id, err := identity.Load(p)
	if errors.Is(err, os.ErrNotExist) {
		id, err = identity.Generate()
		if err == nil {
			err = id.Save(p)
		}
	}
	if err != nil {
		return identity.Public{}, err
	}
	return id.Public(protocol.Address("google", name)), nil
}

type GoogleOptions struct{ Hub, CertPEM string }

func GoogleConfiguration(ctx context.Context, o GoogleOptions) (protocol.GoogleConfig, error) {
	c, err := newHubConn(o.Hub, o.CertPEM, "", nil)
	if err != nil {
		return protocol.GoogleConfig{}, err
	}
	defer c.release()
	var cfg protocol.GoogleConfig
	err = c.do(ctx, "GET", "/v1/google/config", nil, &cfg)
	return cfg, err
}

func GoogleExchangeCode(ctx context.Context, home string, o GoogleOptions, e protocol.GoogleExchange) (string, error) {
	id, err := identity.Load(filepath.Join(home, "identity.json"))
	if err != nil {
		return "", err
	}
	e.Sign(id.Sign)
	c, err := newHubConn(o.Hub, o.CertPEM, "", nil)
	if err != nil {
		return "", err
	}
	defer c.release()
	var res struct {
		IDToken string `json:"id_token"`
	}
	if err = c.do(ctx, "POST", "/v1/google/exchange", e, &res); err != nil {
		return "", errors.New("Google sign-in failed; check the workspace's Google setup and try again")
	}
	return res.IDToken, nil
}

// JoinGoogle keeps only keys and enrollment intent/binding. The ID token
// remains in memory. Reauthentication retries the saved request's exact
// keys and roster/consent after an ambiguous relay response or a crash.
func JoinGoogle(ctx context.Context, home string, o GoogleOptions, token, name string) (*Agent, error) {
	pub, err := GoogleDevice(home, name)
	if err != nil {
		return nil, err
	}
	id, err := identity.Load(filepath.Join(home, "identity.json"))
	if err != nil {
		return nil, err
	}
	st, err := openStore(filepath.Join(home, "agent.db"))
	if err != nil {
		return nil, err
	}
	defer st.db.Close()
	if enrolled, _ := st.config("enrolled"); enrolled == "1" {
		return nil, errors.New("this computer already joined; open AgentNet")
	}
	if previous, _ := st.config("hub"); previous != "" && previous != o.Hub {
		return nil, errors.New("finish joining the original workspace first")
	}
	c, err := newHubConn(o.Hub, o.CertPEM, "", nil)
	if err != nil {
		return nil, err
	}
	defer c.release()
	var version protocol.VersionInfo
	if err = c.do(ctx, "GET", "/v1/version", nil, &version); err != nil {
		return nil, err
	}
	if version.Protocol != protocol.ProtocolVersion {
		return nil, errors.New("workspace needs an AgentNet update")
	}
	if version.RealmID != "" {
		if err = st.recordRealm(version.RealmID); err != nil {
			return nil, err
		}
	}
	req := protocol.GoogleRequest{IDToken: token, Public: pub}
	req.Sign(id.Sign)
	var prepared protocol.GooglePrepared
	if err = c.do(ctx, "POST", "/v1/google/prepare", req, &prepared); err != nil {
		return nil, err
	}
	if email, e := protocol.NormalizeEmail(prepared.Email); e != nil || email != prepared.Email {
		return nil, errors.New("invalid Google email binding")
	}
	req.Public = id.Public(prepared.Address)
	if saved, _ := st.config("google_intent"); saved != "" {
		var intent protocol.GoogleRequest
		if json.Unmarshal([]byte(saved), &intent) != nil || intent.Public.Address != prepared.Address || intent.Public.Fingerprint() != pub.Fingerprint() || intent.Public.BoxRecipient != pub.BoxRecipient {
			return nil, errors.New("Google enrollment intent changed; use the same Google account to retry")
		}
		if intent.Link == nil || prepared.Enrolled || intent.Link.Expires > time.Now().Unix() {
			req.First, req.Link = intent.First, intent.Link
		}
	}
	if req.First == nil && req.Link == nil {
		if prepared.Head == nil {
			r := protocol.PersonRoster{Person: protocol.NewID(), Label: prepared.Name, Email: prepared.Email, Devices: []identity.Public{req.Public}}
			r.Sign(id.Sign)
			if err = r.VerifyFirst(); err != nil {
				return nil, err
			}
			req.First = &r
		} else {
			h := prepared.Head
			ap := prepared.Approver
			if h.Email != prepared.Email || h.Validate() != nil || ap == nil || !h.Has(ap.Address, ap.Fingerprint()) {
				return nil, errors.New("invalid existing Google person")
			}
			req.Link = &protocol.GoogleLink{Email: prepared.Email, Person: h.Person, Seq: h.Seq, Roster: h.Hash(), Approver: protocol.LinkApprover{Address: ap.Address, Fingerprint: ap.Fingerprint()}, Expires: time.Now().Unix() + protocol.MaxLinkTTL, Offer: protocol.NewID(), Join: ed25519.Sign(id.Sign, protocol.JoinBytes(h.Person, h.Seq+1, h.Hash(), req.Public))}
		}
	}
	// Never marshal a token or an authorization signature into local storage.
	intent := req
	intent.IDToken = ""
	intent.Sig = nil
	b, _ := json.Marshal(intent)
	if err = st.setConfig(map[string]string{"hub": o.Hub, "hub_cert": o.CertPEM, "address": req.Public.Address, "google_email": prepared.Email, "google_intent": string(b)}); err != nil {
		return nil, err
	}
	req.Sign(id.Sign)
	if err = c.do(ctx, "POST", "/v1/google/join", req, nil); err != nil {
		var he *HubError
		if errors.As(err, &he) && (he.Code == protocol.CodeRosterStale || he.Code == protocol.CodeTooManyDevices || he.Code == protocol.CodeBadStep) {
			if e := st.deleteConfig("google_intent"); e != nil {
				return nil, e
			}
		}
		return nil, err
	}
	saved := map[string]string{"enrolled": "1"}
	if req.First != nil {
		raw, _ := json.Marshal(req.First)
		if _, err = st.pinChain(req.First.Person, [][]byte{raw}, req.Public, true); err != nil {
			return nil, err
		}
		saved["person_published"] = req.First.Hash()
	} else {
		l := req.Link
		state, _ := json.Marshal(LinkStatus{Google: true, State: LinkPending, Person: l.Person, Approver: l.Approver.Address, ApproverKey: l.Approver.Fingerprint, Expires: l.Expires, Seq: l.Seq + 1})
		saved["link"] = string(state)
	}
	if err = st.setConfig(saved); err != nil {
		return nil, err
	}
	if err = st.deleteConfig("google_intent"); err != nil {
		return nil, err
	}
	st.db.Close()
	return Open(home)
}

// Google sign-in can request a local decision, never take one. Unlike a
// code link there is no secret shared outside the relay: the UI must
// explicitly approve the exact key. Signed roster validation still runs.
func (a *Agent) takeGoogleLinkRequest(ev protocol.LinkEvent) error {
	l := ev.Google
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || me.roster.Email == "" || l.Email != me.roster.Email || l.Person != me.info.Person || !me.roster.Has(a.Address, a.Self().Fingerprint()) || !me.roster.Has(l.Approver.Address, l.Approver.Fingerprint) || l.Offer != ev.Offer || !protocol.ValidID(ev.Offer) || l.Expires <= time.Now().Unix() || l.Expires > time.Now().Unix()+protocol.MaxLinkTTL ||
		l.Seq != me.info.Seq || l.Roster != me.info.Roster || ev.Device.Verify() != nil || !ed25519.Verify(ev.Device.SignKey, protocol.JoinBytes(l.Person, l.Seq+1, l.Roster, ev.Device), ev.Join) {
		return ErrLinkForged
	}
	if label, _, err := protocol.SplitAddress(ev.Device.Address); err != nil || label != protocol.GoogleLabel(l.Email) {
		return ErrLinkForged
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRow(`SELECT count(*) FROM device_links WHERE offer=?`, ev.Offer).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return nil
	}
	pub, _ := json.Marshal(ev.Device)
	now := time.Now().Unix()
	if _, err = tx.Exec(`INSERT INTO link_offers(offer,secret,person,seq,roster,expires,created_at,used) VALUES(?,?,?,?,?,?,?,1)`, ev.Offer, []byte{}, l.Person, l.Seq, l.Roster, l.Expires, now); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO device_links(offer,address,public,join_sig,requested_at,expires,state,updated_at) VALUES(?,?,?,?,?,?,?,?)`, ev.Offer, ev.Device.Address, string(pub), ev.Join, now, l.Expires, LinkPending, now); err != nil {
		return err
	}
	return a.store.done(tx.Commit())
}

func (a *Agent) GoogleAccess(ctx context.Context) (protocol.GoogleAccess, error) {
	var v protocol.GoogleAccess
	err := a.hub.do(ctx, "GET", "/v1/google/access", nil, &v)
	return v, err
}
func (a *Agent) ChangeGoogleAccess(ctx context.Context, c protocol.GoogleAccessChange) error {
	return a.hub.do(ctx, "PUT", "/v1/google/access", c, nil)
}
