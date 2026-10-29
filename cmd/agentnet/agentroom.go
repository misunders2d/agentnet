package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Owner decision, October 29, 2026 (MEL-713): an agent asks its person's
// other agents in the "My agents" room, posting as itself: from a terminal
// session, or from a background run serving a request (chain rule:
// client.RunAgentRoomAsk). Another person's agent is invited into the job's
// topic as a visitor. Nothing starts a version 1 turn any more: a person at
// a plain shell sends in a DM and asks in a chat; a delegated reply
// binding's run keeps its binding's thread until version 1 is deleted.

// roomAsk is where an ask/task to an own device comes from.
type roomAsk struct {
	from    client.LocalAgentInfo // a terminal's agent
	session string                // a terminal's native session
	cause   string                // a run's request
}

// roomAskFrom reports whether ask/task to target goes to the room.
func roomAskFrom(a *client.Agent, target, fromAgent string) (roomAsk, bool, error) {
	var r roomAsk
	cause := ""
	if inRun() {
		if receiverRun() {
			return r, false, nil
		}
		for _, k := range []string{client.AgentRequestEnv, client.RoomRequestEnv, client.ProgressRequestEnv} {
			if cause = os.Getenv(k); cause != "" {
				break
			}
		}
		if cause == "" {
			return r, false, nil
		}
	}
	harness, session := client.SessionHarness(os.Getenv)
	if handle := os.Getenv("AGENTNET_REPLY_SESSION"); cause == "" && harness == "" && handle != "" {
		// A Pi or OMP session names itself by its registered reply session.
		if h, sid, err := a.ReplySessionHarness(handle, os.Getenv("AGENTNET_REPLY_SESSION_HOME")); err == nil {
			harness, session = h, sid
		}
	}
	if cause == "" && harness == "" && fromAgent == "" {
		return r, false, nil
	}
	if _, _, err := protocol.SplitTarget(target); err != nil {
		return r, false, err
	}
	if cause != "" {
		r.cause = cause
		return r, true, nil
	}
	var err error
	if r.from, err = a.SessionAgent(harness, fromAgent); err != nil {
		return r, true, err
	}
	if session != "" {
		r.session = harness + "/" + session
	}
	return r, true, nil
}

// plainShellAsk is why a person's plain shell cannot ask or task an agent
// directly: a terminal speaks as an agent, and a person asks in a chat.
const plainShellAsk = "ask from your agent's session (Claude Code, Codex, Pi or OMP) or name the agent to speak as with --from-agent ID; as a person, ask in a chat: agentnet dm new ADDRESS, agentnet dm invite ID HOST, then agentnet dm ask-agent PID TEXT"

// sendPlain is `agentnet send` (not --progress): from an agent's session or
// run it is a plain message in that job's topic of the "My agents" room, as
// that agent; from a person's plain shell it goes in their DM with the
// person at target. It asks for nothing.
func sendPlain(ctx context.Context, a *client.Agent, target, body, replyTo, fromAgent string, files []string, out io.Writer) error {
	host, session, err := protocol.SplitTarget(target)
	if err != nil {
		return err
	}
	if session != "" {
		return errors.New("ADDRESS#SESSION belonged to the old device messages; send to the device's agent or person")
	}
	var named []client.OutgoingFile
	for _, p := range files {
		named = append(named, client.OutgoingFile{Path: p})
	}
	via, room, err := roomAskFrom(a, target, fromAgent)
	if err != nil {
		return err
	}
	if room {
		r := client.AgentRoomAsk{From: via.from, SessionKey: via.session, Host: host, Kind: envelope.KindMessage, Body: body, ReplyTo: replyTo, Files: named}
		if r.AgentID, err = a.RoomTargetAgent(ctx, host, "", r.From.Record.ID); err != nil {
			return err
		}
		var sent client.AgentRoomSent
		if via.cause != "" {
			sent, err = a.RunAgentRoomAsk(ctx, via.cause, r)
		} else {
			sent, err = a.SendAgentRoomAsk(ctx, r)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s %s: sent in %q, topic %s\n", sent.ID, sent.State, client.MyAgentsRoomTitle, sent.Topic)
		return nil
	}
	if inRun() {
		return errors.New("this run serves no request here, so it has no agent to send as")
	}
	own, err := a.OwnAgentHost(host)
	if err != nil {
		return err
	}
	if own || host == a.Address {
		return errors.New("your own devices' agents are reached in your My agents room: send from your agent's session or with --from-agent ID, or write in your chat with that agent in the app")
	}
	conv, err := a.DMWith(ctx, host)
	if err != nil {
		return err
	}
	sent, err := a.SendConv(ctx, conv, client.ConvOutgoing{Kind: envelope.KindMessage, Body: body, Files: named, ReplyTo: replyTo, Quote: replyTo})
	if err != nil {
		return err
	}
	line := sent.ID + " " + sent.State
	if sent.Detail != "" {
		line += " (" + sent.Detail + ")"
	}
	fmt.Fprintf(out, "%s: sent in your DM %s (agentnet dm show %s)\n", line, conv, conv)
	return nil
}

// runRoomAsk posts the request in the room and waits for its answer here.
func runRoomAsk(ctx context.Context, a *client.Agent, r client.AgentRoomAsk, cause, target string, wait time.Duration, out io.Writer) error {
	host, _, err := protocol.SplitTarget(target)
	if err != nil {
		return err
	}
	if r.AgentID, err = a.RoomTargetAgent(ctx, host, r.AgentID, r.From.Record.ID); err != nil {
		return err
	}
	r.Host = host
	var sent client.AgentRoomSent
	if cause != "" {
		sent, err = a.RunAgentRoomAsk(ctx, cause, r)
	} else {
		sent, err = a.SendAgentRoomAsk(ctx, r)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s: asked as agent %s in %q, topic %s\n", sent.ID, sent.State, r.From.Record.Label, client.MyAgentsRoomTitle, sent.Topic)
	if sent.Detail != "" {
		fmt.Fprintln(out, termText(sent.Detail, ""))
	}
	if cause != "" {
		return awaitHelper(ctx, a, cause, sent.ID, wait, out, os.Stderr)
	}
	return awaitAnswer(ctx, a, sent.ID, "agentnet dm wait "+sent.ID, wait, r.Receiver, out, os.Stderr)
}

// awaitHelper is awaitAnswer inside a run (owner, October 29, 2026; MEL-540):
// an answer that does not come in time is waited for by the run's request,
// which runs again with it after this run ends, once every such ask of it
// has finished. An ask that ended without an answer is said now.
func awaitHelper(ctx context.Context, a *client.Agent, cause, id string, wait time.Duration, out, errw io.Writer) error {
	show := "agentnet dm wait " + id
	if wait > 0 {
		fmt.Fprintf(errw, "sent; waiting up to %s for the answer\n", wait)
		r, err := a.AwaitReply(ctx, id, wait, func(v client.ExecView) {
			if line := statusLine(v); line != "" {
				fmt.Fprintln(errw, line)
			}
		})
		switch {
		case err == nil && r.Answer != nil:
			printAnswer(out, r.Answer)
			return nil
		case err == nil && r.Stopped != nil && r.Stopped.State != "awaiting" && r.Stopped.State != "needs_human":
			fmt.Fprintln(errw, stopLine(*r.Stopped, show))
			return nil
		case err != nil && !errors.Is(err, client.ErrDaemonNotRunning) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled):
			return err
		}
	}
	if err := a.WaitForHelper(cause, id); err != nil {
		return err
	}
	fmt.Fprintf(out, "No answer yet to %s. Go on with other work, or end your turn with a short status for your requester: AgentNet runs this request again with the answer once it comes and your other asks still out have finished. Do not wait for it yourself.\n", id)
	return nil
}
