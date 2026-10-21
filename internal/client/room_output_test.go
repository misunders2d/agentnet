package client

import (
	"net/http"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestRoomCompletedChildOutputAfterParentAnswer(t *testing.T) {
	stub := installAgentStub(t)
	Harnesses["roomparent"] = Harnesses["agentstub"]
	t.Cleanup(func() { delete(Harnesses, "roomparent") })
	w, _, packet, stops := groupTurnsFixture(t)
	a, conv := w.alice, packet.State.Conv
	var parts []ParticipationInfo
	for i, name := range []string{"parent", "child"} {
		entry, err := a.CreateLocalAgent(name, Responder{Harness: []string{"roomparent", "agentstub"}[i], Dir: stub.dir})
		if err != nil {
			t.Fatal(err)
		}
		if err = a.PublishAgentCatalog(tctx(t)); err != nil {
			t.Fatal(err)
		}
		p, err := a.InviteNamedAgent(tctx(t), conv, a.Address, entry.ID, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "named own membership", func() bool { return stateAt(t, a, p.PID).Claimable() })
		parts = append(parts, p)
	}
	for _, stop := range stops {
		stop()
	}
	claim := func(id, cause string) job {
		t.Helper()
		j, ok, _, _, _, err := a.store.claimAgentPageForCause("agentstub", a.Address, a.Self().Fingerprint(), 0, agentPage, conv, cause, nil,
			func(q dbq, j job) (*ExecutorStamp, error) { return a.ResolveExecutorIn(q, j.AgentID, nil) })
		if err != nil || !ok || j.ID != id {
			t.Fatalf("claim %s: %+v %v %v", id, j, ok, err)
		}
		return j
	}
	root, err := a.AskAgent(tctx(t), parts[0].PID, envelope.KindQuestion, "parent request")
	if err != nil {
		t.Fatal(err)
	}
	claim(root.ID, "")
	child, err := a.SendRoomAsk(tctx(t), root.ID, parts[1].PID, envelope.KindQuestion, "child request")
	if err != nil {
		t.Fatal(err)
	}
	j := claim(child.ID, root.ID)
	transport := a.hub.http.Transport
	a.hub.http.Transport = resetFailureTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method == "POST" && req.URL.Path == "/v1/messages" {
			return nil, errInjected
		}
		return transport.RoundTrip(req)
	})
	defer func() { a.hub.http.Transport = transport }()
	a.finishAgent(tctx(t), j, &Responder{Harness: "agentstub"}, envelope.StatusDone, "completed child answer\nemotion: calm")
	var output string
	if err = a.store.db.QueryRow(`SELECT result_id FROM inbox WHERE id=? AND state=?`, child.ID, stateAnswered).Scan(&output); err != nil {
		t.Fatal(err)
	}
	var copies int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE reply_to=? AND kind='answer' AND state=?`, child.LID, stateQueued).Scan(&copies); err != nil || copies == 0 {
		t.Fatalf("queued completed copies: %d %v", copies, err)
	}
	for _, state := range []string{stateRunning, stateAnswered, stateCancelled, stateJobFailed, stateNotDelivered} {
		t.Run(state, func(t *testing.T) {
			for _, update := range []struct {
				sql  string
				args []any
			}{
				{`UPDATE inbox SET state=? WHERE id=?`, []any{state, root.ID}},
				{`UPDATE inbox SET state=? WHERE id=?`, []any{stateAnswered, child.ID}},
				{`UPDATE outbox SET state=? WHERE reply_to=? AND kind='answer'`, []any{stateQueued, child.LID}},
			} {
				if _, err := a.store.db.Exec(update.sql, update.args...); err != nil {
					t.Fatal(err)
				}
			}
			if state == stateAnswered {
				for _, output := range []bool{false, true} {
					v, _, err := agentVerdict(a.store.db, j.agentReq(stateRunning), a.Address, a.Self().Fingerprint(), output, map[string]*partView{})
					if err != nil || v != verdictStop {
						t.Fatalf("completed parent authorized unfinished child: output=%v verdict=%d err=%v", output, v, err)
					}
				}
			}
			held, err := a.holdEndedOutputs("")
			if err != nil {
				t.Fatal(err)
			}
			if state == stateRunning || state == stateAnswered {
				if held != 0 || jobState(t, a, child.ID) != stateAnswered {
					t.Fatalf("completed reply invalidated by parent %s: held=%d state=%s", state, held, jobState(t, a, child.ID))
				}
			} else if held != copies {
				t.Fatalf("stopped parent released output: held=%d want=%d", held, copies)
			}
		})
	}
	// Only an exact completed result gets the exception, never progress or
	// another output merely pointing at an answered request.
	var status string
	if err = a.store.db.QueryRow(`SELECT coalesce(status,'') FROM outbox WHERE id=?`, output).Scan(&status); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"progress", "unbound"} {
		t.Run(invalid, func(t *testing.T) {
			if _, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id IN (?,?)`, stateAnswered, root.ID, child.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = a.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, stateQueued, output); err != nil {
				t.Fatal(err)
			}
			if invalid == "progress" {
				_, err = a.store.db.Exec(`UPDATE outbox SET status=? WHERE id=?`, envelope.StatusProgress, output)
			} else {
				_, err = a.store.db.Exec(`UPDATE inbox SET result_id=NULL WHERE id=?`, child.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if held, err := a.holdEndedOutputs(output); err != nil || held != 1 {
				t.Fatalf("%s output gained completion exception: held=%d err=%v", invalid, held, err)
			}
			if _, err = a.store.db.Exec(`UPDATE inbox SET result_id=? WHERE id=?`, output, child.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = a.store.db.Exec(`UPDATE outbox SET status=NULLIF(?,'') WHERE id=?`, status, output); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Completion does not outlive current participation authority.
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id IN (?,?)`, stateAnswered, root.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.db.Exec(`UPDATE outbox SET state=? WHERE reply_to=? AND kind='answer'`, stateQueued, child.LID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DismissParticipation(tctx(t), parts[0].PID); err != nil {
		t.Fatal(err)
	}
	if held, err := a.holdEndedOutputs(""); err != nil || held != copies {
		t.Fatalf("dismissed parent participation released output: held=%d want=%d err=%v", held, copies, err)
	}
}
