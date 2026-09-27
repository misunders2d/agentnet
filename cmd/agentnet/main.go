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
  send ADDRESS TEXT             send an end-to-end encrypted message
  reply ID TEXT                 reply to an inbox message
  inbox [--unread] [--json]     list received messages (marks them read)
  status ID                     show what the Hub can prove about a sent message
  daemon                        stay connected and receive messages as they arrive
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
	case "status":
		if len(rest) != 1 {
			return errors.New("usage: status ID")
		}
		r, err := a.Status(ctx, rest[0])
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", r.ID, r.State)
		return nil
	case "daemon":
		a.Logf = log.Printf
		return a.Run(ctx)
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
	if len(args) != 2 {
		if reply {
			return errors.New("usage: reply ID TEXT")
		}
		return errors.New("usage: send ADDRESS TEXT")
	}
	var r client.SendResult
	var err error
	if reply {
		r, err = a.Reply(ctx, args[0], args[1])
	} else {
		r, err = a.Send(ctx, args[0], args[1], "")
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", r.ID, r.State)
	if r.Detail != "" {
		fmt.Fprintf(os.Stderr, "queued for retry by the daemon: %s\n", r.Detail)
	}
	return nil
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
