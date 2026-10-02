package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	sse "github.com/tmaxmax/go-sse"
)

func boundedGroupHeads(n int) []protocol.GroupHead {
	heads := make([]protocol.GroupHead, n)
	for i := range heads {
		heads[i] = protocol.GroupHead{Conv: fmt.Sprintf("%064x", i+1), Bootstrap: strings.Repeat("b", 64), Hash: strings.Repeat("c", 64), Seq: int64(i)}
	}
	return heads
}

func parseGroupHeadFrames(t *testing.T, wire []byte) []protocol.GroupHead {
	t.Helper()
	var got []protocol.GroupHead
	for event, err := range sse.Read(bytes.NewReader(wire), &sse.ReadConfig{MaxEventSize: 2 * protocol.MaxBody}) {
		if err != nil {
			t.Fatal(err)
		}
		if event.Type != "groups" {
			t.Fatalf("unexpected event %q", event.Type)
		}
		var batch []protocol.GroupHead
		if err = json.Unmarshal([]byte(event.Data), &batch); err != nil {
			t.Fatal(err)
		}
		got = append(got, batch...)
	}
	return got
}

func TestGroupHeadFramesBoundedAndComplete(t *testing.T) {
	heads := boundedGroupHeads(12000)
	all, _ := json.Marshal(heads)
	if len(all) <= 2*protocol.MaxBody {
		t.Fatal("fixture below former event limit")
	}
	// The old one-array emission fails the unchanged upstream event bound.
	tooLarge := false
	for _, err := range sse.Read(strings.NewReader(fmt.Sprintf(groupHeadsFrame, all)), &sse.ReadConfig{MaxEventSize: 2 * protocol.MaxBody}) {
		if errors.Is(err, bufio.ErrTooLong) {
			tooLarge = true
		}
	}
	if !tooLarge {
		t.Fatal("former unbatched frame did not reproduce parser size refusal")
	}
	var wire bytes.Buffer
	sent := ""
	frames := 0
	write := func(format string, args ...any) bool {
		frame := fmt.Sprintf(format, args...)
		if len(frame) > groupHeadsFrameLimit {
			t.Fatalf("frame %d too large: %d", frames, len(frame))
		}
		frames++
		wire.WriteString(frame)
		return true
	}
	if !writeGroupHeads(heads, &sent, write) {
		t.Fatal("batch emission failed")
	}
	if frames < 2 || !reflect.DeepEqual(parseGroupHeadFrames(t, wire.Bytes()), heads) {
		t.Fatal("heads lost, duplicated or reordered")
	}
	before := frames
	if !writeGroupHeads(heads, &sent, write) || frames != before {
		t.Fatal("unchanged snapshot resent")
	}
	// A changed head invalidates whole-snapshot cache; no stale truncation.
	heads[len(heads)-1].Seq++
	wire.Reset()
	if !writeGroupHeads(heads, &sent, write) || !reflect.DeepEqual(parseGroupHeadFrames(t, wire.Bytes()), heads) {
		t.Fatal("changed tail head omitted")
	}
}

func TestGroupHeadDisconnectReplaysWholeSnapshot(t *testing.T) {
	heads := boundedGroupHeads(12000)
	sent := "previous"
	calls := 0
	var partial bytes.Buffer
	if writeGroupHeads(heads, &sent, func(format string, args ...any) bool {
		calls++
		if calls == 2 {
			return false
		}
		partial.WriteString(fmt.Sprintf(format, args...))
		return true
	}) {
		t.Fatal("disconnect reported success")
	}
	if sent != "previous" {
		t.Fatal("partial emission advanced cache")
	}
	first := parseGroupHeadFrames(t, partial.Bytes())
	if len(first) == 0 || len(first) >= len(heads) {
		t.Fatal("fixture not interrupted midset")
	}
	var replay bytes.Buffer
	reconnected := ""
	if !writeGroupHeads(heads, &reconnected, func(format string, args ...any) bool { replay.WriteString(fmt.Sprintf(format, args...)); return true }) {
		t.Fatal("reconnect failed")
	}
	got := parseGroupHeadFrames(t, replay.Bytes())
	if !reflect.DeepEqual(got, heads) || !reflect.DeepEqual(first, got[:len(first)]) {
		t.Fatal("reconnect lost heads or altered idempotent prefix")
	}
}

func TestGroupHeadEmptyAndOversizedFailClosed(t *testing.T) {
	var wire bytes.Buffer
	sent := ""
	if !writeGroupHeads(nil, &sent, func(format string, args ...any) bool { wire.WriteString(fmt.Sprintf(format, args...)); return true }) || wire.String() != "event: groups\ndata: []\n\n" {
		t.Fatalf("empty frame %q", wire.String())
	}
	old := sent
	wire.Reset()
	oversized := []protocol.GroupHead{{Conv: strings.Repeat("x", groupHeadsFrameLimit)}}
	if writeGroupHeads(oversized, &sent, func(format string, args ...any) bool { wire.WriteString(fmt.Sprintf(format, args...)); return true }) || sent != old || wire.Len() != 0 {
		t.Fatal("oversized head emitted or cache advanced")
	}
}

type groupOrderRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (r *groupOrderRecorder) SetWriteDeadline(time.Time) error { return nil }
func (r *groupOrderRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(p)
	if bytes.Contains(p, []byte("event: message\n")) {
		r.cancel()
	}
	return n, err
}

func TestGroupHeadBatchesPrecedeMessagesInActualStream(t *testing.T) {
	h, _, _ := testHub(t)
	alice, bob := joinMember(t, h, "stream-a"), joinMember(t, h, "stream-b")
	roster := personOf(t, h, bob)
	heads := boundedGroupHeads(12000)
	admins, _ := json.Marshal([]string{roster.Person})
	tx, err := h.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("INSERT INTO group_heads(conv,bootstrap,realm,seq,hash,admins)VALUES(?,?,?,?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	for _, head := range heads {
		if _, err = stmt.Exec(head.Conv, head.Bootstrap, protocol.NewID(), head.Seq, head.Hash, string(admins)); err != nil {
			t.Fatal(err)
		}
	}
	if err = stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	sendMessage(t, h, alice, bob)
	ad := protocol.SessionAd{Address: bob.addr, Session: protocol.NewID()}
	protocol.SignAd(&ad, bob.id.Sign)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/stream?ad="+ad.Encode(), nil).WithContext(ctx)
	protocol.SignRequest(req, bob.addr, bob.id.Sign, nil)
	recorder := &groupOrderRecorder{httptest.NewRecorder(), cancel}
	h.routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("stream %d %s", recorder.Code, recorder.Body.String())
	}
	var got []protocol.GroupHead
	message := false
	batches := 0
	for event, err := range sse.Read(bytes.NewReader(recorder.Body.Bytes()), &sse.ReadConfig{MaxEventSize: 2 * protocol.MaxBody}) {
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == "groups" {
			if message {
				t.Fatal("head batch after message")
			}
			var batch []protocol.GroupHead
			if err = json.Unmarshal([]byte(event.Data), &batch); err != nil {
				t.Fatal(err)
			}
			got = append(got, batch...)
			batches++
		}
		if event.Type == "message" {
			message = true
			if len(got) != len(heads) {
				t.Fatal("message overtook remaining heads")
			}
		}
	}
	if !message || batches < 2 || !reflect.DeepEqual(got, heads) {
		t.Fatalf("stream ordering/completeness: message=%v batches=%d heads=%d", message, batches, len(got))
	}
}
