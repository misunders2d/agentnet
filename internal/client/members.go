package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	// held conversation messages on the stream's worker.
	a.convWork.due(convPersons | convRetry | convRelease)
	if a.kick != nil {
		a.kick()
	}
}
