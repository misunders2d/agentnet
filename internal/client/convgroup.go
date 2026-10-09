package client

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Group journal upkeep on the stream's worker (groups.go holds the journal
// itself): heads the relay pushed are noted first, then fetched, and this
// device's own unpublished records are replayed once per connection. No
// request is made that an event did not make due.
type groupWork struct {
	mu      sync.Mutex
	due     map[string]bool // conversations whose head changed
	recover atomic.Bool     // replay unpublished records (set on connect)
}

// onGroupHeads takes the relay's "groups" event: every head is noted
// before anything is fetched, so a fetch that falls short of it leaves
// membership fail-closed (groups.go), and only a head this device does not
// hold yet makes a fetch due.
func (a *Agent) onGroupHeads(raw []byte) {
	var heads []protocol.GroupHead
	if err := json.Unmarshal(raw, &heads); err != nil {
		a.Logf("group heads ignored: %v", err)
		return
	}
	wake := false
	for _, h := range heads {
		if err := a.NoteGroupHead(h); err != nil {
			a.Logf("group head for %s ignored: %v", h.Conv, err)
			continue
		}
		if p, err := a.GroupContext(h.Conv); err == nil && p.State.Seq == h.Seq && p.State.Hash() == h.Hash {
			continue // held already
		}
		a.groupWork.mu.Lock()
		if a.groupWork.due == nil {
			a.groupWork.due = map[string]bool{}
		}
		a.groupWork.due[h.Conv] = true
		a.groupWork.mu.Unlock()
		wake = true
	}
	if wake {
		a.kick()
	}
}

// groupSync does the group work due: unpublished records of this device
// first (a crash between the relay's answer and the fan-out), then the
// heads the stream said changed. What fails stays due for the next pass.
func (a *Agent) groupSync(ctx context.Context) {
	if a.groupWork.recover.CompareAndSwap(true, false) {
		a.convWork.due(convHistory)
		if err := a.RecoverGroupPublications(ctx); err != nil {
			a.Logf("group publications: %v", err)
			a.groupWork.recover.Store(true)
		}
		if err := a.RecoverGroupInvitations(ctx); err != nil {
			a.Logf("group invitations: %v", err)
			a.groupWork.recover.Store(true)
		}
		if err := a.RecoverGroupWithdrawals(ctx); err != nil {
			a.Logf("group departures: %v", err)
			a.groupWork.recover.Store(true)
		}
	}
	a.groupWork.mu.Lock()
	due := a.groupWork.due
	a.groupWork.due = nil
	a.groupWork.mu.Unlock()
	for conv := range due {
		err := a.SyncGroup(ctx, conv)
		if errors.Is(err, ErrGroupContextPending) {
			continue // a reader must re-encrypt the context for this device first
		}
		if err != nil {
			a.Logf("group %s: %v", conv, err)
			a.groupWork.mu.Lock()
			if a.groupWork.due == nil {
				a.groupWork.due = map[string]bool{}
			}
			a.groupWork.due[conv] = true
			a.groupWork.mu.Unlock()
			continue
		}
		a.convWork.due(convRetry | convRelease) // new membership evidence: held and waiting messages look again
		a.changes.bump()
	}
}
