// Command agentnet is the AgentNet client CLI and Hub server.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/misunders2d/agentnet/internal/a2abind"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

func main() {
	log.SetFlags(log.LstdFlags)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agentnet:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	global := flag.NewFlagSet("agentnet", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	home := global.String("home", defaultHome(), "agent home directory")
	if err := global.Parse(args); errors.Is(err, flag.ErrHelp) {
		return printHelp(os.Stdout, nil)
	} else if err != nil {
		return fmt.Errorf("%v (see agentnet --help)", err)
	}
	args = global.Args()
	if len(args) == 0 || wantsHelp(args) {
		return printHelp(os.Stdout, args) // before anything touches the home
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd, rest := args[0], args[1:]
	if err := receiverCommandGuard(cmd, rest); err != nil {
		return err
	}
	switch cmd {
	case "hub":
		return runHub(ctx, rest)
	case "version":
		fmt.Printf("agentnet %s (protocol %d)\n", protocol.Version, protocol.ProtocolVersion)
		if len(rest) == 1 && rest[0] == "--schema" {
			// For agentnet update: which home databases this program can open.
			fmt.Printf("schema %d\n", client.SchemaSteps())
			return nil
		}
		fmt.Println(currentBuildDescription())
		// A saved recommendation goes to stderr, so the line above stays
		// parseable; nothing is created and the Hub is not contacted.
		if r, ok := client.LocalRelease(*home); ok && protocol.Newer(r.Version, protocol.Version) {
			fmt.Fprintf(os.Stderr, "your Hub recommends agentnet %s (this is %s): see agentnet help update and %s\n", r.Version, protocol.Version, r.URL)
		}
		return nil
	case "update":
		return runUpdate(ctx, *home, rest)
	case appUpdateHelperCmd:
		return runAppUpdateHelper(*home, rest, os.Stdin)
	case "app-install": // hidden: first-open installation by the Linux shell
		return runAppInstall(rest, os.Stdout)
	case updateHelperCmd: // hidden: see restart_windows.go
		return runUpdateHelper(*home, rest)
	case "skill":
		return runSkill(os.Stdout, rest)
	case "join":
		return runJoin(ctx, *home, rest)
	case "hook":
		return runHook(*home, rest, os.Stdin, os.Stdout)
	case "hooks":
		return runHooks(*home, rest)
	case "ui":
		return runUI(ctx, *home, rest, os.Stdout)
	case "app": // the AgentNet app's own program (app.go); it opens the home itself
		return runApp(ctx, *home, rest, os.Stdin, os.Stdout)
	}
	if _, known := topics[cmd]; !known {
		return fmt.Errorf("unknown command %q (see agentnet --help)", cmd)
	}
	if cmd == "daemon" {
		keepDaemonInstallDirOnPath(os.Args[0])
	}
	a, err := client.Open(*home)
	if err != nil {
		return err
	}
	defer a.Close()
	switch cmd {
	case "whoami":
		return runWhoami(ctx, a, os.Stdout)
	case "send":
		return runSend(ctx, a, rest, false)
	case "ask", "task":
		return runSendKind(ctx, a, cmd, rest)
	case "do":
		return runDo(ctx, a, rest)
	case "accept", "approve", "unapprove":
		if len(rest) == 2 && ((cmd == "accept" && rest[0] == "--always") || (cmd != "accept" && rest[0] == "--tasks")) {
			return runTaskGrant(a, cmd, rest[1])
		}
		fallthrough
	case "cancel", "resolve":
		if len(rest) != 1 || strings.HasPrefix(rest[0], "-") {
			return fmt.Errorf("usage: %s ID-or-ADDRESS", cmd)
		}
		var err error
		switch cmd {
		case "accept":
			err = a.Accept(rest[0])
		case "cancel":
			err = a.Cancel(rest[0])
		case "approve":
			err = a.Approve(rest[0])
		case "unapprove":
			err = a.Unapprove(rest[0])
		case "resolve":
			err = a.Resolve(rest[0])
		}
		if err == nil {
			fmt.Printf("%s %s\n", cmd, rest[0])
		}
		return err
	case "review-to":
		switch {
		case len(rest) == 0:
			to, err := a.ReviewTo()
			if err != nil {
				return err
			}
			if to == "" {
				fmt.Println("off: waiting items are shown only here (desktop notification, agentnet inbox --review)")
			} else {
				fmt.Printf("review notices go to %s\n", to)
			}
			return nil
		case len(rest) == 1 && rest[0] == "--off":
			if err := a.ClearReviewTo(); err != nil {
				return err
			}
			fmt.Println("review notices off")
			return nil
		case len(rest) == 1 && !strings.HasPrefix(rest[0], "-"):
			if err := a.SetReviewTo(ctx, rest[0]); err != nil {
				return err
			}
			fmt.Printf("review notices go to %s\n", rest[0])
			return nil
		}
		return errors.New("usage: review-to [ADDRESS | --off]")
	case "approvals":
		return runApprovals(a)
	case "decline":
		if len(rest) < 1 || len(rest) > 2 {
			return errors.New("usage: decline ID [REASON]")
		}
		reason := "declined"
		if len(rest) == 2 {
			reason = rest[1]
		}
		r, err := a.Decline(ctx, rest[0], reason)
		if err == nil {
			fmt.Printf("%s %s %s\n", r.ID, r.State, r.Path)
		}
		return err
	case "responder":
		return runResponder(a, rest)
	case "operator":
		return runOperator(ctx, a, rest, os.Stdout)
	case "team":
		return runTeam(ctx, a, rest, os.Stdout, os.Stderr)
	case "room":
		return runRoom(ctx, a, rest, os.Stdout)
	case "group":
		return runGroup(ctx, a, rest, os.Stdout, os.Stderr)
	case "a2a":
		return runA2A(ctx, a, *home, rest)
	case "doctor":
		failed := false
		for _, c := range append(a.Doctor(ctx), client.Check{Name: "hub-role", OK: true, Result: hubRoleDescription(ctx, a)}) {
			if c.Name == "version" {
				c.Result += "; " + currentBuildDescription()
			}
			mark := "ok  "
			if !c.OK {
				mark, failed = "FAIL", true
			}
			fmt.Printf("%s %-10s %s\n", mark, c.Name, c.Result)
		}
		if failed {
			return errors.New("some checks failed")
		}
		return nil
	case "cleanup":
		fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
		saved := fs.Bool("saved", false, "also remove directly received ciphertext of attachments already saved as files")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		r, err := a.Cleanup(*saved)
		if err == nil {
			fmt.Printf("removed %d spooled and %d directly received files\n", r.SpoolFiles, r.DirectFiles)
		}
		return err
	case "reply":
		return runSend(ctx, a, rest, true)
	case "open":
		return runOpen(a, rest)
	case "conversation":
		return runConversation(a, rest)
	case "inbox":
		return runInbox(a, rest)
	case "download":
		return runDownload(ctx, a, rest)
	case "status":
		return runStatus(ctx, a, rest, os.Stdout)
	case "daemon":
		fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
		var opts client.RunOptions
		fs.StringVar(&opts.Listen, "listen", "", "accept direct deliveries on this address (e.g. :7443); off by default")
		fs.StringVar(&opts.Advertise, "advertise", "", "https://host:port peers can reach (default https://LISTEN)")
		uiAddr := fs.String("ui", "", "serve the messenger page on this loopback address (e.g. 127.0.0.1:0); off by default")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		a.Logf = log.Printf
		// The program file as started: an update replaces it, and only an
		// update of this file switches this daemon to the new one.
		if exe, err := os.Executable(); err == nil {
			if exe, err = filepath.EvalSymlinks(exe); err == nil {
				opts.Executable = exe
			}
		}
		opts.CanSwitch, opts.PrepareSwitch = switchHooks(*home, opts.Executable)
		if *uiAddr != "" {
			opts.Owned = func() (func(), error) {
				stop, err := startDaemonUI(a, *home, *uiAddr, log.Printf)
				if err != nil {
					return nil, fmt.Errorf("--ui: %w", err)
				}
				return stop, nil
			}
			opts.OpenConv = func(conv string) []string { return workspacePageCommand(*home, conv, client.DefaultWorkspace) }
		}
		err := a.Run(ctx, opts)
		var rs *client.RestartForUpdate
		if errors.As(err, &rs) {
			a.Close()
			return restartForUpdate(*home, opts.Executable, rs.Request)
		}
		return err
	case "members":
		return runMembers(ctx, a, rest, os.Stdout, os.Stderr)
	case "person":
		return runPerson(ctx, a, rest, os.Stdout)
	case "remind":
		return runRemind(a, rest, os.Stdout, time.Now())
	case "dm":
		return runDM(ctx, a, rest, os.Stdout)
	case "receivers":
		return runReceivers(a, rest, os.Stdout)
	case "sessions":
		if len(rest) != 1 {
			return errors.New("usage: sessions ADDRESS")
		}
		infos, err := a.Sessions(ctx, rest[0])
		if err != nil {
			return err
		}
		for _, in := range infos {
			state := "connected"
			if !in.Connected {
				state = "reconnecting"
			}
			direct := "hub only"
			if in.Ad.Endpoint != "" {
				direct = "direct " + in.Ad.Endpoint
			}
			fmt.Printf("%s#%s  %s  %s\n", in.Ad.Address, in.Ad.Session, state, direct)
		}
		return nil
	case "fingerprint":
		if len(rest) != 1 {
			return errors.New("usage: fingerprint ADDRESS")
		}
		return runFingerprint(ctx, a, rest[0], os.Stdout)
	case "trust":
		if len(rest) != 1 {
			return errors.New("usage: trust ADDRESS")
		}
		fp, err := a.Trust(ctx, rest[0])
		if err != nil {
			return err
		}
		fmt.Printf("trusted %s %s\n", rest[0], fp)
		return nil
	case "admin":
		return runAdmin(ctx, a, rest)
	}
	return fmt.Errorf("unknown command %q (see agentnet --help)", cmd)
}

func defaultHome() string {
	if h := os.Getenv("AGENTNET_HOME"); h != "" {
		return h
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".agentnet"
	}
	return filepath.Join(dir, "agentnet")
}

func runJoin(ctx context.Context, home string, args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	name := fs.String("agent", "", "this agent's name, chosen by its person (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: join --agent NAME CODE")
	}
	if *name == "" {
		// Nothing is created and the Hub is not contacted until a name is given.
		label := "LABEL"
		if inv, err := protocol.DecodeInvite(fs.Arg(0)); err == nil {
			label = inv.Label
		}
		return fmt.Errorf("--agent NAME is required: use the name the person gave for this agent on this computer, or ask them "+
			"(lowercase letters, digits, hyphens, e.g. laptop); the address becomes %s/NAME and cannot be changed later", label)
	}
	join := client.Join
	if _, err := protocol.DecodeLinkOffer(fs.Arg(0)); err == nil {
		join = client.JoinAndLink
	}
	a, err := join(ctx, home, fs.Arg(0), *name)
	if err != nil {
		return err
	}
	defer a.Close()
	if chosen, err := a.ResponderChosen(); err == nil && !chosen {
		fmt.Fprintln(os.Stderr, "next: configure the person's chosen responder on this device (agentnet help responder); if they have not chosen, ask them or offer manual handling")
	}
	if link := a.LinkState(); link.State == client.LinkPending {
		fmt.Printf("%s is waiting for approval on %s\nfingerprint %s\n", a.Address, link.Approver, a.Self().Fingerprint())
		fmt.Fprintln(os.Stderr, "next: start agentnet daemon on this device, then approve the request on your existing device (page, or agentnet person links / person approve ID; person approve --native ID, for a computer like this one, also lets its invites of your own agents there need no accept)")
		return nil
	}
	fmt.Printf("enrolled %s\nfingerprint %s\n", a.Address, a.Self().Fingerprint())
	return nil
}

func runSend(ctx context.Context, a *client.Agent, args []string, reply bool) error {
	name, target := "send", "ADDRESS"
	if reply {
		name, target = "reply", "ID"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var files []string
	fs.Func("file", "attach a file (repeatable)", func(p string) error { files = append(files, p); return nil })
	fallback := fs.Bool("fallback", false, "if ADDRESS#SESSION has ended, deliver to the agent's inbox instead")
	wait := fs.Duration("wait", defaultWait, "wait up to this long for the recipient's receipt (0: return at once)")
	var replyTo *string
	var progress *bool
	if !reply {
		replyTo = fs.String("reply-to", "", "continue the conversation containing this message ID")
		progress = fs.Bool("progress", false, "mark this correlated message as a nonterminal responder update")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		extra := ""
		if !reply {
			extra = " [--reply-to ID] [--progress]"
		}
		return fmt.Errorf("usage: %s [--file PATH]... [--wait 5s]%s %s TEXT", name, extra, target)
	}
	if !reply && *progress && *replyTo == "" {
		return errors.New("--progress requires --reply-to ID")
	}
	if !reply && *replyTo != "" && !*progress { // progress binds its own exact stored request
		if err := a.CheckReplyTo(*replyTo, fs.Arg(0)); err != nil {
			return err
		}
	}
	var r client.SendResult
	var err error
	if reply {
		r, err = a.ReplyWait(ctx, fs.Arg(0), fs.Arg(1), *wait, files...)
	} else if *progress {
		r, err = a.SendProgress(ctx, fs.Arg(0), *replyTo, fs.Arg(1), *wait, *fallback, files...)
	} else {
		r, err = a.SendMessage(ctx, client.Outgoing{To: fs.Arg(0), Body: fs.Arg(1), ReplyTo: *replyTo, Files: files, Fallback: *fallback, Wait: *wait})
	}
	if err != nil {
		return err
	}
	printResult(r, *wait)
	return nil
}

// defaultWait is how long send/ask/task/reply wait for the delivery receipt.
const defaultWait = 5 * time.Second

// printResult prints "ID STATE PATH" on stdout (scripts read the first
// field) and what it means on stderr, for the agent reading it.
func printResult(r client.SendResult, wait time.Duration) {
	fmt.Printf("%s %s %s\n", r.ID, r.State, r.Path)
	switch {
	case r.Detail != "":
		fmt.Fprintf(os.Stderr, "queued for retry by the daemon: %s\n", r.Detail)
	case r.State == protocol.StateDelivered:
		fmt.Fprintln(os.Stderr, "delivered: stored in the recipient's inbox (not necessarily read or answered yet)")
	case r.State == protocol.StateCustody && wait > 0:
		fmt.Fprintf(os.Stderr, "held by the Hub; delivery to the recipient not confirmed yet. Check: agentnet status --wait 30s %s\n", r.ID)
	case r.State == protocol.StateQuarantined:
		fmt.Fprintln(os.Stderr, "the recipient received it but could not verify it (e.g. your key changed for them)")
	}
}

// runStatus shows what is known about a message sent from here: one copy
// by its id, or, by the logical id of a conversation message, each copy
// (one per device), all within one --wait.
func runStatus(ctx context.Context, a *client.Agent, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	wait := fs.Duration("wait", 0, "wait up to this long for a message still held by the Hub to be delivered")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: status [--wait D] ID")
	}
	copies, err := a.SentCopies(fs.Arg(0))
	if err != nil {
		return err
	}
	if len(copies) == 0 || len(copies) == 1 && copies[0].ID == fs.Arg(0) {
		copies = []client.ConvCopy{{ID: fs.Arg(0)}} // a copy's (or a device message's) own id
	}
	deadline := time.Now().Add(*wait)
	for _, c := range copies {
		to := ""
		if len(copies) > 1 || c.ID != fs.Arg(0) {
			to = " to " + c.To
		}
		if c.NotSent { // nothing was sealed for it: no Hub record to ask about
			fmt.Fprintf(stdout, "%s %s%s (local record; %s)\n", c.ID, c.State, to, c.Detail)
			continue
		}
		wait := max(time.Until(deadline), 0)
		if c.Suspended {
			wait = 0 // nobody waits for a device the relay serves nothing until it updates
		}
		r, err := a.Status(ctx, c.ID, wait)
		var local *client.LocalStatus
		if errors.As(err, &local) { // this device's own record, marked as such
			why := "Hub not reachable"
			if local.Cause == nil {
				why = "not at the Hub"
				if local.Detail != "" {
					why += ": " + local.Detail
				}
			}
			fmt.Fprintf(stdout, "%s %s %s%s (local record; %s)\n", r.ID, r.State, r.Path, to, why)
			continue
		}
		if err != nil {
			return err
		}
		note := ""
		if c.Suspended {
			note = " (" + client.SuspendedText(c.To) + ")"
		}
		fmt.Fprintf(stdout, "%s %s %s%s%s\n", r.ID, r.State, r.Path, to, note)
	}
	return nil
}

func runDownload(ctx context.Context, a *client.Agent, args []string) error {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	dir := fs.String("dir", ".", "directory to save into")
	force := fs.Bool("force", false, "replace existing files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: download [--dir DIR] [--force] ID")
	}
	paths, err := a.Download(ctx, fs.Arg(0), *dir, *force)
	for _, p := range paths {
		fmt.Println(p)
	}
	return err
}

// detailLabel names what an inbox item's detail text is.
func detailLabel(state string) string {
	switch state {
	case "summarized":
		return "follow-up summary"
	case "needs_human":
		return "needs your decision"
	}
	return "note"
}

// runOpen is what clicking a review notification runs in a new terminal:
// the chosen coding agent, interactively, with a review prompt; or, without
// one, the item or review list printed here.
func runOpen(a *client.Agent, args []string) error {
	target := ""
	switch {
	case len(args) == 1 && args[0] == "--review":
	case len(args) == 1 && !strings.HasPrefix(args[0], "-"):
		target = args[0]
	default:
		return errors.New("usage: open --review | open ID")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	o, err := a.ReviewOpening(target, self)
	if err != nil {
		return err
	}
	if o.Argv != nil {
		fmt.Printf("Opening %s to review this with you (a new session; nothing has been accepted or run).\n", filepath.Base(o.Argv[0]))
		cmd := exec.Command(o.Argv[0], o.Argv[1:]...)
		cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = o.Dir, os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	fmt.Printf("No coding agent opened: %s. Showing it here.\n\n", o.Why)
	if target != "" {
		err = runConversation(a, []string{target})
	} else {
		err = runInbox(a, []string{"--review"})
	}
	if st, serr := os.Stdin.Stat(); serr == nil && st.Mode()&os.ModeCharDevice != 0 {
		fmt.Print("\nPress Enter to close.")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return err
}

func runConversation(a *client.Agent, args []string) error {
	fs := flag.NewFlagSet("conversation", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	offset := fs.Int("offset", 0, "skip this many messages from the start")
	limit := fs.Int("limit", 50, "show at most this many messages (0: all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: conversation [--json] [--offset N] [--limit N] ID")
	}
	c, err := a.Conversation(fs.Arg(0), *offset, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		if c.Messages == nil {
			c.Messages = []client.ConversationMessage{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(c)
	}
	first, last := c.Offset+1, c.Offset+len(c.Messages)
	if len(c.Messages) == 0 {
		first = c.Offset
	}
	fmt.Printf("conversation with %s: messages %d-%d of %d\n", c.Peer, first, last, c.Total)
	for _, m := range c.Messages {
		arrow := "<"
		if m.Dir == "out" {
			arrow = ">"
		}
		kind := m.Kind
		if m.Status != "" {
			kind += " (" + m.Status + ")"
		}
		if m.State != "" {
			kind += " [" + m.State + "]"
		}
		fmt.Printf("%s %s  %s  %s  %s\n", arrow, m.ID, m.From, m.At.Format(time.DateTime), termText(kind, "  "))
		fmt.Printf("  %s\n", termText(shownText(m.Body, m.Controls), "  "))
		if m.Summary != "" {
			fmt.Printf("  [follow-up summary] %s\n", termText(m.Summary, "  "))
		}
		if m.Detail != "" {
			fmt.Printf("  [note] %s\n", termText(m.Detail, "  "))
		}
		for _, f := range m.Attachments {
			if m.Deleted {
				break // its files went with it
			}
			fmt.Printf("  [file] %q %d bytes sha256 %s\n", f.Name, f.Size, termText(f.SHA256, "")) // the sender's; only its length is checked
		}
	}
	if last < c.Total {
		fmt.Printf("(%d more: agentnet conversation --offset %d %s)\n", c.Total-last, last, fs.Arg(0))
	}
	return nil
}

// runAdminRelease shows, sets or clears the client version the Hub
// recommends to its members.
func runAdminRelease(ctx context.Context, a *client.Agent, args []string) error {
	usage := errors.New("usage: admin release show | admin release clear | admin release set --url URL [--note TEXT] VERSION")
	if len(args) == 0 {
		return usage
	}
	var r protocol.Release
	switch args[0] {
	case "show":
		if len(args) != 1 {
			return usage
		}
		var err error
		if r, err = a.HubRelease(ctx); err != nil {
			return err
		}
	case "clear":
		if len(args) != 1 {
			return usage
		}
		if _, err := a.SetRelease(ctx, protocol.Release{}); err != nil {
			return err
		}
	case "set":
		fs := flag.NewFlagSet("admin release set", flag.ContinueOnError)
		url := fs.String("url", "", "https page with update instructions (required)")
		note := fs.String("note", "", "short note for people (not shown to models)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 || *url == "" {
			return usage
		}
		var err error
		if r, err = a.SetRelease(ctx, protocol.Release{Version: fs.Arg(0), URL: *url, Note: *note}); err != nil {
			return err
		}
	default:
		return usage
	}
	if r.Version == "" {
		fmt.Println("no client version recommended")
		return nil
	}
	fmt.Printf("recommended client version %s\nurl %s\n", r.Version, r.URL)
	if r.Note != "" {
		fmt.Printf("note %s\n", r.Note)
	}
	return nil
}

// runAdminWorkspace shows, sets or clears the workspace name every member
// sees ("Mellanni"); setting and clearing are admin only.
func runAdminWorkspace(ctx context.Context, a *client.Agent, args []string) error {
	usage := errors.New("usage: admin workspace [show] | admin workspace set NAME | admin workspace clear")
	var name string
	var err error
	switch {
	case len(args) == 0 || len(args) == 1 && args[0] == "show":
		name, err = a.HubWorkspace(ctx)
	case len(args) == 1 && args[0] == "clear":
		name, err = a.SetWorkspaceName(ctx, "")
	case len(args) == 2 && args[0] == "set":
		if strings.TrimSpace(args[1]) == "" {
			return usage
		}
		name, err = a.SetWorkspaceName(ctx, args[1])
	default:
		return usage
	}
	if err != nil {
		return err
	}
	if name == "" {
		fmt.Println("no workspace name set (members see the relay's host name)")
		return nil
	}
	fmt.Printf("workspace name %s\n", name)
	return nil
}

func runAdmin(ctx context.Context, a *client.Agent, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: admin invite|revoke|release|workspace ...")
	}
	switch args[0] {
	case "google":
		return runGoogleAdmin(ctx, a, args[1:])
	case "invite":
		fs := flag.NewFlagSet("admin invite", flag.ContinueOnError)
		ttl := fs.Duration("ttl", 7*24*time.Hour, "invite lifetime (max 720h)")
		admin := fs.Bool("admin", false, "grant admin rights")
		raw := fs.Bool("raw", false, "print only the invite code (for scripts)")
		link := fs.Bool("link", false, "print the invitation link a person opens to get the AgentNet app and join (Hub needs --web and browser-trusted HTTPS)")
		name := fs.String("name", "", "with --link: the invited person's name, written on the invitation (LABEL is then made from it unless given)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *raw && *link {
			return errors.New("choose either --raw or --link")
		}
		if *name != "" && !*link {
			return errors.New("--name is for --link invitations")
		}
		if fs.NArg() > 1 || (fs.NArg() == 0 && (*name == "" || !*link)) {
			return errors.New("usage: admin invite [--ttl D] [--admin] [--raw] LABEL   or   admin invite [--ttl D] [--admin] --link (--name NAME [LABEL] | LABEL)\n" +
				"LABEL is the invited person's AgentNet name (e.g. bob). Use the name your person gave for this invitation; if they have not, ask them who is being invited and what name to use. " +
				"Do not infer it or reuse your own label, \"admin\", a user, host or model name unless your person chose it. It grants no rights; --admin does")
		}
		if *link {
			// The one invitation kind people get: refused before anything is
			// created unless a browser and the app can use the link.
			inv, err := a.CreateInvite(ctx, client.InviteOptions{Label: fs.Arg(0), Name: *name, TTL: *ttl, Admin: *admin, Workspace: a.WorkspaceName()})
			if err != nil {
				return err
			}
			address, err := inviteLink(inv.Code)
			if err != nil {
				return fmt.Errorf("invite created, but no browser link printed: %w", err)
			}
			fmt.Println(address)
			return nil
		}
		code, err := a.Invite(ctx, fs.Arg(0), *ttl, *admin)
		if err != nil {
			return err
		}
		if *raw {
			fmt.Println(code)
			return nil
		}
		packet, err := invitePacket(code, a.Address)
		if err != nil {
			return err
		}
		fmt.Print(packet)
		return nil
	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: admin revoke ADDRESS")
		}
		if err := a.Revoke(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("revoked %s\n", args[1])
		return nil
	case "release":
		return runAdminRelease(ctx, a, args[1:])
	case "workspace":
		return runAdminWorkspace(ctx, a, args[1:])
	}
	return fmt.Errorf("unknown admin command %q", args[0])
}

func runSendKind(ctx context.Context, a *client.Agent, kind string, args []string) error {
	fs := flag.NewFlagSet(kind, flag.ContinueOnError)
	var files []string
	fs.Func("file", "attach a file (repeatable)", func(p string) error { files = append(files, p); return nil })
	wait := fs.Duration("wait", defaultWait, "wait up to this long for the recipient's receipt (0: return at once)")
	followUp := fs.String("follow-up", "", "when the reply arrives, have your responder process it once with these instructions and keep a summary for you (nothing is sent back)")
	returnSelection := receiverFlags(fs)
	remoteAgent := fs.String("agent", "", "exact named remote executor AgentID, independent of reply receiver")
	replyTo := fs.String("reply-to", "", "continue a conversation: the id of a message you sent to or received from ADDRESS")
	answerDefault := client.AskAnswerWait // asked here, answered here (MEL-537)
	if kind == "task" {
		answerDefault = 0
	}
	answerFor := answerWaitFlag(fs, answerDefault)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: %s [--file PATH]... [--wait 5s] [--answer-wait D] [--follow-up TEXT] [--reply-to ID] ADDRESS TEXT", kind)
	}
	beyond := ""
	if *remoteAgent != "" {
		beyond = "with --agent"
	} else if *followUp != "" {
		beyond = "with --follow-up"
	}
	if err := receiverGuard(a, kind, beyond, *replyTo); err != nil {
		return err
	}
	if *replyTo != "" {
		if err := a.CheckReplyTo(*replyTo, fs.Arg(0)); err != nil {
			return err
		}
	}
	msgKind := envelope.KindQuestion
	if kind == "task" {
		msgKind = envelope.KindTask
	}
	receiver, err := returnSelection.selected(a, true)
	if err != nil {
		return err
	}
	var target *envelope.Target
	if *remoteAgent != "" {
		host, _, e := protocol.SplitTarget(fs.Arg(0))
		if e != nil {
			return e
		}
		catalog, e := a.AgentCatalog(ctx, host)
		if e != nil {
			return e
		}
		for _, record := range catalog {
			if record.ID == *remoteAgent {
				target = &envelope.Target{Address: host, Fingerprint: record.HostKey, AgentID: record.ID}
			}
		}
		if target == nil {
			return client.ErrUnknownAgent
		}
	}
	r, err := a.SendMessage(ctx, client.Outgoing{To: fs.Arg(0), Body: fs.Arg(1), Files: files, Kind: msgKind, Wait: *wait, FollowUp: *followUp, ReplyTo: *replyTo, ReplyReceiver: receiver, Target: target})
	if err != nil {
		return err
	}
	printResult(r, *wait)
	return awaitAnswer(ctx, a, r.ID, "agentnet conversation "+r.ID, answerWait(answerFor), receiver, os.Stdout, os.Stderr)
}

func runResponder(a *client.Agent, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: responder list|set|show|off")
	}
	switch args[0] {
	case "list":
		printHarnesses()
		return nil
	case "off":
		if err := a.SetResponder(nil); err != nil {
			return err
		}
		fmt.Println("manual only: no automatic responder; questions and tasks wait for you")
		return nil
	case "show":
		r, err := a.Responder()
		if err != nil {
			return err
		}
		if r == nil {
			if chosen, err := a.ResponderChosen(); err != nil {
				return err
			} else if chosen {
				fmt.Println("manual only (chosen): questions and tasks wait for you")
			} else {
				fmt.Println("not chosen yet: ask the person, starting from agentnet responder list")
			}
			return nil
		}
		limit := "none"
		if r.Timeout > 0 {
			limit = r.Timeout.String()
		}
		fmt.Printf("harness %s\ndir %s\ntimeout %s\n", r.Harness, r.Dir, limit)
		for _, c := range r.Context {
			fmt.Printf("context %s\n", c)
		}
		if l := client.HarnessLimits(r.Harness); l != "" {
			fmt.Printf("note %s\n", l)
		}
		if n, err := a.Approvals(); err != nil {
			return err
		} else if n == 0 {
			fmt.Printf("note %s\n", client.NoApprovals)
		} else {
			fmt.Printf("approved %d agent(s)\n", n)
		}
		return nil
	case "set":
		fs := flag.NewFlagSet("responder set", flag.ContinueOnError)
		var r client.Responder
		fs.StringVar(&r.Harness, "harness", "", "responder: "+strings.Join(client.HarnessNames(), ", "))
		fs.StringVar(&r.Dir, "dir", "", "working directory (its agent instructions apply)")
		fs.DurationVar(&r.Timeout, "timeout", 0, "your own limit per question or task (0: none; AgentNet sets none)")
		fs.Func("context", "file given with every question (repeatable)", func(p string) error { r.Context = append(r.Context, p); return nil })
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if r.Harness == "" || r.Dir == "" {
			return errors.New("usage: responder set --harness NAME --dir DIR [--context FILE]... [--timeout D]")
		}
		if err := a.SetResponder(&r); err != nil {
			return err
		}
		fmt.Printf("responder %s in %s\n", r.Harness, r.Dir)
		if l := client.HarnessLimits(r.Harness); l != "" {
			fmt.Printf("note: %s\n", l)
		}
		if n, err := a.Approvals(); err == nil && n == 0 {
			fmt.Printf("note: %s\n", client.NoApprovals)
		}
		return nil
	}
	return fmt.Errorf("unknown responder command %q", args[0])
}

// runTaskGrant handles accept --always ID, approve --tasks ADDRESS and
// unapprove --tasks ADDRESS.
func runTaskGrant(a *client.Agent, cmd, arg string) error {
	const perms = "they run with this computer's normal task permissions for your responder"
	switch cmd {
	case "accept":
		sender, fp, err := a.AcceptAlways(arg)
		if err != nil {
			return err
		}
		fmt.Printf("accept %s\n", arg)
		if protocol.ValidID(sender) {
			fmt.Printf("tasks from %q's current and future verified devices now run without asking; %s. Stop: agentnet unapprove --tasks %s\n", a.PermissionLabel(sender), perms, sender)
		} else {
			fmt.Printf("tasks from %s (key %s) now run without asking; %s. Stop: agentnet unapprove --tasks %s\n", sender, fp, perms, sender)
		}
	case "approve":
		fp, err := a.GrantTasks(arg)
		if err != nil {
			return err
		}
		if protocol.ValidID(fp) {
			fmt.Printf("tasks from %q's current and future verified devices now run without asking; %s. Tasks already waiting still need accept ID. Stop: agentnet unapprove --tasks %s\n", a.PermissionLabel(fp), perms, fp)
		} else {
			fmt.Printf("tasks from %s (key %s) now run without asking; %s. Tasks already waiting still need accept ID. Stop: agentnet unapprove --tasks %s\n", arg, fp, perms, arg)
		}
	case "unapprove":
		running, err := a.RevokeTasks(arg)
		if err != nil {
			return err
		}
		fmt.Printf("tasks from %s wait for you again\n", arg)
		for _, id := range running {
			fmt.Printf("still running: %s (it may finish; stop it with agentnet cancel %s)\n", id, id)
		}
	}
	return nil
}

// runFingerprint shows the key trusted here for address and the directory's.
func runFingerprint(ctx context.Context, a *client.Agent, address string, stdout io.Writer) error {
	pinned, current, err := a.Fingerprints(ctx, address)
	if pinned == "" {
		pinned = "(not yet trusted)"
	}
	fmt.Fprintf(stdout, "trusted   %s\ndirectory %s\n", pinned, current)
	if err != nil {
		return err
	}
	if revoked, _ := a.Revoked(ctx, address); revoked {
		fmt.Fprintln(stdout, "status    revoked by a Hub admin: nothing from or to it counts, and trust refuses it")
	}
	return nil
}

func runApprovals(a *client.Agent) error {
	qs, err := a.QuestionApprovals()
	if err != nil {
		return err
	}
	ts, err := a.TaskGrants()
	if err != nil {
		return err
	}
	for _, q := range qs {
		fmt.Printf("questions  %s\n", q)
	}
	for _, t := range ts {
		fmt.Printf("tasks      %s  key %s  %s\n", t.Address, t.Fingerprint, t.Status)
	}
	pgs, err := a.PersonGrants()
	if err != nil {
		return err
	}
	for _, g := range pgs {
		if g.Questions {
			fmt.Printf("questions  person %s %q (%s)\n", g.Person, g.Label, g.State)
		}
		if g.Tasks {
			fmt.Printf("tasks      person %s %q (%s; current verified devices)\n", g.Person, g.Label, g.State)
		}
	}
	// An agent of this device accepted into a conversation runs, without
	// asking, the tasks of the member keys its invitation named: accepting
	// it was that grant, which stands until it is dismissed.
	convs, err := a.Conversations()
	if err != nil {
		return err
	}
	granted := 0
	for _, c := range convs {
		parts, err := a.Participations(c.ID)
		if err != nil {
			return err
		}
		for _, p := range parts {
			if !p.HostHere || !p.Claimable() || len(p.TaskKeys) == 0 {
				continue
			}
			granted++
			fmt.Printf("tasks      from %s  in conversation %s through your agent %s (accepting it granted this; %s)\n",
				strings.Join(taskGrantees(a, p), ", "), c.ID, p.PID, grantEnd(p))
		}
	}
	if len(qs) == 0 && len(ts) == 0 && len(pgs) == 0 && granted == 0 {
		fmt.Println("none: every question and task waits for you")
	}
	return nil
}

// printHarnesses lists the choices for the default responder. It only
// looks executables up on PATH; it runs nothing.
func printHarnesses() {
	for _, h := range client.ListHarnesses() {
		where := "not found on PATH"
		if h.Path != "" {
			where = "found at " + h.Path
		}
		tested := h.Tested
		if tested == "" {
			tested = "not tested live"
		}
		mode := "questions: see agentnet help responder"
		if h.Limits != "" {
			mode = "questions use your native tools and permissions unchanged (see agentnet help responder)"
		}
		fmt.Printf("%-7s %s; %s; %s\n", h.Name, where, mode, tested)
	}
	fmt.Println("manual  no automatic responder: questions and tasks wait for you (agentnet responder off)")
	fmt.Println()
	fmt.Println("Found only means the program is on PATH; it was not run, so login and setup are unchecked.")
	fmt.Println("Other coding agents can read and reply by hand; Antigravity CLI is available as the agy responder.")
	fmt.Println("Choose with the person: agentnet responder set --harness NAME --dir DIR, or agentnet responder off.")
}

func runA2A(ctx context.Context, a *client.Agent, home string, args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: a2a serve --peer PERSON/AGENT [--listen 127.0.0.1:0]")
	}
	fs := flag.NewFlagSet("a2a serve", flag.ContinueOnError)
	peer := fs.String("peer", "", "the enrolled agent this adapter talks to")
	listen := fs.String("listen", "127.0.0.1:0", "loopback address to serve on")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if _, _, err := protocol.SplitAddress(*peer); err != nil {
		return fmt.Errorf("--peer: %w", err)
	}
	host, _, err := net.SplitHostPort(*listen)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return errors.New("--listen must be a loopback address such as 127.0.0.1:0")
	}
	tokenPath := filepath.Join(home, "a2a-token")
	token, err := a2aToken(tokenPath)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	base := "http://" + ln.Addr().String()
	srv := &http.Server{Handler: a2abind.New(a, *peer, base, token).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	fmt.Printf("A2A adapter for %s at %s (bearer token in %s)\n", *peer, base, tokenPath)
	log.Printf("replies arrive through `agentnet daemon`; keep it running")
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// a2aToken returns the owner-only local A2A bearer token, creating it once.
func a2aToken(path string) (string, error) {
	if data, err := secfile.Read(path); err == nil {
		return strings.TrimSpace(string(data)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	token := protocol.NewID() + protocol.NewID()
	return token, secfile.Write(path, []byte(token+"\n"))
}
