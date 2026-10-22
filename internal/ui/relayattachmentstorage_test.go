package ui

import (
	"os"
	"testing"

	"github.com/misunders2d/agentnet/internal/gdrive"
)

func TestRelayAttachmentStorageModel(t *testing.T) {
	if _, err := os.Stat("web/node_modules/esbuild"); err != nil {
		t.Skip("requires the existing pinned frontend dependencies")
	}
	link, _, err := (gdrive.OAuthConfig{ClientID: "fixture-client", ClientSecret: "fixture-secret"}).WebConsent("https://relay.example.test/oauth/drive/attachments", "fixture-state")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTNET_RELAY_CONSENT_URL", link)
	runNodeCheck(t, "testdata/relay_attachment_storage_model.cjs")
}

func TestRelayAttachmentStorageRendered(t *testing.T) {
	t.Log(browserCheck(t, "testdata/relay_attachment_storage_rendered.cjs"))
}
