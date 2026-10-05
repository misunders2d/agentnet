package client

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sort"

	"github.com/misunders2d/agentnet/internal/protocol"
)

type HumanSupport struct {
	Label string
	Me    bool
	Role  string
	State string
}

// HumanInviteSupport checks the exact pinned host and original member devices.
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
	if _, member := m.persons[me.info.Person]; !ok || !member || !externalDM(m.root) {
		return nil, errors.New("only an original DM member checks a human invitation")
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
	persons := map[string]personRow{}
	for id, p := range m.persons {
		persons[id] = p
	}
	persons[p.info.Person] = p
	var out []HumanSupport
	for _, person := range persons {
		v := HumanSupport{Label: person.info.Label, Me: person.info.State == personSelf, State: "ok"}
		v.Role = "member"
		if v.Me {
			v.Role = "me"
		} else if person.info.Person == p.info.Person {
			v.Role = "guest"
		}
		offline := false
		for _, d := range person.roster.Devices {
			if v.Role == "guest" && d.Address != host {
				continue
			}
			label, device, _ := protocol.SplitAddress(d.Address)
			var profile protocol.Profile
			if err := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile); err != nil {
				return nil, err
			}
			state := humanSupportState(profile, d.Address, d.SignKey)
			if state == "not set up" || state == "update" && v.State != "not set up" {
				v.State = state
			}
			offline = v.Role == "guest" && d.Address == host && !profile.Live
		}
		if v.State == "ok" && offline {
			v.State = "offline"
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
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
