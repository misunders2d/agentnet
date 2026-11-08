package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Reactions, edits and deletes are local changes: a stalled request must not make
// them wait, in a chat or a group (owner, MEL-748: "Updating your reaction…").
func TestLiveControlsAcceptThroughAStalledRequest(t *testing.T) {
	alice, bob, live, hub := liveWorldHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
	}
	runDaemon(t, alice)
	var dm string
	for deadline := time.Now().Add(20 * time.Second); dm == ""; time.Sleep(50 * time.Millisecond) {
		c, err := bob.CreateDM(ctx, alice.Address)
		if err == nil {
			dm = c
		} else if time.Now().After(deadline) {
			t.Fatalf("no DM: %v", err)
		}
	}
	group := liveTwoMemberGroup(t, alice, bob)
	row := func(conv, lid string) (DMMessage, bool) {
		thread, err := live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(thread.Messages, func(m DMMessage) bool { return m.LID == lid })
		if i < 0 {
			return DMMessage{}, false
		}
		return thread.Messages[i], true
	}
	for _, c := range []struct{ name, conv string }{{"dm", dm}, {"group", group}} {
		mine, err := bob.SendConv(ctx, c.conv, client.ConvOutgoing{Body: "my own words"})
		// Bob's copy of the new group arrives on his own stream after Alice
		// publishes it; until then the send is refused before anything is written.
		for deadline := time.Now().Add(20 * time.Second); err != nil && strings.Contains(err.Error(), "no conversation "+c.conv+" here") && time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
			mine, err = bob.SendConv(ctx, c.conv, client.ConvOutgoing{Body: "my own words"})
		}
		if err != nil {
			t.Fatal(err)
		}
		gone, err := bob.SendConv(ctx, c.conv, client.ConvOutgoing{Body: "going away"})
		if err != nil {
			t.Fatal(err)
		}
		theirs, err := alice.SendConv(ctx, c.conv, client.ConvOutgoing{Body: "their words"})
		if err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if _, ok := row(c.conv, theirs.LID); ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("their message never arrived")
			}
		}
		theirRow, _ := row(c.conv, theirs.LID)
		mineRow, _ := row(c.conv, mine.LID)
		goneRow, _ := row(c.conv, gone.LID)

		release := make(chan struct{})
		var once sync.Once
		free := func() {
			once.Do(func() {
				hub.SetHold(nil)
				close(release)
			})
		}
		defer free()
		hub.SetHold(func(r *http.Request) {
			if r.Header.Get(protocol.HeaderAgent) == bob.Address && (strings.HasPrefix(r.URL.Path, "/v1/persons/") || r.Method == "POST" && r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/members") {
				<-release
			}
		})
		for _, action := range []struct {
			name string
			run  func() (string, error)
			done func() bool
		}{
			{"react", func() (string, error) {
				return live.React(ControlAction{Conv: c.conv, ID: theirRow.ID, Dir: theirRow.Dir, Emoji: "👍"})
			}, func() bool {
				m, ok := row(c.conv, theirs.LID)
				return ok && len(m.Reactions) == 1
			}},
			{"edit", func() (string, error) {
				return live.EditMessage(ControlAction{Conv: c.conv, ID: mineRow.ID, Dir: mineRow.Dir, Text: "better words"})
			}, func() bool {
				m, ok := row(c.conv, mine.LID)
				raw, _ := json.Marshal(m)
				return ok && strings.Contains(string(raw), "better words")
			}},
			{"delete", func() (string, error) {
				return live.DeleteMessage(ControlAction{Conv: c.conv, ID: goneRow.ID, Dir: goneRow.Dir})
			}, func() bool {
				m, ok := row(c.conv, gone.LID)
				return !ok || m.Body == ""
			}},
		} {
			started := time.Now()
			note, err := action.run()
			took := time.Since(started)
			t.Logf("%s %s: %v note=%q err=%v", c.name, action.name, took, note, err)
			if err != nil {
				t.Errorf("%s %s failed after %v: %v", c.name, action.name, took, err)
				continue
			}
			if took >= time.Second {
				t.Errorf("%s %s waited %v for a stalled request", c.name, action.name, took)
			}
			if !action.done() {
				t.Errorf("%s %s: not shown locally", c.name, action.name)
			}
		}
		free()
	}
}
