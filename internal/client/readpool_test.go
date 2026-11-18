package client

import (
	"path/filepath"
	"testing"
	"time"
)

// A chat's projection reads beside a held write: sync, catch-up and sends take
// the one writer, and the page's reads must not queue behind them (S1).
func TestChatProjectionReadsBesideAHeldWrite(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	if _, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "before the held write"}); err != nil {
		t.Fatal(err)
	}
	tx, err := a.store.db.DB.Begin() // immediate: the writer is held
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO config(k,v) VALUES('readpool-test','held')`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			if _, err := a.ContinuityConversation(conv); err != nil {
				return err
			}
			if _, err := a.MessagingConversations(); err != nil {
				return err
			}
			msgs, err := a.MessagingConversationMessages(conv)
			if err != nil {
				return err
			}
			if len(msgs) == 0 {
				t.Error("the sent message is not read")
			}
			for _, m := range msgs {
				if _, err := a.ContinuationFor(m.ID, m.Key, m.Exec); err != nil {
					return err
				}
				if _, err := a.StopFor(m.ID, m.Key, m.Exec); err != nil {
					return err
				}
				if _, err := a.ProposalOf(m.ID); err != nil {
					return err
				}
			}
			if _, err := a.ChatMainTopicForMessages(conv, msgs); err != nil {
				return err
			}
			if _, err := a.ChatTopicsForMessages(conv, msgs, ChatTopicAssignments(msgs)); err != nil {
				return err
			}
			if _, err := a.MessagingParticipations(conv); err != nil {
				return err
			}
			if _, err := a.QueuedSends(conv); err != nil {
				return err
			}
			if _, err := a.KnownPersons(); err != nil {
				return err
			}
			if _, err := a.MessagingPersons(); err != nil {
				return err
			}
			if _, _, err := a.Person(); err != nil {
				return err
			}
			if _, _, err := a.ViewDirtySince(0); err != nil {
				return err
			}
			_, _, err = a.UnreadDirtySince(0)
			return err
		}()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the chat's projection waited behind a held write")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n, ok := a.store.rowChanges(); !ok || n == 0 {
		t.Fatalf("total_changes left the writer: %d %v", n, ok)
	}
}

// The reader pool keeps one connection. Each new SQLite connection parses the
// whole schema on its first statement (about 8 ms, 250 ms under -race); with
// four, every loop a daemon starts opened its own at once and the start, and
// with it the capability record peers wait for, came late (v0.11.6).
func TestReaderPoolIsOneConnection(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if n := s.db.reads.Stats().MaxOpenConnections; n != 1 {
		t.Fatalf("reader pool allows %d connections", n)
	}
	if n := s.db.reads.Stats().OpenConnections; n != 1 {
		t.Fatalf("reader opened %d connections at open, want 1 ready", n)
	}
}
