// Command agentnet is the AgentNet client CLI and Hub server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/hub"
)

const usage = `usage: agentnet [--home DIR] <command> [flags] [args]

Client:
  join [--agent NAME] CODE      enroll this agent with an invite code
  whoami                        show this agent's address and key fingerprint
  send [--file PATH]... [--fallback] ADDRESS[#SESSION] TEXT
                                send an end-to-end encrypted message with files,
                                directly when the recipient is reachable
  ask [--file PATH]... ADDRESS TEXT
                                send a question (approved peers may get an automatic answer)
  task [--file PATH]... ADDRESS TEXT
                                send a task (runs only if the recipient accepts it)
  reply [--file PATH]... ID TEXT
                                reply to an inbox message (takes over a question or task)
  accept ID                     let the responder run a task, answer a held question,
                                or retry an interrupted/failed one
  decline ID [REASON]           decline a task or question
  cancel ID                     stop the responder working on ID
  approve ADDRESS / unapprove ADDRESS
                                allow / stop automatic answers to ADDRESS's questions
  responder set --harness NAME --dir DIR [--context FILE]... [--timeout 5m]
  responder show | responder off
  inbox [--unread] [--json]     list received messages (marks them read)
  download [--dir DIR] [--force] ID
                                save a message's attachments (never overwrites
                                unless --force)
  status ID                     show what the Hub can prove about a sent message
  daemon [--listen ADDR] [--advertise URL]
                                stay connected (one session) and receive messages;
                                --listen also accepts direct deliveries
  sessions ADDRESS              list an agent's live sessions
  fingerprint ADDRESS           compare trusted and directory keys for ADDRESS
  trust ADDRESS                 trust ADDRESS's current keys after verifying them

Admin:
  admin invite [--ttl 168h] [--admin] LABEL
  admin revoke ADDRESS

Hub:
  hub serve --data DIR [--listen ADDR] [--public-url URL] [--admin-label LABEL]

ADDRESS is person/agent, e.g. alice/laptop. Home defaults to $AGENTNET_HOME or
the user config directory.
`

func main() {
	log.SetFlags(log.LstdFlags)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agentnet:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	global := flag.NewFlagSet("agentnet", flag.ContinueOnError)
	global.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	home := global.String("home", defaultHome(), "agent home directory")
	if err := global.Parse(args); err != nil {
		return err
	}
	args = global.Args()
	if len(args) == 0 {
		global.Usage()
		return errors.New("missing command")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "hub":
		return runHub(ctx, rest)
	case "join":
		return runJoin(ctx, *home, rest)
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
	case "accept", "cancel", "approve", "unapprove":
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
	case "reply":
		return runSend(ctx, a, rest, true)
	case "inbox":
		return runInbox(a, rest)
	case "download":
		return runDownload(ctx, a, rest)
	case "status":
		if len(rest) != 1 {
			return errors.New("usage: status ID")
		}
		r, err := a.Status(ctx, rest[0])
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
	global.Usage()
	return fmt.Errorf("unknown command %q", cmd)
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

func runHub(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: hub serve --data DIR [--listen ADDR] [--public-url URL]")
	}
	fs := flag.NewFlagSet("hub serve", flag.ContinueOnError)
	data := fs.String("data", "", "data directory (required)")
	listen := fs.String("listen", "127.0.0.1:8443", "listen address")
	public := fs.String("public-url", "", "URL clients use (default https://LISTEN)")
	adminLabel := fs.String("admin-label", "admin", "person label for the bootstrap admin invite")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *data == "" {
		return errors.New("--data is required")
	}
	if *public == "" {
		*public = "https://" + *listen
	}
	h, err := hub.Open(hub.Config{DataDir: *data, PublicURL: *public, AdminLabel: *adminLabel})
	if err != nil {
		return err
	}
	defer h.Close()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	log.Printf("hub listening on %s (public %s)", ln.Addr(), *public)
	return h.Serve(ctx, ln)
}

func runJoin(ctx context.Context, home string, args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	name := fs.String("agent", "", "agent name, e.g. laptop (default: hostname)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: join [--agent NAME] CODE")
	}
	if *name == "" {
		host, _ := os.Hostname()
		*name = sanitizeName(host)
	}
	a, err := client.Join(ctx, home, fs.Arg(0), *name)
	if err != nil {
		return err
	}
	defer a.Close()
	fmt.Printf("enrolled %s\nfingerprint %s\n", a.Address, a.Self().Fingerprint())
	return nil
}

// sanitizeName turns a hostname into a valid agent name.
func sanitizeName(s string) string {
	s = strings.ToLower(strings.SplitN(s, ".", 2)[0])
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9' && b.Len() > 0, r == '-' && b.Len() > 0:
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "agent"
	}
	return b.String()[:min(b.Len(), 32)]
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: %s [--file PATH]... %s TEXT", name, target)
	}
	var r client.SendResult
	var err error
	if reply {
		r, err = a.Reply(ctx, fs.Arg(0), fs.Arg(1), files...)
	} else {
		r, err = a.SendMessage(ctx, client.Outgoing{To: fs.Arg(0), Body: fs.Arg(1), Files: files, Fallback: *fallback})
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s\n", r.ID, r.State, r.Path)
	if r.Detail != "" {
		fmt.Fprintf(os.Stderr, "queued for retry by the daemon: %s\n", r.Detail)
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

func runInbox(a *client.Agent, args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	unread := fs.Bool("unread", false, "only unread messages")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	msgs, err := a.Inbox(*unread, true)
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

func runAdmin(ctx context.Context, a *client.Agent, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: admin invite|revoke ...")
	}
	switch args[0] {
	case "invite":
		fs := flag.NewFlagSet("admin invite", flag.ContinueOnError)
		ttl := fs.Duration("ttl", 7*24*time.Hour, "invite lifetime (max 720h)")
		admin := fs.Bool("admin", false, "grant admin rights")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: admin invite [--ttl D] [--admin] LABEL")
		}
		code, err := a.Invite(ctx, fs.Arg(0), *ttl, *admin)
		if err != nil {
			return err
		}
		fmt.Println(code)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: %s [--file PATH]... ADDRESS TEXT", kind)
	}
	r, err := a.SendMessage(ctx, client.Outgoing{To: fs.Arg(0), Body: fs.Arg(1), Files: files, Kind: kind})
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s\n", r.ID, r.State, r.Path)
	return nil
}

func runResponder(a *client.Agent, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: responder set|show|off")
	}
	switch args[0] {
	case "off":
		if err := a.SetResponder(nil); err != nil {
			return err
		}
		fmt.Println("automatic responder off; questions and tasks wait for you")
		return nil
	case "show":
		r, err := a.Responder()
		if err != nil {
			return err
		}
		if r == nil {
			fmt.Println("no responder selected")
			return nil
		}
		fmt.Printf("harness %s\ndir %s\ntimeout %s\n", r.Harness, r.Dir, r.Timeout)
		for _, c := range r.Context {
			fmt.Printf("context %s\n", c)
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
		return nil
	}
	return fmt.Errorf("unknown responder command %q", args[0])
}
