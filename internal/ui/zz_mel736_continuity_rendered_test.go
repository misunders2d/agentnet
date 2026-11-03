// Kept last in discovery order to preserve baseline UI race partitions.
package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Presentation contract over the production page. The cryptographic and
// routing journey is separate: TestMEL736OneHubRecoveryDMAndNoGrantInheritance.
func TestMEL736OneRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: AGENTNET_PLAYWRIGHT required")
	}
	for _, history := range []int{10, 100} {
		t.Run(fmt.Sprint(history), func(t *testing.T) { mel736Rendered(t, history) })
	}
}

func mel736Rendered(t *testing.T, history int) {
	f := NewFixture(time.Now)
	f.role = RolePerson
	f.person = &PersonView{Person: "peer", Label: "Colleague", Address: f.me.Address, State: PersonSelf, Published: true}
	f.me.Agent = false
	f.threads = nil
	f.listed = nil
	peer := PersonView{Person: "59c89e536113a047e24569888bcc106a", Label: "Bohdan", Email: "bohdan@mellanni.com", Address: "bohdan/new", State: PersonPinned}
	d := &fxDM{id: strings.Repeat("a", 64), peer: peer, created: time.Now().Add(-time.Hour)}
	for i := range history {
		d.msgs = append(d.msgs, DMMessage{ID: strings.Repeat("b", 28) + fmt.Sprintf("%04d", i), Dir: "in", From: "bohdan/lost", Kind: KindMessage, Body: fmt.Sprintf("Retained order note %d", i), At: d.created.Add(time.Duration(i) * time.Second)})
	}
	d.msgs = append(d.msgs, DMMessage{ID: strings.Repeat("c", 32), Dir: "in", Kind: KindMessage, Event: "Bohdan signed in again", At: time.Now().Add(-time.Second)}, DMMessage{ID: strings.Repeat("d", 32), Dir: "in", Kind: KindMessage, Body: "Current device message", From: peer.Address, At: time.Now()})
	f.dms = []*fxDM{d}
	ts := httptest.NewUnstartedServer(nil)
	handler := New(mel736RenderProvider{f}, ts.Listener.Addr().String(), testToken).Handler()
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/dm/send" {
			time.AfterFunc(100*time.Millisecond, func() {
				f.mu.Lock()
				defer f.mu.Unlock()
				f.nextID++
				d.msgs = append(d.msgs, DMMessage{ID: fmt.Sprintf("%032x", f.nextID), Dir: "in", Kind: KindMessage, Body: "Concurrent synchronized arrival", From: peer.Address, At: time.Now()})
				f.bump()
			})
			time.Sleep(1500 * time.Millisecond)
			http.Error(w, "Fixture backend unavailable; retry", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	})
	ts.Start()
	defer ts.Close()
	evidence := os.Getenv("AGENTNET_SCREENSHOTS")
	if evidence == "" {
		evidence = t.TempDir()
	}
	os.MkdirAll(evidence, 0700)
	cmd := exec.CommandContext(t.Context(), "node", "testdata/personcontinuity_rendered.cjs")
	cmd.Env = append(os.Environ(), "MEL736_URL="+ts.URL+"/?t="+testToken, "MEL736_HISTORY="+fmt.Sprint(history), "AGENTNET_SCREENSHOTS="+filepath.Clean(evidence))
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
	t.Logf("%s", out)
}

type mel736RenderProvider struct{ *Fixture }

func (p mel736RenderProvider) DM(id string) (DMThread, error) {
	v, e := p.Fixture.DM(id)
	v.Role = "member"
	return v, e
}
