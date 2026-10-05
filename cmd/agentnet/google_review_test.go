package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/googleauth"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
	"github.com/misunders2d/agentnet/internal/ui"
)

func TestGoogleReviewSetupCancelAndDisabled(t *testing.T) {
	oldConfig, oldDesktop, oldBrowser := googleConfiguration, startGoogleDesktop, openGoogleBrowser
	t.Cleanup(func() { googleConfiguration, startGoogleDesktop, openGoogleBrowser = oldConfig, oldDesktop, oldBrowser })
	googleConfiguration = func(context.Context, client.GoogleOptions) (protocol.GoogleConfig, error) {
		return protocol.GoogleConfig{}, nil
	}
	s := &appSetup{r: &appRunner{home: t.TempDir(), logf: func(string, ...any) {}}, ctx: context.Background(), device: "laptop"}
	if err := s.SetupGoogle("https://workspace.example"); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.r.home, "identity.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled Google created keys", err)
	}
	googleConfiguration = func(context.Context, client.GoogleOptions) (protocol.GoogleConfig, error) {
		return protocol.GoogleConfig{DesktopClientID: "fixture"}, nil
	}
	startGoogleDesktop = func(_ context.Context, _, _ string, _ func(context.Context, string, string, string) (string, error), complete func(string, error)) (string, func(), error) {
		var once sync.Once
		return "https://unused.invalid", func() { once.Do(func() { complete("", googleauth.ErrCredential) }) }, nil
	}
	openGoogleBrowser = func(string) error { return nil }
	for n := 0; n < 2; n++ {
		if err := s.SetupGoogle("https://workspace.example"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetupGoogleCancel(); err != nil {
			t.Fatal(err)
		}
		v, done := s.SetupGoogleState()
		select {
		case <-done:
		default:
			t.Fatal("cancel did not publish result")
		}
		if v.State != "error" || !strings.Contains(v.Problem, "cancelled") {
			t.Fatalf("cancel status %+v", v)
		}
	}

	var finish func(string, error)
	startGoogleDesktop = func(_ context.Context, _, _ string, _ func(context.Context, string, string, string) (string, error), complete func(string, error)) (string, func(), error) {
		finish = complete
		return "https://unused.invalid", func() {}, nil
	}
	if err := s.SetupGoogle("https://workspace.example"); err != nil {
		t.Fatal(err)
	}
	finish("", &client.HubError{Status: 409, Msg: "no existing device can approve"})
	v, _ := s.SetupGoogleState()
	if v.State != "error" || !strings.Contains(v.Problem, "can approve") || strings.Contains(v.Problem, "not invited") {
		t.Fatalf("callback hid no-approver reason: %+v", v)
	}
	if !s.mu.TryLock() {
		t.Fatal("cancel kept join locked")
	}
	s.mu.Unlock()
}
func TestGoogleReviewErrorReasons(t *testing.T) {
	for _, tc := range []struct {
		err   error
		words string
	}{
		{googleauth.ErrCredential, "cancelled"}, {&client.HubError{Status: 403}, "not invited"}, {&client.HubError{Status: 409}, "can approve"},
		{&client.HubError{Status: 409, Code: protocol.CodeRosterStale}, "devices changed"}, {&client.HubError{Status: 409, Code: protocol.CodeTooManyDevices}, "too many"},
		{&client.HubError{Status: 0}, "connection"}, {errors.New("this computer already joined"), "Open AgentNet"},
	} {
		if got := googleJoinWords(tc.err); !strings.Contains(got, tc.words) {
			t.Fatalf("%v: %q", tc.err, got)
		}
	}
	// Successful join/startup failures preserve the refusal, not invitation advice.
	refusal := ui.Refuse("Joined, but AgentNet could not start.")
	s := &appSetup{ctx: context.Background(), joined: make(chan appJoined, 1)}
	go func() { joined := <-s.joined; joined.started <- errors.New("fixture startup failure") }()
	relay := testhub.Start(t, t.TempDir(), "127.0.0.1:0", "")
	a, joinErr := client.Join(context.Background(), t.TempDir(), testhub.BootstrapCode(t, relay.Dir), "laptop")
	if joinErr != nil {
		t.Fatal(joinErr)
	}
	defer a.Close()
	_, err := s.completeJoin(a, "fixture")
	if err == nil || err.Error() != refusal.Error() {
		t.Fatalf("post-join failure: %v", err)
	}
}
