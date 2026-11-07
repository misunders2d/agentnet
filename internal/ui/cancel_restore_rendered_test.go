package ui

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Cancel of a queued send in the real daemon page and built Comic bundle:
// exact restoration, a restoration storage refused recovered after a reload,
// and one active tab. Synthetic fixture; only queued-send endpoints are stubbed.
func TestCancelRestoreRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	// Plain browser pages at both widths, then the synthetic desktop and Android
	// shell markers (stand-ins defined before page scripts, not the native shells).
	for _, v := range [][2]string{{"1440", ""}, {"390", ""}, {"1440", "desktop"}, {"390", "android"}} {
		width, native := v[0], v[1]
		f := NewFixture(time.Now)
		if _, _, err := f.CreatePerson("Fixture Me"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.NewDM("vitalii/laptop"); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewUnstartedServer(nil)
		ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
		ts.Start()
		cmd := exec.Command("node", "testdata/cancel_restore_rendered_check.cjs", ts.URL+"/?t="+testToken)
		cmd.Env = append(os.Environ(), "CANCEL_WIDTH="+width, "CANCEL_NATIVE="+native)
		out, err := cmd.CombinedOutput()
		ts.Close()
		if err != nil || !strings.Contains(string(out), "Cancel restore rendered PASS") {
			t.Errorf("width %s %s: %v\n%s", width, native, err, out) // the other variants still run
			continue
		}
		t.Logf("%s", out)
	}
}

// Without Web Locks two pages on one origin must never both write saved state:
// a browser page is refused; a native shell's page (desktop or Android marker)
// opens read-only and leaves seeded drafts, journal and recovery unchanged;
// with locks the native page writes as usual. Synthetic fixture; the markers
// are defined and navigator.locks removed or refused before any page script.
func TestNativeWithoutLocksRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	f := NewFixture(time.Now)
	if _, _, err := f.CreatePerson("Fixture Me"); err != nil {
		t.Fatal(err)
	}
	dm, err := f.NewDM("vitalii/laptop")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	defer ts.Close()
	cmd := exec.Command("node", "testdata/native_nolock_rendered_check.cjs", ts.URL+"/?t="+testToken, dm)
	cmd.Env = append(os.Environ(), "NOLOCK_EVIDENCE="+os.Getenv("CANCEL_EVIDENCE"))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Native without locks rendered PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// Saved messages → Retry saved send on the phone, through the real daemon page
// and built bundle: a held message is not sent from there; one the host never
// stored is sent once under its ID. Synthetic fixture.
func TestSavedSendRetryRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	f := NewFixture(time.Now)
	if _, _, err := f.CreatePerson("Fixture Me"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewDM("vitalii/laptop"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	defer ts.Close()
	out, err := exec.Command("node", "testdata/saved_send_retry_rendered_check.cjs", ts.URL+"/?t="+testToken).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Saved send retry rendered PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
