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
	damaged    bool // a line was dropped or unreadable since the last agent message
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
	if c.skipping {
		c.drop() // too long to read: it may have been the final message
	} else {
		c.event(c.line)
	}
	c.line, c.skipping = c.line[:0], false
}

// drop records an event that could not be read. An earlier agent message is
// no longer known to be the last one, so it is discarded; only a later
// readable agent message can become the answer.
func (c *codexStream) drop() {
	c.last, c.hasLast, c.damaged = "", false, true
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
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	if json.Unmarshal(line, &ev) != nil {
		c.drop()
		return
	}
	switch ev.Type {
	case "thread.started":
		if c.threadID == "" {
			c.threadID = ev.ThreadID
		}
	case "item.completed":
		if ev.Item.Type == "agent_message" {
			c.last, c.hasLast, c.damaged = ev.Item.Text, true, false
		}
	case "turn.completed":
		c.completed = true
	case "turn.failed":
		c.turnFailed = true
	}
}

// result is the answer of a turn that completed and did not fail: its last
// agent message (ok), or, when the turn carried none and nothing was lost,
// neither text nor failure (the caller may use Codex's -o file). Otherwise
// it is a failure reason, safe to show the peer; no partial text is given.
func (c *codexStream) result() (text string, ok bool, failure string) {
	switch {
	case c.turnFailed:
		return "", false, "codex reported that its turn failed"
	case !c.completed:
		return "", false, "codex did not report a completed turn"
	case c.hasLast:
		return c.last, true, ""
	case c.damaged:
		return "", false, "codex's final message could not be read"
	}
	return "", false, ""
}
