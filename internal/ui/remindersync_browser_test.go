package ui

import (
	"bytes"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/ui/static"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestBrowserReminderSync(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/remindersync_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
func TestBrowserReminderSyncWireMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	m := protocol.ReminderMark{Conv: "", Fingerprint: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd", LID: protocol.NewID(), Due: 1790000000, State: "pending", Revision: 1790000000000, Writer: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"}
	r := protocol.ReminderSync{V: 1, Person: protocol.NewID(), Roster: strings.Repeat("a", 64), Reminders: []protocol.ReminderMark{m}}
	var cases []map[string]any
	add := func(raw string) {
		_, err := protocol.ParseReminderSync([]byte(raw))
		cases = append(cases, map[string]any{"raw": raw, "valid": err == nil})
	}
	for _, state := range []string{"pending", "done", "cancelled", "replied", "working"} {
		r.Reminders[0].State = state
		b, _ := json.Marshal(r)
		add(string(b))
	}
	r.Reminders = []protocol.ReminderMark{m}
	raw, _ := json.Marshal(r)
	for _, bad := range []string{`"conv"`, `"fingerprint"`, `"lid"`, `"due"`, `"state"`, `"revision"`, `"writer"`} {
		var fields map[string]any
		json.Unmarshal(raw, &fields)
		marks := fields["reminders"].([]any)
		delete(marks[0].(map[string]any), strings.Trim(bad, `"`))
		b, _ := json.Marshal(fields)
		add(string(b))
	}
	for _, old := range []string{`"due":1790000000`, `"revision":1790000000000`} {
		for _, value := range []string{"0", "-1", "1.5", "9007199254740992", "null"} {
			add(strings.Replace(string(raw), old, strings.Split(old, ":")[0]+":"+value, 1))
		}
	}
	add(strings.Replace(string(raw), `"v":1`, `"v":1,"extra":true`, 1))
	r.Reminders = []protocol.ReminderMark{m, m}
	b, _ := json.Marshal(r)
	add(string(b))
	input, _ := json.Marshal(cases)
	cmd := exec.Command(node, "--input-type=module", "-e", `import * as w from './static/wire.mjs';let s='';for await(const b of process.stdin)s+=b;const cases=JSON.parse(s);for(const c of cases){let valid=true;try{w.parseReminderSync(c.raw);}catch{valid=false;}if(valid!==c.valid)throw Error('Go/browser mismatch: '+c.raw);}console.log(cases.length+' reminder parser parity vectors PASS');`)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

// The same journey on a real browser store (IndexedDB), including the reopen.
func TestBrowserReminderSyncRealIndexedDB(t *testing.T) {
	chrome := os.Getenv("AGENTNET_CHROME")
	if chrome == "" {
		t.Skip("AGENTNET_CHROME not set")
	}
	const check = "/testdata/remindersync_engine_check.mjs"
	done := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<script type="module">let result;try{await import('`+check+`');result=window.reminderSyncResult}catch(e){result={error:e.stack}}await fetch('/result',{method:'POST',body:JSON.stringify(result)});</script>`)
		case r.URL.Path == "/result":
			var result map[string]any
			if json.NewDecoder(r.Body).Decode(&result) == nil {
				done <- result
			}
		case strings.HasPrefix(r.URL.Path, "/static/"):
			http.StripPrefix("/static/", http.FileServer(http.FS(static.Files))).ServeHTTP(w, r)
		case r.URL.Path == check:
			http.ServeFile(w, r, strings.TrimPrefix(r.URL.Path, "/"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--disable-background-networking", "--no-first-run", "--no-default-browser-check", "--disable-sync", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome"), server.URL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	select {
	case result := <-done:
		if result["ok"] != true {
			t.Fatalf("actual IndexedDB: %v", result)
		}
		t.Log(result)
	case <-time.After(90 * time.Second):
		t.Fatalf("actual IndexedDB timed out: %s", stderr.String())
	}
}
