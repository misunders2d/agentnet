package client

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The upstream witness verifier owns signatures, original admission and current
// own authority. This selection check covers a signed predecessor present only
// in that verified inert witness, without a standalone inbox/live-event row.
func TestHistoryCatchupWitnessOnlyPredecessor(t *testing.T) {
	w := newWorld(t, "")
	a := w.alice
	conv, pid, id := strings.Repeat("a", 64), protocol.NewID(), protocol.NewID()
	parent := protocol.ParticipationEvent{V: 1, Conv: conv, PID: pid, Type: protocol.EventAccept, Prev: strings.Repeat("b", 64), TS: 1790000100,
		Author: protocol.EventAuthor{Person: protocol.NewID(), Roster: strings.Repeat("c", 64), Address: a.Address, Fingerprint: a.Self().Fingerprint()}}
	parent.Sign(a.id.Sign)
	for _, mode := range []string{"exact", "absent", "other-conversation", "other-participation", "other-predecessor"} {
		t.Run(mode, func(t *testing.T) {
			previous := parent
			if mode == "other-conversation" {
				previous.Conv = strings.Repeat("d", 64)
			}
			if mode == "other-participation" {
				previous.PID = protocol.NewID()
			}
			previous.Sign(a.id.Sign)
			child := protocol.ParticipationEvent{V: 1, Conv: conv, PID: pid, Type: protocol.EventDismiss, Prev: previous.Hash(), TS: 1790000101, Author: parent.Author}
			if mode == "other-predecessor" {
				child.Prev = strings.Repeat("e", 64)
			}
			child.Sign(a.id.Sign)
			body, err := json.Marshal(child)
			if err != nil {
				t.Fatal(err)
			}
			if err = child.Verify(a.Self().SignKey); err != nil {
				t.Fatal(err)
			}
			row := historySourceRow{conv: conv, dir: "in", in: envelope.Inner{ID: id, Conv: conv, PID: pid, Sub: envelope.SubEvent, Body: string(body)}}
			item := HistoryItem{ID: id, PID: pid, GroupHistory: &GroupContext{Memberships: []protocol.ParticipationEvent{previous}}}
			if mode == "absent" {
				item.GroupHistory = nil
			}
			deps, err := a.historyDependencies(row, item)
			if mode == "exact" {
				if err != nil || len(deps) != 0 {
					t.Fatalf("verified witness predecessor stayed deferred: %v %v", deps, err)
				}
			} else if !errors.Is(err, ErrGroupContextPending) {
				t.Fatalf("mismatched witness satisfied dependency: %v", err)
			}
		})
	}
	var n int
	if err := a.store.db.QueryRow(`SELECT (SELECT count(*) FROM participation_events)+(SELECT count(*) FROM inbox)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("inert dependency installed a row: %d %v", n, err)
	}
}
