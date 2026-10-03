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

// runPerson shows or sets up this installation's person and its devices
// (agentnet help person).
func runPerson(ctx context.Context, a *client.Agent, args []string, stdout io.Writer) error {
	usage := errors.New("usage: person | person create NAME | person rename NAME | person service | person link | person links | person approve ID | person refuse ID | person remove ADDRESS (see agentnet help person)")
	if len(args) == 0 {
		p, ok, err := a.Person()
		if err != nil {
			return err
		}
		if !ok {
			if role, _ := a.Role(); role == "service" {
				fmt.Fprintln(stdout, "this installation is a service: it speaks as itself, not for a person")
				return nil
			}
			if link := a.LinkState(); link.State == client.LinkPending {
				return fmt.Errorf("no person on this installation yet: it waits for approval on %s (keep agentnet daemon running here, and approve it there with person links / person approve ID)", link.Approver)
			}
			return errors.New("no person on this installation (agentnet person create NAME, or link this device from your other one)")
		}
		fmt.Fprintf(stdout, "%s  %q  roster %d\n", p.Person, p.Label, p.Seq)
		for _, d := range p.Devices {
			this := ""
			if d.This {
				this = "  (this device)"
			}
			fmt.Fprintf(stdout, "  %s  %s%s\n", d.Address, d.Fingerprint, this)
		}
		return nil
	}
	switch {
	case args[0] == "rename":
		return runPersonLabel(ctx, a, args[1:], stdout)
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
	case args[0] == "service" && len(args) == 1:
		if err := a.SetService(); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "this installation is a service: it speaks as itself, not for a person")
		return nil
	case args[0] == "link" && len(args) == 1:
		o, err := a.NewDeviceLink(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Device link code (one use, until %s; show it only to yourself):\n%s\n", o.Expires.Format("15:04"), o.Code)
		fmt.Fprintln(stdout, "Join your new device with it; then approve it here: agentnet person links, agentnet person approve ID")
		return nil
	case args[0] == "links" && len(args) == 1:
		links, err := a.PendingLinks()
		if err != nil {
			return err
		}
		for _, l := range links {
			fmt.Fprintf(stdout, "%s  %s  %s  key %s  asked %s\n", l.ID, l.State, l.Address, l.Fingerprint, time.Unix(l.RequestedAt, 0).Format("2006-01-02 15:04"))
		}
		return nil
	case (args[0] == "approve" || args[0] == "refuse") && len(args) == 2:
		if err := a.DecideLink(ctx, args[1], args[0] == "approve"); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%sd\n", args[0])
		return nil
	case args[0] == "remove" && len(args) == 2:
		if err := a.RemoveDevice(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed %s from your person\n", args[1])
		return nil
	}
	return usage
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
			if c.Deleted {
				continue
			}
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
			if m.Via != "" {
				who = "you on " + m.Via
			}
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
			for _, f := range m.Attachments {
				line := fmt.Sprintf("  [file] %q %d bytes", f.Name, f.Size)
				if f.SavedPath != "" {
					line += " saved " + f.SavedPath
				}
				fmt.Fprintln(stdout, line)
			}
		}
		return nil
	case "send":
		fs := flag.NewFlagSet("dm send", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		question := fs.Bool("question", false, "ask the person a question")
		task := fs.Bool("task", false, "ask the person for work")
		returnSelection := receiverFlags(fs)
		replyTo := fs.String("reply-to", "", "reply to exact same-conversation physical/logical reference")
		var files []client.OutgoingFile
		fs.Func("file", "attach a file (repeatable)", func(p string) error { files = append(files, client.OutgoingFile{Path: p}); return nil })
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() < 1 || fs.NArg() > 2 || (fs.NArg() == 1 && len(files) == 0) || (*question && *task) {
			return errors.New("usage: dm send [--question|--task] [--file PATH]... ID [TEXT]   (TEXT may be left out when files are attached)")
		}
		receiver, err := returnSelection.selected(a)
		if err != nil {
			return err
		}
		m := client.ConvOutgoing{Kind: envelope.KindMessage, Body: fs.Arg(1), Files: files, ReplyReceiver: receiver, ReplyTo: *replyTo}
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
		returnSelection := receiverFlags(fs)
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 2 {
			return errors.New("usage: dm ask-agent [--task] PID TEXT")
		}
		kind := envelope.KindQuestion
		if *task {
			kind = envelope.KindTask
		}
		receiver, err := returnSelection.selected(a)
		if err != nil {
			return err
		}
		sent, err := a.AskAgentWithReceiver(ctx, fs.Arg(0), kind, fs.Arg(1), receiver)
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
