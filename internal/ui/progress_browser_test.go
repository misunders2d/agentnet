package ui

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// TestBrowserProgressMatchesGo checks the browser's progress shape guard
// against the shared Go vectors (envelope.TestProgressShapeVectors, written
// fresh by the Go test) and against envelope.Seal case by case, that progress
// never clears Waiting, and that the browser's signed caps read prg1 and
// hgp1.
func TestBrowserProgressMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	alice, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	bob, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := bob.Public("bob/b").Recipient()
	if err != nil {
		t.Fatal(err)
	}
	base := envelope.Inner{V: envelope.Version, ID: strings.Repeat("1", 32), From: "alice/a", To: "bob/b", TS: 1700000000,
		Kind: envelope.KindMessage, Body: "accepted; checking", ReplyTo: strings.Repeat("2", 32), Status: envelope.StatusProgress}
	file := envelope.Attachment{Blob: envelope.Blob{ID: strings.Repeat("3", 32), Size: 100, SHA256: strings.Repeat("4", 64)}, Name: "x.txt", Size: 10, SHA256: strings.Repeat("5", 64)}
	cases := map[string]func(*envelope.Inner){
		"valid":           func(in *envelope.Inner) {},
		"byte order mark": func(in *envelope.Inner) { in.Body = "\ufeff" }, // not blank to strings.TrimSpace
		"ordinary reply":  func(in *envelope.Inner) { in.Status = "" },
		"version 2": func(in *envelope.Inner) {
			in.V, in.Conv, in.LID, in.Root = envelope.Version2, strings.Repeat("6", 64), strings.Repeat("7", 32), json.RawMessage(`{}`)
		},
		"not a message":   func(in *envelope.Inner) { in.Kind = envelope.KindAnswer },
		"question":        func(in *envelope.Inner) { in.Kind = envelope.KindQuestion },
		"no request":      func(in *envelope.Inner) { in.ReplyTo = "" },
		"empty text":      func(in *envelope.Inner) { in.Body = " \n\t" },
		"next line only":  func(in *envelope.Inner) { in.Body = "\u0085\u3000" }, // blank to Go, not to JS trim
		"with attachment": func(in *envelope.Inner) { in.Attachments = []envelope.Attachment{file} },
		"named author":    func(in *envelope.Inner) { in.AgentID = strings.Repeat("8", 32) },
		"execution target": func(in *envelope.Inner) {
			in.Target = &envelope.Target{Address: "bob/b", Fingerprint: bob.Public("bob/b").Fingerprint()}
		},
	}
	type item struct {
		Name  string         `json:"name"`
		Inner map[string]any `json:"inner"`
	}
	var items []item
	want := map[string]string{}
	for name, change := range cases {
		in := base
		change(&in)
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(raw, &m)
		if root, ok := m["root"]; ok { // the browser takes a root as its JSON text
			text, _ := json.Marshal(root)
			m["root"] = string(text)
		}
		items = append(items, item{name, m})
		switch _, err := envelope.Seal(in, alice.Sign, recipient); {
		case err == nil:
			want[name] = "ok"
		case strings.Contains(err.Error(), "progress is a plain-text update replying to one request"):
			want[name] = "progress"
		default:
			want[name] = "other: " + err.Error()
		}
	}
	if want["valid"] != "ok" || want["next line only"] != "progress" || want["byte order mark"] != "ok" {
		t.Fatalf("Go verdicts = %v", want)
	}
	path := filepath.Join(t.TempDir(), "vectors.json")
	gen := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "-count=1", "-run", "^TestProgressShapeVectors$", "github.com/misunders2d/agentnet/internal/envelope")
	gen.Env = append(os.Environ(), "AGENTNET_PROGRESS_VECTORS="+path)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("Go vectors: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name  string          `json:"name"`
		Inner json.RawMessage `json:"inner"`
		Valid bool            `json:"valid"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil || len(vectors) < 14 {
		t.Fatalf("vectors: %d, %v", len(vectors), err)
	}
	input, _ := json.Marshal(map[string]any{"cases": items, "vectors": vectors})
	cmd := exec.Command(node, "testdata/progress_browser_check.mjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got struct {
		Shapes   map[string]bool   `json:"shapes"`
		Verdicts map[string]string `json:"verdicts"`
		Waiting  string            `json:"waiting"`
		Caps     []string          `json:"caps"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, v := range vectors {
		if got.Shapes[v.Name] != v.Valid {
			t.Errorf("shared vector %s: browser valid=%v, Go valid=%v", v.Name, got.Shapes[v.Name], v.Valid)
		}
	}
	for name, verdict := range want {
		if got.Verdicts[name] != verdict {
			t.Errorf("%s: browser %q, Go %q", name, got.Verdicts[name], verdict)
		}
	}
	record := protocol.CapsRecord{Caps: got.Caps}
	if got.Waiting != "ok" || !record.Reads(protocol.CapProgress) || !record.Reads(protocol.CapHumanParticipation) || len(got.Caps) > protocol.MaxAdvertisedCaps {
		t.Fatalf("waiting %q negotiated readers %v", got.Waiting, got.Caps)
	}
	roomOnly := protocol.CapsRecord{Caps: []string{protocol.CapRoom}}
	for _, cap := range []string{protocol.CapGroupHumanParticipation, protocol.CapGroupInvitationControl, protocol.CapReadSync} {
		if !slices.Contains(got.Caps, cap) || roomOnly.Reads(cap) {
			t.Fatalf("new capability %s must remain explicit: %v", cap, got.Caps)
		}
	}
}
