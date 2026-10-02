package client

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type byteChunks struct {
	s string
	n int
}

func (c *byteChunks) Read(p []byte) (int, error) {
	if c.s == "" {
		return 0, io.EOF
	}
	k := min(c.n, len(p), len(c.s))
	copy(p, c.s[:k])
	c.s = c.s[k:]
	return k, nil
}

type got struct{ event, data string }

func collect(t *testing.T, r io.Reader) (events []got, touches int, healthy bool, err error) {
	t.Helper()
	healthy, err = readStream(r, func() { touches++ }, func(e, d string) error {
		events = append(events, got{e, d})
		return nil
	})
	return
}

// The Hub's frames, whole or split at every byte, arrive as the same events
// in the same order, and the stream ends as io.EOF.
func TestStreamHubFramesAndChunking(t *testing.T) {
	msg := `{"id":"abc","from":"bob/x","ct":"AAAA"}`
	frames := "event: release\ndata: {\"v\":1}\n\nevent: message\ndata: " + msg + "\n\nevent: ping\ndata: {\"conn\":\"c1\"}\n\n"
	want := []got{{"release", `{"v":1}`}, {"message", msg}, {"ping", `{"conn":"c1"}`}}
	for _, n := range []int{0, 1, 7} {
		var r io.Reader = strings.NewReader(frames)
		if n > 0 {
			r = &byteChunks{frames, n}
		}
		events, touches, healthy, err := collect(t, r)
		if !healthy || !errors.Is(err, io.EOF) || len(events) != 3 || touches == 0 {
			t.Fatalf("chunk %d: healthy=%v err=%v events=%v touches=%d", n, healthy, err, events, touches)
		}
		for i := range want {
			if events[i] != want[i] {
				t.Fatalf("chunk %d: event %d = %+v, want %+v", n, i, events[i], want[i])
			}
		}
	}
}

// A connection cut before an event's final blank line, or mid-line, drops
// that event: nothing half-received is dispatched, and the end is io.EOF.
func TestStreamIncompleteEventAtEOFDropped(t *testing.T) {
	msg := `{"id":"abc","from":"bob/x","ct":"AAAA"}`
	for _, tail := range []string{"event: message\ndata: " + msg + "\n", "event: message\ndata: " + msg[:10]} {
		events, _, healthy, err := collect(t, strings.NewReader("event: ping\ndata: {\"conn\":\"c1\"}\n\n"+tail))
		if !healthy || !errors.Is(err, io.EOF) || len(events) != 1 || events[0].event != "ping" {
			t.Fatalf("tail %q: healthy=%v err=%v events=%v", tail, healthy, err, events)
		}
	}
}

// A dispatch error ends the stream as unhealthy, with that error, before
// any later event is looked at.
func TestStreamDispatchErrorIsUnhealthy(t *testing.T) {
	boom := errors.New("not processed yet")
	var seen []string
	healthy, err := readStream(strings.NewReader("event: message\ndata: a\n\nevent: ping\ndata: b\n\n"), nil, func(e, d string) error {
		seen = append(seen, e)
		return boom
	})
	if healthy || !errors.Is(err, boom) || len(seen) != 1 {
		t.Fatalf("healthy=%v err=%v seen=%v", healthy, err, seen)
	}
}

// Comments and keepalives carry no event, but their bytes touch the
// watchdog all the same.
func TestStreamCommentsTouchWatchdog(t *testing.T) {
	events, touches, _, err := collect(t, &byteChunks{":keepalive\n:still here\n", 5})
	if len(events) != 0 || touches < 4 || !errors.Is(err, io.EOF) {
		t.Fatalf("events=%v touches=%d err=%v", events, touches, err)
	}
}

type counting struct {
	r io.Reader
	n int
}

func (c *counting) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.n += k
	return k, err
}

// The size bound is per event, all its lines together: an event made of
// very many short data lines is refused once it passes streamEventLimit,
// with an explicit error, nothing of it dispatched, and no more than about
// twice the limit read from the wire; a long stream of ordinary events
// keeps flowing, and two events each near the limit pass.
func TestStreamBoundIsPerEvent(t *testing.T) {
	short := strings.Repeat("data: x\n", streamEventLimit/8+1024) // > limit in 8-byte lines
	c := &counting{r: strings.NewReader("event: message\n" + short + "\n")}
	events, _, healthy, err := collect(t, c)
	if !healthy || err == nil || errors.Is(err, io.EOF) || len(events) != 0 {
		t.Fatalf("oversized many-line event: healthy=%v err=%v events=%d", healthy, err, len(events))
	}
	if !strings.Contains(err.Error(), "larger than") || c.n > 2*streamEventLimit+64<<10 {
		t.Fatalf("bounded consumption: read %d bytes, err %v", c.n, err)
	}
	var b strings.Builder
	for i := range 5000 {
		fmt.Fprintf(&b, "event: ping\ndata: {\"conn\":\"%d\"}\n\n", i)
	}
	near := strings.Repeat("y", streamEventLimit*9/10)
	b.WriteString("event: members\ndata: " + near + "\n\nevent: members\ndata: " + near + "\n\n")
	events, _, healthy, err = collect(t, &byteChunks{b.String(), 4093})
	if !healthy || !errors.Is(err, io.EOF) || len(events) != 5002 || events[4999].data != `{"conn":"4999"}` || len(events[5001].data) != len(near) {
		t.Fatalf("many-event stream: healthy=%v err=%v n=%d", healthy, err, len(events))
	}
	events, _, healthy, err = collect(t, strings.NewReader("event: members\ndata: "+strings.Repeat("y", streamEventLimit)+"\n\n"))
	if !healthy || err == nil || errors.Is(err, io.EOF) || len(events) != 0 {
		t.Fatalf("single line over the event limit: healthy=%v err=%v events=%d", healthy, err, len(events))
	}
}
