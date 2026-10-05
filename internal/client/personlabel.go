package client

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// RenamePerson changes this person's self-claimed display label with a signed
// roster step. Identity, devices, keys and local permissions are unchanged.
// Hub acceptance precedes local pinning; an ambiguous response is not retried.
func (a *Agent) RenamePerson(ctx context.Context, label string) (PersonInfo, error) {
	return a.changePersonProfile(ctx, &label, nil)
}

func (a *Agent) changePersonProfile(ctx context.Context, label, picture *string) (PersonInfo, error) {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return PersonInfo{}, err
	}
	if !ok {
		return PersonInfo{}, ErrNoPerson
	}
	person := me.info.Person
	for attempt := 0; attempt < 2; attempt++ {
		if _, err = a.refreshPerson(ctx, person, false); err != nil {
			return PersonInfo{}, err
		}
		me, ok, err = a.store.selfPerson(a.Address)
		if err != nil {
			return PersonInfo{}, err
		}
		if !ok || me.info.Person != person || !me.roster.Has(a.Address, a.Self().Fingerprint()) {
			return PersonInfo{}, errors.New("this device no longer speaks for its person")
		}
		r := protocol.PersonRoster{Person: person, Label: me.roster.Label, Email: me.roster.Email, Picture: me.roster.Picture, Seq: me.roster.Seq + 1, Prev: me.roster.Hash(), Devices: me.roster.Devices, HumanKeys: me.roster.Humans(), By: a.Self().Fingerprint()}
		if label != nil {
			r.Label = *label
		}
		if picture != nil {
			r.Picture = *picture
		}
		r.Sign(a.id.Sign)
		if _, err = r.VerifyNext(me.roster); err != nil {
			return PersonInfo{}, err
		}
		if me.roster.Label == r.Label && me.roster.Picture == r.Picture {
			return me.info, nil
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return PersonInfo{}, err
		}
		if len(raw) > protocol.MaxPersonRecord {
			return PersonInfo{}, errors.New("person: record too large")
		}
		err = a.hub.doBytes(ctx, "PUT", "/v1/person", raw, nil)
		if err != nil {
			var he *HubError
			if attempt == 0 && errors.As(err, &he) && he.Code == protocol.CodeRosterStale {
				continue
			}
			return PersonInfo{}, err
		}
		if _, err = a.store.pinChain(person, [][]byte{raw}, a.Self(), false); err != nil {
			return PersonInfo{}, err
		}
		latest, found, err := a.store.selfPerson(a.Address)
		if err != nil {
			return PersonInfo{}, err
		}
		if !found {
			return PersonInfo{}, ErrNoPerson
		}
		if latest.info.Roster == r.Hash() {
			if err = a.store.setConfig(map[string]string{"person_published": r.Hash()}); err != nil {
				return latest.info, err
			}
		}
		a.convWork.due(convPersons | convRetry | convRelease)
		a.NoteChange()
		a.kickNow()
		return latest.info, nil
	}
	return PersonInfo{}, ErrRosterStale
}
