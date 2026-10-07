package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestAppBridgeUsesExistingHandlerAndFences(t *testing.T) {
	var out bytes.Buffer
	exe := "fixture.AppImage"
	if runtime.GOOS == "darwin" {
		exe = "/Applications/AgentNet.app/Contents/MacOS/agentnet-app"
	} else if runtime.GOOS == "windows" {
		exe = `C:\Users\fixture\AppData\Local\AgentNet\agentnet-app.exe`
	}
	r := &appRunner{home: t.TempDir(), addr: "127.0.0.1:17443", token: "private-app", exe: exe, out: &out}
	r.controlsReady.Store(true)
	for _, test := range []struct {
		action, body string
		status       int
		contains     string
	}{
		{"status", "{}", 200, "app_update_supported"},
		{"cli", `{"replace":false}`, 400, "Choose Replace command first"},
		{"update", `{}`, 409, "Another daemon owns this home"},
		{"arbitrary", "{}", 400, "Unknown app action"},
	} {
		t.Run(test.action, func(t *testing.T) {
			out.Reset()
			if test.action == "update" {
				r.updating.Store(true)
				test.contains = "An update is already being prepared"
			}
			r.appCall(context.Background(), `{"command":"app-api","id":1,"action":"`+test.action+`","body":`+test.body+`}`)
			var event appEvent
			if err := json.Unmarshal(out.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			if event.Event != "appreply" || event.CallID != 1 || event.Status != test.status || !strings.Contains(event.Body, test.contains) {
				t.Fatalf("reply: %+v", event)
			}
			r.updating.Store(false)
		})
	}
	out.Reset()
	r.controlsReady.Store(false)
	r.appCall(context.Background(), `{"command":"app-api","id":2,"action":"status"}`)
	var event appEvent
	json.Unmarshal(out.Bytes(), &event)
	if event.Status != 503 {
		t.Fatalf("unready status=%d", event.Status)
	}
}

func TestAppBridgeStatusPreservesUpdateResult(t *testing.T) {
	home := t.TempDir()
	if err := writeAppUpdateResult(home, protocol.Version, "pending", "fixture pending"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, appUpdateResultFile)
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r := &appRunner{home: home, addr: "127.0.0.1:17443", token: "private-app", exe: "fixture.AppImage", out: &out}
	r.controlsReady.Store(true)
	r.appCall(context.Background(), `{"command":"app-api","id":1,"action":"status"}`)
	var event appEvent
	if err := json.Unmarshal(out.Bytes(), &event); err != nil || event.Status != 200 {
		t.Fatalf("status: %+v %v", event, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !info.ModTime().Equal(stamp) {
		t.Fatal("GET status changed update result")
	}
	if _, err := os.Stat(filepath.Join(home, "update-request.json")); !os.IsNotExist(err) {
		t.Fatalf("GET requested switch: %v", err)
	}
}
