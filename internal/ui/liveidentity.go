package ui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// One person on several devices (MEL-433) on the daemon's page, through the
// client's person and link calls only: what this installation is, its
// person's devices, a one-use link for a new device, the requests to
// decide here, and this device's own request to join its person.

// identityOverview adds the role, this device's own link, and the new
// devices asking to join this person.
func (l *Live) identityOverview(o *Overview) error {
	role, err := l.a.Role()
	if err != nil {
		return err
	}
	o.Role = role
	if o.Role == "" {
		o.Role = RoleUnset
	}
	switch st := l.a.LinkState(); st.State {
	case "", "linked", "approved":
	default:
		o.Link = &LinkState{State: st.State, Detail: st.Detail}
	}
	jobs, err := l.a.HistoryProgress()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		o.History = append(o.History, HistoryCopy{Device: j.Device, Name: j.Name, Done: j.ConvsDone, Total: j.ConvsTotal, State: j.State})
	}
	reqs, err := l.a.PendingLinks()
	if err != nil {
		return err
	}
	for _, r := range reqs {
		if r.State == "pending" {
			o.Links = append(o.Links, LinkRequest{ID: r.ID, Address: r.Address, Name: r.Name, Fingerprint: r.Fingerprint,
				RequestedAt: time.Unix(r.RequestedAt, 0), Expires: time.Unix(r.Expires, 0), State: r.State})
		}
	}
	return nil
}

// deviceViews are a person's devices for the page.
func deviceViews(devs []client.DeviceInfo) []DeviceView {
	var out []DeviceView
	for _, d := range devs {
		out = append(out, DeviceView{Address: d.Address, Name: d.Name, Fingerprint: d.Fingerprint, This: d.This})
	}
	return out
}

// SetService implements Identity.
func (l *Live) SetService() (string, error) {
	if err := l.a.SetService(); err != nil {
		return "", Refuse(linkWords(err))
	}
	return "This computer is a service now: it has no person.", nil
}

// NewDeviceLink implements Identity. The link opens this server's browser
// page when the server serves one to browsers (its invites pin no
// certificate of their own); otherwise it is the code alone, for the
// command line (agentnet join).
func (l *Live) NewDeviceLink() (DeviceLink, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	offer, err := l.a.NewDeviceLink(ctx)
	if err != nil {
		return DeviceLink{}, Refuse(linkWords(err))
	}
	return DeviceLink{URL: linkURL(offer.Code), Expires: offer.Expires}, nil
}

// linkURL puts code in the fragment of this server's page (never in a
// query: it stays out of logs and referrers).
func linkURL(code string) string {
	o, err := protocol.DecodeLinkOffer(code)
	if err != nil {
		return code
	}
	inv, err := protocol.DecodeInvite(o.Invite)
	if err != nil || inv.CertPEM != "" || !strings.HasPrefix(inv.Hub, "https://") {
		return code
	}
	return strings.TrimSuffix(inv.Hub, "/") + "/#" + code
}

// DecideLink implements Identity.
func (l *Live) DecideLink(id string, accept bool) (string, error) {
	name := "The device"
	if reqs, err := l.a.PendingLinks(); err == nil {
		for _, r := range reqs {
			if r.ID == id {
				name = r.Name
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	if err := l.a.DecideLink(ctx, id, accept); err != nil {
		return "", Refuse(linkWords(err))
	}
	if !accept {
		return "Refused: " + name + " did not join as you.", nil
	}
	return name + " is now one of your devices.", nil
}

// RemoveDevice implements Identity.
func (l *Live) RemoveDevice(address string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	if err := l.a.RemoveDevice(ctx, address); err != nil {
		return "", Refuse(linkWords(err))
	}
	return address + " is no longer one of your devices.", nil
}

// linkWords says a person or link refusal plainly.
func linkWords(err error) string {
	switch {
	case errors.Is(err, client.ErrLinkExpired):
		return "That expired: make a new link on this device and use it again."
	case errors.Is(err, client.ErrLinkStale), errors.Is(err, client.ErrRosterStale):
		return "Your devices changed meanwhile: make a new link and try again."
	case errors.Is(err, client.ErrLinkUsed):
		return "That link was used already: make a new one."
	case errors.Is(err, client.ErrLinkForged):
		return "That request does not match your link: refuse it and make a new link."
	case errors.Is(err, client.ErrLinkCrossPerson):
		return "That device already belongs to someone."
	case errors.Is(err, client.ErrLinkRefused):
		return "The link was refused."
	case errors.Is(err, client.ErrService):
		return "This computer is a service: it has no person."
	case errors.Is(err, client.ErrNoPerson):
		return "Set up your person first."
	case errors.Is(err, client.ErrNotPublished):
		return "Your person is not on your server yet: AgentNet publishes it when it connects. Try again in a moment."
	}
	return sentence(err)
}

// copyViews are a sent message's copies for the page.
func copyViews(cs []client.ConvCopy) []CopyView {
	var out []CopyView
	for _, c := range cs {
		out = append(out, CopyView{To: c.To, State: c.State, Detail: c.Detail})
	}
	return out
}

// laggingCopy names whom m's state (its least advanced copy's) is about:
// the other person's device whose copy that is, or "all devices" when only
// copies to your own other devices are that far behind (one of them is
// never named as if it were the recipient); peer when m has no copies.
func laggingCopy(m client.ConvMessage, peer string) string {
	who := peer
	for _, c := range m.Copies {
		if c.State != m.State {
			continue
		}
		if !c.Own {
			return c.To
		}
		who = "all devices"
	}
	return who
}

// syncedFrom is the device m came from as history, or "".
func syncedFrom(m client.ConvMessage) string {
	if m.History {
		return m.SyncedFrom
	}
	return ""
}
