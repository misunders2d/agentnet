package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// releaseRequests counts what reaches the fake release origin, by path end.
type releaseRequests struct {
	mu   sync.Mutex
	base http.RoundTripper
	hits map[string]int
}

func (r *releaseRequests) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.hits[filepath.Base(req.URL.Path)]++
	r.mu.Unlock()
	return r.base.RoundTrip(req)
}

func (r *releaseRequests) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[name]
}

func (r *releaseRequests) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, v := range r.hits {
		n += v
	}
	return n
}

// countReleases counts the requests to the fake release origin
// (fakeReleaseServer must run first).
func countReleases(t *testing.T) *releaseRequests {
	t.Helper()
	r := &releaseRequests{base: updateClient.Transport, hits: map[string]int{}}
	updateClient.Transport = r
	return r
}

// The daemon's automatic update of a standalone home: the release the Hub
// named is downloaded once from the fixed origin, verified, installed with
// the previous file kept, and the daemon is asked to switch to it. A file
// that already holds it only needs the switch.
func TestDaemonAutoUpdateInstallsAndRequestsSwitch(t *testing.T) {
	fakeReleaseServer(t, &releaseStub{latest: "v9.9.10", asset: selfBytes(t)})
	hits := countReleases(t)
	exe, before := installed(t, "v9.9.8")
	t.Setenv("AGENTNET_FAKE_VERSION", "v9.9.9") // what the downloaded file reports
	home := t.TempDir()
	detail, err := daemonAutoUpdate(home, exe)(context.Background(), "v9.9.9")
	if err != nil || !strings.Contains(detail, "agentnet v9.9.9") {
		t.Fatalf("auto update: %q %v", detail, err)
	}
	if unchanged(exe, before) {
		t.Fatal("the release was not installed")
	}
	if old, err := fileIdentity(exe + ".old"); err != nil || !os.SameFile(old, before) {
		t.Fatalf("previous file not kept: %v", err)
	}
	if hits.count("SHA256SUMS") != 1 || hits.count(assetName()) != 1 || hits.total() != 2 {
		t.Fatalf("release requests: %v (want one checksum list and one file; never latest: the Hub named the version)", hits.hits)
	}
	if id, to := client.PendingUpdate(home); id == "" || to != "v9.9.9" {
		t.Fatalf("no switch requested: %q %q", id, to)
	}
	if l := leftovers(t, exe); len(l) != 0 {
		t.Fatalf("staged files left: %v", l)
	}
	// Installed, the switch not done yet (a job ran): the next trigger
	// downloads nothing and asks for the switch again.
	os.Remove(exe + ".fakeversion") // the new file reports the release's version
	firstID, _ := client.PendingUpdate(home)
	if _, err := daemonAutoUpdate(home, exe)(context.Background(), "v9.9.9"); err != nil {
		t.Fatal(err)
	}
	if hits.total() != 2 {
		t.Fatalf("downloaded again: %v", hits.hits)
	}
	if id, to := client.PendingUpdate(home); id == "" || id == firstID || to != "v9.9.9" {
		t.Fatalf("switch not asked again: %q %q", id, to)
	}
}

// A download that fails its checksum changes nothing and asks for no
// switch; a development build never updates itself and asks nothing of
// the release origin; neither does a version that is not newer.
func TestDaemonAutoUpdateRefusals(t *testing.T) {
	sum := sha256.Sum256([]byte("something else"))
	for _, c := range []struct {
		name, current, target, sums, want string
	}{
		{"checksum mismatch", "v9.9.8", "v9.9.9", hex.EncodeToString(sum[:]) + "  " + assetName() + "\n", "does not match the release checksum"},
		{"development build", "v9.9.8-3-gabcdef0", "v9.9.9", "", "never updates itself"},
		{"plain development build", "dev", "v9.9.9", "", "never updates itself"},
		{"not newer", "v9.9.9", "v9.9.9", "", "not a release newer"},
		{"not a release", "v9.9.8", "v9.9.9-rc1", "", "not a release newer"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeReleaseServer(t, &releaseStub{latest: "v9.9.9", asset: selfBytes(t), sums: c.sums})
			hits := countReleases(t)
			exe, before := installed(t, c.current)
			t.Setenv("AGENTNET_FAKE_VERSION", "v9.9.9")
			home := t.TempDir()
			_, err := daemonAutoUpdate(home, exe)(context.Background(), c.target)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error: %v", err)
			}
			if !unchanged(exe, before) || len(leftovers(t, exe)) != 0 {
				t.Fatal("the installed file changed")
			}
			if _, err := os.Stat(exe + ".old"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a refused update moved the file aside")
			}
			if id, _ := client.PendingUpdate(home); id != "" {
				t.Fatal("a refused update asked the daemon to switch")
			}
			if c.sums == "" && hits.total() != 0 {
				t.Fatalf("asked the release origin: %v", hits.hits)
			}
		})
	}
}

// In a home the AgentNet app manages, the automatic update asks the app
// for its whole-app update of the named release (the app's own updater
// downloads); a closed app is not opened from a daemon.
func TestDaemonAutoUpdateAppManagedAsksTheApp(t *testing.T) {
	fakeReleaseServer(t, &releaseStub{latest: "v9.9.9", asset: selfBytes(t)})
	hits := countReleases(t)
	exe, before := installed(t, "v9.9.8")
	home := t.TempDir()
	if err := secfile.Write(filepath.Join(home, appExeFile), []byte(filepath.Join(t.TempDir(), "AgentNet")+"\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonAutoUpdate(home, exe)(context.Background(), "v9.9.9"); err == nil || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("closed app: %v", err)
	}
	var asked []string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var choice struct {
			Version string `json:"version"`
		}
		cookie, err := r.Cookie("agentnet_ui")
		if err != nil || cookie.Value != "private" || r.Method != http.MethodPost || r.URL.Path != "/api/app/update" || json.NewDecoder(r.Body).Decode(&choice) != nil {
			t.Error("not the app's authenticated update request")
		}
		asked = append(asked, choice.Version)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state":"restarting","message":"Restarting AgentNet with the update…"}`))
	}))
	t.Cleanup(app.Close)
	if err := secfile.Write(filepath.Join(home, appControlsURLFile), []byte(app.URL+"/?t=private\n")); err != nil {
		t.Fatal(err)
	}
	detail, err := daemonAutoUpdate(home, exe)(context.Background(), "v9.9.9")
	if err != nil || !strings.Contains(detail, "app") {
		t.Fatalf("app update: %q %v", detail, err)
	}
	if len(asked) != 1 || asked[0] != "v9.9.9" {
		t.Fatalf("app asked for %v", asked)
	}
	if !unchanged(exe, before) || hits.total() != 0 {
		t.Fatalf("the standalone path ran in an app-managed home: %v", hits.hits)
	}
	if id, _ := client.PendingUpdate(home); id != "" {
		t.Fatal("a standalone switch was requested in an app-managed home")
	}
}

// agentnet update --auto on|off is the owner-only switch the daemon reads.
func TestUpdateAutoSwitch(t *testing.T) {
	home := t.TempDir()
	if on, err := client.AutoUpdateOn(home); !on || err != nil {
		t.Fatalf("default: %v %v", on, err)
	}
	if err := runUpdate(context.Background(), home, []string{"--auto", "off"}); err != nil {
		t.Fatal(err)
	}
	if on, err := client.AutoUpdateOn(home); on || err != nil {
		t.Fatalf("off: %v %v", on, err)
	}
	if err := runUpdate(context.Background(), home, []string{"--auto", "on"}); err != nil {
		t.Fatal(err)
	}
	if on, err := client.AutoUpdateOn(home); !on || err != nil {
		t.Fatalf("on: %v %v", on, err)
	}
	for _, args := range [][]string{{"--auto", "maybe"}, {"--auto", "off", "v9.9.9"}, {"--auto", "off", "--check"}} {
		if err := runUpdate(context.Background(), home, args); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

// The app that its update helper starts again after a failed install tells
// the daemon's automatic update so: the record of the handed-over attempt
// says failed, in the helper's words, and the wait before the same release
// is tried again runs from now. A failure of another release, or a record
// of another attempt, changes nothing.
func TestAppStartRecordsFailedAutoUpdate(t *testing.T) {
	home := t.TempDir()
	handedOver := client.AutoUpdateRecord{To: "v9.9.9", From: protocol.Version, State: client.AutoUpdated, Detail: "Restarting AgentNet with the update…", At: time.Now().Add(-time.Hour), Tries: 2}
	write := func(r client.AutoUpdateRecord) {
		t.Helper()
		b, _ := json.Marshal(r)
		if err := secfile.Write(filepath.Join(home, "auto-update.json"), b); err != nil {
			t.Fatal(err)
		}
	}
	write(handedOver)
	if err := writeAppUpdateResult(home, "v9.9.8", "failed", "Update failed; the previous app remains at its verified path: no space left on device"); err != nil {
		t.Fatal(err)
	}
	(&appRunner{home: home}).confirmAppUpdate()
	if got, _, _ := client.ReadAutoUpdate(home); got.State != client.AutoUpdated {
		t.Fatalf("another release's failure changed the record: %+v", got)
	}
	if err := writeAppUpdateResult(home, "v9.9.9", "failed", "Update failed; the previous app remains at its verified path: permission denied"); err != nil {
		t.Fatal(err)
	}
	(&appRunner{home: home}).confirmAppUpdate()
	got, ok, err := client.ReadAutoUpdate(home)
	if err != nil || !ok || got.State != client.AutoUpdateFailed || !strings.Contains(got.Detail, "permission denied") || time.Since(got.At) > time.Minute || got.Tries != 2 || got.To != "v9.9.9" {
		t.Fatalf("record after a failed install: %+v %v %v", got, ok, err)
	}
	failedAt := got.At
	(&appRunner{home: home}).confirmAppUpdate() // the next start: already recorded
	if again, _, _ := client.ReadAutoUpdate(home); !again.At.Equal(failedAt) {
		t.Fatalf("each start moves the wait on: %v then %v", failedAt, again.At)
	}
}

// The help says what a recommendation does now (SEC-2): member daemons with
// automatic update on install it, from the fixed origin, and how a person
// opts out; versions are ordered, not only compared for equality; while
// suspended, the required release comes first.
func TestHelpSaysRecommendationsInstall(t *testing.T) {
	text := func(topic ...string) string {
		var out strings.Builder
		if err := printHelp(&out, topic); err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(out.String()), " ")
	}
	admin := text("admin")
	for _, want := range []string{"installs the recommended release by itself unless its person turned that off", "SHA256SUMS", "never suspends anyone"} {
		if !strings.Contains(admin, want) {
			t.Errorf("help admin lacks %q", want)
		}
	}
	if strings.Contains(admin, "compared only for equality") || strings.Contains(admin, "installs nothing") {
		t.Errorf("help admin still says a recommendation is only a notice: %s", admin)
	}
	update := text("update")
	for _, want := range []string{"--auto off turns this home's automatic updates off", "the release it requires (its own) is the one installed, before any newer recommendation", "With automatic update on (the default) the daemon installs it by itself"} {
		if !strings.Contains(update, want) {
			t.Errorf("help update lacks %q", want)
		}
	}
}
