package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestHookOrdinaryChatDoesNotBlockStop(t *testing.T) {
	for _, harness := range []string{"codex", "claude", "pi"} {
		t.Run(harness, func(t *testing.T) {
			home, db := hookClaudeFixture(t)
			t.Setenv(client.BackgroundEnv, "")
			if _, err := db.Exec(`INSERT INTO attention(harness,session,pos,seen_at) VALUES(?,'unrelated-work',0,unixepoch())`, harness); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state) VALUES('ordinary-chat','peer/desk',1,'message','unrelated chat',unixepoch(),'')`); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := runHook(home, []string{harness}, strings.NewReader(`{"session_id":"unrelated-work","hook_event_name":"Stop"}`), &out); err != nil {
				t.Fatal(err)
			}
			if harness == "pi" {
				if out.Len() != 0 {
					t.Errorf("ordinary chat triggered a Pi Stop continuation: %s", out.String())
				}
			} else if out.Len() != 0 {
				var got map[string]any
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got["decision"] == "block" {
					t.Errorf("ordinary chat blocked unrelated completion: %s", out.String())
				}
			}
			var pos int64
			if err := db.QueryRow(`SELECT pos FROM attention WHERE harness=? AND session='unrelated-work'`, harness).Scan(&pos); err != nil || pos != 0 {
				t.Fatalf("unshown Stop arrival consumed: pos=%d err=%v", pos, err)
			}
			out.Reset()
			if err := runHook(home, []string{harness}, strings.NewReader(`{"session_id":"unrelated-work","hook_event_name":"UserPromptSubmit"}`), &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "ordinary-chat") {
				t.Fatalf("next normal boundary lost unshown chat: %s", out.String())
			}
		})
	}
}
