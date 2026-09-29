package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/ui"
)

// uiURLFile holds the running daemon's messenger page address, including
// its token (good for that daemon's lifetime), owner-only in the home. It is
// never logged.
const uiURLFile = "ui-url"

// convPageCommand returns the command that opens this daemon's messenger
// page on a conversation ("" : the page itself), for a click on a DM alert:
// the page's address without its token (a command line can be read by
// other local users), so it opens where the browser still holds the page's
// session and otherwise asks for `agentnet ui`. Linux only (xdg-open), as
// only the Linux notifier takes clicks; nil elsewhere or with no page.
func convPageCommand(home, conv string) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	data, err := secfile.Read(filepath.Join(home, uiURLFile))
	if err != nil {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(string(data)))
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil
	}
	page := url.URL{Scheme: "http", Host: u.Host, Path: "/"}
	if protocol.ValidHash(conv) {
		page.Fragment = "conv=" + conv
	}
	return []string{"xdg-open", page.String()}
}

// runUI prints the running daemon's messenger page address, or serves the
// invented demo data with --demo (no home, Hub, network or harness).
func runUI(ctx context.Context, home string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	demo := fs.Bool("demo", false, "serve invented demo data")
	listen := fs.String("listen", "127.0.0.1:0", "loopback address to serve the demo on")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%v (see agentnet help ui)", err)
	}
	if fs.NArg() > 0 {
		return errors.New("usage: agentnet ui   or   agentnet ui --demo [--listen 127.0.0.1:0]")
	}
	if !*demo {
		if *listen != "127.0.0.1:0" {
			return errors.New("--listen is for --demo; the daemon's page address is chosen with agentnet daemon --ui")
		}
		noPage := errors.New("no messenger page is running for this home: start the daemon with `agentnet daemon --ui 127.0.0.1:0` (see agentnet help ui)")
		data, err := secfile.Read(filepath.Join(home, uiURLFile))
		if errors.Is(err, os.ErrNotExist) {
			return noPage
		}
		if err != nil {
			return err
		}
		// A daemon that was killed (a crash, a power cut, a stop on Windows)
		// leaves its address behind; only a running daemon serves it.
		if release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock")); err == nil {
			release()
			return noPage
		}
		fmt.Fprintf(out, "Open: %s\n", strings.TrimSpace(string(data)))
		return nil
	}
	ln, err := listenLoopback(*listen)
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	token := protocol.NewID() + protocol.NewID()
	srv := &http.Server{Handler: ui.New(ui.NewFixture(time.Now), addr, token).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	fmt.Fprintf(out, "AgentNet messenger demo (invented data; nothing is sent, run or saved)\n")
	fmt.Fprintf(out, "Open: http://%s/?t=%s\n", addr, token)
	fmt.Fprintf(out, "Stop with Ctrl+C.\n")
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func listenLoopback(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return nil, errors.New("the messenger page listens only on a loopback address such as 127.0.0.1:0")
	}
	return net.Listen("tcp", addr)
}

// startDaemonUI serves the messenger page over a's real data, from inside
// the daemon that owns a's home. The address with its token goes
// only to an owner-only file (agentnet ui prints it), never to the log.
func startDaemonUI(a *client.Agent, home, listen string, logf func(string, ...any)) (stop func(), err error) {
	var ln net.Listener
	token := ""
	if prev, prevToken := takeUIHandoff(home, listen); prev != "" {
		if ln, err = listenLoopback(prev); err == nil {
			token = prevToken
		} else {
			logf("messenger page: the previous address %s is taken; serving on a new one (run `agentnet ui`)", prev)
			ln = nil
		}
	}
	if ln == nil {
		if ln, err = listenLoopback(listen); err != nil {
			return nil, err
		}
		token = protocol.NewID() + protocol.NewID()
	}
	addr := ln.Addr().String()
	path := filepath.Join(home, uiURLFile)
	if err := secfile.Write(path, []byte("http://"+addr+"/?t="+token+"\n")); err != nil {
		ln.Close()
		return nil, err
	}
	page := ui.New(ui.NewLive(a), addr, token)
	srv := &http.Server{Handler: page.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			logf("messenger page stopped: %v", err)
		}
	}()
	logf("messenger page on http://%s (run `agentnet ui` for the address to open)", addr)
	return func() {
		if a != nil {
			if r := a.UpdateSwitching(); r != nil {
				// Tell open pages, let them receive it, and leave the address
				// and token for the program that takes this daemon's place.
				page.Restarting()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				srv.Shutdown(ctx)
				cancel()
				h, _ := json.Marshal(uiHandoff{ID: r.ID, Addr: addr, Token: token})
				if err := secfile.Write(filepath.Join(home, uiHandoffFile), h); err != nil {
					logf("messenger page: the address will change after the update: %v", err)
				}
			}
		}
		srv.Close()
		os.Remove(path)
	}, nil
}

// uiHandoffFile carries the page's address and token from a daemon that
// stopped for an update to the program started in its place.
const uiHandoffFile = "ui-handoff.json"

type uiHandoff struct {
	ID    string `json:"id"` // the update request it belongs to
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

// takeUIHandoff returns the address and token to keep serving the page at.
// Only the program started to complete that very update gets them, and only
// once: the file is removed whatever it says.
func takeUIHandoff(home, listen string) (addr, token string) {
	path := filepath.Join(home, uiHandoffFile)
	data, err := secfile.Read(path)
	os.Remove(path)
	if err != nil {
		return "", ""
	}
	var h uiHandoff
	if json.Unmarshal(data, &h) != nil || h.ID == "" || len(h.Token) < 32 {
		return "", ""
	}
	// This start completes that update: it was put in the daemon's place
	// for it (Unix), or the request is still open and this program is the
	// version it asked for (on Windows the scheduled task starts it).
	pendingID, pendingTo := client.PendingUpdate(home)
	if os.Getenv(client.UpdateRestartEnv) != h.ID && (pendingID != h.ID || pendingTo != protocol.Version) {
		return "", ""
	}
	lh, lp, err1 := net.SplitHostPort(listen)
	hh, hp, err2 := net.SplitHostPort(h.Addr)
	if err1 != nil || err2 != nil || lh != hh || (lp != "0" && lp != hp) {
		return "", "" // the daemon now asks for another address
	}
	return h.Addr, h.Token
}
