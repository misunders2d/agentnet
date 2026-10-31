package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
)

func runRoom(ctx context.Context, a *client.Agent, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: room ask --pid PID [--kind question|task] TEXT | room wait REQUEST_ID")
	}
	if args[0] == "organize" {
		if err := receiverGuard(a, "room organize", "topic organization", ""); err != nil {
			return err
		}
		cause := os.Getenv(client.AgentRequestEnv)
		attempt, err := strconv.Atoi(os.Getenv(client.AgentAttemptEnv))
		if err != nil || cause == "" {
			return errors.New("topic organization belongs to the current admitted native agent request")
		}
		fs := flag.NewFlagSet("room organize", flag.ContinueOnError)
		topic := fs.String("topic", "", "active destination topic")
		fresh := fs.Bool("new", false, "create a topic for this selection")
		title := fs.String("title", "", "local title for the new topic")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) == 0 || !*fresh && *topic == "" {
			return errors.New("usage: room organize --topic ID | --new [--title TITLE] MESSAGE_LID...")
		}
		main := *topic == "main"
		if main {
			*topic = ""
		}
		sent, err := a.OrganizeAgentTopic(ctx, cause, attempt, client.TopicOrganizationRequest{IDs: fs.Args(), Topic: *topic, New: *fresh, Title: *title, Main: main})
		if err != nil {
			return err
		}
		fmt.Fprintln(out, sent.LID, sent.State)
		if link, e := a.OrganizationLink(sent); e == nil {
			fmt.Fprintln(out, link)
		} else {
			fmt.Fprintln(out, "Destination link unavailable; the move remains stored.")
		}
		return nil
	}
	if args[0] == "react" {
		if err := receiverGuard(a, "room react", "an agent reaction", ""); err != nil {
			return err
		}
		fs := flag.NewFlagSet("room react", flag.ContinueOnError)
		pid := fs.String("pid", "", "this host's current agent participation")
		message := fs.String("message", "", "exact ordinary message logical ID")
		key := fs.String("key", "", "original message author fingerprint")
		remove := fs.Bool("remove", false, "remove this agent's reaction")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *pid == "" || *message == "" || *key == "" || len(fs.Args()) != 1 {
			return errors.New("usage: room react --pid PID --message LID --key AUTHOR_KEY [--remove] EMOJI")
		}
		part, err := a.Participation(*pid)
		if err != nil {
			return err
		}
		sent, err := a.SendAgentReaction(ctx, *pid, client.ControlRef{Conv: part.Conv, ID: *message, Fingerprint: *key}, fs.Args()[0], *remove)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, sent.ID, sent.State)
		return nil
	}
	if args[0] == "send" {
		if err := receiverGuard(a, "room send", "a proactive message", ""); err != nil {
			return err
		}
		fs := flag.NewFlagSet("room send", flag.ContinueOnError)
		pid := fs.String("pid", "", "this device's active agent participation")
		topic := fs.String("topic", "", "existing topic ID; defaults to the invited scope or Main")
		var files []client.OutgoingFile
		fs.Func("file", "attach a file (repeatable)", func(path string) error { files = append(files, client.OutgoingFile{Path: path}); return nil })
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *pid == "" || len(fs.Args()) == 0 && len(files) == 0 {
			return errors.New("usage: room send --pid PID [--topic ID] [--file PATH]... [TEXT]")
		}
		sent, err := a.SendAgentMessage(ctx, *pid, *topic, strings.Join(fs.Args(), " "), files...)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, sent.ID, sent.State)
		if sent.Detail != "" {
			fmt.Fprintln(out, sent.Detail)
		}
		return nil
	}
	cause := os.Getenv(client.RoomRequestEnv)
	if cause == "" {
		return errors.New("room asks and waits belong to a running group agent request")
	}
	ref := ""
	switch args[0] {
	case "ask":
		fs := flag.NewFlagSet("room ask", flag.ContinueOnError)
		pid := fs.String("pid", "", "other group agent participation")
		kind := fs.String("kind", "question", "question or task")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *pid == "" || len(fs.Args()) == 0 {
			return errors.New("choose --pid and write a question or task")
		}
		sent, err := a.SendRoomAsk(ctx, cause, *pid, *kind, strings.Join(fs.Args(), " "))
		if err != nil {
			return err
		}
		ref = sent.LID
		fmt.Fprintln(out, "request", ref, "stored; waiting for its correlated reply")
	case "wait":
		if len(args) != 2 {
			return errors.New("usage: room wait REQUEST_ID")
		}
		ref = args[1]
	default:
		return errors.New("usage: room ask | room wait")
	}
	// Existing local daemon wakes only; no timer or extra Hub stream.
	reply, err := a.AwaitRoomReply(ctx, cause, ref)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "reply %s from agent participation %s\n%s\n", reply.ReplyTo, reply.PID, reply.Body)
	return nil
}
