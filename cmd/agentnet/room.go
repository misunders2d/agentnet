package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

func runRoom(ctx context.Context, a *client.Agent, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: room ask --pid PID [--kind question|task] TEXT | room wait REQUEST_ID")
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
	// Local storage only. No Hub polling, second stream or new daemon.
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		reply, err := a.RoomReply(cause, ref)
		if err != nil {
			return err
		}
		if reply != nil {
			fmt.Fprintf(out, "reply %s from agent participation %s\n%s\n", reply.ReplyTo, reply.PID, reply.Body)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
