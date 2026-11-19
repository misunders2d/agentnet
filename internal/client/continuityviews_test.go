package client

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// continuityViews gives every DM the first chat its continuityFamily gives it.
func TestContinuityViewsMatchEachFamily(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 500 {
		n, people := 1+r.IntN(30), 1+r.IntN(10)
		rows := make([]ConversationInfo, n)
		aliases := map[string]string{}
		for p := range people {
			aliases[fmt.Sprint("p", p)] = fmt.Sprint("p", p)
		}
		for range r.IntN(4) { // recovered people: an earlier identity names a later one
			aliases[fmt.Sprint("p", r.IntN(people))] = fmt.Sprint("p", r.IntN(people))
		}
		for i := range rows {
			rows[i] = ConversationInfo{ID: fmt.Sprint("c", r.IntN(1000), "-", i), Kind: protocol.ConvKindDM, Created: int64(r.IntN(5))}
			if r.IntN(5) == 0 {
				rows[i].Kind = protocol.ConvKindGroup
			}
			rows[i].Peer.Person = fmt.Sprint("p", r.IntN(people+1)) // p<people>: no alias
		}
		views := continuityViews(rows, aliases)
		for _, c := range rows {
			if c.Kind != protocol.ConvKindDM {
				continue
			}
			want := c.ID
			if f := continuityFamily(rows, aliases, c.ID); len(f) > 0 {
				want = f[0].ID
			}
			got := c.ID
			if v := views[c.ID]; v != "" {
				got = v
			}
			if got != want {
				t.Fatalf("%s: view %s, family's first %s (rows %+v aliases %v)", c.ID, got, want, rows, aliases)
			}
		}
	}
}
