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
  reply [--file PATH]... ID TEXT
                                reply to an inbox message
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
		fmt.Printf("%s %s  %s  %s\n", mark, m.ID, m.From, m.SentAt.Format(time.DateTime))
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
