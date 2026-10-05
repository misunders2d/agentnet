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
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/googleauth"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/ui"
)

var googleConfiguration = client.GoogleConfiguration
var startGoogleDesktop = googleauth.Desktop

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
	cfg, err := googleConfiguration(ctx, o)
	if err != nil {
		cancel()
		s.mu.Unlock()
		return ui.Refuse("Could not reach this workspace's Google sign-in.")
	}
	if cfg.DesktopClientID == "" {
		cancel()
		s.mu.Unlock()
		return ui.Refuse("Google sign-in is not set up for this workspace yet. Use your invitation instead.")
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
	auth, stop, err := startGoogleDesktop(ctx, cfg.DesktopClientID, protocol.GoogleNonce(pub), func(ctx context.Context, code, verifier, redirect string) (string, error) {
		return client.GoogleExchangeCode(ctx, s.r.home, o, protocol.GoogleExchange{Code: code, Verifier: verifier, Redirect: redirect, Public: pub})
	}, func(token string, err error) {
		defer cancel()
		defer s.mu.Unlock()
		joined := false
		if err == nil {
			var a *client.Agent
			a, err = client.JoinGoogle(ctx, s.r.home, o, token, s.device)
			if err == nil {
				joined = true
				_, err = s.completeJoin(a, hostOf(hubURL))
			}
		}
		v := ui.SetupGoogleStatus{State: "joined"}
		if err != nil {
			v.State = "error"
			v.Problem = googleJoinWords(err)
			if joined {
				v.Problem = err.Error()
			}
			s.r.logf("Google sign-in failed: %s", v.Problem)
		}
		s.stateMu.Lock()
		s.googleStatus = v
		s.googleStop = nil
		close(s.googleDone)
		s.stateMu.Unlock()
	})
	if err != nil {
		cancel()
		s.mu.Unlock()
		return ui.Refuse(err.Error())
	}
	s.stateMu.Lock()
	if s.googleStatus.State == "waiting" {
		s.googleStop = stop
	}
	s.stateMu.Unlock()
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
	c := protocol.GoogleAccessChange{}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "admin" {
			c.Admin = admin
		}
	})
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

// Cancel waits for an already-running callback; it never interrupts enrollment.
func (s *appSetup) SetupGoogleCancel() error {
	s.stateMu.Lock()
	stop := s.googleStop
	s.stateMu.Unlock()
	if stop != nil {
		stop()
	}
	return nil
}

func googleJoinWords(err error) string {
	if errors.Is(err, googleauth.ErrCredential) {
		return "Google sign-in was cancelled or timed out. Try again."
	}
	var he *client.HubError
	if errors.As(err, &he) {
		switch he.Code {
		case protocol.CodeRosterStale:
			return "Your devices changed. Try signing in again."
		case protocol.CodeTooManyDevices:
			return "You have too many devices. Remove one on your existing device, then try again."
		case protocol.CodeAddressTaken:
			return "This device name is taken. Try signing in again."
		case protocol.CodeBadStep:
			return "This sign-in request expired or changed. Try signing in again."
		}
		if he.Status == 403 {
			return "This Google email is not invited here. Ask your workspace admin."
		}
		if he.Status == 409 {
			return "None of your devices can approve this one. Ask your workspace admin."
		}
		if he.Status == 0 || he.Status >= 500 {
			return "Could not reach the workspace. Check your connection and try again."
		}
	}
	if strings.Contains(err.Error(), "already joined") {
		return "This computer already joined. Open AgentNet."
	}
	if strings.Contains(err.Error(), "update") {
		return "The workspace needs an AgentNet update. Ask its admin."
	}
	return "Could not sign in. Check your connection and try again."
}
