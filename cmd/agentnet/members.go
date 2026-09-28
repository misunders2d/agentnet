package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// runMembers prints the agents enrolled on a's Hub, most recently joined
// first, with the Hub's view of their daemons (agentnet help members).
func runMembers(ctx context.Context, a *client.Agent, args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: members (see agentnet help members)")
	}
	m, err := a.Members(ctx)
	if err != nil {
		return err
	}
	for _, e := range m.Members {
		self := ""
		if e.Address == a.Address {
			self = "  (this agent)"
		}
		fmt.Fprintf(stdout, "%s  %s  joined %s%s\n", e.Address, e.Presence, time.Unix(e.Joined, 0).Format("2006-01-02"), self)
	}
	if m.Truncated {
		fmt.Fprintf(stderr, "only the %d most recently joined are listed; the Hub has more members\n", len(m.Members))
	}
	return nil
}
