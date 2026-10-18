package client

import (
	"context"
	"crypto/ed25519"
	"errors"

	"github.com/misunders2d/agentnet/internal/protocol"
)

type HumanSupport struct {
	Label string
	Me    bool
	Role  string
	State string
}

// HumanInviteSupport checks the exact pinned invited host. Other readers
// cannot veto an invitation; incompatible copies wait independently.
// Offline is separate from support: the last session still advertises readers.
func (a *Agent) HumanInviteSupport(ctx context.Context, conv, host string) ([]HumanSupport, error) {
	m, err := a.dmMembers(conv)
	if err != nil {
		return nil, err
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return nil, err
	}
	if _, member := m.persons[me.info.Person]; !ok || !member || !humanRoom(m) || !m.device(a.Address, me.info.Fingerprint) {
		return nil, errors.New("only a current conversation member checks a human invitation")
	}
	key, err := a.sendKey(ctx, host)
	if err != nil {
		return nil, err
	}
	p, err := a.personOfKey(ctx, host, key)
	if err != nil {
		return nil, err
	}
	if !p.has(host, key.Fingerprint()) || (p.info.State != personPinned && p.info.State != personSelf) {
		return nil, errors.New("host has no current pinned person/device proof")
	}
	v := HumanSupport{Label: p.info.Label, Me: p.info.State == personSelf, Role: "guest"}
	if v.Me {
		v.Role = "me"
	}
	profile, err := a.peerProfile(ctx, host)
	if err != nil {
		return nil, err
	}
	v.State = humanSupportState(profile, host, key.SignKey)
	if m.group != nil && v.State != "not set up" && (!profile.Supports(host, key.SignKey, protocol.CapGroup) || !profile.Supports(host, key.SignKey, protocol.CapRoom) || !profile.Supports(host, key.SignKey, protocol.CapGroupHumanParticipation)) {
		v.State = "update"
	}
	return []HumanSupport{v}, nil
}

func humanSupportState(p protocol.Profile, address string, signKey ed25519.PublicKey) string {
	if len(p.Sessions) == 0 {
		return "not set up"
	}
	if !p.Supports(address, signKey, protocol.CapHumanParticipation) {
		return "update"
	}
	if !p.Live {
		return "offline"
	}
	return "ok"
}
