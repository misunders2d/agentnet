package client

import (
	"context"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// HubRoleUnknown is HubRole's answer when the relay did not say.
const HubRoleUnknown = "unknown"

// HubRole asks the relay what this installation is there: "admin" or
// "member" (protocol.RoleAdmin, RoleMember), from its own profile. An
// older relay, or a profile without the field, gives HubRoleUnknown and no
// error; not reaching the relay gives HubRoleUnknown and the error. The
// label in an address says nothing about this.
func (a *Agent) HubRole(ctx context.Context) (string, error) {
	label, name, err := protocol.SplitAddress(a.Address)
	if err != nil {
		return HubRoleUnknown, err
	}
	var prof protocol.Profile
	if err := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
		return HubRoleUnknown, err
	}
	switch prof.SelfRole {
	case protocol.RoleAdmin, protocol.RoleMember:
		return prof.SelfRole, nil
	}
	return HubRoleUnknown, nil
}
