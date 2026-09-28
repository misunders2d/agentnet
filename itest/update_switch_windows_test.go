//go:build windows

package itest

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// TestWindowsUpdateSwitchThroughTask: the product's switch on Windows, with
// the real program and the scheduled task agentnet help startup documents.
// A daemon started by the task switches through it (same arguments, page
// address and cookie, keys and history); one started any other way is not
// stopped; a helper lost mid-way leaves the switch visibly pending and the
// next start completes it; a task that cannot start records the failure.
// It creates, runs and deletes the task \agentnet, so it is opt-in
// (AGENTNET_WINTASK=1) and refuses to run where that task exists.
//
// The file is replaced here as agentnet update does (rename the running file
// to .old, put the new one in place) and the switch is requested through the
// same client call; the download itself is covered by the updater's tests.
func TestWindowsUpdateSwitchThroughTask(t *testing.T) {
	if os.Getenv("AGENTNET_WINTASK") != "1" {
		t.Skip("creates and runs the scheduled task agentnet; set AGENTNET_WINTASK=1")
	}
	if _, err := winSchtasks("/query", "/tn", `\agentnet`); err == nil {
		t.Skip("a scheduled task named agentnet exists here; this test would replace it")
	}
	c := buildCLI(t)
	c.env = []string{"AGENTNET_NOTIFY=off"}
	c.setup(t, "hub") // Hub and joins run from the test build, not the installed file
	root, err := os.MkdirTemp("", "anu")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin dir", "agentnet.exe")
	os.MkdirAll(filepath.Dir(bin), 0o700)
	builds := filepath.Join(c.dir, "builds")
	os.MkdirAll(builds, 0o700)
	base := "https://127.0.0.1:1/releases" // not used: nothing is downloaded here
	old := buildVersion(t, filepath.Join(builds, "old.exe"), "v9.1.0", base)
	next := buildVersion(t, filepath.Join(builds, "next.exe"), "v9.1.1", base)
	third := buildVersion(t, filepath.Join(builds, "third.exe"), "v9.1.2", base)
	os.WriteFile(bin, old, 0o700)
	home, err := filepath.Abs(filepath.Join(c.dir, "bob"))
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{"--home", home, "daemon", "--ui", "127.0.0.1:0"}
	tr := syscall.EscapeArg(bin)
	for _, a := range argv {
		tr += " " + syscall.EscapeArg(a)
	}
	t.Cleanup(func() {
		winSchtasks("/end", "/tn", `\agentnet`)
		if out, err := winSchtasks("/delete", "/tn", `\agentnet`, "/f"); err != nil {
			t.Errorf("delete task: %v: %s", err, out)
		}
		time.Sleep(time.Second)
		os.RemoveAll(root)
	})
	create := []string{"/create", "/sc", "onlogon", "/tn", "agentnet", "/tr", tr}
	if out, err := winSchtasks(create...); err != nil {
		t.Fatalf("schtasks /create: %v: %s", err, out)
	}
	url := filepath.Join(home, "ui-url")
	started := func(d time.Duration) bool {
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
			if _, err := os.Stat(url); err == nil {
				return true
			}
		}
		return false
	}
	winSchtasks("/run", "/tn", `\agentnet`)
	if !started(45 * time.Second) { // no interactive logon on this machine: the non-interactive form
		u, _ := user.Current()
		winSchtasks("/end", "/tn", `\agentnet`)
		winSchtasks("/delete", "/tn", `\agentnet`, "/f")
		if out, err := winSchtasks(append(create, "/ru", u.Username, "/np")...); err != nil {
			t.Fatalf("schtasks /create /ru: %v: %s", err, out)
		}
		winSchtasks("/run", "/tn", `\agentnet`)
		if !started(45 * time.Second) {
			out, _ := winSchtasks("/query", "/tn", `\agentnet`, "/v", "/fo", "list")
			t.Fatalf("the task did not start the daemon:\n%s", out)
		}
	}
	instance := func() int {
		t.Helper()
		_, pids, err := winTaskInstances("agentnet")
		if err != nil || len(pids) != 1 {
			t.Fatalf("task instances: %v %v", pids, err)
		}
		return pids[0]
	}
	bp := c.openPage(t, "bob")
	urlBefore, _ := os.ReadFile(url)
	c.run("--home", "alice", "send", "bob/desk", "history before the update")
	waitFor(t, "history", func() bool { return len(bp.overview().Threads) == 1 })

	// replace installs data as agentnet update does and asks for the switch.
	replace := func(data []byte, to string) string {
		t.Helper()
		os.Remove(bin + ".old")
		if err := os.Rename(bin, bin+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, data, 0o700); err != nil {
			t.Fatal(err)
		}
		id := protocol.NewID()
		if err := client.RequestUpdateSwitch(home, client.UpdateRequest{ID: id, Exe: bin, From: "", To: to, At: time.Now()}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	settled := func(id string, d time.Duration) (client.UpdateActivation, bool) {
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(250 * time.Millisecond) {
			if act, ok, _ := client.ReadUpdateActivation(home); ok && act.ID == id {
				return act, true
			}
		}
		return client.UpdateActivation{}, false
	}

	// 1. Idle: the task's instance switches through the task.
	oldPID := instance()
	id := replace(next, "v9.1.1")
	act, ok := settled(id, 2*time.Minute)
	if !ok || act.Result != client.ActivationRunning || act.Running != "v9.1.1" {
		t.Fatalf("switch: %+v (%v)", act, ok)
	}
	newPID := instance()
	if newPID == oldPID || act.PID != newPID {
		t.Fatalf("instance %d -> %d, activation pid %d", oldPID, newPID, act.PID)
	}
	if !winSameFile(winImage(newPID), bin) {
		t.Fatalf("new instance runs %s", winImage(newPID))
	}
	if after, _ := os.ReadFile(url); string(after) != string(urlBefore) {
		t.Fatal("the page address changed")
	}
	waitFor(t, "page back", func() bool { return bp.get("/api/overview", nil) == 200 })
	if o := bp.overview(); o.Version != "v9.1.1" || len(o.Threads) != 1 {
		t.Fatalf("after the switch: %q, %d threads", o.Version, len(o.Threads))
	}

	// 2. A helper lost mid-way: the daemon stops, nothing is started, the
	// switch stays visibly pending; the next start of the task completes it.
	id = replace(third, "v9.1.2")
	ready := filepath.Join(home, "update-helper-"+id+".ready")
	waitFor(t, "helper ready", func() bool { _, err := os.Stat(ready); return err == nil })
	data, _ := os.ReadFile(ready)
	helper, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if p, err := os.FindProcess(helper); err == nil {
		p.Kill()
	}
	if _, ok := settled(id, 20*time.Second); ok {
		t.Fatal("a switch was recorded although the helper was lost")
	}
	if out := c.run("--home", "bob", "update", "--status"); !strings.Contains(out, "requested") {
		t.Fatalf("status with a lost helper: %s", out)
	}
	winSchtasks("/run", "/tn", `\agentnet`)
	if act, ok := settled(id, time.Minute); !ok || act.Result != client.ActivationRunning || act.Running != "v9.1.2" {
		t.Fatalf("after starting the task again: %+v (%v)", act, ok)
	}

	// 3. The task cannot start the new program: the failure is recorded.
	id = replace(old, "v9.1.0") // (going back only exercises the path here)
	ready = filepath.Join(home, "update-helper-"+id+".ready")
	waitFor(t, "helper ready", func() bool { _, err := os.Stat(ready); return err == nil })
	winSchtasks("/change", "/tn", `\agentnet`, "/disable")
	act, ok = settled(id, 3*time.Minute)
	if !ok || act.Result != client.ActivationFailed || !strings.Contains(act.Detail, "schtasks /run") {
		t.Fatalf("task that cannot start: %+v (%v)", act, ok)
	}
	winSchtasks("/change", "/tn", `\agentnet`, "/enable")

	// 4. A daemon started some other way is never stopped for an update.
	c.bin = bin // the installed file, started from a console instead of the task
	stop := c.start("bob-console.log", argv...)
	defer stop()
	waitFor(t, "console daemon", func() bool { _, err := os.Stat(url); return err == nil })
	id = replace(next, "v9.1.1")
	act, ok = settled(id, time.Minute)
	if !ok || act.Result != client.ActivationNotApplied || !strings.Contains(act.Detail, "not the running instance") {
		t.Fatalf("console daemon: %+v (%v)", act, ok)
	}
	if out, err := c.try("--home", "bob", "whoami"); err != nil {
		t.Fatalf("home unusable: %v %s", err, out)
	}
}
