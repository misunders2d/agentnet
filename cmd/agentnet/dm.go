package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// runPerson shows or sets up this installation's person and its devices
// (agentnet help person).
func runPerson(ctx context.Context, a *client.Agent, args []string, stdout io.Writer) error {
	usage := errors.New("usage: person | person create NAME | person rename NAME | person service | person link | person links | person approve [--native] ID | person refuse ID | person untrust ADDRESS | person remove ADDRESS (see agentnet help person)")
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
		trusted, err := a.SelfConsentTrust()
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s  %q  roster %d\n", p.Person, p.Label, p.Seq)
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second) // the Hub's word on each device, briefly
		defer cancel()
		for _, d := range p.Devices {
			this := ""
			if d.This {
				this = "  (this device)"
			} else if revoked, _ := a.Revoked(lookup, d.Address); revoked { // offline: not known here, nothing said
				this = "  (revoked by a Hub admin: agentnet person remove " + d.Address + " takes it off your person)"
			}
			if !d.This && slices.Contains(trusted, client.TrustedDevice{Address: d.Address, Fingerprint: d.Fingerprint}) {
				this = "  (trusted: its invites of your agents here need no accept)"
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
		fmt.Fprintln(stdout, "(approve --native ID, only for a computer running agentnet and never a browser, also lets its invites of your own agents here need no accept)")
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
	case args[0] == "approve" && len(args) == 3 && args[1] == "--native":
		if err := a.ApproveNativeLink(ctx, args[2]); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "approved as a computer running agentnet: its invites of your own agents here need no accept (never approve a browser with --native; agentnet person untrust ADDRESS stops it)")
		return nil
	case (args[0] == "approve" || args[0] == "refuse") && len(args) == 2 && !strings.HasPrefix(args[1], "-"):
		if err := a.DecideLink(ctx, args[1], args[0] == "approve"); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%sd\n", args[0])
		return nil
	case (args[0] == "admin" || args[0] == "unadmin") && len(args) == 2 && !strings.HasPrefix(args[1], "-"):
		grant := args[0] == "admin"
		if err := a.SetDeviceAdmin(ctx, args[1], grant); err != nil {
			return err
		}
		if grant {
			fmt.Fprintf(stdout, "%s may now change company settings (workspace name, invites, release notices, storage); agentnet person unadmin %s takes it back\n", args[1], args[1])
		} else {
			fmt.Fprintf(stdout, "%s may no longer change company settings\n", args[1])
		}
		return nil
	case args[0] == "untrust" && len(args) == 2:
		if err := a.UntrustOwnDevice(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "untrusted %s: its invites of your own agents here wait for your accept again\n", args[1])
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
		return errors.New("usage: dm new ADDRESS | dm list | dm show ID | dm send [--question|--task] ID TEXT | dm invite ID HOST | dm agents ID | dm accept-agent PID | dm decline-agent PID | dm dismiss-agent PID | dm ask-agent [--task] PID TEXT | dm invite-guest ID HOST | dm accept-guest PID | dm decline-guest PID | dm end-guest PID (see agentnet help dm)")
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
			fmt.Fprintln(stdout, convLine(c))
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
		if len(msgs) == 0 {
			convs, err := a.Conversations()
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(convs, func(c client.ConversationInfo) bool { return c.ID == args[1] }) {
				return client.ErrNoConversation
			}
		}
		printConvMessages(stdout, msgs)
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
		beyond := ""
		if !*question && !*task {
			beyond = "without --question or --task"
		}
		if err := receiverGuard(a, "dm send", beyond, *replyTo); err != nil {
			return err
		}
		receiver, err := returnSelection.selected(a, *question || *task)
		if err != nil {
			return err
		}
		m := client.ConvOutgoing{Kind: envelope.KindMessage, Body: fs.Arg(1), Files: files, ReplyReceiver: receiver, ReplyTo: *replyTo, Quote: *replyTo}
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
			if p.Role == protocol.RoleHuman {
				line = fmt.Sprintf("%s  %s  guest on %s (%q), invited by %q, %d earlier messages shared", p.PID, p.State, p.Host.Address,
					p.Host.Label, p.Inviter.Label, len(p.Grant))
			}
			if keys := taskGrantees(a, p); len(keys) > 0 {
				// Accepting the invitation is the standing grant (agentjob.go).
				line += "; tasks from " + strings.Join(keys, ", ") + " run on the host without asking while it participates"
			}
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
		if keys := taskGrantees(a, p); args[0] == "accept-agent" && p.HostHere && len(keys) > 0 {
			fmt.Fprintf(stdout, "tasks from %s now run on this device without asking while this agent participates (%s)\n", strings.Join(keys, ", "), grantEnd(p))
		}
		if why := a.NothingRuns(p.AgentID); args[0] == "accept-agent" && p.HostHere && why != "" {
			fmt.Fprintf(stdout, "warning: nothing here runs what is asked of this agent yet, so it waits: %s\n", why)
		}
		return nil
	case "invite-guest":
		fs := flag.NewFlagSet("dm invite-guest", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		share := fs.String("share", "", "comma-separated logical ids of earlier messages the guest is shown")
		note := fs.String("note", "", "a note for the guest")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 2 {
			return errors.New("usage: dm invite-guest [--share LID,...] [--note TEXT] ID HOST")
		}
		p, err := a.InviteHuman(ctx, fs.Arg(0), fs.Arg(1), splitList(*share), *note)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %s\n", p.PID, p.State)
		return nil
	case "accept-guest", "decline-guest", "end-guest":
		if len(args) != 2 {
			return fmt.Errorf("usage: dm %s PID", args[0])
		}
		if p, err := a.Participation(args[1]); err != nil {
			return err
		} else if p.Role != protocol.RoleHuman {
			return errors.New("that is an agent's participation: use dm accept-agent, decline-agent or dismiss-agent")
		}
		decide := a.AcceptParticipation
		switch args[0] {
		case "decline-guest":
			decide = a.DeclineParticipation
		case "end-guest":
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
		receiver, err := returnSelection.selected(a, true)
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

// convLine is one conversation in dm list: a group by its title and
// members, a DM this person is not a member of (it hosts an assistant
// there, or is a guest there who can read and send) by both its people,
// any other DM by the other person.
func convLine(c client.ConversationInfo) string {
	since := time.Unix(c.Created, 0).Format("2006-01-02")
	labels := make([]string, len(c.Members))
	for i, m := range c.Members {
		labels[i] = fmt.Sprintf("%q", m.Label)
	}
	switch {
	case c.Kind == protocol.ConvKindGroup:
		return fmt.Sprintf("%s  group %q with %s  since %s", c.ID, c.Title, strings.Join(labels, ", "), since)
	case c.Role == "visitor" && len(labels) > 0:
		return fmt.Sprintf("%s  between %s (you are not a member)  since %s", c.ID, strings.Join(labels, " and "), since)
	}
	return fmt.Sprintf("%s  with %q (%s, %s)  since %s", c.ID, c.Peer.Label, c.Peer.Address, c.Peer.State, since)
}

// printConvMessages prints a conversation's messages (dm show), oldest
// first.
func printConvMessages(stdout io.Writer, msgs []client.ConvMessage) {
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
		state := m.Delivery
		if state == "" {
			state = m.State
		}
		if len(m.Copies) > 0 {
			for _, c := range m.Copies {
				state += "; " + c.To + ": " + c.State
			}
		}
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
		header := fmt.Sprintf("%s  %s %s %s (%s)  %s lid %s", time.Unix(func() int64 {
			if m.Sent > 0 && m.Sent < 253370764800 && m.Sent <= m.At {
				return m.Sent
			}
			return m.At
		}(), 0).Format("2006-01-02 15:04"), m.Dir, who, kind, state, m.ID, m.LID)
		text := shownText(m.Body, m.Controls)
		if m.Sub == envelope.SubEvent {
			text = eventLine(m.Body) // the record in words, not its signed JSON
		}
		fmt.Fprintf(stdout, "%s\n  %s\n", termText(header, "  "), termText(text, "  "))
		if m.Deleted {
			continue // its files went with it
		}
		for _, f := range m.Attachments {
			line := fmt.Sprintf("  [file] %q %d bytes", f.Name, f.Size)
			if f.SavedPath != "" {
				line += " saved " + f.SavedPath
			}
			fmt.Fprintln(stdout, line)
		}
	}
}

// shownText is a message's text as people see it now: its latest edit,
// marked as edited, or a mark that its sender deleted it.
func shownText(body string, c client.Controls) string {
	switch {
	case c.Deleted:
		return "(deleted)"
	case c.Edited:
		return c.Shown(body) + " (edited)"
	}
	return body
}

// eventLine says what a participation event records, in place of its
// signed JSON (the messenger page words it with the people's names).
func eventLine(body string) string {
	ev, err := protocol.ParseParticipationEvent([]byte(body))
	if err != nil {
		return "(a participation record that cannot be read here)"
	}
	switch ev.Type {
	case protocol.EventInvite, protocol.EventScope:
		invited := "an agent"
		if ev.Host != nil && ev.Role == protocol.RoleHuman {
			invited = "the person on " + ev.Host.Address + " as a guest"
		} else if ev.Host != nil {
			invited = "the agent on " + ev.Host.Address
		}
		return fmt.Sprintf("%s invited %s (participation %s)", ev.Author.Address, invited, ev.PID)
	case protocol.EventAccept:
		return fmt.Sprintf("%s accepted (participation %s)", ev.Author.Address, ev.PID)
	case protocol.EventDecline:
		return fmt.Sprintf("%s declined (participation %s)", ev.Author.Address, ev.PID)
	case protocol.EventDismiss:
		return fmt.Sprintf("%s ended it (participation %s)", ev.Author.Address, ev.PID)
	}
	return fmt.Sprintf("%s recorded %q (participation %s)", ev.Author.Address, ev.Type, ev.PID)
}

// grantEnd says how the standing task grant of p, an agent hosted on this
// device, ends. Dismissing it ends it, but only a DM member may dismiss: a
// host outside the DM cannot dismiss its own agent's participation.
func grantEnd(p client.ParticipationInfo) string {
	if p.External {
		return "only a DM member can end it: they run agentnet dm dismiss-agent " + p.PID + "; this device cannot"
	}
	return "agentnet dm dismiss-agent " + p.PID + " ends it"
}

// taskGrantees names the member keys whose tasks p's agent runs without
// asking once its host accepted: by person and device where that key is
// held here, otherwise by the key alone.
func taskGrantees(a *client.Agent, p client.ParticipationInfo) []string {
	if len(p.TaskKeys) == 0 {
		return nil
	}
	var people []client.PersonInfo
	if me, ok, err := a.Person(); err == nil && ok {
		people = append(people, me)
	}
	if convs, err := a.Conversations(); err == nil {
		for _, c := range convs {
			if c.ID == p.Conv {
				people = append(append(people, c.Peer), c.Members...)
			}
		}
	}
	var out []string
	for _, fp := range p.TaskKeys {
		name := "key " + fp
		for _, person := range people {
			for _, d := range person.Devices {
				if d.Fingerprint == fp {
					name = fmt.Sprintf("%q (%s, key %s)", person.Label, d.Address, fp)
				}
			}
		}
		out = append(out, name)
	}
	return out
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
