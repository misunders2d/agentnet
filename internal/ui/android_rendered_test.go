package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Exercises the actual shared Comic package with Android's narrow host bridge.
// This is rendered UI evidence; native WebView/SAF qualification is separate.
func TestAndroidComicRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered Android-host checks")
	}
	f := NewFixture(time.Now)
	if _, _, err := f.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewDM("vitalii/laptop"); err != nil {
		t.Fatal(err)
	}
	var page http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { page.ServeHTTP(w, r) }))
	defer ts.Close()
	s := New(f, strings.TrimPrefix(ts.URL, "http://"), testToken)
	s.SetMobile()
	page = s.Handler()
	cmd := exec.CommandContext(t.Context(), "node", "testdata/android_comic_rendered.cjs", ts.URL+"/?t="+testToken)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Android Comic: %v\n%s", err, out)
	}
	t.Log(string(out))
}
