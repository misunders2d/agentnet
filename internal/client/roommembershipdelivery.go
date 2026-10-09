package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An end goes to current members, including those who joined while its first
// fan-out was in flight. Original signed bytes suffice; no past turns are shared.
func (a *Agent) discloseRoomDismissals(ctx context.Context, features []string) {
	rows, err := a.store.db.Query(`SELECT DISTINCT e.conv FROM participation_events e JOIN group_context g ON g.conv=e.conv WHERE e.type=?`, protocol.EventDismiss)
	if err != nil {
		return
	}
	var convs []string
	for rows.Next() {
		var conv string
		if err = rows.Scan(&conv); err != nil {
			break
		}
		convs = append(convs, conv)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return
	}
	for _, conv := range convs {
		if err := a.discloseRoomConv(ctx, conv, features); err != nil {
			a.Logf("group membership end disclosure held: %v", err)
		}
	}
}

func roomDismissalShared(q dbq, ev protocol.ParticipationEvent, body, address, fp, admission string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND pid=? AND sub=? AND body=? AND recipient=? AND recipient_fp=? AND coalesce(group_admission,'')=?`, ev.Conv, ev.PID, envelope.SubEvent, body, address, fp, admission).Scan(&n)
	return n > 0, err
}

func (a *Agent) discloseRoomConv(ctx context.Context, conv string, features []string) error {
	m, err := a.dmMembers(conv)
	if err != nil {
		return err
	}
	if m.group == nil || groupTurnCheck(a.store.db, *m.group, a.Address, a.Self().Fingerprint()) != nil {
		return nil
	}
	infos, err := a.Participations(conv)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if !info.Member || info.Held != 0 || info.State != PartDismissed || info.Decision == "" || info.Dismissal == "" {
			continue
		}
		events, err := a.store.participationEvents(conv, info.PID)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(events, func(ev protocol.ParticipationEvent) bool {
			return ev.Hash() == info.Decision && ev.Type == protocol.EventAccept
		}) {
			continue
		}
		for _, ev := range events {
			if ev.Hash() != info.Dismissal || !m.mayRemoveAgent(info, ev.Author) {
				continue
			}
			for _, person := range m.persons {
				for _, dev := range person.roster.Devices {
					if dev.Address != a.Address {
						if err = a.shareRoomDismissal(ctx, m, ev, dev, features); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

func (a *Agent) shareRoomDismissal(ctx context.Context, m dmMembers, ev protocol.ParticipationEvent, dev identity.Public, features []string) error {
	admission, err := groupMemberAdmission(a.store.db, *m.group, dev.Address, dev.Fingerprint())
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(ev)
	body := string(raw)
	if sent, err := roomDismissalShared(a.store.db, ev, body, dev.Address, dev.Fingerprint(), admission.Hash()); err != nil || sent {
		return err
	}
	key, err := a.sendKey(ctx, dev.Address)
	if err != nil {
		return err
	}
	if key.Fingerprint() != dev.Fingerprint() {
		return errors.New("group: membership end recipient key changed")
	}
	recipient, err := key.Recipient()
	if err != nil {
		return err
	}
	root, _ := json.Marshal(m.root)
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: body, Conv: ev.Conv, LID: protocol.NewID(), Root: root, PID: ev.PID, Sub: envelope.SubEvent, Origin: envelope.OriginUI}
	for person, p := range m.persons {
		if p.has(a.Address, a.Self().Fingerprint()) || p.has(dev.Address, dev.Fingerprint()) {
			in.Fan = append(in.Fan, envelope.Fan{Person: person, Roster: p.roster.Hash()})
		}
	}
	sealed, err := envelope.Seal(in, a.id.Sign, recipient)
	if err != nil {
		return err
	}
	copy := outCopy{in: in, env: sealed, state: stateQueued, required: protocol.CapGroup, recipientFP: dev.Fingerprint(), groupAdmission: admission.Hash()}
	if ok, why, _ := a.convSupport(ctx, dev.Address, key, features); !ok {
		copy.state, copy.why = stateConvWaiting, why
	}
	guard := func(tx *sql.Tx, _ string) error {
		if sent, err := roomDismissalShared(tx, ev, body, dev.Address, dev.Fingerprint(), admission.Hash()); err != nil || sent {
			if err == nil {
				err = errHumanShared
			}
			return err
		}
		current, err := membersIn(tx, ev.Conv)
		if err != nil {
			return err
		}
		if current.group == nil || groupTurnCheck(tx, *current.group, a.Address, a.Self().Fingerprint()) != nil {
			return errors.New("group: membership end sender no longer current")
		}
		to, err := groupMemberAdmission(tx, *current.group, dev.Address, dev.Fingerprint())
		if err != nil {
			return err
		}
		if to.Hash() != admission.Hash() {
			return ErrGroupContextPending
		}
		info, err := participationIn(tx, ev.Conv, ev.PID, current, a.Address)
		if err != nil {
			return err
		}
		if !info.Member || info.Held != 0 || info.State != PartDismissed || info.Dismissal != ev.Hash() || !current.mayRemoveAgent(info, ev.Author) {
			return errors.New("group: membership end no longer counted")
		}
		return externalTurn(in, info, current, a.Address, a.Self().Fingerprint(), tx)
	}
	if err = a.store.addConvOutbox([]outCopy{copy}, in, guard, ""); errors.Is(err, errHumanShared) {
		return nil
	} else if err != nil {
		return err
	}
	if copy.state == stateQueued {
		_, err = a.deliver(ctx, sealed, nil)
		if retryable(err) {
			return nil
		}
	}
	return err
}
