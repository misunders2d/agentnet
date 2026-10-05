package ui

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestP3ReviewSkinsRendered(t *testing.T) {
	f := NewFixture(time.Now)
	if _, _, err := f.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	conv, err := f.NewDM(f.listed[0].Address)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := f.SendDM(DMDraft{Conv: conv, Body: "P3 PARENT TEXT"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.SendDM(DMDraft{Conv: conv, Body: "P3 QUOTED TEXT", Quote: parent.ID}); err != nil {
		t.Fatal(err)
	}
	q := &f.dms[0].msgs[1]
	q.State, q.Delivery = "waiting", "waiting"
	q.Detail = client.WaitServerUnavailable + "cannot reach the Hub"
	q.StateText = DMStateText("out", "message", q.State, "Bob", q.Detail)
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	defer ts.Close()
	t.Setenv("AGENTNET_P3_REVIEW", "1")
	out := browserCheck(t, "testdata/skin_contract_check.cjs", ts.URL+"/?t="+testToken, filepath.Join("static", "skins", "comic"), filepath.Join("static", "skins", "classic"), filepath.Join("static", "skins", "zoom"))
	if !strings.Contains(out, "skin contract check PASS") {
		t.Fatal(out)
	}
	t.Log(out)
}

func TestBrowserHistoryQuoteVectors(t *testing.T) {
	w := startWireNode(t)
	data, err := os.ReadFile("testdata/history_quote_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name   string
		Fields map[string]any
		Valid  bool
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		h := map[string]any{"v": 1, "from": "alice/desk", "from_key": "aaaaaaaa-aaaaaaaa-aaaaaaaa-aaaaaaaa", "id": strings.Repeat("1", 32), "lid": strings.Repeat("2", 32), "ts": 1790000000, "at": 1790000000000, "kind": "message", "body": "quote", "quote": strings.Repeat("3", 32), "origin": "ui"}
		for k, value := range v.Fields {
			h[k] = value
		}
		raw, _ := json.Marshal(h)
		result := w.call(map[string]any{"op": "history", "json": string(raw)})
		if (result["error"] == nil) != v.Valid {
			t.Errorf("%s: %+v", v.Name, result)
		}
	}
}

func TestWaitingReasonVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/waiting_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ Name, Peer, Detail, Want string }
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		if got := DMStateText("out", "message", "waiting", v.Peer, v.Detail); got != v.Want {
			t.Errorf("%s: %q want %q", v.Name, got, v.Want)
		}
	}
}

func TestGuestCheckWordsMatchRoles(t *testing.T) {
	for _, role := range []string{"me", "member", "guest"} {
		v := guestCheckOf([]client.HumanSupport{{Label: "Bob", Me: role == "me", Role: role, State: "update"}})
		if v.Ready || len(v.NeedsUpdate) != 1 || v.NeedsUpdate[0].Role != role {
			t.Fatalf("%s: %+v", role, v)
		}
		switch role {
		case "me":
			if !strings.Contains(v.Text, "your other devices") || strings.Contains(v.Text, "joining") {
				t.Fatal(v.Text)
			}
		case "member":
			if !strings.Contains(v.Text, "keep this chat working with a guest") || strings.Contains(v.Text, "joining") {
				t.Fatal(v.Text)
			}
		case "guest":
			if !strings.Contains(v.Text, "before joining") {
				t.Fatal(v.Text)
			}
		}
	}
	v := guestCheckOf([]client.HumanSupport{{Label: "Bob", Role: "member", State: "offline"}, {Label: "Carol", Role: "guest", State: "offline"}})
	if len(v.Offline) != 1 || v.Offline[0] != "Carol" || !strings.Contains(v.Text, "Carol is not connected") || strings.Contains(v.Text, "Bob") {
		t.Fatal(v)
	}
}

func TestFixtureSentTimeFallsBackAndQuotePersists(t *testing.T) {
	f := NewFixture(func() time.Time { return time.Unix(1790000000, 0) })
	sent, err := f.Send(Draft{To: "bob/desk", Kind: "message", Body: "quoted fixture", Quote: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := f.Thread(sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(thread)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "0001-01-01") {
		t.Fatal("fixture serializes year one")
	}
	found := false
	for _, m := range thread.Messages {
		if m.ID == sent.ID {
			found = true
			if m.Quote != "parent" {
				t.Fatal(m)
			}
		}
	}
	if !found {
		t.Fatal("sent fixture missing")
	}
}
