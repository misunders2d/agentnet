package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A peer may start an older session after consenting. Complete group guest
// readers must still be present at every handoff, including selected history
// and lifecycle copies. Waiting keeps the sealed copy and local end intact.
func (a *Agent) groupGuestDeliveryGate(env envelope.Envelope) (bool, bool, error) {
	var conv, pid, human, state, fp string
	err := a.store.db.QueryRow(`SELECT coalesce(conv,''),coalesce(pid,''),coalesce(human,''),state,coalesce(recipient_fp,'') FROM outbox WHERE id=?`, env.ID).Scan(&conv, &pid, &human, &state, &fp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if conv == "" {
		return false, false, nil
	}
	root, _, found, err := a.store.conversation(conv)
	if err != nil {
		return true, false, err
	}
	if !found || root.Kind != protocol.ConvKindGroup {
		return false, false, nil
	}
	guest := false
	if pid != "" {
		p, e := a.Participation(pid)
		if e != nil && !errors.Is(e, ErrNoParticipation) {
			return true, false, e
		}
		guest = p.Role == protocol.RoleHuman
	}
	if human != "" {
		var h envelope.HumanTurn
		if err = json.Unmarshal([]byte(human), &h); err != nil {
			return true, false, err
		}
		for _, e := range h.Proof {
			guest = guest || e.Role == protocol.RoleHuman
		}
	}
	if !guest {
		return false, false, nil
	}
	if state != stateQueued {
		return true, false, nil
	}
	key, pending, ok, err := a.store.peer(env.To)
	if err != nil {
		return true, false, err
	}
	if !ok || pending != nil || fp == "" || key.Fingerprint() != fp {
		return true, false, nil
	}
	if err = a.requireParticipationCaps(context.Background(), key, protocol.CapGroupHumanParticipation); err != nil {
		if errors.Is(err, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+err.Error(), "")
		}
		return true, false, err
	}
	return false, false, nil // existing authority and admission checks still decide
}
