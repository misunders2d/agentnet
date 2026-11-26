package client

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

type fakeQueueEntry struct {
	ID       string `json:"id"`
	ClientID string `json:"clientUserMessageId"`
	Thread   string `json:"thread"`
	Text     string `json:"text"`
}

var fakeQueueMu sync.Mutex

func fakeQueuePath() string { return filepath.Join(os.Getenv("CODEX_HOME"), "queue.json") }

func readFakeQueue(path string) []fakeQueueEntry {
	var q []fakeQueueEntry
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &q)
	return q
}

func writeFakeQueue(path string, q []fakeQueueEntry) {
	raw, _ := json.Marshal(q)
	tmp := path + ".tmp"
	os.WriteFile(tmp, raw, 0600)
	os.Rename(tmp, path)
}

func fakeCodexAppServer() {
	home := os.Getenv("CODEX_HOME")
	if _, err := os.Stat(filepath.Join(home, "hang")); err == nil {
		time.Sleep(time.Hour)
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 16<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var m struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &m) != nil || m.ID == 0 {
			continue
		}
		var p struct {
			Thread   string `json:"threadId"`
			ClientID string `json:"clientUserMessageId"`
			QueueID  string `json:"queuedSubmissionId"`
			Input    []struct {
				Text string `json:"text"`
			} `json:"input"`
		}
		json.Unmarshal(m.Params, &p)
		path := fakeQueuePath()
		q := readFakeQueue(path)
		var result any = map[string]any{}
		switch m.Method {
		case "thread/queue/add":
			if _, err := os.Stat(filepath.Join(home, "refuse")); err == nil {
				out.Encode(map[string]any{"id": m.ID, "error": map[string]any{"code": -32600, "message": "no"}})
				continue
			}
			e := fakeQueueEntry{ID: protocol.NewID(), ClientID: p.ClientID, Thread: p.Thread, Text: p.Input[0].Text}
			writeFakeQueue(path, append(q, e))
			if _, err := os.Stat(filepath.Join(home, "drop-reply")); err == nil {
				os.Exit(1) // added, then the answer is lost
			}
			result = map[string]any{"queuedSubmission": map[string]any{"id": e.ID, "clientUserMessageId": e.ClientID, "input": []any{}}}
		case "thread/queue/list":
			data := []map[string]any{}
			for _, e := range q {
				if e.Thread == p.Thread {
					data = append(data, map[string]any{"id": e.ID, "clientUserMessageId": e.ClientID, "input": []any{}})
				}
			}
			result = map[string]any{"data": data, "nextCursor": nil}
		case "thread/queue/delete":
			deleted, kept := false, q[:0]
			for _, e := range q {
				if e.ID == p.QueueID && e.Thread == p.Thread {
					deleted = true
					continue
				}
				kept = append(kept, e)
			}
			writeFakeQueue(path, kept)
			result = map[string]any{"deleted": deleted}
		}
		out.Encode(map[string]any{"id": m.ID, "result": result})
	}
}

