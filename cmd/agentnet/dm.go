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
		return errors.New("usage: dm new ADDRESS | dm list | dm show ID | dm send [--question|--task] ID TEXT (see agentnet help dm)")
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
			if m.Origin != "" {
				who += " [" + m.Origin + "]"
			}
			state := m.State
			if m.Detail != "" {
				state += ": " + m.Detail
			}
			fmt.Fprintf(stdout, "%s  %s %s %s (%s)  %s\n  %s\n", time.Unix(m.At, 0).Format("2006-01-02 15:04"), m.Dir, who, m.Kind, state,
				m.ID, strings.ReplaceAll(m.Body, "\n", "\n  "))
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
	}
	return fmt.Errorf("unknown dm command %q (see agentnet help dm)", args[0])
}
