package client

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/drivecontract"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const relayStorageAnswer = `{"can_admin":true,"configured":true,"connected":true,"state":"connected","account":{"email":"admin@example.com","name":"Admin"},
"folder":{"id":"folder-1","name":"AgentNet attachments"},"local":{"files":2,"bytes":20},"remote":{"files":3,"bytes":30},"pending":{"files":1,"bytes":10},
"remote_only_files":3,"migration":{"running":false,"files":0,"bytes":0,"total_files":0,"total_bytes":0},"consent_url":"https://accounts.google.com/o/oauth2/v2/auth?state=synthetic"}`

type relayStorageCall struct{ method, path, body string }

// relayStorageAgent answers every Hub request with code and body after
// checking it is signed by this device; calls records what reached the Hub.
func relayStorageAgent(t *testing.T, code int, body string) (*Agent, *[]relayStorageCall) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := &[]relayStorageCall{}
	a := &Agent{hub: &hubConn{base: "https://relay.invalid", agent: "admin/laptop", key: key, timeout: time.Second,
		http: &http.Client{Transport: storageTransport(func(r *http.Request) (*http.Response, error) {
			signed, err := protocol.ReadSignedRequest(r, func(agent string) (ed25519.PublicKey, error) {
				if agent != "admin/laptop" {
					return nil, errors.New("unexpected signer")
				}
				return pub, nil
			})
			if err != nil {
				t.Errorf("unsigned or mis-signed relay storage request: %v", err)
			}
			*calls = append(*calls, relayStorageCall{r.Method, r.URL.RequestURI(), string(signed.Body)})
			return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})}}}
	return a, calls
}

// Reading the status is one signed GET with no body that never yields a
// consent link; each change is one signed POST of exactly the chosen action,
// and only connect keeps the initiating admin's consent link.
func TestRelayStorageSignedReadAndChanges(t *testing.T) {
	a, calls := relayStorageAgent(t, 200, relayStorageAnswer)
	v, err := a.RelayAttachmentStorage(context.Background())
	if err != nil || v.ConsentURL != "" || v.State != "connected" || v.Account == nil || v.Folder == nil || v.Remote.Files != 3 || v.RemoteOnlyFiles != 3 {
		t.Fatalf("read: %+v %v", v, err)
	}
	if len(*calls) != 1 || (*calls)[0] != (relayStorageCall{"GET", "/v1/storage/attachments", ""}) {
		t.Fatalf("read reached the Hub as %+v", *calls)
	}
	for _, tc := range []struct {
		change drivecontract.RelayStorageAction
		body   string
	}{
		{drivecontract.RelayStorageAction{Action: "connect"}, `{"action":"connect"}`},
		{drivecontract.RelayStorageAction{Action: "move", Confirm: true}, `{"action":"move","confirm":true}`},
		{drivecontract.RelayStorageAction{Action: "disconnect", Confirm: true}, `{"action":"disconnect","confirm":true}`},
	} {
		*calls = nil
		v, err := a.ChangeRelayAttachmentStorage(context.Background(), tc.change)
		if err != nil || (v.ConsentURL != "") != (tc.change.Action == "connect") {
			t.Fatalf("%s: %+v %v", tc.change.Action, v, err)
		}
		if len(*calls) != 1 || (*calls)[0] != (relayStorageCall{"POST", "/v1/admin/storage/attachments", tc.body}) {
			t.Fatalf("%s reached the Hub as %+v", tc.change.Action, *calls)
		}
	}
	*calls = nil
	for _, action := range []string{"", "delete", "Connect", "status"} {
		if _, err := a.ChangeRelayAttachmentStorage(context.Background(), drivecontract.RelayStorageAction{Action: action, Confirm: true}); !errors.Is(err, ErrRelayStorageAction) {
			t.Fatalf("action %q: %v", action, err)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("unsupported actions reached the Hub: %+v", *calls)
	}
}

// Hub refusals keep their status and words (an older relay's 404, a member's
// 403, the Hub's 409 notice); answers the page would show wrongly fail.
func TestRelayStorageRefusalsAndUnclearAnswers(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
		msg  string
	}{
		{404, `{"error":"not found"}`, "not found"},
		{403, `{"error":"admin only"}`, "admin only"},
		{409, `{"error":"3 attachments exist only in Drive; move them back before disconnecting"}`, "3 attachments exist only in Drive; move them back before disconnecting"},
	} {
		a, _ := relayStorageAgent(t, tc.code, tc.body)
		v, err := a.ChangeRelayAttachmentStorage(context.Background(), drivecontract.RelayStorageAction{Action: "disconnect", Confirm: true})
		var he *HubError
		if !errors.As(err, &he) || he.Status != tc.code || he.Msg != tc.msg || v.State != "" {
			t.Fatalf("%d: %+v %v", tc.code, v, err)
		}
	}
	for _, answer := range []string{
		strings.Replace(relayStorageAnswer, `"state":"connected"`, `"state":"guess"`, 1),
		strings.Replace(relayStorageAnswer, `"remote_only_files":3`, `"remote_only_files":-1`, 1),
		strings.Replace(relayStorageAnswer, `https://accounts.google.com`, `http://accounts.google.com`, 1),
		strings.Replace(relayStorageAnswer, `https://accounts.google.com/o/oauth2/v2/auth?state=synthetic`, `javascript:alert(1)`, 1),
	} {
		a, _ := relayStorageAgent(t, 200, answer)
		if v, err := a.ChangeRelayAttachmentStorage(context.Background(), drivecontract.RelayStorageAction{Action: "connect"}); err == nil || v.ConsentURL != "" || v.State != "" {
			t.Fatalf("unclear answer accepted: %+v %v", v, err)
		}
	}
}
