package client

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func (a *Agent) GoogleRecoveries(ctx context.Context) ([]protocol.RecoveryRecord, error) {
	var v []protocol.RecoveryRecord
	e := a.hub.do(ctx, "GET", "/v1/google/recovery", nil, &v)
	return v, e
}
func (a *Agent) GoogleRecoveryDecision(ctx context.Context, id string, approve bool) (protocol.RecoveryRecord, error) {
	var v protocol.RecoveryRecord
	if !protocol.ValidHash(id) {
		return v, errors.New("exact recovery hash required")
	}
	action := "deny"
	if approve {
		action = "approve"
	}
	e := a.hub.do(ctx, "POST", "/v1/google/recovery/"+id+"/"+action, struct{}{}, &v)
	return v, e
}
func (a *Agent) RecoveryGoogleConfig(ctx context.Context) (protocol.GoogleConfig, error) {
	var v protocol.GoogleConfig
	e := a.hub.do(ctx, "GET", "/v1/google/config", nil, &v)
	return v, e
}
func (a *Agent) RecoveryGoogleExchange(ctx context.Context, code, verifier, redirect string) (string, error) {
	e := protocol.GoogleExchange{Code: code, Verifier: verifier, Redirect: redirect, Public: a.Self()}
	e.Sign(a.id.Sign)
	var v struct {
		IDToken string `json:"id_token"`
	}
	err := a.hub.do(ctx, "POST", "/v1/google/exchange", e, &v)
	return v.IDToken, err
}

// RequestGoogleRecovery requires an enrolled new human device. It keeps OAuth
// credentials in memory and persists only exact signed consent for safe retry.
func (a *Agent) RequestGoogleRecovery(ctx context.Context, old, token string) (protocol.RecoveryRecord, error) {
	var out protocol.RecoveryRecord
	if !protocol.ValidID(old) {
		return out, errors.New("explicit old person id required")
	}
	me, ok, e := a.Person()
	if e != nil {
		return out, e
	}
	if !ok || me.Person == old {
		return out, errors.New("enrolled successor person required")
	}
	oldChain, e := a.fetchChain(ctx, old, -1)
	if e != nil {
		return out, e
	}
	newChain, e := a.fetchChain(ctx, me.Person, -1)
	if e != nil {
		return out, e
	}
	if len(oldChain) == 0 || len(newChain) == 0 {
		return out, errors.New("missing roster chain")
	}
	var o, g, n protocol.PersonRoster
	if e = json.Unmarshal(oldChain[len(oldChain)-1], &o); e != nil {
		return out, e
	}
	if e = json.Unmarshal(newChain[0], &g); e != nil {
		return out, e
	}
	if e = json.Unmarshal(newChain[len(newChain)-1], &n); e != nil {
		return out, e
	}
	var version protocol.VersionInfo
	if e = a.hub.do(ctx, "GET", "/v1/version", nil, &version); e != nil {
		return out, e
	}
	s := protocol.PersonSuccessor{Version: 1, Realm: version.RealmID, OldPerson: old, OldHead: o.Hash(), NewPerson: me.Person, NewGenesis: g.Hash(), Email: o.Email, Created: time.Now().Unix(), Nonce: protocol.NewID(), Signer: a.Self().Fingerprint()}
	s.Sign(a.id.Sign)
	if e = s.Verify(version.RealmID, g, n); e != nil {
		return out, e
	}
	// Reuse exact consent after an ambiguous response, within the admission window.
	key := "recovery-consent:" + old
	if saved, _ := a.store.config(key); saved != "" {
		var prior protocol.PersonSuccessor
		if json.Unmarshal([]byte(saved), &prior) == nil && prior.OldHead == s.OldHead && prior.NewGenesis == s.NewGenesis && prior.Verify(version.RealmID, g, n) == nil && time.Now().Unix()-prior.Created <= 900 {
			s = prior
		}
	}
	raw, _ := json.Marshal(s)
	if e = a.store.setConfig(map[string]string{key: string(raw)}); e != nil {
		return out, e
	}
	req := protocol.RecoveryRequest{Successor: s, Google: protocol.GoogleRequest{Public: a.Self(), IDToken: token}}
	req.Google.Sign(a.id.Sign)
	e = a.hub.do(ctx, "POST", "/v1/google/recovery", req, &out)
	if e == nil && out.Status == "denied" {
		if err := a.store.setConfig(map[string]string{key: ""}); err != nil {
			return out, err
		}
		return out, errors.New("recovery was denied; sign in again to consent to a new request")
	}
	return out, e
}
