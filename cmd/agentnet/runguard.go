package main

import (
	"fmt"
	"os"

	"github.com/misunders2d/agentnet/internal/client"
)

// Background execution uses the owner's native permissions. Only an explicit
// delegated reply binding constrains its continuation's frozen routing.
// replyBindingEnv names the binding of a selected reply receiver's run
// (client receiverBindingEnv).
const replyBindingEnv = "AGENTNET_REPLY_BINDING"

func inRun() bool { return os.Getenv(client.BackgroundEnv) == "1" }

// receiverRun reports a selected reply receiver's run.
func receiverRun() bool { return os.Getenv(replyBindingEnv) != "" }

// receiverGuard applies only to an explicitly selected reply receiver's
// run, as an answer to a message of that run's own binding (--reply-to), so
// the binding's thread, not the run, decides who receives them. beyond names
// anything used that would reach past that thread (--agent, --follow-up, a
// plain dm message), "" when nothing does. replyReceiverFlags.selected keeps
// the run's own receiver.
func receiverGuard(a *client.Agent, what, beyond, replyTo string) error {
	if !inRun() || !receiverRun() {
		return nil
	}
	binding := os.Getenv(replyBindingEnv)
	switch {
	case beyond != "":
		return refusedInRun(what + " " + beyond)
	case replyTo == "":
		return refusedInRun(what + " without --reply-to")
	}
	held, err := a.ReplyBindingHolds(binding, replyTo)
	if err != nil {
		return err
	}
	if !held {
		return refusedInRun(what + " --reply-to " + replyTo + ", a message outside your reply binding,")
	}
	return nil
}

// refusedInRun describes the exact delegated continuation boundary.
func refusedInRun(what string) error {
	return fmt.Errorf("%s is outside this delegated reply binding. Continue only the exact bound request, receiver and recipients with --reply-to; native permissions do not change its frozen routing", what)
}

// A selected continuation cannot use an unbound send route to escape its
// frozen receiver. Ordinary background runs use the normal CLI handlers.
func receiverCommandGuard(cmd string, args []string) error {
	if inRun() && receiverRun() && (cmd == "send" || cmd == "reply" || cmd == "do" || cmd == "dm" && len(args) > 0 && args[0] == "ask-agent") {
		return refusedInRun(cmd)
	}
	return nil
}
