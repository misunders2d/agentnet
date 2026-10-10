package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// The Hub's member list (protocol.Members): who else is enrolled on this
// Hub and whether the Hub sees their daemons. It is for discovery only:
// nothing here trusts, pins, approves or contacts anyone, and a member who
// disappears from it keeps their history here.

// ErrNoMemberList means the Hub does not list its members (an older Hub).
var ErrNoMemberList = errors.New("this Hub does not list its members (it runs an older agentnet; its operator can update it)")

// Members asks the Hub for its member list now.
func (a *Agent) Members(ctx context.Context) (protocol.Members, error) {
	var m protocol.Members
	err := a.hub.do(ctx, "GET", "/v1/agents", nil, &m)
	var he *HubError
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		return protocol.Members{}, ErrNoMemberList
	}
	if err != nil {
		return protocol.Members{}, err
	}
	if err := m.Valid(); err != nil {
		return protocol.Members{}, fmt.Errorf("the Hub sent an invalid member list: %w", err)
	}
	return m, nil
}

// Whether the Hub lists its members, as learned from the push stream.
const (
	MembersUnknown   = "unknown"    // not connected to the Hub since this daemon started
	MembersListed    = "listed"     // the Hub sends its member list
	MembersNotListed = "not_listed" // the Hub does not (an older Hub)
)

// MemberView is what this daemon's push stream last said about members.
type MemberView struct {
	Listed  string           // MembersUnknown, MembersListed or MembersNotListed
	Members protocol.Members // the last valid list; empty before one arrived
	At      time.Time        // when that list arrived; zero before one arrived
	// Current: the list arrived on the connection open now and nothing
	// unreadable came after it, so its presence is the Hub's current view.
	// Otherwise presence is unknown and the list is only as of At.
	Current bool
}

type memberState struct {
	mu   sync.Mutex
	view MemberView
}

// MemberView returns what the push stream last said about members.
func (a *Agent) MemberView() MemberView {
	a.members.mu.Lock()
	defer a.members.mu.Unlock()
	v := a.members.view
	if v.Listed == "" {
		v.Listed = MembersUnknown
	}
	v.Members.Members = slices.Clone(v.Members.Members)
	return v
}

// membersConnected records that the push stream opened, with the Hub's
// response headers: a Hub that lists members says so there and sends the
// list next.
func (a *Agent) membersConnected(h http.Header) {
	a.members.mu.Lock()
	a.members.view.Current = false
	if h.Get(protocol.MembersHeader) == "1" {
		a.members.view.Listed = MembersListed
	} else {
		a.members.view.Listed = MembersNotListed
	}
	a.members.mu.Unlock()
	a.changes.bump()
}

// membersDisconnected records that the push stream closed: presence in the
// last list is no longer current.
func (a *Agent) membersDisconnected() {
	a.members.mu.Lock()
	a.members.view.Current = false
	a.members.mu.Unlock()
	a.changes.bump()
}

// onMembers takes a "members" event. An unreadable or invalid list keeps the
// previous one but marks it not current, since something newer was missed.
func (a *Agent) onMembers(data []byte) {
	var m protocol.Members
	err := json.Unmarshal(data, &m)
	if err == nil {
		err = m.Valid()
	}
	a.members.mu.Lock()
	if err != nil {
		a.members.view.Current = false
		a.members.mu.Unlock()
		a.Logf("hub member list ignored: %v", err)
		a.typingMembershipChanged()
		return
	}
	a.members.view = MemberView{Listed: MembersListed, Members: m, At: time.Now(), Current: true}
	a.members.mu.Unlock()
	a.keepMemberFacts(m) // before the bump below, so a page reads them
	if a.reviewAgain.note() {
		a.wakeWorker() // a device that could not read reports may now (reviewnotice.go)
	}
	connected := 0
	for _, e := range m.Members {
		if e.Presence == protocol.PresenceConnected {
			connected++
		}
	}
	more := ""
	if m.Truncated {
		more = ", list truncated"
	}
	a.Logf("hub members: %d listed, %d connected%s", len(m.Members), connected, more)
	a.typingMembershipChanged()
	// Presence, persons or capabilities may have changed: look again at
	// held conversation messages on the stream's worker. Every list does:
	// evidence for a held message also arrives through local admissions
	// that announce nothing (a root or roster sync after linking), and the
	// Hub no longer re-sends an unchanged list, so this stays bounded; the
	// look itself resumes from its cursor (heldLook).
	work := convRelease | convPersons | convRetry
	if a.convWork.readerWait.Swap(false) {
		work |= convHistory // own-device history held back for a device's program
	}
	a.convWork.due(work)
	if a.kick != nil {
		a.kick()
	}
}

// keepMemberFacts keeps what the member list says that this device shows
// offline too: the workspace name and which other devices run an agent.
// Each is written only when it changed. A name ValidWorkspaceName refuses
// is ignored (the list is kept, and so is the name known before).
func (a *Agent) keepMemberFacts(m protocol.Members) {
	name, ok := protocol.ValidWorkspaceName(m.Workspace)
	if m.Workspace != "" && !ok {
		a.Logf("hub workspace name ignored: not 1–120 readable characters")
	} else if old, _ := a.store.config("workspace_name"); name != old {
		var err error
		if name == "" {
			err = a.store.deleteConfig("workspace_name")
		} else {
			err = a.store.setConfig(map[string]string{"workspace_name": name})
		}
		if err != nil {
			a.Logf("keeping the workspace name: %v", err)
		}
	}
	agents := []string{}
	for _, e := range m.Members {
		if e.Agent && e.Address != a.Address {
			agents = append(agents, e.Address)
		}
	}
	slices.Sort(agents)
	raw, _ := json.Marshal(agents)
	if old, _ := a.store.config("agent_devices"); string(raw) != old {
		if err := a.store.setConfig(map[string]string{"agent_devices": string(raw)}); err != nil {
			a.Logf("keeping the agent devices: %v", err)
		}
	}
	suspended := []string{}
	for _, e := range m.Members {
		if e.Suspended && e.Address != a.Address {
			suspended = append(suspended, e.Address)
		}
	}
	slices.Sort(suspended)
	raw, _ = json.Marshal(suspended)
	if old, _ := a.store.config("suspended_devices"); string(raw) != old && (old != "" || len(suspended) > 0) {
		if err := a.store.setConfig(map[string]string{"suspended_devices": string(raw)}); err != nil {
			a.Logf("keeping the suspended devices: %v", err)
		}
	}
}

// SuspendedDevices are the other devices the relay, as last listed,
// serves nothing until they update AgentNet. Availability only, never
// authority: nobody waits for them, and their copies never hold anyone
// else's (a copy each can read goes to the relay's custody; one it cannot
// waits here, unchecked until the relay lists it current again).
func (a *Agent) SuspendedDevices() map[string]bool {
	return a.store.suspendedDevices()
}

func (s *store) suspendedDevices() map[string]bool {
	var list []string
	if v, err := s.config("suspended_devices"); err == nil && v != "" {
		json.Unmarshal([]byte(v), &list)
	}
	out := make(map[string]bool, len(list))
	for _, address := range list {
		out[address] = true
	}
	return out
}

// SuspendedText is how a suspended device is named to its senders.
func SuspendedText(who string) string {
	return who + " is suspended until it updates AgentNet"
}

// WorkspaceName is the name the Hub's admin gave this workspace, as last
// listed (kept offline), or "" when none is set.
func (a *Agent) WorkspaceName() string {
	v, _ := a.store.config("workspace_name")
	return v
}

// AgentDevices are the other devices that, as last listed, say they run an
// agent (the relay's reading of their signed capability records). A hint
// for display and offers only: it grants nothing.
func (a *Agent) AgentDevices() []string {
	out := []string{}
	if v, err := a.store.config("agent_devices"); err == nil {
		json.Unmarshal([]byte(v), &out)
	}
	return out
}

// HubWorkspace asks the Hub for the workspace name now ("" when none, or
// when the Hub sent one that is not a workspace name).
func (a *Agent) HubWorkspace(ctx context.Context) (string, error) {
	m, err := a.Members(ctx)
	if err != nil {
		return "", err
	}
	name, _ := protocol.ValidWorkspaceName(m.Workspace)
	return name, nil
}

// SetWorkspaceName names the workspace for every member (admin only); an
// empty name clears it. The name is kept here at once; members learn it
// from the member list.
func (a *Agent) SetWorkspaceName(ctx context.Context, name string) (string, error) {
	if strings.TrimSpace(name) != "" {
		var ok bool
		if name, ok = protocol.ValidWorkspaceName(name); !ok {
			return "", ErrWorkspaceName
		}
	} else {
		name = ""
	}
	var out protocol.WorkspaceNameRequest
	if err := a.hub.do(ctx, "PUT", "/v1/admin/workspace", protocol.WorkspaceNameRequest{Name: name}, &out); err != nil {
		return "", err
	}
	var err error
	if out.Name == "" {
		err = a.store.deleteConfig("workspace_name")
	} else {
		err = a.store.setConfig(map[string]string{"workspace_name": out.Name})
	}
	a.store.changed()
	return out.Name, err
}

// RelayHost is the host name of this device's relay ("agentnet.bezosapp.uk"):
// what people see for a workspace its admin has not named.
func (a *Agent) RelayHost() string {
	u, err := url.Parse(a.hub.base)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
