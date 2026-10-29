package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Owner decision, October 29, 2026 (MEL-713): an agent in a terminal asks
// its person's other agents in the "My agents" room, posting as itself.
// A person at a plain shell, background runs and other people's agents
// keep their current paths for now (later MEL-713 phases).

// roomAskFrom reports whether ask/task to target goes to the room, and as
// which agent hosted here.
func roomAskFrom(a *client.Agent, target, fromAgent string) (client.LocalAgentInfo, string, bool, error) {
	if inRun() {
		return client.LocalAgentInfo{}, "", false, nil
	}
	harness, session := client.SessionHarness(os.Getenv)
	if harness == "" && fromAgent == "" {
		return client.LocalAgentInfo{}, "", false, nil
	}
	host, _, err := protocol.SplitTarget(target)
	if err != nil {
		return client.LocalAgentInfo{}, "", false, err
	}
	if own, err := a.OwnAgentHost(host); err != nil || !own {
		return client.LocalAgentInfo{}, "", false, err
	}
	from, err := a.SessionAgent(harness, fromAgent)
	if err != nil {
		return client.LocalAgentInfo{}, "", true, err
	}
	key := ""
	if session != "" {
		key = harness + "/" + session
	}
	return from, key, true, nil
}

// runRoomAsk posts the request in the room and waits for its answer here.
func runRoomAsk(ctx context.Context, a *client.Agent, r client.AgentRoomAsk, target string, wait time.Duration, out io.Writer) error {
	host, _, err := protocol.SplitTarget(target)
	if err != nil {
		return err
	}
	if r.AgentID, err = a.RoomTargetAgent(ctx, host, r.AgentID, r.From.Record.ID); err != nil {
		return err
	}
	r.Host = host
	sent, err := a.SendAgentRoomAsk(ctx, r)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s: asked as agent %s in %q, topic %s\n", sent.ID, sent.State, r.From.Record.Label, client.MyAgentsRoomTitle, sent.Topic)
	if sent.Detail != "" {
		fmt.Fprintln(out, termText(sent.Detail, ""))
	}
	return awaitAnswer(ctx, a, sent.ID, "agentnet dm show "+sent.Conv, wait, r.Receiver, out, os.Stderr)
}
