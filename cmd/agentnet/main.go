// Command agentnet is the AgentNet client CLI and Hub server.
package main

import (
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
	switch cmd {
	case "hub":
		return runHub(ctx, rest)
	case "version":
		fmt.Printf("agentnet %s (protocol %d)\n", protocol.Version, protocol.ProtocolVersion)
		return nil
	case "join":
		return runJoin(ctx, *home, rest)
	}
	if _, known := topics[cmd]; !known {
		return fmt.Errorf("unknown command %q (see agentnet --help)", cmd)
	}
	a, err := client.Open(*home)
	if err != nil {
		return err
	}
	defer a.Close()
	switch cmd {
	case "whoami":
		fmt.Printf("%s\nfingerprint %s\n", a.Address, a.Self().Fingerprint())
		return nil
	case "send":
		return runSend(ctx, a, rest, false)
	case "ask", "task":
		return runSendKind(ctx, a, cmd, rest)
	case "accept", "cancel", "approve", "unapprove", "resolve":
		if len(rest) != 1 {
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
	case "a2a":
		return runA2A(ctx, a, *home, rest)
	case "doctor":
		failed := false
		for _, c := range a.Doctor(ctx) {
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
	case "inbox":
		return runInbox(a, rest)
	case "download":
		return runDownload(ctx, a, rest)
	case "status":
		fs := flag.NewFlagSet("status", flag.ContinueOnError)
		wait := fs.Duration("wait", 0, "wait up to this long for a message still held by the Hub to be delivered")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: status [--wait D] ID")
		}
		r, err := a.Status(ctx, fs.Arg(0), *wait)
		if err != nil {
			return err
		}
		fmt.Printf("%s %s %s\n", r.ID, r.State, r.Path)
		return nil
	case "daemon":
		fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
		var opts client.RunOptions
		fs.StringVar(&opts.Listen, "listen", "", "accept direct deliveries on this address (e.g. :7443); off by default")
		fs.StringVar(&opts.Advertise, "advertise", "", "https://host:port peers can reach (default https://LISTEN)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		a.Logf = log.Printf
		return a.Run(ctx, opts)
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
		pinned, current, err := a.Fingerprints(ctx, rest[0])
		if pinned == "" {
			pinned = "(not yet trusted)"
		}
		fmt.Printf("trusted   %s\ndirectory %s\n", pinned, current)
		return err
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
	a, err := client.Join(ctx, home, fs.Arg(0), *name)
	if err != nil {
		return err
	}
	defer a.Close()
	fmt.Printf("enrolled %s\nfingerprint %s\n", a.Address, a.Self().Fingerprint())
	if chosen, err := a.ResponderChosen(); err == nil && !chosen {
		fmt.Fprintln(os.Stderr, "next: ask the person how questions and tasks should be handled (agentnet responder list)")
	}
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: %s [--file PATH]... [--wait 5s] %s TEXT", name, target)
	}
	var r client.SendResult
	var err error
	if reply {
		r, err = a.ReplyWait(ctx, fs.Arg(0), fs.Arg(1), *wait, files...)
	} else {
		r, err = a.SendMessage(ctx, client.Outgoing{To: fs.Arg(0), Body: fs.Arg(1), Files: files, Fallback: *fallback, Wait: *wait})
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

func runInbox(a *client.Agent, args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	unread := fs.Bool("unread", false, "only unread messages")
	review := fs.Bool("review", false, "only items waiting for your decision (does not mark them read)")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var msgs []client.Message
	var err error
	if *review {
		msgs, err = a.Review()
	} else {
		msgs, err = a.Inbox(*unread, true)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if msgs == nil {
			msgs = []client.Message{}
		}
		return enc.Encode(msgs)
	}
	for _, m := range msgs {
		mark := " "
		if !m.Read {
			mark = "*"
		}
		kind := m.Kind
		if m.State != "" {
			kind += " [" + m.State + "]"
		}
		if m.Status != "" {
			kind += " (" + m.Status + ")"
		}
		fmt.Printf("%s %s  %s  %s  %s\n", mark, m.ID, m.From, m.SentAt.Format(time.DateTime), kind)
		if m.ReplyTo != "" {
			fmt.Printf("  (reply to %s)\n", m.ReplyTo)
		}
		fmt.Printf("  %s\n", strings.ReplaceAll(m.Body, "\n", "\n  "))
		if m.Detail != "" {
			fmt.Printf("  [%s] %s\n", detailLabel(m.State), strings.ReplaceAll(m.Detail, "\n", "\n  "))
		}
		for _, f := range m.Attachments {
			fmt.Printf("  [file] %q %d bytes", f.Name, f.Size)
			if f.SavedPath != "" {
				fmt.Printf(" saved %s", f.SavedPath)
			}
			fmt.Println()
		}
	}
	return nil
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

func runAdmin(ctx context.Context, a *client.Agent, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: admin invite|revoke ...")
	}
	switch args[0] {
	case "invite":
		fs := flag.NewFlagSet("admin invite", flag.ContinueOnError)
		ttl := fs.Duration("ttl", 7*24*time.Hour, "invite lifetime (max 720h)")
		admin := fs.Bool("admin", false, "grant admin rights")
		raw := fs.Bool("raw", false, "print only the invite code (for scripts)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: admin invite [--ttl D] [--admin] [--raw] LABEL\n" +
				"LABEL is the invited person's AgentNet name (e.g. bob). Use the name your person gave for this invitation; if they have not, ask them who is being invited and what name to use. " +
				"Do not infer it or reuse your own label, \"admin\", a user, host or model name unless your person chose it. It grants no rights; --admin does")
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
	}
	return fmt.Errorf("unknown admin command %q", args[0])
}

func runSendKind(ctx context.Context, a *client.Agent, kind string, args []string) error {
	fs := flag.NewFlagSet(kind, flag.ContinueOnError)
	var files []string
	fs.Func("file", "attach a file (repeatable)", func(p string) error { files = append(files, p); return nil })
	wait := fs.Duration("wait", defaultWait, "wait up to this long for the recipient's receipt (0: return at once)")
	followUp := fs.String("follow-up", "", "when the reply arrives, have your responder process it once with these instructions and keep a summary for you (nothing is sent back)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: %s [--file PATH]... [--wait 5s] [--follow-up TEXT] ADDRESS TEXT", kind)
	}
	msgKind := envelope.KindQuestion
	if kind == "task" {
		msgKind = envelope.KindTask
	}
	r, err := a.SendMessage(ctx, client.Outgoing{To: fs.Arg(0), Body: fs.Arg(1), Files: files, Kind: msgKind, Wait: *wait, FollowUp: *followUp})
	if err != nil {
		return err
	}
	printResult(r, *wait)
	return nil
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
		fmt.Printf("harness %s\ndir %s\ntimeout %s\n", r.Harness, r.Dir, r.Timeout)
		for _, c := range r.Context {
			fmt.Printf("context %s\n", c)
		}
		if l := client.HarnessLimits(r.Harness); l != "" {
			fmt.Printf("note %s\n", l)
		}
		return nil
	case "set":
		fs := flag.NewFlagSet("responder set", flag.ContinueOnError)
		var r client.Responder
		fs.StringVar(&r.Harness, "harness", "", "responder: "+strings.Join(client.HarnessNames(), ", "))
		fs.StringVar(&r.Dir, "dir", "", "working directory (its agent instructions apply)")
		fs.DurationVar(&r.Timeout, "timeout", 5*time.Minute, "limit per question or task")
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
		return nil
	}
	return fmt.Errorf("unknown responder command %q", args[0])
}

// printHarnesses lists the choices for the default responder. It only
// looks executables up on PATH; it runs nothing.
func printHarnesses() {
	for _, h := range client.ListHarnesses() {
		where := "not found on PATH"
		if h.Path != "" {
			where = "found at " + h.Path
		}
		tested := "not tested live"
		if h.Tested {
			tested = "tested live"
		}
		mode := "questions with no tools"
		if h.Limits != "" {
			mode = "questions restricted, not tool-free (see agentnet help responder)"
		}
		fmt.Printf("%-7s %s; %s; %s\n", h.Name, where, mode, tested)
	}
	fmt.Println("manual  no automatic responder: questions and tasks wait for you (agentnet responder off)")
	fmt.Println()
	fmt.Println("Found only means the program is on PATH; it was not run, so login and setup are unchecked.")
	fmt.Println("Other coding agents (e.g. Antigravity) can read and reply by hand but cannot be the responder.")
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
