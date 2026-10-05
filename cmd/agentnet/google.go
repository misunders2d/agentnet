package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/googleauth"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/ui"
)

var openGoogleBrowser = func(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return errors.New("could not open the system browser")
	}
	go cmd.Wait()
	return nil
}

func (s *appSetup) SetupGoogle(hubURL string) error {
	if !s.mu.TryLock() {
		return ui.Refuse("Already signing in or joining. Wait for the result.")
	}
	if client.EnrollEnded(s.current()) {
		s.mu.Unlock()
		return ui.Refuse("Press Start again first.")
	}
	hubURL, err := protocol.NormalizeHubURL(hubURL)
	if err != nil {
		s.mu.Unlock()
		return ui.Refuse("Enter your workspace's HTTPS address.")
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
	o := client.GoogleOptions{Hub: hubURL}
	cfg, err := client.GoogleConfiguration(ctx, o)
	if err != nil {
		cancel()
		s.mu.Unlock()
		return ui.Refuse("Could not reach this workspace's Google sign-in.")
	}
	pub, err := client.GoogleDevice(s.r.home, s.device)
	if err != nil {
		cancel()
		s.mu.Unlock()
		return ui.Refuse("Could not prepare this computer's key.")
	}
	s.stateMu.Lock()
	s.googleDone = make(chan struct{})
	s.googleStatus = ui.SetupGoogleStatus{State: "waiting"}
	s.stateMu.Unlock()
	auth, stop, err := googleauth.Desktop(ctx, cfg.DesktopClientID, protocol.GoogleNonce(pub), func(ctx context.Context, code, verifier, redirect string) (string, error) {
		return client.GoogleExchangeCode(ctx, s.r.home, o, protocol.GoogleExchange{Code: code, Verifier: verifier, Redirect: redirect, Public: pub})
	}, func(token string, err error) {
		defer cancel()
		defer s.mu.Unlock()
		if err == nil {
			var a *client.Agent
			a, err = client.JoinGoogle(ctx, s.r.home, o, token, s.device)
			if err == nil {
				_, err = s.completeJoin(a, hostOf(hubURL))
			}
		}
		v := ui.SetupGoogleStatus{State: "joined"}
		if err != nil {
			v.State = "error"
			v.Problem = "Google sign-in did not finish. Check that your email is invited, then try again."
		}
		s.stateMu.Lock()
		s.googleStatus = v
		close(s.googleDone)
		s.stateMu.Unlock()
	})
	if err != nil {
		cancel()
		s.mu.Unlock()
		return ui.Refuse(err.Error())
	}
	if err = openGoogleBrowser(auth); err != nil {
		stop()
		return ui.Refuse("Could not open Google in your system browser.")
	}
	return nil
}

func (s *appSetup) SetupGoogleState() (ui.SetupGoogleStatus, <-chan struct{}) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.googleDone == nil {
		s.googleDone = make(chan struct{})
	}
	return s.googleStatus, s.googleDone
}

func (s *appSetup) completeJoin(a *client.Agent, host string) (ui.SetupResult, error) {
	res := ui.SetupResult{Host: host}
	if l := a.LinkState(); l.State == client.LinkPending {
		res.Waiting = l.Approver
	}
	started := make(chan error, 1)
	select {
	case s.joined <- appJoined{a: a, started: started}:
	case <-s.ctx.Done():
		a.Close()
		return ui.SetupResult{}, ui.Refuse("AgentNet is closing.")
	}
	t := time.NewTimer(appStartTimeout)
	defer t.Stop()
	select {
	case err := <-started:
		if err != nil {
			return ui.SetupResult{}, ui.Refuse("Joined, but AgentNet could not start.")
		}
	case <-t.C:
		return ui.SetupResult{}, ui.Refuse("Joined; close and open AgentNet again in a moment.")
	}
	return res, nil
}

func runGoogleAdmin(ctx context.Context, a *client.Agent, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		v, e := a.GoogleAccess(ctx)
		if e == nil {
			return json.NewEncoder(os.Stdout).Encode(v)
		}
		return e
	}
	fs := flag.NewFlagSet("admin google", flag.ContinueOnError)
	admin := fs.Bool("admin", false, "make this invited email an admin")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: admin google invite [--admin] EMAIL | allow-domain DOMAIN | remove EMAIL | remove-domain DOMAIN | list")
	}
	c := protocol.GoogleAccessChange{Admin: *admin}
	switch args[0] {
	case "invite":
		c.Email = fs.Arg(0)
	case "remove":
		c.Email = fs.Arg(0)
		c.Remove = true
	case "allow-domain":
		c.Domain = fs.Arg(0)
	case "remove-domain":
		c.Domain = fs.Arg(0)
		c.Remove = true
	default:
		return errors.New("unknown Google membership command")
	}
	if err := a.ChangeGoogleAccess(ctx, c); err != nil {
		return err
	}
	fmt.Println("Google membership updated.")
	return nil
}
