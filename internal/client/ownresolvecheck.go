package client

import (
	"context"
	"errors"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// CheckOwnResolution is a read-only, current capability check before the page
// offers confirmation. It grants nothing; Decide and the host recheck on send.
func (a *Agent) CheckOwnResolution(ctx context.Context, host, id, key string, attempt int64) error {
	if !protocol.ValidID(id) || !protocol.ValidFingerprint(key) || attempt < 1 || host == a.Address {
		return errors.New("name the exact waiting request on its other host")
	}
	me, found, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !found || me.info.State != personSelf || !me.roster.Human(a.Self().Fingerprint()) {
		return errors.New("only a current own human device can handle this request")
	}
	current, hostFP := false, ""
	for _, d := range me.roster.Devices {
		if d.Address == a.Address && d.Fingerprint() == a.Self().Fingerprint() {
			current = true
		}
		if d.Address == host {
			hostFP = d.Fingerprint()
		}
	}
	pin, pending, known, err := a.store.peer(host)
	if err != nil {
		return err
	}
	if !current || hostFP == "" || !known || pending != nil || pin.Fingerprint() != hostFP {
		return errors.New("the host's current verified key needs attention first")
	}
	features, err := a.relayFeatures(ctx)
	if err != nil {
		return err
	}
	if ok, why := a.capSupport(ctx, host, pin, features, protocol.CapOwnSyncV3); !ok {
		return &controlCapabilityError{why}
	}
	return nil
}
