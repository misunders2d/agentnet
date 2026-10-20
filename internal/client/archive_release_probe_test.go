package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Temporary investigation probe: record only what the original caller reads,
// without an extra profile request or replacing a response/error.
type archiveProbeBody struct {
	io.ReadCloser
	body bytes.Buffer
	err  error
	done func([]byte, error)
}

func (b *archiveProbeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.body.Write(p[:n])
	if err != nil && err != io.EOF {
		b.err = err
	}
	return n, err
}

func (b *archiveProbeBody) Close() error {
	err := b.ReadCloser.Close()
	b.done(b.body.Bytes(), b.err)
	return err
}

func observeArchiveRelease(t *testing.T, sender, phone *Agent) func(string, ...any) {
	t.Helper()
	var mu sync.Mutex
	var observations []string
	note := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		observations = append(observations, fmt.Sprintf(format, args...))
	}
	label, device, _ := protocol.SplitAddress(phone.Address)
	profilePath := "/v1/agents/" + label + "/" + device + "/profile"
	for _, a := range []*Agent{sender, phone} {
		base := a.hub.http.Transport
		a.hub.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			start := time.Now()
			deadline, bounded := req.Context().Deadline()
			note("%s start %s %s bounded=%t remaining=%s", a.Address, req.Method, req.URL.RequestURI(), bounded, time.Until(deadline))
			if req.Method == "PUT" && req.URL.Path == "/v1/caps" && req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					note("%s caps request clone: %v", a.Address, err)
				} else {
					var record protocol.CapsRecord
					err = json.NewDecoder(body).Decode(&record)
					body.Close()
					note("%s PUT caps session=%s ts=%d archive=%t proactive=%t decode=%v", a.Address, record.Session, record.TS, record.Reads(protocol.CapHistoryArchive), record.Reads(protocol.CapProactiveAgent), err)
				}
			}
			resp, err := base.RoundTrip(req)
			if err != nil {
				note("%s %s %s elapsed=%s error=%v context=%v", a.Address, req.Method, req.URL.RequestURI(), time.Since(start), err, req.Context().Err())
				return resp, err
			}
			note("%s %s %s status=%d elapsed=%s context=%v", a.Address, req.Method, req.URL.RequestURI(), resp.StatusCode, time.Since(start), req.Context().Err())
			if req.Method == "GET" && req.URL.Path == profilePath {
				resp.Body = &archiveProbeBody{ReadCloser: resp.Body, done: func(body []byte, readErr error) {
					var profile protocol.Profile
					decodeErr := json.Unmarshal(body, &profile)
					note("%s phone profile sessions=%v live=%t supports_archive=%t elapsed=%s context=%v read=%v decode=%v", a.Address, profile.Sessions, profile.Live, profile.Supports(phone.Address, phone.Self().SignKey, protocol.CapHistoryArchive), time.Since(start), req.Context().Err(), readErr, decodeErr)
					for _, raw := range profile.Caps {
						record, parseErr := protocol.ParseCapsRecord(raw)
						note("%s profile record session=%s ts=%d archive=%t proactive=%t parse=%v", a.Address, record.Session, record.TS, record.Reads(protocol.CapHistoryArchive), record.Reads(protocol.CapProactiveAgent), parseErr)
					}
				}}
			}
			return resp, err
		})
		t.Cleanup(func() {
			a.stopBackgroundPosts()
			a.stopArchivePosts()
			a.hub.http.Transport = base
		})
	}
	t.Cleanup(func() {
		if t.Failed() {
			mu.Lock()
			defer mu.Unlock()
			for i, observation := range observations {
				t.Logf("archive release observation %d: %s", i, observation)
			}
		}
	})
	return note
}
