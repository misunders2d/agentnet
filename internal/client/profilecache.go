package client

import (
	"context"
	"sync"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// profileCache keeps the Hub's device availability profiles only while the push
// stream is delivering complete member lists. The Hub pushes them whenever a
// device's capabilities, sessions, person roster or role change (lookAgain),
// so every list and every disconnect drop all entries: a profile is at most as
// old as the push this program already relies on. Without a stream (CLI
// commands, tests without a daemon) nothing is kept and every read asks.
// Authority reads such as HubRole bypass this availability cache.
type profileCache struct {
	mu      sync.Mutex
	active  bool   // a complete member list arrived on the open stream
	gen     uint64 // advances on every reset, so a read in flight is not kept
	entries map[string]protocol.Profile
}

// reset drops every profile. active says whether a stream is delivering
// complete lists, so later reads may be kept until the next list or disconnect.
func (c *profileCache) reset(active bool) {
	c.mu.Lock()
	c.active = active
	c.gen++
	c.entries = nil
	c.mu.Unlock()
}

// peerProfile returns the Hub's profile of the device at address: a cached
// one while it is current, otherwise a fresh read. Errors are never kept.
// Callers read the result only; it may be shared with other callers.
func (a *Agent) peerProfile(ctx context.Context, address string) (protocol.Profile, error) {
	c := &a.profiles
	c.mu.Lock()
	prof, ok := c.entries[address]
	gen := c.gen
	c.mu.Unlock()
	if ok {
		return prof, nil
	}
	return askOnce(ctx, func(rt *roundTrips) *map[string]*roundTrip[protocol.Profile] { return &rt.profiles }, address, func() (protocol.Profile, error) {
		var prof protocol.Profile
		label, name, err := protocol.SplitAddress(address)
		if err != nil {
			return protocol.Profile{}, err
		}
		if err = a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
			return protocol.Profile{}, err
		}
		c.mu.Lock()
		if c.active && c.gen == gen {
			if c.entries == nil {
				c.entries = map[string]protocol.Profile{}
			}
			c.entries[address] = prof
		}
		c.mu.Unlock()
		return prof, nil
	})
}

// relayCache keeps, while the push stream is open, what changes only with
// the relay itself or with what this program publishes: the relay's
// features and the capability record and agent catalog this program last
// published on that connection. A relay restarts or updates only by
// dropping the stream, so connecting and disconnecting drop everything and
// a new connection asks and publishes again. Without a stream (CLI
// commands, tests without a daemon) nothing is kept.
type relayCache struct {
	mu      sync.Mutex
	active  bool   // the push stream is open
	gen     uint64 // advances on every reset, so a read in flight is not kept
	feats   []string
	known   bool   // feats were read on this connection
	caps    string // the capability record last published on it, as published
	catalog string // the agent catalog last published on it, as published
}

func (c *relayCache) reset(active bool) {
	c.mu.Lock()
	c.active, c.gen = active, c.gen+1
	c.feats, c.known, c.caps, c.catalog = nil, false, "", ""
	c.mu.Unlock()
}

// published reports whether value is what was last published as *field on
// this connection; a publish is skipped then, since the relay keeps it and
// sending it again only wakes every device to look again.
func (c *relayCache) published(field *string, value string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active && *field == value
}

// keep records value as published as *field on the connection of gen.
func (c *relayCache) keep(field *string, value string, gen uint64) {
	c.mu.Lock()
	if c.active && c.gen == gen {
		*field = value
	}
	c.mu.Unlock()
}

func (c *relayCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}
