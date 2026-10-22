package ui

import (
	"os"
	"testing"
)

func TestRelayAttachmentStorageModel(t *testing.T) {
	if _, err := os.Stat("web/node_modules/esbuild"); err != nil {
		t.Skip("requires the existing pinned frontend dependencies")
	}
	runNodeCheck(t, "testdata/relay_attachment_storage_model.cjs")
}

func TestRelayAttachmentStorageRendered(t *testing.T) {
	t.Log(browserCheck(t, "testdata/relay_attachment_storage_rendered.cjs"))
}
