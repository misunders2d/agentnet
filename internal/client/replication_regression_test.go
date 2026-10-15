package client

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestHistoryArchiveFinalProducerPageWakesWorker(t *testing.T) {
	t.Run("queued-sync-before-producer", func(t *testing.T) {
		_, source, phone, conv := archiveFixture(t, 1)
		source.stopBackgroundPosts()
		source.stopArchivePosts()
		rows, err := source.historySourceRows(source.store.db, "conv=? AND dir='in'", "conv,ms,id", 1, conv)
		if err != nil || len(rows) != 1 {
			t.Fatalf("retained history source: %d %v", len(rows), err)
		}
		_, root := rootOf(t, source, conv)
		copy, err := source.historyCopy(phone.Self(), conv, root, itemOf(rows[0].in, rows[0].key, rows[0].pos.Ms))
		if err != nil {
			t.Fatal(err)
		}
		if err = source.store.addConvOutbox([]outCopy{copy}, envelope.Inner{}, nil, ""); err != nil {
			t.Fatal(err)
		}
		if err = source.store.resendReceipt(rows[0].in.ID); err != nil {
			t.Fatal(err)
		}
		release, err := lockfile.Wait(source.spoolLockPath())
		if err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		unblock := func() { once.Do(release) }
		defer unblock()
		base := source.hub.http.Transport
		posted := make(chan bool, 1)
		var receipt atomic.Bool
		source.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/v1/messages/"+rows[0].in.ID+"/ack" {
				receipt.Store(true)
			}
			if r.Method == "POST" && r.URL.Path == "/v1/messages" {
				body, err := r.GetBody()
				if err != nil {
					return nil, err
				}
				var env envelope.Envelope
				err = json.NewDecoder(body).Decode(&env)
				body.Close()
				if err != nil {
					return nil, err
				}
				if env.ID == copy.env.ID {
					resp, err := base.RoundTrip(r)
					if err == nil {
						posted <- receipt.Load()
					}
					return resp, err
				}
			}
			return base.RoundTrip(r)
		})
		defer func() { source.hub.http.Transport = base }()
		ctx, cancel := context.WithCancel(tctx(t))
		defer cancel()
		source.convWork.take()
		source.convWork.due(convHistory)
		done := make(chan struct{})
		go func() { source.sync(ctx); close(done) }()
		defer func() { cancel(); unblock(); <-done }()
		early, receiptFirst := false, false
		select {
		case receiptFirst = <-posted:
			early = true
		case <-time.After(2 * time.Second):
		}
		// The same sync resumes its existing producer after the lock releases.
		unblock()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("sync did not finish after releasing the producer")
		}
		if !early {
			t.Fatal("already-queued sync copy waited behind the blocked bulk producer")
		}
		if !receiptFirst {
			t.Fatal("queued sync copy overtook durable receipts")
		}
		state, _, _, err := source.store.outboxState(copy.env.ID)
		if err != nil || state != protocol.StateCustody {
			t.Fatalf("exact queued copy did not reach relay custody: %s %v", state, err)
		}
		var stored string
		if err = source.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, copy.env.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		original, _ := json.Marshal(copy.env)
		if stored != string(original) {
			t.Fatal("queued delivery replaced the original signed envelope")
		}
	})
	_, source, phone, _ := archiveFixture(t, 1)
	ctx, cancel := context.WithCancel(tctx(t))
	defer cancel()
	// Drain the external wake before any source child exists. The producer
	// commits one final page afterward and does not request another page.
	source.postArchiveBackground(ctx)
	for _, p := range []*backgroundPosts{&source.archivePosting, &source.archiveImporting} {
		p.Lock()
		done := p.done
		p.Unlock()
		if done != nil {
			<-done
		}
	}
	source.convWork.due(convHistory)
	source.sync(ctx)
	eventually(t, "final committed source page becomes an uploaded descriptor", func() bool {
		var n int
		err := source.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub=? AND state IN ('queued','custody','delivered')`, phone.Address, envelope.SubHistoryArchive).Scan(&n)
		return err == nil && n > 0
	})
}

func logReplicationFailure(t *testing.T, a *Agent, label string) {
	t.Helper()
	if !t.Failed() {
		return
	}
	rows, err := a.store.db.Query(`SELECT coalesce(sub,''),state,count(*) FROM outbox GROUP BY sub,state`)
	if err != nil {
		t.Log(label, err)
		return
	}
	for rows.Next() {
		var sub, state string
		var count int
		if err := rows.Scan(&sub, &state, &count); err == nil {
			t.Logf("%s replication sub=%q state=%q count=%d", label, sub, state, count)
		}
	}
	rows.Close()
	rows, err = a.store.db.Query(`SELECT retained,pos,done,error FROM history_archive_jobs`)
	if err != nil {
		t.Log(label, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var retained, pos, done int
		var problem string
		if err := rows.Scan(&retained, &pos, &done, &problem); err == nil {
			t.Logf("%s archive retained=%d pos=%d done=%d error=%q", label, retained, pos, done, problem)
		}
	}
}
