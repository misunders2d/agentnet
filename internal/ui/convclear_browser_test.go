package ui

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// TestBrowserConversationDeletionMatchesGo runs the browser engine's
// conversation deletion checks (testdata/convclear_engine_check.mjs) against
// Go-judged clear control vectors: every vector judged as
// envelope.ValidateControl judges it, the exact Go payload bytes, plus the
// engine's own-device deletion, release, late-copy, new-link, refusal,
// unfinished-work and device-thread checks.
func TestBrowserConversationDeletionMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	id := func(c string) string { return strings.Repeat(c, 32) }
	const fp = "01234567-89abcdef-01234567-89abcdef"
	conv := strings.Repeat("a", 64)
	body := func(c envelope.Clear) string { b, _ := json.Marshal(c); return string(b) }
	ok := envelope.Clear{Deletion: id("1"), Part: 1, Parts: 2, Turns: []envelope.Ref{{ID: id("2"), Fingerprint: fp}}}
	base := envelope.Inner{V: envelope.Version3, ID: id("3"), From: "alice/desk", To: "alice/phone", TS: 1790000000, Kind: envelope.KindMessage, Sub: envelope.SubClear,
		Body: body(ok), Ref: &envelope.Ref{ID: id("4"), Fingerprint: fp}, Conv: conv, LID: id("5"), Replica: true,
		Fan: []envelope.Fan{{Person: id("6"), Roster: strings.Repeat("b", 64)}}}
	with := func(change func(*envelope.Inner)) envelope.Inner { in := base; change(&in); return in }
	type vector struct {
		Name  string         `json:"name"`
		Inner envelope.Inner `json:"inner"`
		Valid bool           `json:"valid"`
	}
	vectors := []vector{
		{Name: "exact part", Inner: base},
		{Name: "single part no turns", Inner: with(func(in *envelope.Inner) { in.Body = body(envelope.Clear{Deletion: id("1"), Part: 1, Parts: 1}) })},
		{Name: "device thread", Inner: with(func(in *envelope.Inner) { in.Conv, in.LID, in.Replica, in.Fan = "", "", false, nil })},
		{Name: "a cut", Inner: with(func(in *envelope.Inner) { in.Body = strings.TrimSuffix(in.Body, "}") + `,"cut":true}` })},
		{Name: "anchors", Inner: with(func(in *envelope.Inner) { in.Body = strings.TrimSuffix(in.Body, "}") + `,"anchors":[]}` })},
		{Name: "part beyond parts", Inner: with(func(in *envelope.Inner) { in.Body = body(envelope.Clear{Deletion: id("1"), Part: 3, Parts: 2}) })},
		{Name: "no part", Inner: with(func(in *envelope.Inner) { in.Body = body(envelope.Clear{Deletion: id("1"), Parts: 1}) })},
		{Name: "too many parts", Inner: with(func(in *envelope.Inner) {
			in.Body = body(envelope.Clear{Deletion: id("1"), Part: 1, Parts: envelope.MaxClearParts + 1})
		})},
		{Name: "bad turn", Inner: with(func(in *envelope.Inner) {
			in.Body = body(envelope.Clear{Deletion: id("1"), Part: 1, Parts: 1, Turns: []envelope.Ref{{ID: "x", Fingerprint: fp}}})
		})},
		{Name: "no deletion", Inner: with(func(in *envelope.Inner) { in.Body = body(envelope.Clear{Part: 1, Parts: 1}) })},
		{Name: "files", Inner: with(func(in *envelope.Inner) { in.Attachments = []envelope.Attachment{{Name: "x"}} })},
		{Name: "no ref", Inner: with(func(in *envelope.Inner) { in.Ref = nil })},
	}
	for i := range vectors {
		vectors[i].Valid = envelope.ValidateControl(vectors[i].Inner) == nil
	}
	if !vectors[0].Valid || !vectors[1].Valid || vectors[2].Valid {
		t.Fatalf("Go judgement of the base vectors changed: %+v", vectors[:3])
	}
	input, _ := json.Marshal(map[string]any{"vectors": vectors, "body": body(ok)})
	cmd := exec.Command(node, "testdata/convclear_engine_check.mjs")
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	var got struct {
		Shapes map[string]bool `json:"shapes"`
		Checks int             `json:"checks"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil {
		t.Fatalf("%v\n%s", err, stdout.Bytes())
	}
	for _, v := range vectors {
		if valid, ok := got.Shapes[v.Name]; !ok || valid != v.Valid {
			t.Errorf("shared vector %s: browser valid=%v (judged %v), Go valid=%v", v.Name, valid, ok, v.Valid)
		}
	}
	if got.Checks < 30 {
		t.Fatalf("engine checks ran %d", got.Checks)
	}
	t.Logf("%d shared vectors agree; %d engine checks", len(vectors), got.Checks)
}
