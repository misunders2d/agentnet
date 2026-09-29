package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

// runPerson shows or creates this installation's person (agentnet help person).
func runPerson(ctx context.Context, a *client.Agent, args []string, stdout io.Writer) error {
	switch {
	case len(args) == 0:
		p, ok, err := a.Person()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("no person on this installation (agentnet person create NAME)")
		}
		fmt.Fprintf(stdout, "%s  %q  on %s (%s)\n", p.Person, p.Label, p.Address, p.Fingerprint)
		return nil
	case args[0] == "create" && len(args) == 2:
		p, err := a.CreatePerson(ctx, args[1])
		if errors.Is(err, client.ErrNotPublished) {
			fmt.Fprintf(stdout, "%s  %q  on %s\n", p.Person, p.Label, p.Address)
			return err
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s  %q  on %s\n", p.Person, p.Label, p.Address)
		return nil
	}
	return errors.New("usage: person | person create NAME (see agentnet help person)")
}

// runDM handles two-person conversations (agentnet help dm).
func runDM(ctx context.Context, a *client.Agent, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: dm new ADDRESS | dm list | dm show ID | dm send [--question|--task] ID TEXT | dm invite ID HOST | dm agents ID | dm accept-agent PID | dm decline-agent PID | dm dismiss-agent PID | dm ask-agent [--task] PID TEXT (see agentnet help dm)")
	}
	switch args[0] {
	case "new":
		if len(args) != 2 {
			return errors.New("usage: dm new ADDRESS")
		}
		id, err := a.CreateDM(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, id)
		return nil
	case "list":
		convs, err := a.Conversations()
		if err != nil {
			return err
		}
		for _, c := range convs {
			fmt.Fprintf(stdout, "%s  with %q (%s, %s)  since %s\n", c.ID, c.Peer.Label, c.Peer.Address, c.Peer.State,
				time.Unix(c.Created, 0).Format("2006-01-02"))
		}
		return nil
	case "show":
		if len(args) != 2 {
			return errors.New("usage: dm show ID")
		}
		msgs, err := a.ConversationMessages(args[1])
		if err != nil {
			return err
		}
		for _, m := range msgs {
			who := m.From
			if m.Origin != "" && m.Emotion != "" {
				who += " [" + m.Origin + ", " + m.Emotion + "]"
			} else if m.Origin != "" {
				who += " [" + m.Origin + "]"
			}
			state := m.State
			if m.Detail != "" {
				state += ": " + m.Detail
			}
			if m.Job != "" && m.Job != m.State { // a request to this device's agent, asked here
				state += "; agent job " + m.Job
				if m.JobDetail != "" {
					state += ": " + m.JobDetail
				}
			}
			kind := m.Kind
			if m.Sub != "" {
				kind += " " + m.Sub
			}
			if m.PID != "" {
				kind += " pid " + m.PID
			}
			fmt.Fprintf(stdout, "%s  %s %s %s (%s)  %s lid %s\n  %s\n", time.Unix(m.At, 0).Format("2006-01-02 15:04"), m.Dir, who, kind, state,
				m.ID, m.LID, strings.ReplaceAll(m.Body, "\n", "\n  "))
		}
		return nil
	case "send":
		fs := flag.NewFlagSet("dm send", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		question := fs.Bool("question", false, "ask the person a question")
		task := fs.Bool("task", false, "ask the person for work")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 2 || (*question && *task) {
			return errors.New("usage: dm send [--question|--task] ID TEXT")
		}
		m := client.ConvOutgoing{Kind: envelope.KindMessage, Body: fs.Arg(1)}
		if *question {
			m.Kind = envelope.KindQuestion
		}
		if *task {
			m.Kind = envelope.KindTask
		}
		sent, err := a.SendConv(ctx, fs.Arg(0), m)
		if err != nil {
			return err
		}
		line := sent.ID + " " + sent.State
		if sent.Detail != "" {
			line += " (" + sent.Detail + ")"
		}
		fmt.Fprintln(stdout, line)
		return nil
	case "invite":
		fs := flag.NewFlagSet("dm invite", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		grant := fs.String("grant", "", "comma-separated logical ids of earlier messages the agent may be given")
		tasks := fs.String("tasks", "", "comma-separated member key fingerprints allowed follow-up tasks")
		note := fs.String("note", "", "a note for the host's person")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 2 {
			return errors.New("usage: dm invite [--grant LID,...] [--tasks FINGERPRINT,...] [--note TEXT] ID HOST")
		}
		p, err := a.InviteAgent(ctx, fs.Arg(0), fs.Arg(1), splitList(*grant), splitList(*tasks), *note)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %s\n", p.PID, p.State)
		return nil
	case "agents":
		if len(args) != 2 {
			return errors.New("usage: dm agents ID")
		}
		parts, err := a.Participations(args[1])
		if err != nil {
			return err
		}
		for _, p := range parts {
			line := fmt.Sprintf("%s  %s  agent on %s (%q's), invited by %q, %d earlier messages shared", p.PID, p.State, p.Host.Address,
				p.Host.Label, p.Inviter.Label, len(p.Grant))
			if p.Conflict != "" {
				line += "  (" + p.Conflict + ")"
			}
			if p.Held > 0 {
				line += fmt.Sprintf("  [%d records not counted here]", p.Held)
			}
			fmt.Fprintln(stdout, line)
		}
		return nil
	case "accept-agent", "decline-agent", "dismiss-agent":
		if len(args) != 2 {
			return fmt.Errorf("usage: dm %s PID", args[0])
		}
		decide := a.AcceptParticipation
		switch args[0] {
		case "decline-agent":
			decide = a.DeclineParticipation
		case "dismiss-agent":
			decide = a.DismissParticipation
		}
		p, err := decide(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %s\n", p.PID, p.State)
		return nil
	case "ask-agent":
		fs := flag.NewFlagSet("dm ask-agent", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		task := fs.Bool("task", false, "a task instead of a question")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 2 {
			return errors.New("usage: dm ask-agent [--task] PID TEXT")
		}
		kind := envelope.KindQuestion
		if *task {
			kind = envelope.KindTask
		}
		sent, err := a.AskAgent(ctx, fs.Arg(0), kind, fs.Arg(1))
		if err != nil {
			return err
		}
		line := sent.ID + " " + sent.State
		if sent.Detail != "" {
			line += " (" + sent.Detail + ")"
		}
		fmt.Fprintln(stdout, line)
		return nil
	}
	return fmt.Errorf("unknown dm command %q (see agentnet help dm)", args[0])
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
