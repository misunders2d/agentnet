package ui

import (
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// termsSetupFixture counts the joins and Google sign-ins the page asks for:
// none may arrive before the terms of use are accepted.
type termsSetupFixture struct {
	mu     sync.Mutex
	joins  []SetupJoin
	google []string
}

func (*termsSetupFixture) SetupState() SetupView {
	return SetupView{State: "none", Device: "android-phone", DeviceWords: "Android phone"}
}
func (*termsSetupFixture) SetupInspect(code string) SetupInvite {
	if strings.HasPrefix(code, "agentnet-link-v2:") {
		return SetupInvite{Kind: "link", Host: "relay.example:8443"}
	}
	return SetupInvite{Kind: "invite", Host: "relay.example:8443", From: "Ann", Workspace: "Team"}
}
func (f *termsSetupFixture) SetupJoin(j SetupJoin) (SetupResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joins = append(f.joins, j)
	return SetupResult{}, Refuse("Fixture join stopped here.")
}
func (*termsSetupFixture) SetupStartAgain() (SetupView, error) { return SetupView{State: "none"}, nil }
func (f *termsSetupFixture) SetupGoogle(hub string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.google = append(f.google, hub)
	return Refuse("Fixture sign-in stopped here.")
}
func (*termsSetupFixture) SetupGoogleState() (SetupGoogleStatus, <-chan struct{}) {
	return SetupGoogleStatus{}, nil
}

// TestSetupTermsRendered: Google sign-in, Join and Add this computer wait
// for the terms of use checkbox (Google Play user-generated content policy).
func TestSetupTermsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: local Chromium")
	}
	f := &termsSetupFixture{}
	setup := httptest.NewUnstartedServer(nil)
	setup.Config.Handler = NewMobileSetup(f, setup.Listener.Addr().String(), testToken)
	setup.Start()
	defer setup.Close()
	out := browserCheck(t, "testdata/setup_terms_rendered.cjs", setup.URL, testToken)
	if !strings.Contains(out, "Setup terms PASS") {
		t.Fatalf("%s", out)
	}
	t.Log(out)
	f.mu.Lock()
	defer f.mu.Unlock()
	// Two widths; each accepted once per action after the refused attempt.
	if len(f.google) != 2 || len(f.joins) != 4 {
		t.Fatalf("calls before/after acceptance: google %v joins %+v", f.google, f.joins)
	}
	for i, j := range f.joins {
		if want := map[bool]string{true: "Bea", false: ""}[i%2 == 0]; j.Name != want {
			t.Fatalf("join %d: %+v", i, j)
		}
	}
}
