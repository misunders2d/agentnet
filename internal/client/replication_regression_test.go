package client

import (
	"context"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestHistoryArchiveFinalProducerPageWakesWorker(t *testing.T) {
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
