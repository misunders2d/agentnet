package client

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/protocol"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func humanLinked(t *testing.T, a *Agent) *Agent {
	t.Helper()
	phone, awaited, _ := linkPhone(t, a, "phone")
	req := pendingLink(t, a)
	if err := a.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	fakeNotify(phone)
	humanTestCaps(t, phone)
	a.convWork.due(convRetry)
	a.kickNow()
	return phone
}
func TestHumanHistoryUnchangedDescriptorOriginalOnlyFiles(t *testing.T) {
	w, carol, conv, _, stub := humanWorld(t)
	p, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest invited", func() bool { return stateAt(t, carol, p.PID).State == PartInvited })
	if _, err = carol.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest active", func() bool { return stateAt(t, w.alice, p.PID).HumanActive() })
	file := filepath.Join(t.TempDir(), "guest-history.txt")
	os.WriteFile(file, []byte("SYNTHETIC_RETAINED_GUEST_FILE"), 0600)
	sent, err := carol.SendConv(tctx(t), conv, ConvOutgoing{PID: p.PID, Body: "human retained turn", Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest turn on original", func() bool { return humanBodyCount(t, w.alice, conv, "human retained turn") == 1 })
	if _, err = w.alice.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest ended", func() bool { return stateAt(t, carol, p.PID).State == PartDismissed })
	phone := humanLinked(t, w.alice)
	eventually(t, "original member own linked history", func() bool { return humanBodyCount(t, phone, conv, "human retained turn") == 1 })
	msgs, _ := phone.ConversationMessages(conv)
	var id string
	for _, m := range msgs {
		if m.LID == sent.LID {
			if !m.History || m.Human == nil || m.Human.AuthorPID != p.PID || m.Job != "" {
				t.Fatalf("human history lost provenance/nonexecution: %+v", m)
			}
			id = m.ID
			h, err := storedHuman(phone.store.db, "in", m.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(h)
			src, _ := w.alice.ConversationMessages(conv)
			for _, original := range src {
				if original.LID == sent.LID {
					want, _ := json.Marshal(original.Human)
					if string(got) != string(want) {
						t.Fatal("history changed captured scope")
					}
				}
			}
		}
	}
	if err := phone.RequestFile(tctx(t), id, 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "human history file bytes", func() bool {
		r, _, err := phone.OpenFileFrom(tctx(t), "in", id, 0)
		if err != nil {
			return false
		}
		defer r.Close()
		b, err := io.ReadAll(r)
		return err == nil && string(b) == "SYNTHETIC_RETAINED_GUEST_FILE"
	})
	// A guest's other installation inherits identity, NOT original membership,
	// accepted HostHere, selected context, historical audience or file access.
	guestPhone := humanLinked(t, carol)
	eventually(t, "guest own history scan completes", func() bool { jobs, _ := carol.HistoryProgress(); return len(jobs) > 0 && jobs[0].State == "done" })
	rows, _ := guestPhone.Conversations()
	for _, c := range rows {
		if c.ID == conv {
			t.Fatal("guest linked installation inherited original conversation")
		}
	}
	if _, err = guestPhone.SendConv(tctx(t), conv, ConvOutgoing{PID: p.PID, Body: "linked guest widening"}); err == nil {
		t.Fatal("guest linked installation sent")
	}
	if stub.runs() != 0 {
		t.Fatal("human history executed")
	}
	// Ended guest and pending third party cannot be a history reader even if
	// carrying an unchanged descriptor and a root signed by original members.
	root, _ := rootOf(t, w.alice, conv)
	if len(root.Members) != 2 || root.Kind != protocol.ConvKindDM {
		t.Fatal("history changed original root")
	}
}
