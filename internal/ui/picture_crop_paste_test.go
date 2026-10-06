package ui

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestPictureCropPNGAndClipboardPaste(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node missing")
	}
	out, err := exec.Command(node, "testdata/picture_crop_paste_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var v struct {
		PNG, Metadata string
		Checks        int
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	clean, err := base64.StdEncoding.DecodeString(v.PNG)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := base64.StdEncoding.DecodeString(v.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidatePicture(metadata); err == nil {
		t.Fatal("server accepted identifying metadata")
	}
	if err := protocol.ValidatePicture(clean); err != nil {
		t.Fatalf("server refused cleaned browser PNG: %v", err)
	}
	t.Logf("%d browser crop/paste checks; Go refuses metadata and accepts cleaned PNG", v.Checks)
}

func TestPictureCropEditorRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in rendered crop check")
	}
	f := newPictureFixture()
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	defer ts.Close()
	cmd := exec.Command("node", "testdata/picture_rendered_check.cjs", ts.URL+"/?t="+testToken)
	cmd.Env = append(os.Environ(), "AGENTNET_PICTURE_EDITOR_ONLY=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "P22 shared crop editor PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
