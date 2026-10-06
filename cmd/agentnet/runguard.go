package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
)

// The run guard (docs/plans/ROOM_V1.md §4.5). A harness run that the worker
// starts gets client.BackgroundEnv and AGENTNET_HOME (worker.go), so through
// this CLI it could act for the local user: message people, decide on
// requests, trust keys, bring agents in, change groups, its person, its
// responder or the Hub. Inside a run the CLI therefore allows only the
// commands runAllowed lists and refuses every other one, including any
// command added later, before anything opens the home. This is a guard
// against a run doing so by mistake or because something it read told it
// to, not a security boundary: the run has the same home and keys and could
// act without the CLI.
//
// Besides reading, a run may send only what its prompt offers it. These
// sends pass runAllowed and are checked once their flags are parsed, after
// the home is open:
//   - an update or clarification to its own request, send --reply-to
//     $AGENTNET_REQUEST_ID (sendGuard);
//   - in a selected reply receiver's run, a follow-up on the local user's
//     delegated work with ask, task or dm send --question|--task, only as an
//     answer to a message of that run's own binding (receiverGuard), with
//     that binding's receiver (replyReceiverFlags.selected); the client then
//     keeps it with that binding's original recipients or conversation
//     (bindReplyReceiver).

// replyBindingEnv names the binding of a selected reply receiver's run
// (client receiverBindingEnv).
const replyBindingEnv = "AGENTNET_REPLY_BINDING"

func inRun() bool { return os.Getenv(client.BackgroundEnv) == "1" }

// runGuard refuses, inside a run, every command runAllowed does not list.
// run calls it right after the global flags and help, before any command
// (and before anything opens the home).
func runGuard(cmd string, args []string) error {
	if !inRun() || runAllowed(cmd, args) {
		return nil
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && subcommands[cmd] {
		cmd += " " + args[0]
	}
	if cmd == "inbox" {
		cmd += " without --peek" // it would mark messages read
	}
	return refusedInRun(cmd)
}

// subcommands are the commands whose first argument names what they do.
var subcommands = map[string]bool{"admin": true, "a2a": true, "dm": true, "group": true, "hooks": true, "hub": true,
	"operator": true, "person": true, "remind": true, "responder": true, "room": true, "team": true}

// runAllowed is the allow-list: commands that only read (or print), the
// harness's own hook, which does nothing inside a run (hook.go), and the
// sends a run's prompt offers it, which their own guards check once the
// flags are parsed. Help never reaches it.
func runAllowed(cmd string, args []string) bool {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch cmd {
	case "version", "skill", "whoami", "approvals", "status", "fingerprint", "sessions", "members", "doctor",
		"conversation", "receivers", "download", "hook":
		return true
	case "send": // sendGuard
		return true
	case "inbox": // without --peek it marks messages read
		_, f, err := parseInbox(args)
		return err == nil && *f.peek
	case "team":
		return sub == "list" || sub == "snapshot"
	case "dm":
		return sub == "list" || sub == "show" || sub == "agents" || sub == "send" && receiverRun() // receiverGuard
	case "room":
		return sub == "ask" || sub == "wait"
	case "group":
		return sub == "invitations"
	case "person":
		return len(args) == 0 || sub == "links"
	case "operator":
		return sub == "list"
	case "review-to":
		return len(args) == 0
	case "responder":
		return sub == "list" || sub == "show"
	case "remind":
		return sub == "list"
	case "ask", "task": // receiverGuard
		return receiverRun()
	}
	return false
}

// receiverRun reports a selected reply receiver's run.
func receiverRun() bool { return os.Getenv(replyBindingEnv) != "" }

// receiverGuard is runGuard for ask, task and dm send once their flags are
// parsed: inside a run they are allowed only in a selected reply receiver's
// run, as an answer to a message of that run's own binding (--reply-to), so
// the binding's thread, not the run, decides who receives them. beyond names
// anything used that would reach past that thread (--agent, --follow-up, a
// plain dm message), "" when nothing does. replyReceiverFlags.selected keeps
// the run's own receiver.
func receiverGuard(a *client.Agent, what, beyond, replyTo string) error {
	if !inRun() {
		return nil
	}
	binding := os.Getenv(replyBindingEnv)
	switch {
	case binding == "":
		return refusedInRun(what)
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
	if receiverRun() {
		instead += "; a follow-up on your delegated work answers a message of your reply binding: agentnet ask (or task) --reply-to ID ADDRESS TEXT " +
			"for a request to a device, or agentnet dm send --question (or --task) --reply-to ID CONV TEXT in a conversation, " +
			"where ID is a message of your reply binding (one shown in your prompt)"
	}
	// The text never names the switch: a run that was told to act must not
	// learn from it how to turn the guard off.
	return fmt.Errorf("%s is not allowed inside a run AgentNet started for a request. Such a run may read, and send only what its prompt offers; "+
		"it may not otherwise message people, decide on requests, trust keys, change who takes part in a conversation or change AgentNet's settings. "+
		"Do not change AgentNet's environment to get around this. Instead, %s", what, instead)
}
