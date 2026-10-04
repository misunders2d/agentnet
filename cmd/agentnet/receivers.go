package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
)

type replyReceiverFlags struct{ receiver, instructions, mode, binding, onClose *string }

func receiverFlags(fs *flag.FlagSet) replyReceiverFlags {
	return replyReceiverFlags{fs.String("reply-receiver", "", "local return receiver: human, managed AgentID, or session:HANDLE"), fs.String("continue", "", "original local continuation instructions (managed or explicit on-close)"), fs.String("continue-mode", "", "explicit local question or task mode"), fs.String("reply-binding", "", "reuse exact existing local receiver/instructions/context"), fs.String("on-close-agent", "", "explicit managed AgentID to continue an exact native receiver after normal shutdown")}
}

// selected is the local return receiver of a send; request is true for a
// question or task.
func (f replyReceiverFlags) selected(a *client.Agent, request bool) (*client.ReplyReceiver, error) {
	id := *f.binding
	if id == "" && *f.receiver == "" && os.Getenv(client.BackgroundEnv) == "1" {
		id = os.Getenv("AGENTNET_REPLY_BINDING")
	}
	if bound := os.Getenv(replyBindingEnv); inRun() && bound != "" && id != bound {
		return nil, refusedInRun("choosing another reply receiver") // the run guard (runguard.go)
	}
	if id != "" {
		if *f.receiver != "" || *f.instructions != "" || *f.mode != "" || *f.onClose != "" {
			return nil, errors.New("reply-binding reuses frozen receiver/instructions/mode; cannot redirect or upgrade")
		}
		return a.ReplyReceiverForBinding(id)
	}
	if *f.receiver == "" {
		if *f.onClose == "" && (*f.instructions != "" || *f.mode != "") {
			return nil, errors.New("continuation requires an explicit managed reply receiver")
		}
		// Origin return brings an answer back to the session that asked. A
		// plain message expects none, so the session it is sent from never
		// blocks it, unless --on-close-agent asks for that session's
		// continuation.
		origin := os.Getenv(client.BackgroundEnv) != "1" && (request || *f.onClose != "")
		if origin && os.Getenv("AGENTNET_REPLY_SESSION") != "" {
			generation, e := strconv.ParseInt(os.Getenv("AGENTNET_REPLY_SESSION_GENERATION"), 10, 64)
			if e != nil {
				return nil, e
			}
			r, e := a.CurrentReplySession(os.Getenv("AGENTNET_REPLY_SESSION"), os.Getenv("AGENTNET_REPLY_SESSION_HOME"), generation)
			if e != nil {
				return nil, e
			}
			return f.closedSelection(r)
		}
		if origin {
			// Asked from a Claude Code or Codex session: the answer returns to
			// that exact registered session, or nothing is sent.
			r, e := a.NativeOriginReceiver(os.Getenv(client.ClaudeSessionEnv), os.Getenv(client.CodexThreadEnv))
			if e != nil {
				return nil, e
			}
			if r != nil {
				return f.closedSelection(r)
			}
		}
		if *f.onClose != "" {
			return nil, errors.New("on-close-agent requires an exact registered native reply receiver")
		}
		return nil, nil
	}
	r := &client.ReplyReceiver{Kind: "managed_agent", AgentID: *f.receiver, Instructions: *f.instructions, Mode: *f.mode}
	if *f.receiver == "human" {
		r.Kind = "human"
		r.AgentID = ""
	}
	if strings.HasPrefix(*f.receiver, "session:") {
		r.Kind = "live_session"
		r.AgentID = ""
		r.SessionHandle = strings.TrimPrefix(*f.receiver, "session:")
	}
	return f.closedSelection(r)
}
func (f replyReceiverFlags) closedSelection(r *client.ReplyReceiver) (*client.ReplyReceiver, error) {
	if *f.onClose != "" {
		if r.Kind != "live_session" {
			return nil, errors.New("on-close-agent requires an exact native reply receiver")
		}
		r.OnClose = &client.ManagedReplyHandoff{AgentID: *f.onClose, Instructions: *f.instructions, Mode: *f.mode}
		r.Instructions = ""
		r.Mode = ""
	}
	return r, nil
}

func runReceivers(a *client.Agent, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("receivers", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print local pending/accepted/completed/refused receiver bindings")
	sessions := fs.Bool("sessions", false, "list registered local native session receivers (no credentials or paths)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("usage: receivers [--sessions] [--json]")
	}
	if *sessions {
		rows, e := a.ReplySessions()
		if e != nil {
			return e
		}
		if *asJSON {
			return json.NewEncoder(out).Encode(rows)
		}
		for _, r := range rows {
			fmt.Fprintf(out, "%s %s %s registered=%t\n", r.Handle, r.Harness, termText(r.Label, ""), r.Active)
		}
		return nil
	}
	rows, err := a.ReplyReceiverBindings()
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(rows)
	}
	for _, b := range rows {
		fmt.Fprintf(out, "%s %s %s %s\n", b.ID, b.Receiver.Kind, b.Receiver.AgentID, b.State)
		if b.HandoffState != "" {
			fmt.Fprintln(out, "  handoff:", b.HandoffState)
		}
		if b.Detail != "" {
			fmt.Fprintln(out, "  "+termText(b.Detail, "  "))
		}
		for _, in := range b.Inputs {
			fmt.Fprintf(out, "  %s %s\n", in.ID, in.State)
			if in.Detail != "" {
				fmt.Fprintln(out, "    "+termText(in.Detail, "    "))
			}
		}
	}
	return nil
}
