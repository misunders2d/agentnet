package client

import (
	"bytes"
	"encoding/json"
)

// codexStream reads `codex exec --json` output as it is written and keeps
// only what the worker needs: the thread id, the text of the last completed
// agent message, and how the turn ended. Codex's own `-o` file is written
// from the same last agent message (exec's JSONL event processor, pinned at
// rust-v0.157.1), so this is the answer by the harness's contract; the rest
// of the stream (tool events, reasoning) is dropped, however long it is.
type codexStream struct {
	line     []byte
	skipping bool // inside a line longer than codexLineMax

	threadID   string
	last       string
	hasLast    bool
	completed  bool
	turnFailed bool
}

// codexLineMax bounds one JSON line; longer ones are skipped whole.
const codexLineMax = 4 << 20

func (c *codexStream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		if !c.skipping {
			if len(c.line)+len(chunk) > codexLineMax {
				c.skipping, c.line = true, c.line[:0]
			} else {
				c.line = append(c.line, chunk...)
			}
		}
		if i < 0 {
			break
		}
		c.endLine()
		p = p[i+1:]
	}
	return n, nil
}

// flush handles a last line that had no newline.
func (c *codexStream) flush() {
	if len(c.line) > 0 || c.skipping {
		c.endLine()
	}
}

func (c *codexStream) endLine() {
	if !c.skipping {
		c.event(c.line)
	}
	c.line, c.skipping = c.line[:0], false
}

func (c *codexStream) event(line []byte) {
	var ev struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Item     struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return // not an event: ignored
	}
	switch ev.Type {
	case "thread.started":
		if c.threadID == "" {
			c.threadID = ev.ThreadID
		}
	case "item.completed":
		if ev.Item.Type == "agent_message" {
			c.last, c.hasLast = ev.Item.Text, true
		}
	case "turn.completed":
		c.completed = true
	case "turn.failed":
		c.turnFailed = true
	}
}

// answer is the final agent message of a turn that completed and did not
// fail.
func (c *codexStream) answer() (string, bool) {
	if c == nil {
		return "", false
	}
	return c.last, c.completed && !c.turnFailed && c.hasLast
}
