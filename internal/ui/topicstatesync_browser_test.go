package ui

import (
	"bytes"
	"encoding/json"
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
	"github.com/misunders2d/agentnet/internal/ui/static"
)

func TestBrowserTopicStateSync(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/topicstatesync_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

// The browser's strict mark parser and its order (wire.topicMarkNewer) are
// the Go ones (protocol.ParseTopicStateSync, TopicMark.Newer).
func TestBrowserTopicStateSyncWireMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	mark := protocol.TopicMark{Scope: strings.Repeat("b", 64), Topic: protocol.NewID(), Mark: protocol.TopicMarkDone, Count: 2, At: 1700000000, Writer: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"}
	base := protocol.TopicStateSync{V: 1, Person: protocol.NewID(), Roster: strings.Repeat("a", 64), Marks: []protocol.TopicMark{mark}}
	var cases []map[string]any
	add := func(raw string) {
		_, err := protocol.ParseTopicStateSync([]byte(raw))
		cases = append(cases, map[string]any{"raw": raw, "valid": err == nil})
	}
	with := func(change func(*protocol.TopicMark)) string {
		r := base
		r.Marks = []protocol.TopicMark{mark}
		change(&r.Marks[0])
		return marshal(t, r)
	}
	for _, change := range []func(*protocol.TopicMark){
		func(m *protocol.TopicMark) {},
		func(m *protocol.TopicMark) { m.Mark = protocol.TopicMarkOpen },
		func(m *protocol.TopicMark) { m.Mark = protocol.TopicMarkArchived },
		func(m *protocol.TopicMark) { m.Mark, m.Count = protocol.TopicMarkNone, 0 },
		func(m *protocol.TopicMark) { m.Mark = protocol.TopicMarkNone },
		func(m *protocol.TopicMark) { m.Mark = "Done" },
		func(m *protocol.TopicMark) { m.Count = 0 },
		func(m *protocol.TopicMark) { m.Count = -1 },
		func(m *protocol.TopicMark) { m.Count = protocol.MaxTopicTitleRevision },
		func(m *protocol.TopicMark) { m.Count = protocol.MaxTopicTitleRevision + 1 },
		func(m *protocol.TopicMark) { m.At = 0 },
		func(m *protocol.TopicMark) { m.At = protocol.MaxTopicTitleRevision },
		func(m *protocol.TopicMark) { m.At = protocol.MaxTopicTitleRevision + 1 },
		func(m *protocol.TopicMark) { m.Scope = "admin/laptop" },
		func(m *protocol.TopicMark) { m.Scope = "wrong scope" },
		func(m *protocol.TopicMark) { m.Topic = "0123" },
		func(m *protocol.TopicMark) { m.Writer = "nobody" },
	} {
		add(with(change))
	}
	raw := marshal(t, base)
	for _, value := range []string{
		strings.Replace(raw, `"mark":"done",`, "", 1),
		strings.Replace(raw, `"mark":"done"`, `"mark":null`, 1),
		strings.Replace(raw, `"count":2,`, "", 1),
		strings.Replace(raw, `"count":2`, `"count":2.5`, 1),
		strings.Replace(raw, `"at":1700000000,`, "", 1),
		strings.Replace(raw, `"v":1`, `"v":2`, 1),
		strings.Replace(raw, `"v":1`, `"v":1,"unexpected":true`, 1),
		strings.Replace(raw, `"writer"`, `"task":true,"writer"`, 1),
		strings.Replace(raw, `"marks"`, `"titles"`, 1),
		raw + strings.Repeat(" ", 65537-len(raw)),
	} {
		add(value)
	}
	dup := base
	dup.Marks = []protocol.TopicMark{mark, mark}
	add(marshal(t, dup))
	full := base
	full.Marks = nil
	for range protocol.MaxTopicMarks + 1 {
		m := mark
		m.Topic = protocol.NewID()
		full.Marks = append(full.Marks, m)
	}
	add(marshal(t, full))
	full.Marks = full.Marks[:protocol.MaxTopicMarks]
	add(marshal(t, full))
	// Order vectors: every field that breaks a tie, each way.
	var orders []map[string]any
	low, high := "00000000-00000000-00000000-00000000", "ffffffff-ffffffff-ffffffff-ffffffff"
	for _, pair := range [][2]protocol.TopicMark{
		{{At: 2, Writer: low, Mark: "open", Count: 1}, {At: 1, Writer: high, Mark: "open", Count: 1}},
		{{At: 1, Writer: high, Mark: "", Count: 0}, {At: 1, Writer: low, Mark: "open", Count: 1}},
		{{At: 1, Writer: low, Mark: "open", Count: 1}, {At: 1, Writer: low, Mark: "done", Count: 9}},
		{{At: 1, Writer: low, Mark: "done", Count: 3}, {At: 1, Writer: low, Mark: "done", Count: 2}},
		{{At: 1, Writer: low, Mark: "done", Count: 2}, {At: 1, Writer: low, Mark: "done", Count: 2}},
	} {
		for _, p := range [][2]protocol.TopicMark{pair, {pair[1], pair[0]}} {
			orders = append(orders, map[string]any{"a": p[0], "b": p[1], "newer": p[0].Newer(p[1])})
		}
	}
	cmd := exec.Command(node, "--input-type=module", "-e", `import * as wire from './static/wire.mjs';let input='';for await(const s of process.stdin)input+=s;const {cases,orders}=JSON.parse(input);for(const [i,c] of cases.entries()){let valid=true;try{wire.parseTopicStateSync(c.raw)}catch{valid=false}if(valid!==c.valid)throw Error('topic state sync Go/browser validation differs at '+i+': '+c.raw.slice(0,300))}for(const [i,o] of orders.entries())if(wire.topicMarkNewer(o.a,o.b)!==o.newer)throw Error('topic mark order differs at '+i);console.log('topic state sync Go/browser wire checks',cases.length,'orders',orders.length)`)
	data, _ := json.Marshal(map[string]any{"cases": cases, "orders": orders})
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

// The same journey on a real browser store (IndexedDB), including the reopen.
func TestBrowserTopicStateSyncRealIndexedDB(t *testing.T) {
	chrome := os.Getenv("AGENTNET_CHROME")
	if chrome == "" {
		t.Skip("AGENTNET_CHROME not set")
	}
	const check = "/testdata/topicstatesync_engine_check.mjs"
	done := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<script type="module">let result;try{await import('`+check+`');result=window.topicStateSyncResult}catch(e){result={error:e.stack}}await fetch('/result',{method:'POST',body:JSON.stringify(result)});</script>`)
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
