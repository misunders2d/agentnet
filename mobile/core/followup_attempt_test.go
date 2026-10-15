package core

import (
	"github.com/misunders2d/agentnet/internal/protocol"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAndroidRequestFollowupAttemptPublicEndpoint(t *testing.T) {
	s, home := fixture(t)
	s.Close()
	app, err := OpenApp(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	c := appClient(t, app)
	for _, value := range []string{"-1", "9007199254740992"} {
		body := `{"id":"` + protocol.NewID() + `","ref":{"id":"` + protocol.NewID() + `","fingerprint":"` + app.session.a.Self().Fingerprint() + `"},"body":"correct","attempt":` + value + `}`
		req, _ := http.NewRequest("POST", app.Origin()+"/api/request/followup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", app.Origin())
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 409 || !strings.Contains(string(data), "retain its send ID") {
			t.Fatal(r.StatusCode, string(data))
		}
	}
}
