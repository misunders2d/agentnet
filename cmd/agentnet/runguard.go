package main

import (
	"fmt"
	"os"
	"slices"

	"github.com/misunders2d/agentnet/internal/client"
)

// The run guard (docs/plans/ROOM_V1.md §4.5). A harness run that the worker
// starts gets client.BackgroundEnv and AGENTNET_HOME (worker.go), so through
// this CLI it could act for the local user: message people, decide on
// requests, trust keys, bring agents in or change groups. Inside a run the
// CLI refuses those commands. This is a guard against a run doing so by
// mistake or because something it read told it to, not a security boundary:
// the run has the same home and keys and could act without the CLI.
//
// A run may still read, and send what its prompt offers it:
//   - an update or clarification to its own request, send --reply-to
//     $AGENTNET_REQUEST_ID (checked by sendGuard once the flags are parsed);
//   - in a selected reply receiver's run, a follow-up on the local user's
//     delegated work with ask, task, dm send or dm ask-agent, kept on that
//     run's own binding (replyReceiverFlags.selected).

// replyBindingEnv names the binding of a selected reply receiver's run
// (client receiverBindingEnv).
const replyBindingEnv = "AGENTNET_REPLY_BINDING"

func inRun() bool { return os.Getenv(client.BackgroundEnv) == "1" }

// runGuard refuses cmd inside a run before anything opens the home.
func runGuard(cmd string, args []string) error {
	if !inRun() {
		return nil
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch {
	case slices.Contains([]string{"reply", "accept", "approve", "unapprove", "decline", "trust"}, cmd):
	case cmd == "ask" || cmd == "task":
		if os.Getenv(replyBindingEnv) != "" {
			return nil
		}
	case cmd == "dm" && (sub == "send" || sub == "ask-agent"):
		if os.Getenv(replyBindingEnv) != "" {
			return nil
		}
		cmd += " " + sub
	case cmd == "dm" && slices.Contains([]string{"new", "invite", "accept-agent", "decline-agent", "dismiss-agent"}, sub),
		cmd == "group" && slices.Contains([]string{"create", "invite", "accept", "decline", "retry", "rename", "promote", "demote", "remove", "leave"}, sub):
		cmd += " " + sub
	default:
		return nil // send: sendGuard
	}
	return refusedInRun(cmd)
}

// sendGuard is runGuard for send: inside a run it only reaches the run's own
// request, with or without --progress.
func sendGuard(replyTo string) error {
	if !inRun() || replyTo != "" && replyTo == os.Getenv(client.ProgressRequestEnv) {
		return nil
	}
	return refusedInRun("send")
}

// refusedInRun says what was refused and what the run can do instead.
func refusedInRun(what string) error {
	instead := "put your answer, or what the local user needs to decide, in your final output"
	if os.Getenv(client.ProgressRequestEnv) != "" {
		instead += fmt.Sprintf(`; to update the requester before you finish, use agentnet send --reply-to "$%s" --progress "$%s" TEXT`, client.ProgressRequestEnv, client.ProgressPeerEnv)
	}
	if os.Getenv(replyBindingEnv) != "" {
		instead += "; a follow-up on your delegated work goes through agentnet ask or task without --reply-receiver, which keeps your reply binding"
	}
	return fmt.Errorf("%s is not allowed inside an agent run (%s=1). A run may not message people, decide on requests, trust keys or change who takes part in a conversation. Instead, %s", what, client.BackgroundEnv, instead)
}
