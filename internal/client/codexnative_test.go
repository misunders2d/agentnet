package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func codexRollout(t *testing.T, sid string, rows ...map[string]any) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "rollout.jsonl")
	data, _ := json.Marshal(map[string]any{"type": "session_meta", "ordinal": 0, "payload": map[string]any{"id": sid, "cli_version": codexNativeVersion, "source": "vscode"}})
	data = append(data, '\n')
	for _, r := range rows {
		v, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		data = append(data, v...)
		data = append(data, '\n')
	}
	if e := os.WriteFile(file, data, 0600); e != nil {
		t.Fatal(e)
	}
	return file
}
func codexReceiptRow(sid, text string) map[string]any {
	return map[string]any{"ordinal": 1, "type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": sid, "turn_id": "turn", "item": map[string]any{"type": "UserMessage", "id": "native-item", "client_id": "native-client", "content": []map[string]string{{"type": "text", "text": text}}}}}
}
func TestCodexNativeExactReceipt(t *testing.T) {
	sid := "01a0f81b-8e3d-7002-a05d-5622e2723fd1"
	d := &ReplyReceiverDelivery{BindingID: "binding", InputID: "input", ClaimID: "claim", InputToken: "token", RequestBody: "original local goal", Message: Message{From: "peer", Kind: "question", Body: "untrusted clarification"}}
	text := codexInputText(d)
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		want   bool
	}{
		{"exact", func(map[string]any) {}, true},
		{"another thread", func(r map[string]any) { r["payload"].(map[string]any)["thread_id"] = "other" }, false},
		{"assistant echo", func(r map[string]any) {
			r["payload"].(map[string]any)["item"].(map[string]any)["type"] = "AgentMessage"
		}, false},
		{"transport id only", func(r map[string]any) { r["payload"].(map[string]any)["item"].(map[string]any)["client_id"] = "" }, false},
		{"payload differs", func(r map[string]any) {
			r["payload"].(map[string]any)["item"].(map[string]any)["content"] = []map[string]string{{"type": "text", "text": text + " changed"}}
		}, false},
		{"no completed turn", func(r map[string]any) { r["payload"].(map[string]any)["turn_id"] = "" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := codexReceiptRow(sid, text)
			tc.change(r)
			f := codexRollout(t, sid, r)
			got, e := codexNativeReceipt(f, sid, text)
			if e != nil || got != tc.want {
				t.Fatalf("receipt %v %v", got, e)
			}
		})
	}
}
func TestCodexNativeRolloutIdentityAndBounds(t *testing.T) {
	sid := "exact-sid"
	file := codexRollout(t, sid, codexReceiptRow(sid, "text"))
	if _, e := codexNativeEntries(file, "another-sid"); e == nil {
		t.Fatal("wrong native identity accepted")
	}
	r := codexReceiptRow(sid, "text")
	file = codexRollout(t, sid, r, r)
	if _, e := codexNativeEntries(file, sid); e == nil {
		t.Fatal("ordinal rewind accepted")
	}
	file = codexRollout(t, sid, map[string]any{"type": "session_meta", "ordinal": 1, "payload": map[string]any{"id": sid}})
	if _, e := codexNativeEntries(file, sid); e == nil {
		t.Fatal("duplicate header accepted")
	}
	file = codexRollout(t, sid)
	f, e := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.Write(make([]byte, (2<<20)+1))
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = codexNativeEntries(file, sid); e == nil {
		t.Fatal("oversized record accepted")
	}
}
func TestCodexUnavailableRouteCannotClaim(t *testing.T) {
	w := newWorld(t, "")
	owner, call := nativeReceiverFixture(t, w.alice, "pi")
	binding, input := nativeInput(t, w.alice, w.bob, owner.Handle)
	r, e := replySessionIn(w.alice.store.db, owner.Handle)
	if e != nil {
		t.Fatal(e)
	}
	r.Harness = "codex"
	tx, e := w.alice.store.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = saveReplySession(tx, r); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if d, e := w.alice.TakeReplyReceiverInput(call); e == nil || d != nil {
		t.Fatalf("unqualified route claimed %+v %v", d, e)
	}
	var claim *string
	if e = w.alice.store.db.QueryRow(`SELECT live_claim FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, binding, input).Scan(&claim); e != nil {
		t.Fatal(e)
	}
	if claim != nil {
		t.Fatal("unavailable route persisted a claim")
	}
	if _, ok, e := w.alice.claimReplyReceiverJob(); e != nil || ok {
		t.Fatalf("selected input ran managed/default %v %v", ok, e)
	}
	if _, ok, e := w.alice.store.claimJob("stub"); e != nil || ok {
		t.Fatalf("selected input escaped default fence %v %v", ok, e)
	}
}
