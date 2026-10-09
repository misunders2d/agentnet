package client

import (
	"database/sql"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

var errLifecycleHistoryProof = errors.New("historical public participation waits for its exact scope, host or predecessor proof")

func (a *Agent) lifecycleHistoryAuthorKey(q dbq, author protocol.EventAuthor) (identity.Public, bool, error) {
	key, present, err := pinnedKey(q, author.Address)
	if err != nil || present {
		return key, present, err // a changed transport pin is never replaced by a roster
	}
	if author.Address == a.Address {
		return a.Self(), true, nil
	}
	p, known, err := personByIDIn(q, author.Person)
	if err != nil {
		return key, false, err
	}
	if known && (p.info.State == personPinned || p.info.State == personSelf) && p.has(author.Address, author.Fingerprint) {
		key, present = p.roster.Device(author.Fingerprint)
	}
	return key, present, nil
}

// Old own-device producers attributed public lifecycle disclosures to their
// forwarding transport. Correct only those inert DM replicas, under current
// own-human authority and the original author's independently verified event.
func (a *Agent) dmLifecycleHistoryReceived(q dbq, sender, conv string, item HistoryItem) (HistoryItem, bool, error) {
	if item.Sub != envelope.SubEvent {
		return item, false, nil
	}
	ev, err := protocol.ParseParticipationEvent([]byte(item.Body))
	if err != nil || ev.Author.Address == item.From && ev.Author.Fingerprint == item.FromKey {
		return item, false, nil // ordinary signed history keeps its existing path
	}
	if item.Kind != envelope.KindMessage || len(item.Attachments) != 0 || item.Target != nil || item.Human != nil || item.Ref != nil || item.ReceiverRoute != nil || item.Followup != nil || item.GroupHistory != nil {
		return item, true, errors.New("historical public lifecycle carries unrelated request or disclosure fields")
	}
	key, found, err := pinnedKey(q, sender)
	if err != nil {
		return item, true, err
	}
	if !found || item.From != sender || item.FromKey != key.Fingerprint() {
		return item, true, errors.New("historical lifecycle forwarder differs from its authenticated carrier")
	}
	if err := (historyRecoveryDevices{Sender: key, Reader: a.Self()}).check(q); err != nil {
		return item, true, err
	}
	corrected, err := a.dmLifecycleHistoryAuthor(q, conv, item, true)
	if err == nil {
		var storedConv, storedLID, storedAuthor string
		e := q.QueryRow(`SELECT conv,lid,coalesce(verified_by,claimed_fp) FROM inbox WHERE id=?`, corrected.ID).Scan(&storedConv, &storedLID, &storedAuthor)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return item, true, e
		}
		if e == nil && (storedConv != conv || storedLID != corrected.LID || storedAuthor != corrected.FromKey) {
			return item, true, errors.New("historical public lifecycle source ID conflicts with an accepted record")
		}
	}
	return corrected, true, err
}
