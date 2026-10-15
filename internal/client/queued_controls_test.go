package client

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Four real signed memberships, then delivery paused after durable enqueue.
// This is a local discriminator, not an affected-device latency measurement.
func TestQueuedFourMemberControlsCommitBeforeDelivery(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	dave := proofReader(t, w, "dave")
	stopDave := runAgent(t, dave)
	defer stopDave()
	person, _, err := dave.store.selfPerson(dave.Address)
	if err != nil {
		t.Fatal(err)
	}
	adm, err := dave.SignGroupAdmission(packet.Root, packet.State.Seq+1, packet.State.Hash(), nil)
	if err != nil {
		t.Fatal(err)
	}
	next := packet
	next.Proof = nil
	next.State.Seq++
	next.State.Prev = packet.State.Hash()
	next.State.Members = slices.Clone(packet.State.Members)
	next.State.Members = append(next.State.Members, protocol.GroupMember{ConvMember: protocol.ConvMember{Person: person.info.Person, Roster: person.info.Roster}, Admission: adm})
	slices.SortFunc(next.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, next); err != nil {
		t.Fatal(err)
	}
	p := p6Member(t, w.alice, w.alice, next.State.Conv)
	for _, stop := range stops {
		stop()
	}
	turn, err := w.alice.SendConv(tctx(t), next.State.Conv, ConvOutgoing{Topic: "new", Body: "Before Save"})
	if err != nil {
		t.Fatal(err)
	}
	topics, err := w.alice.ChatTopics(next.State.Conv)
	if err != nil || len(topics) != 1 {
		t.Fatalf("topics=%d err=%v", len(topics), err)
	}
	listMembers(t, w.alice, map[string]bool{dave.Address: true}, w.alice.Address, w.bob.Address, "carol/carol", dave.Address)
	for _, action := range []string{"edit", "react", "unreact", "delete", "dismiss", "archive"} {
		t.Run(action, func(t *testing.T) {
			pause := pausePosts(w.alice)
			var once sync.Once
			release := func() { once.Do(func() { close(pause.release) }) }
			var mu sync.Mutex
			counts := map[string]int{}
			precommit := map[string]int{}
			w.alice.hub.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				mu.Lock()
				counts[req.Method+" "+req.URL.Path]++
				if queuedSend(req.Context()) {
					precommit[req.Method+" "+req.URL.Path]++
				}
				mu.Unlock()
				return pause.RoundTrip(req)
			})
			defer func() {
				release()
				joinControlPosts(t, w.alice)
				w.alice.hub.http.Transport = pause.base
			}()
			ctx, cancel := context.WithTimeout(WithQueuedSend(context.Background(), ""), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			start := time.Now()
			go func() {
				var e error
				switch action {
				case "edit":
					_, e = w.alice.Revise(ctx, ControlRef{Conv: next.State.Conv, ID: turn.LID, Fingerprint: w.alice.Self().Fingerprint()}, "After Save")
				case "react", "unreact":
					_, e = w.alice.React(ctx, ControlRef{Conv: next.State.Conv, ID: turn.LID, Fingerprint: w.alice.Self().Fingerprint()}, "👍", action == "unreact")
				case "delete":
					_, e = w.alice.Retract(ctx, ControlRef{Conv: next.State.Conv, ID: turn.LID, Fingerprint: w.alice.Self().Fingerprint()}, "")
				case "dismiss":
					_, e = w.alice.DismissParticipation(ctx, p.PID)
				case "archive":
					_, e = w.alice.ChangeChatTopic(ctx, next.State.Conv, topics[0].ID, "archive", "", topics[0].Count)
				}
				done <- e
			}()
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(time.Second):
				release()
				<-done
				t.Fatal("queued action waited for delivery after durable local commit")
			}
			acknowledged := time.Since(start)
			if action != "archive" {
				select {
				case <-pause.entered:
				case <-time.After(time.Second):
					t.Fatal("background post did not start")
				}
				if action == "dismiss" {
					state, e := w.alice.Participation(p.PID)
					if e != nil || state.State != PartDismissed {
						t.Fatalf("not locally dismissed: %s %v", state.State, e)
					}
				} else {
					c, e := w.alice.controlsOf(ControlRef{Conv: next.State.Conv, ID: turn.LID, Fingerprint: w.alice.Self().Fingerprint()})
					if e != nil {
						t.Fatal(e)
					}
					switch action {
					case "edit":
						if c.Shown("Before Save") != "After Save" {
							t.Fatal("edit not committed")
						}
					case "react":
						if len(c.Reactions) != 1 {
							t.Fatal("reaction not committed")
						}
					case "unreact":
						if len(c.Reactions) != 0 {
							t.Fatal("reaction removal not committed")
						}
					case "delete":
						if !c.Deleted {
							t.Fatal("deletion not committed")
						}
					}
					var lid string
					if e = w.alice.store.db.QueryRow(`SELECT lid FROM outbox WHERE conv=? AND ref_id=? ORDER BY rowid DESC LIMIT 1`, next.State.Conv, turn.LID).Scan(&lid); e != nil {
						t.Fatal(e)
					}
					var copies, suspended int
					if e = w.alice.store.db.QueryRow(`SELECT count(*),sum(recipient=?) FROM outbox WHERE conv=? AND lid=?`, dave.Address, next.State.Conv, lid).Scan(&copies, &suspended); e != nil || copies != 3 || suspended != 1 {
						t.Fatalf("copies=%d suspended copy=%d %v", copies, suspended, e)
					}
				}
			}
			release()
			if e := w.alice.FlushOutbox(ctx); e != nil {
				t.Fatal(e)
			}
			joinControlPosts(t, w.alice)

			mu.Lock()
			defer mu.Unlock()
			if action == "edit" || action == "react" || action == "unreact" {
				var requests, profiles int
				for path, n := range precommit {
					requests += n
					if strings.HasSuffix(path, "/profile") {
						profiles += n
					}
				}
				if requests != 0 || profiles != 0 {
					t.Fatalf("precommit requests=%d profile GETs=%d, want 0/0: %v", requests, profiles, precommit)
				}
			}
			t.Logf("members=4 action=%s elapsed=%s requests=%v", action, acknowledged, counts)
		})
	}
}

func TestQueuedDeviceControlKeepsCLISynchronous(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	waitNamedAgentCaps(t, w.bob)
	original, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	ref := ControlRef{ID: original.ID, Fingerprint: w.alice.Self().Fingerprint()}
	for _, queued := range []bool{true, false} {
		t.Run(map[bool]string{true: "queued", false: "cli"}[queued], func(t *testing.T) {
			p := pausePosts(w.alice)
			var once sync.Once
			release := func() { once.Do(func() { close(p.release) }) }
			defer func() {
				release()
				joinControlPosts(t, w.alice)
				w.alice.hub.http.Transport = p.base
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			if queued {
				ctx = WithQueuedSend(ctx, "")
			}
			done := make(chan error, 1)
			go func() { _, e := w.alice.Revise(ctx, ref, "After"); done <- e }()
			if queued {
				select {
				case e := <-done:
					if e != nil {
						release()
						cancel()
						t.Fatal(e)
					}
				case <-time.After(time.Second):
					release()
					<-done
					cancel()
					t.Fatal("queued device edit waited for delivery")
				}
				c, e := w.alice.controlsOf(ref)
				if e != nil || c.Shown("Before") != "After" {
					release()
					cancel()
					t.Fatalf("local device edit missing: %+v %v", c, e)
				}
				select {
				case <-p.entered:
				case <-time.After(time.Second):
					release()
					cancel()
					t.Fatal("background device control post missing")
				}
			} else {
				select {
				case <-p.entered:
				case e := <-done:
					release()
					cancel()
					t.Fatalf("CLI returned before post: %v", e)
				case <-time.After(time.Second):
					release()
					cancel()
					t.Fatal("CLI post missing")
				}
				select {
				case e := <-done:
					release()
					cancel()
					t.Fatalf("CLI stopped waiting: %v", e)
				case <-time.After(80 * time.Millisecond):
				}
			}
			release()
			if !queued {
				if e := <-done; e != nil {
					cancel()
					t.Fatal(e)
				}
			}
			if e := w.alice.FlushOutbox(ctx); e != nil {
				cancel()
				t.Fatal(e)
			}
			cancel()
		})
	}
	// Current author checks still run before any local queued control is stored.
	if _, e := w.alice.Revise(WithQueuedSend(tctx(t), ""), ControlRef{ID: original.ID, Fingerprint: w.bob.Self().Fingerprint()}, "Wrong author"); e == nil {
		t.Fatal("queued control skipped author check")
	}
}

// FlushOutbox can finish while a background pass is still completing receipts.
// The test must join that pass before replacing the shared HTTP transport.
func joinControlPosts(t *testing.T, a *Agent) {
	t.Helper()
	a.posting.Lock()
	done := a.posting.done
	a.posting.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("background control delivery did not finish")
		}
	}
}
