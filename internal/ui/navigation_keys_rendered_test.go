package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNavigationKeysRendered(t *testing.T) {
	privateScreenshots(t)
	fixture := NewFixture(time.Now)
	if _, _, err := fixture.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.NewDM("vitalii/laptop"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = New(fixture, server.Listener.Addr().String(), testToken).Handler()
	server.Start()
	t.Cleanup(server.Close)
	out := browserCheck(t, "testdata/navigation_keys_rendered.cjs", server.URL+"/?t="+testToken)
	if !strings.Contains(out, "navigation keys PASS") {
		t.Fatalf("incomplete navigation proof: %s", out)
	}
	t.Log(out)
}
