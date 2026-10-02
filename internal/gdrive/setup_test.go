package gdrive

import (
	"strings"
	"testing"
)

func TestDriveSetupOptionalGuideAndValidation(t *testing.T) {
	if e := (StorageConfig{}).Validate(); e != nil {
		t.Fatal("off blocks onboarding", e)
	}
	if e := (StorageConfig{Enabled: true}).Validate(); e == nil {
		t.Fatal("enabled without setup")
	}
	draft := SetupDraft{Config: StorageConfig{Project: "agentnet-fixture"}, CloudAccount: "admin@example.test", ExistingProject: false}
	steps, e := SetupGuide(draft)
	if e != nil || len(steps) != 6 {
		t.Fatal(e)
	}
	commands := strings.Join(steps[1].Commands, "\n") + strings.Join(steps[2].Commands, "\n")
	if !strings.Contains(commands, "gcloud projects create agentnet-fixture --account='admin@example.test'") || !strings.Contains(commands, "gcloud services enable drive.googleapis.com --project=agentnet-fixture --account='admin@example.test'") {
		t.Fatal("unscoped/incorrect commands", commands)
	}
	if strings.Contains(commands, "config set") || strings.Contains(commands, "oauth-clients") {
		t.Fatal("changed defaults or invented client creation")
	}
	draft.ExistingProject = true
	steps, e = SetupGuide(draft)
	if e != nil || strings.Contains(strings.Join(steps[1].Commands, "\n"), "projects create") {
		t.Fatal("existing mode recreates project")
	}
	draft.Completed = map[string]bool{"api": true}
	steps, _ = SetupGuide(draft)
	if !steps[2].Complete || !strings.Contains(steps[5].Detail, "admin confirmations") {
		t.Fatal("manual checklist presented as verified")
	}
	draft.CloudAccount = "x@example.test'; curl bad"
	if _, e = SetupGuide(draft); e == nil {
		t.Fatal("shell injection accepted")
	}
	cfg := StorageConfig{Enabled: true, Project: "agentnet-fixture", DesktopClientID: "123-fixture.apps.googleusercontent.com", BrowserClientID: "123-browser.apps.googleusercontent.com", BrowserOrigins: []string{"https://relay.example", "http://127.0.0.1:4500"}, APIConfirmed: true, ConsentConfirmed: true, ClientsConfirmed: true}
	if e = cfg.Validate(); e != nil {
		t.Fatal(e)
	}
	cfg.BrowserOrigins = []string{"https://relay.example/path"}
	if e = cfg.Validate(); e == nil {
		t.Fatal("invalid OAuth origin accepted")
	}
	cfg.BrowserOrigins = []string{"https://relay.example"}
	cfg.DesktopClientID = "secret-token"
	if e = cfg.Validate(); e == nil {
		t.Fatal("token mistaken for public client ID")
	}
}
