package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/ui"
)

// runUI serves the messenger page. Only the demo exists so far: invented
// data in memory, no home directory, Hub, network or harness.
func runUI(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	demo := fs.Bool("demo", false, "serve invented demo data")
	listen := fs.String("listen", "127.0.0.1:0", "loopback address to serve on")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%v (see agentnet help ui)", err)
	}
	if fs.NArg() > 0 {
		return errors.New("usage: agentnet ui --demo [--listen 127.0.0.1:0]")
	}
	if !*demo {
		return errors.New("the messenger page runs only with --demo for now: it is not connected to your inbox yet (see agentnet help ui)")
	}
	host, _, err := net.SplitHostPort(*listen)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return errors.New("--listen must be a loopback address such as 127.0.0.1:0")
	}
	ln, err := net.Listen("tcp", *listen)
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
