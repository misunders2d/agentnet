package client

import (
	"context"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// One action (a send, an invitation, one outbox batch) asks the Hub about
// each device key, person roster, device profile and the relay's features
// once, and the asks a group action would make one after another go out
// together (prefetchGroup). Every check still runs, its answer at most a few
// seconds old (roundTripFresh), as when one action's checks came moments
// apart; nothing is kept past the action. On a relay 100 ms away a group
// message made nine such round trips in a row before it was even queued,
// and an agent's invitation into a group about seventy. An error is kept
// only when the Hub could not be reached at all, so an action waits for a
// dead Hub once; after any other error a later check asks again.
type roundTrips struct {
	mu       sync.Mutex
	fresh    time.Duration // roundTripFresh
	dir      map[string]*roundTrip[protocol.DirectoryEntry]
	persons  map[string]*roundTrip[pinResult]
	profiles map[string]*roundTrip[protocol.Profile]
	feats    map[string]*roundTrip[[]string] // the relay's, under ""
}

type roundTrip[T any] struct {
	done chan struct{}
	at   time.Time // when the answer came, set before done closes
	v    T
	err  error
}

// roundTripFresh is how long an action reuses an answer. Most actions end
// well within it; a long one (large uploads, a history batch) asks again
// after it, as each of its checks did before.
const roundTripFresh = 5 * time.Second

type roundTripsKey struct{}

// withRoundTrips makes ctx one action whose Hub checks are asked once; an
// action inside another shares the outer one's.
func withRoundTrips(ctx context.Context) context.Context {
	if _, ok := ctx.Value(roundTripsKey{}).(*roundTrips); ok {
		return ctx
	}
	return context.WithValue(ctx, roundTripsKey{}, &roundTrips{fresh: roundTripFresh})
}

// askOnce runs fetch once per key within ctx's action: a concurrent ask of
// the same key waits for that answer, a later one reads it, and one whose
// answer was an error other than an unreachable Hub asks itself. Without an
// action every ask fetches.
func askOnce[T any](ctx context.Context, table func(*roundTrips) *map[string]*roundTrip[T], key string, fetch func() (T, error)) (T, error) {
	rt, ok := ctx.Value(roundTripsKey{}).(*roundTrips)
	if !ok {
		return fetch()
	}
	for {
		rt.mu.Lock()
		m := table(rt)
		c, ok := (*m)[key]
		if ok {
			select {
			case <-c.done:
				if time.Since(c.at) > rt.fresh {
					delete(*m, key)
					ok = false
				}
			default:
			}
		}
		if !ok {
			break
		}
		rt.mu.Unlock()
		select {
		case <-c.done:
			if c.err == nil || hubUnreachable(c.err) {
				return c.v, c.err
			}
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
	m := table(rt)
	if *m == nil {
		*m = map[string]*roundTrip[T]{}
	}
	c := &roundTrip[T]{done: make(chan struct{})}
	(*m)[key] = c
	rt.mu.Unlock()
	c.v, c.err = fetch()
	c.at = time.Now()
	if c.err != nil && !hubUnreachable(c.err) { // not kept: the next ask asks again
		rt.mu.Lock()
		if (*m)[key] == c {
			delete(*m, key)
		}
		rt.mu.Unlock()
	}
	close(c.done)
	return c.v, c.err
}

// askAhead starts ask now, within ctx's action, so the check after the one
// in progress finds its answer already on the way. Without an action it
// does nothing: that check asks itself.
func askAhead(ctx context.Context, ask func()) {
	if _, ok := ctx.Value(roundTripsKey{}).(*roundTrips); ok {
		go ask()
	}
}

// prefetchGroup asks the Hub, together, what a group action checks next one
// by one: every member's roster and the relay's features, then every
// member device's key and profile. The action's own checks find the
// answers and decide as before; an ask failed here is asked again there,
// unless the Hub could not be reached.
func (a *Agent) prefetchGroup(ctx context.Context, persons []string) {
	if _, ok := ctx.Value(roundTripsKey{}).(*roundTrips); !ok {
		return
	}
	var wg sync.WaitGroup
	wg.Go(func() { a.relayFeatures(ctx) })
	for _, person := range persons {
		wg.Go(func() { a.refreshPerson(ctx, person, false) })
	}
	wg.Wait()
	seen := map[string]bool{a.Address: true}
	for _, id := range persons {
		person, ok, err := a.store.personByID(id)
		if err != nil || !ok {
			continue
		}
		for _, device := range person.roster.Devices {
			if seen[device.Address] {
				continue
			}
			seen[device.Address] = true
			wg.Go(func() { a.directory(ctx, device.Address) })
			wg.Go(func() { a.peerProfile(ctx, device.Address) })
		}
	}
	wg.Wait()
}
