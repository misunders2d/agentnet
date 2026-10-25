package client

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/lockfile"
)

func TestDaemonReadinessFreshLaunchChecks(t *testing.T) {
	stub := installStub(t, "answer")
	a := enrolledAt(t, "https://127.0.0.1:1", "")
	defer a.Close()
	setResponder(t, a, "stub", stub.dir, 0)
	other, err := a.CreateLocalAgent("Other", Responder{Harness: "stub2", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetLocalAgentResponder(other.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReadDaemonReadiness(tctx(t)); !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("stopped daemon: %v", err)
	}
	release, err := lockfile.Acquire(filepath.Join(a.home, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := a.ReadDaemonReadiness(tctx(t)); err == nil || errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("older/starting daemon must have unknown readiness: %v", err)
	}
	stop, err := a.listenReadiness(false)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	read := func() DaemonReadiness {
		t.Helper()
		v, err := a.ReadDaemonReadiness(tctx(t))
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Agents) != 2 || !v.Agents[0].Default || !v.Agents[0].Available || v.Agents[1].Enabled || v.Agents[1].Available {
			t.Fatalf("exact enabled/default/disabled agents: %+v", v)
		}
		return v
	}
	if v := read(); v.Connected {
		t.Fatal("lock ownership was mistaken for transport connection")
	}
	a.streamConnected.Store(true)
	if v := read(); !v.Connected {
		t.Fatal("live connection was omitted")
	}
	// A changed prerequisite is resolved now, not from a retained observation.
	if err := os.Remove(stub.dir); err != nil {
		t.Fatal(err)
	}
	v, err := a.ReadDaemonReadiness(tctx(t))
	if err != nil || v.Agents[0].Available || !strings.Contains(v.Agents[0].Problem, "directory") {
		t.Fatalf("removed directory remained ready: %+v %v", v, err)
	}
	if err := os.Mkdir(stub.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(Harnesses["stub"].bin); err != nil {
		t.Fatal(err)
	}
	v, err = a.ReadDaemonReadiness(tctx(t))
	if err != nil || v.Agents[0].Available || !strings.Contains(v.Agents[0].Problem, "launch environment") {
		t.Fatalf("removed executable remained ready: %+v %v", v, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(readinessSockPath(a.home))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("readiness socket permissions: %v %v", info, err)
		}
	}
}

func TestDaemonReadinessCallerProcess(t *testing.T) {
	if home := os.Getenv("AGENTNET_READINESS_TEST_HOME"); home != "" {
		a, err := Open(home)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		v, err := a.ReadDaemonReadiness(tctx(t))
		if err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(os.Stdout).Encode(v)
		return
	}
	stub := installStub(t, "answer")
	a := enrolledAt(t, "https://127.0.0.1:1", "")
	defer a.Close()
	setResponder(t, a, "stub", stub.dir, 0)
	release, err := lockfile.Acquire(filepath.Join(a.home, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stop, err := a.listenReadiness(false)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// The second process has neither the parent's stub registry nor its PATH.
	// It must still obtain the actual daemon's available named executor.
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonReadinessCallerProcess$")
	cmd.Env = append(os.Environ(), "AGENTNET_READINESS_TEST_HOME="+a.home, "PATH=")
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var v DaemonReadiness
	if err := json.NewDecoder(strings.NewReader(string(data))).Decode(&v); err != nil || len(v.Agents) != 1 || !v.Agents[0].Available {
		t.Fatalf("caller environment replaced daemon readiness: %s %v", data, err)
	}
}

func TestDaemonReadinessTracksRealStreamConnection(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	eventually(t, "accepted stream is connected", func() bool {
		v, err := w.bob.ReadDaemonReadiness(tctx(t))
		return err == nil && v.Connected
	})
	w.hub.Stop()
	eventually(t, "closed stream is disconnected", func() bool {
		v, err := w.bob.ReadDaemonReadiness(tctx(t))
		return err == nil && !v.Connected
	})
}

func TestDaemonReadinessLifecycle(t *testing.T) {
	a := enrolledAt(t, "https://127.0.0.1:1", "")
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, RunOptions{HumanOnly: true}) }()
	eventually(t, "headless read-only readiness", func() bool {
		v, err := a.ReadDaemonReadiness(tctx(t))
		return err == nil && v.HumanOnly && !v.Connected
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(readinessSockPath(a.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stopped daemon retained its readiness socket: %v", err)
	}
	if _, err := a.ReadDaemonReadiness(tctx(t)); !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("stopped daemon remained ready: %v", err)
	}
	if running, err := daemonRunning(filepath.Join(a.home, "identity.json")); running || err == nil {
		t.Fatalf("lock I/O failure was labeled running: %v %v", running, err)
	}
}

func TestDaemonReadinessRejectsForeignAndOversize(t *testing.T) {
	a := enrolledAt(t, "https://127.0.0.1:1", "")
	defer a.Close()
	release, err := lockfile.Acquire(filepath.Join(a.home, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, data := range []string{`{"address":"other/host","fingerprint":"foreign","pid":1}`, strings.Repeat("x", readinessLimit+1)} {
		ln, err := net.Listen("unix", readinessSockPath(a.home))
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			c, err := ln.Accept()
			if err == nil {
				c.SetWriteDeadline(time.Now().Add(time.Second))
				c.Write([]byte(data))
				c.Close()
			}
		}()
		_, err = a.ReadDaemonReadiness(tctx(t))
		ln.Close()
		<-done
		if err == nil {
			t.Fatal("invalid daemon response accepted")
		}
	}
}
