package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// BUG-23: accepting an agent's invitation on a device where nothing would
// run what is asked of it (no responder chosen) says so, instead of only
// reporting the participation active while requests wait for good.
func TestDMAcceptAgentWarnsWhenNothingRuns(t *testing.T) {
	alice, bob, _, conv, eventually := dmWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var p client.ParticipationInfo
	eventually("alice invites bob's agent", func() bool {
		var err error
		p, err = alice.InviteAgent(ctx, conv, bob.Address, nil, nil, "")
		return err == nil
	})
	eventually("the invitation at bob", func() bool {
		got, err := bob.Participation(p.PID)
		return err == nil && got.State == client.PartInvited && got.Held == 0
	})
	var out bytes.Buffer
	if err := runDM(ctx, bob, []string{"accept-agent", p.PID}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{p.PID + " active", "warning: nothing here runs what is asked of this agent yet", "no responder is chosen here"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("accept-agent with no responder lacks %q:\n%s", want, out.String())
		}
	}
}
