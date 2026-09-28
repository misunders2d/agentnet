package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/ui"
)

// uiURLFile holds the running daemon's messenger page address, including
// its token (good for that daemon's lifetime), owner-only in the home. It is
// never logged.
const uiURLFile = "ui-url"

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
		data, err := secfile.Read(filepath.Join(home, uiURLFile))
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("no messenger page is running for this home: start the daemon with `agentnet daemon --ui 127.0.0.1:0` (see agentnet help ui)")
		}
		if err != nil {
			return err
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
	ln, err := listenLoopback(listen)
	if err != nil {
		return nil, err
	}
	addr := ln.Addr().String()
	token := protocol.NewID() + protocol.NewID()
	path := filepath.Join(home, uiURLFile)
	if err := secfile.Write(path, []byte("http://"+addr+"/?t="+token+"\n")); err != nil {
		ln.Close()
		return nil, err
	}
	srv := &http.Server{Handler: ui.New(ui.NewLive(a), addr, token).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			logf("messenger page stopped: %v", err)
		}
	}()
	logf("messenger page on http://%s (run `agentnet ui` for the address to open)", addr)
	return func() {
		srv.Close()
		os.Remove(path)
	}, nil
}
