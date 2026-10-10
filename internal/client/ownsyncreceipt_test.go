package client

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// ackLog records the receipts one device gave its relay, by message id.
type ackLog struct {
	mu    sync.Mutex
	state map[string][]string
}

func (l *ackLog) of(id string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.state[id]...)
}

type ackRecorder struct {
	base http.RoundTripper
	log  *ackLog
}

func (r ackRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	id, ok := strings.CutPrefix(req.URL.Path, "/v1/messages/")
	if req.Method != http.MethodPost || !ok || !strings.HasSuffix(id, "/ack") || req.Body == nil {
		return r.base.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	resp, err := r.base.RoundTrip(req)
	if err == nil && resp.StatusCode/100 == 2 {
		var ack protocol.AckRequest
		json.Unmarshal(body, &ack)
		r.log.mu.Lock()
		r.log.state[strings.TrimSuffix(id, "/ack")] = append(r.log.state[strings.TrimSuffix(id, "/ack")], ack.State)
		r.log.mu.Unlock()
	}
	return resp, err
}

// recordAcks logs every receipt a sends its relay; call before its daemon runs.
func recordAcks(a *Agent) *ackLog {
	l := &ackLog{state: map[string][]string{}}
	a.hub.http.Transport = ackRecorder{a.hub.http.Transport, l}
	return l
}

// linkedRecording is linked, with the phone's receipts recorded.
func linkedRecording(t *testing.T, a *Agent) (*Agent, *ackLog) {
	t.Helper()
	phone, awaited, _ := linkPhone(t, a, "phone")
	if err := a.DecideLink(tctx(t), pendingLink(t, a).ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	acks := recordAcks(phone)
	runAgent(t, phone)
	return phone, acks
}

// ownCarriers lists from's copies of sub sealed for to, oldest first.
func ownCarriers(t *testing.T, from, to *Agent, sub string) []envelope.Envelope {
	t.Helper()
	rows, err := from.store.db.Query(`SELECT envelope FROM outbox WHERE sub=? AND recipient=? ORDER BY rowid`, sub, to.Address)
	if err != nil {
		t.Fatal(err)
	}
	var out []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &env)
		}
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		out = append(out, env)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// receipted waits until from has copies of sub for to and each is receipted:
// to told the relay delivered, and from's copy left custody as delivered.
// The relay then answers delivered for each. It returns the newest copy.
func receipted(t *testing.T, from, to *Agent, acks *ackLog, sub string) envelope.Envelope {
	t.Helper()
	var envs []envelope.Envelope
	eventually(t, sub+" from "+from.Address+" receipted by "+to.Address, func() bool {
		envs = ownCarriers(t, from, to, sub)
		for _, e := range envs {
			if got := acks.of(e.ID); len(got) == 0 || got[len(got)-1] != protocol.StateDelivered || outboxState(t, from, e.ID) != protocol.StateDelivered {
				return false
			}
		}
		return len(envs) > 0
	})
	for _, e := range envs {
		if s := state(t, from, e.ID); s != protocol.StateDelivered {
			t.Fatalf("the relay holds %s %s as %s", sub, e.ID, s)
		}
	}
	return envs[len(envs)-1]
}

// redeliver hands env to to again, as a relay that missed its receipt would
// push it on its next connection. What env applied is undone here first: a
// re-delivery must not apply it again (applied stays empty), and is
// receipted again as delivered.
func redeliver(t *testing.T, to *Agent, acks *ackLog, env envelope.Envelope, undo, applied string) {
	t.Helper()
	before := len(acks.of(env.ID))
	if _, err := to.store.db.Exec(undo); err != nil {
		t.Fatal(err)
	}
	if err := to.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if n := count(t, to, applied); n != 0 {
		t.Fatalf("a re-delivered carrier was applied again: %d rows in %s", n, applied)
	}
	eventually(t, "the re-delivered carrier receipted again", func() bool {
		got := acks.of(env.ID)
		return len(got) > before && got[len(got)-1] == protocol.StateDelivered
	})
}

// MIXED-1: own-device carriers that store no inbox row of their own (read,
// topic name and mark, invitation view sync, history file requests and
// offers) and a conversation deletion were stored without a receipt: the
// relay kept them in custody and pushed them again on every connection,
// ahead of everything behind them, and their sender's copy never left
// custody. Each is receipted now, and a re-delivery is receipted again
// without being applied twice.
func TestOwnSyncCarriersReceiptedOnce(t *testing.T) {
	w := newWorld(t, "")
	laptopAcks := recordAcks(w.alice)
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	for _, a := range []*Agent{w.alice, w.bob} {
		publishGroupFixtureCaps(t, a, true)
	}
	conv := newDM(t, w.bob, w.alice)
	read := sendConv(t, w.bob, conv, ConvOutgoing{Body: "a turn to read", Topic: "new"})
	eventually(t, "the DM on the laptop", func() bool { return len(convBodies(t, w.alice, conv)) == 1 })
	path, data := writeFile(t, t.TempDir(), "notes.txt", 100)
	sendConv(t, w.alice, conv, ConvOutgoing{Body: "a file for later", Files: []OutgoingFile{{Path: path}}})
	eventually(t, "both turns on the laptop", func() bool { return len(convBodies(t, w.alice, conv)) == 2 })
	phone, phoneAcks := linkedRecording(t, w.alice)
	eventually(t, "the history on the phone", func() bool { return len(convBodies(t, phone, conv)) == 2 })

	t.Run("read-sync", func(t *testing.T) {
		var id string
		if err := w.alice.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=?`, conv, read.LID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := w.alice.MarkRead([]string{id}); err != nil {
			t.Fatal(err)
		}
		env := receipted(t, w.alice, phone, phoneAcks, envelope.SubReadSync)
		redeliver(t, phone, phoneAcks, env, `DELETE FROM read_marks`, "read_marks")
		// An older program stored the same carrier without any disposition:
		// pushed again after its update, it is applied (idempotently) and
		// receipted at last.
		if _, err := phone.store.db.Exec(`DELETE FROM history_receipts WHERE id=?`, env.ID); err != nil {
			t.Fatal(err)
		}
		before := len(phoneAcks.of(env.ID))
		if err := phone.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if count(t, phone, "read_marks") == 0 || len(phoneAcks.of(env.ID)) == before {
			t.Fatal("a carrier an older program kept unreceipted was not applied and receipted")
		}
	})

	var topic string
	eventually(t, "the topic on the laptop", func() bool {
		ts, err := w.alice.ChatTopics(conv)
		if err != nil || len(ts) != 1 {
			return false
		}
		topic = ts[0].ID
		return true
	})
	t.Run("topic-sync", func(t *testing.T) {
		if _, err := w.alice.ChangeChatTopic(tctx(t), conv, topic, "rename", "A private name", 0); err != nil {
			t.Fatal(err)
		}
		env := receipted(t, w.alice, phone, phoneAcks, envelope.SubTopicSync)
		redeliver(t, phone, phoneAcks, env, `DELETE FROM topic_titles`, "topic_titles")
	})
	t.Run("topic-state-sync", func(t *testing.T) {
		if _, err := w.alice.ChangeChatTopic(tctx(t), conv, topic, "archive", "", 0); err != nil {
			t.Fatal(err)
		}
		env := receipted(t, w.alice, phone, phoneAcks, envelope.SubTopicStateSync)
		redeliver(t, phone, phoneAcks, env, `DELETE FROM topic_marks`, "topic_marks")
	})
	t.Run("file", func(t *testing.T) {
		var id string
		if err := phone.store.db.QueryRow(`SELECT message_id FROM attachments WHERE name='notes.txt'`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := phone.RequestFile(tctx(t), id, 0); err != nil {
			t.Fatal(err)
		}
		request := receipted(t, phone, w.alice, laptopAcks, envelope.SubFile)
		offer := receipted(t, w.alice, phone, phoneAcks, envelope.SubFile)
		redeliver(t, w.alice, laptopAcks, request, `DELETE FROM file_serves`, "file_serves")
		sum := sha(data)
		redeliver(t, phone, phoneAcks, offer, `UPDATE attachments SET blob_id='`+historyBlob+`0' WHERE sha256='`+sum+`'`,
			`attachments WHERE sha256='`+sum+`' AND blob_id NOT LIKE '`+historyBlob+`%'`)
	})
	t.Run("invitation-sync", func(t *testing.T) {
		g, err := w.alice.CreateGroup(tctx(t), "Receipted views")
		if err != nil {
			t.Fatal(err)
		}
		bob, _, err := w.bob.store.selfPerson(w.bob.Address)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.alice.InviteGroup(tctx(t), g.Root.ID(), bob.roster.Person, nil); err != nil {
			t.Fatal(err)
		}
		env := receipted(t, w.alice, phone, phoneAcks, envelope.SubInvitationSync)
		redeliver(t, phone, phoneAcks, env, `DELETE FROM own_invitation_views`, "own_invitation_views")
	})
	t.Run("same-logical-copy", func(t *testing.T) {
		// One logical turn under a second envelope ID: stored once, and
		// each copy is receipted.
		_, root := rootOf(t, w.alice, conv)
		in := envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Body: "sent twice"}
		for i := 0; i < 2; i++ {
			env := craft(t, w.bob, w.alice, in)
			if err := w.bob.hub.do(tctx(t), "POST", "/v1/messages", env, nil); err != nil {
				t.Fatal(err)
			}
			eventually(t, "copy receipted", func() bool {
				got := laptopAcks.of(env.ID)
				return len(got) > 0 && got[len(got)-1] == protocol.StateDelivered
			})
			if s := state(t, w.bob, env.ID); s != protocol.StateDelivered {
				t.Fatalf("the relay holds copy %d as %s", i, s)
			}
		}
		if n := inboxCount(t, w.alice, `lid=?`, in.LID); n != 1 {
			t.Fatalf("one logical turn stored %d times", n)
		}
	})
	t.Run("clear", func(t *testing.T) {
		if _, err := w.alice.DeleteConversation(tctx(t), conv); err != nil {
			t.Fatal(err)
		}
		env := receipted(t, w.alice, phone, phoneAcks, envelope.SubClear)
		redeliver(t, phone, phoneAcks, env, `DELETE FROM conv_erased`, "conv_erased")
	})
}

// MIXED-1: a control under a stored control's key and logical id but with
// other content was dropped with no disposition at all, so the relay kept
// it in custody. It is held as a conflicting duplicate (quarantined).
func TestConflictingControlCopyHeld(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	turn := sendConv(t, w.bob, conv, ConvOutgoing{Body: "react to me"})
	eventually(t, "the turn at alice", func() bool { return inboxCount(t, w.alice, `lid=?`, turn.LID) == 1 })
	recipient, err := w.alice.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	lid := protocol.NewID()
	var ids []string
	for _, r := range []envelope.Reaction{{Emoji: "❤️", Op: "add", N: 1}, {Emoji: "👍", Op: "add", N: 1}} {
		body, _ := json.Marshal(r)
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
			Sub: envelope.SubReaction, Body: string(body), Conv: conv, LID: lid, Ref: &envelope.Ref{ID: turn.LID, Fingerprint: w.bob.Self().Fingerprint()}}
		env, err := envelope.Seal(in, w.bob.id.Sign, recipient)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.alice.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, env.ID)
	}
	if inboxCount(t, w.alice, `id=?`, ids[0]) != 1 || heldReason(t, w.alice, ids[0]) != "" {
		t.Fatal("the first control was not stored")
	}
	if got := heldReason(t, w.alice, ids[1]); got != reasonDuplicate {
		t.Fatalf("the conflicting copy is held as %q", got)
	}
	if d, err := w.alice.store.disposition(ids[1]); err != nil || d != protocol.StateQuarantined {
		t.Fatalf("the conflicting copy's disposition: %q %v", d, err)
	}
}
