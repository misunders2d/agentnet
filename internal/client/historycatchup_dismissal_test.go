package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The event is independently signed by its original author. A retired foreign
// transport's duplicate must not block that same event's valid source or later
// inert history, and must not acquire any new forwarding authority.
func TestHistoryCatchupDuplicateForeignDismissal(t *testing.T) {
	w, a, _, packet := groupHistoryLinkedFixture(t)
	stop := runAgent(t, a)
	part := p6Member(t, w.bob, w.bob, packet.State.Conv)
	eventually(t, "source sees accepted participation", func() bool { return stateAt(t, a, part.PID).Claimable() })
	ask, err := a.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "synthetic retained request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.DismissParticipation(tctx(t), part.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "source sees original dismissal", func() bool { return pollState(t, a, part.PID).State == PartDismissed })
	var original string
	if err = a.store.db.QueryRow(`SELECT id FROM inbox WHERE sender=? AND conv=? AND pid=? AND sub='event' AND json_extract(body,'$.type')='dismiss' LIMIT 1`, w.bob.Address, packet.State.Conv, part.PID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var originalBody string
	if err = a.store.db.QueryRow(`SELECT body FROM inbox WHERE id=?`, original).Scan(&originalBody); err != nil {
		t.Fatal(err)
	}
	event, err := protocol.ParseParticipationEvent([]byte(originalBody))
	if err != nil {
		t.Fatal(err)
	}
	phone, await, _ := linkPhone(t, a, "dismissal-reader")
	request := pendingLink(t, a)
	stop()
	if err = a.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	// Clone only fixture-local durable rows. The embedded event remains exact;
	// only the independently recorded old transport tuple differs.
	clone := func(body string) string {
		t.Helper()
		id := protocol.NewID()
		rows, e := a.store.db.Query(`PRAGMA table_info(inbox)`)
		if e != nil {
			t.Fatal(e)
		}
		var columns, selects []string
		var args []any
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			if e = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); e != nil {
				t.Fatal(e)
			}
			columns = append(columns, `"`+name+`"`)
			switch name {
			case "id", "lid":
				selects = append(selects, "?")
				args = append(args, id)
			case "sender":
				selects = append(selects, "?")
				args = append(args, "retired-foreign/phone")
			case "verified_by", "claimed_fp":
				selects = append(selects, "?")
				args = append(args, w.bob.Self().Fingerprint())
			case "body":
				selects = append(selects, "?")
				args = append(args, body)
			case "arrival":
				selects = append(selects, "(SELECT coalesce(max(arrival),0)+1 FROM inbox)")
			default:
				selects = append(selects, `"`+name+`"`)
			}
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		rows.Close()
		args = append(args, original)
		if _, e = a.store.db.Exec(`INSERT INTO inbox(`+strings.Join(columns, ",")+`) SELECT `+strings.Join(selects, ",")+` FROM inbox WHERE id=?`, args...); e != nil {
			t.Fatal(e)
		}
		if _, e = a.store.db.Exec(`UPDATE config SET v=(SELECT max(arrival) FROM inbox) WHERE k='arrival'`); e != nil {
			t.Fatal(e)
		}
		return id
	}
	duplicate := clone(originalBody)

	for _, id := range []string{duplicate} {
		if _, err = a.store.db.Exec(`INSERT OR IGNORE INTO history_deferred(recipient_fp,dir,id) VALUES(?,'in',?)`, phone.Self().Fingerprint(), id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		more, e := a.pageByHand(tctx(t), phone.Self())
		if e != nil {
			t.Fatal(e)
		}
		if !more {
			break
		}
	}
	count := func(id string) int {
		t.Helper()
		var n int
		if err := a.store.db.QueryRow(`SELECT count(*) FROM history_deferred WHERE recipient_fp=? AND id=?`, phone.Self().Fingerprint(), id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(duplicate) != 0 {
		t.Fatal("exact signed dismissal duplicate stayed deferred")
	}
	var covered int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM history_copies h JOIN outbox o ON o.id=h.carrier WHERE h.recipient_fp=? AND o.recipient=? AND json_extract(o.body,'$.body')=? AND o.state IN ('queued','archive_staged','archive_accepted','custody','delivered','waiting')`, phone.Self().Fingerprint(), phone.Address, originalBody).Scan(&covered); err != nil || covered == 0 {
		t.Fatalf("canonical event not durably covered: %d %v", covered, err)
	}
	var requestCopies int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM history_copies WHERE recipient_fp=? AND conv=? AND lid=?`, phone.Self().Fingerprint(), packet.State.Conv, ask.LID).Scan(&requestCopies); err != nil || requestCopies == 0 {
		t.Fatalf("dependent request stayed blocked: %d %v", requestCopies, err)
	}
	before := historyCarrierCount(t, a, phone.Address)
	if _, err = a.pageByHand(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if after := historyCarrierCount(t, a, phone.Address); after != before {
		t.Fatalf("idle replay generated carriers: %d/%d", before, after)
	}
	forged := event
	forged.Sig = append([]byte(nil), event.Sig...)
	forged.Sig[0] ^= 1
	raw, _ := json.Marshal(forged)
	invalid := clone(string(raw))
	unique := event
	unique.TS++
	unique.Sign(w.bob.id.Sign)
	raw, _ = json.Marshal(unique)
	unmatched := clone(string(raw))
	attached := clone(originalBody)
	if _, err = a.store.db.Exec(`INSERT INTO attachments(message_id,blob_id,name,size,sha256,ct_size,ct_sha256) VALUES(?,?,?,1,?,1,?)`, attached, protocol.NewID(), "retained.txt", strings.Repeat("a", 64), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{invalid, unmatched, attached} {
		if _, err = a.store.db.Exec(`INSERT OR IGNORE INTO history_deferred(recipient_fp,dir,id) VALUES(?,'in',?)`, phone.Self().Fingerprint(), id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.pageByHand(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if count(invalid) != 1 || count(unmatched) != 1 || count(attached) != 1 {
		t.Fatal("invalid, unmatched or attachment-bearing event was discarded")
	}

	reopened, err := Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.pageByHand(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if after := historyCarrierCount(t, reopened, phone.Address); after != before {
		t.Fatalf("restart regenerated historical carriers: %d/%d", before, after)
	}
	if count(duplicate) != 0 || count(invalid) != 1 || count(unmatched) != 1 || count(attached) != 1 {
		t.Fatal("restart lost exact deferred classifications")
	}
	var retained string
	if err = a.store.db.QueryRow(`SELECT body FROM inbox WHERE id=?`, duplicate).Scan(&retained); err != nil || retained != originalBody {
		t.Fatal("original signed event changed")
	}
}

func historyCarrierCount(t *testing.T, a *Agent, to string) int {
	t.Helper()
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, to).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
