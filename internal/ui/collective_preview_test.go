package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestLiveCollectivePreview(t *testing.T) {
	alice, bob, _, dm, _, _ := liveGuestWorld(t)
	group := liveTwoMemberGroup(t, alice, bob)
	live := NewLive(alice)
	refs := make([]string, 1000)
	for i := range refs {
		refs[i] = fmt.Sprintf("p.%032x", i)
	}
	body := "[@Amazon_team](agentnet:collective/v1/" + strings.Join(refs, ",") + ") Check inventory\nRetain the signed body."
	for _, conv := range []string{dm, group} {
		sent, err := alice.SendConv(context.Background(), conv, client.ConvOutgoing{Body: body})
		if err != nil {
			t.Fatal(err)
		}
		check := func(want string) {
			t.Helper()
			o, err := live.Overview()
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range o.DMs {
				if d.ID == conv {
					if d.Last != want {
						t.Fatalf("preview %q, want %q", d.Last, want)
					}
					return
				}
			}
			t.Fatal("conversation missing")
		}
		check("@Amazon_team Check inventory")
		view, err := live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		for _, m := range view.Messages {
			if m.LID == sent.LID {
				id = m.ID
				if m.Body != body {
					t.Fatal("preview rewrote original body")
				}
			}
		}
		if id == "" {
			t.Fatal("sent row absent")
		}
		edited := strings.Replace(body, "Amazon_team", "Команда", 1)
		if _, err = live.EditMessage(ControlAction{Conv: conv, ID: id, Dir: "out", Text: edited}); err != nil {
			t.Fatal(err)
		}
		check("@Команда Check inventory")
		view, err = live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range view.Messages {
			if m.ID == id && (m.Body != body || !m.Edited || m.Text != edited) {
				t.Fatal("preview rewrote original or edited message bytes")
			}
		}
	}
	direct, err := alice.Send(context.Background(), bob.Address, "Original direct body", "")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := alice.RefOf("", direct.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	directEdit := strings.Replace(body, "Amazon_team", "Direct_team", 1)
	if _, err = alice.Revise(context.Background(), ref, directEdit); err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify actual chat-list output")
	}
	o, err := live.Overview()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, thread := range o.Threads {
		if thread.ID == direct.ID {
			found = true
			if thread.Last != "@Direct_team Check inventory" {
				t.Fatalf("native direct overview preview %q", thread.Last)
			}
		}
	}
	if !found {
		t.Fatal("direct thread missing in native overview")
	}
	input, err := json.Marshal(map[string]any{"overview": o, "conversations": []string{dm, group}, "direct": direct.ID})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), node, "testdata/collective_preview_check.mjs")
	cmd.Stdin = bytes.NewReader(input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
