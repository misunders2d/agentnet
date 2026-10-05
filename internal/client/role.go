package client

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/misunders2d/agentnet/internal/identity"
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

// SetDeviceAdmin gives another device of this person the relay's admin
// role, or takes it back: the person's own decision, made on this device,
// which must hold the role. A person's devices never inherit it, so a
// phone may change company settings (the workspace name, invites) only
// after this, and a device that runs agents stays a member unless the
// person grants it too. The relay changes only devices linked to this
// person, never one whose own invite made it an admin.
func (a *Agent) SetDeviceAdmin(ctx context.Context, address string, admin bool) error {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("this installation does not speak for a person")
	}
	if address == a.Address {
		return errors.New("that is this device: grant the role to another of your devices")
	}
	if !slices.ContainsFunc(me.roster.Devices, func(d identity.Public) bool { return d.Address == address }) {
		return fmt.Errorf("%s is not a device of your person", address)
	}
	return a.hub.do(ctx, "POST", "/v1/person/device-admin", protocol.DeviceAdminRequest{Address: address, Admin: admin}, nil)
}
