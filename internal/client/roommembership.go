package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Forward only signed membership metadata, never past message bodies or local
// permission grants. The current-context carrier binds its exact new admission.
func roomMembershipProof(q dbq, packet GroupContext) ([]protocol.ParticipationEvent, error) {
	members := packet.State.EffectiveMembers(packet.Withdrawals)
	var roster []protocol.ConvMember
	for _, p := range members {
		roster = append(roster, p.ConvMember)
	}
	m, err := memberRowsIn(q, packet.Root, roster)
	if err != nil {
		return nil, err
	}
	m.group = &packet
	rows, err := q.Query(`SELECT DISTINCT pid FROM participation_events WHERE conv=?`, packet.State.Conv)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	var proof []protocol.ParticipationEvent
	for _, pid := range ids {
		evs, e := participationEventsIn(q, packet.State.Conv, pid)
		if e != nil {
			return nil, e
		}
		if e = m.loadHosts(q, evs); e != nil {
			return nil, e
		}
		p := resolve(packet.State.Conv, pid, evs, m)
		if !p.Member || !p.Following() {
			continue
		}
		for _, ev := range evs {
			h := ev.Hash()
			if h == p.Invite || h == p.Scope || h == p.Decision || slices.Contains(p.Shares, h) {
				proof = append(proof, ev)
			}
		}
	}
	return proof, nil
}

func admitRoomMembershipProof(tx *sql.Tx, packet GroupContext) error {
	if len(packet.Memberships) == 0 {
		return nil
	}
	byPID := map[string][]protocol.ParticipationEvent{}
	for _, ev := range packet.Memberships {
		if ev.Conv != packet.State.Conv || ev.Validate() != nil || ev.Role != "" || ev.Type != protocol.EventInvite && ev.Type != protocol.EventScope && ev.Type != protocol.EventAccept && ev.Type != protocol.EventShare {
			return errors.New("group: membership carrier has unrelated records")
		}
		p, ok, err := personByIDIn(tx, ev.Author.Person)
		if err != nil {
			return err
		}
		if !ok || p.info.State == personConflict || !p.has(ev.Author.Address, ev.Author.Fingerprint) {
			return ErrGroupContextPending
		}
		var raw []byte
		if err = tx.QueryRow(`SELECT record FROM person_chain WHERE person=? AND hash=?`, ev.Author.Person, ev.Author.Roster).Scan(&raw); err != nil {
			return err
		}
		var roster protocol.PersonRoster
		if err = json.Unmarshal(raw, &roster); err != nil {
			return err
		}
		key, ok := roster.Device(ev.Author.Fingerprint)
		if !ok || key.Address != ev.Author.Address || ev.Verify(key.SignKey) != nil {
			return errors.New("group: membership carrier signature differs")
		}
		if ev.Group != nil {
			c, e := groupProofRecord(tx, ev.Conv, packet.Root.Creator.Fingerprint, ev.Group.Seq)
			if e != nil {
				return e
			}
			if c.Hash != ev.Group.Hash || ev.Group.Seq > packet.State.Seq {
				return errors.New("group: membership carrier original binding differs")
			}
		}
		byPID[ev.PID] = append(byPID[ev.PID], ev)
	}
	for pid, events := range byPID {
		var invite *protocol.ParticipationEvent
		for i := range events {
			ev := &events[i]
			if ev.Type == protocol.EventInvite && ev.Audience == protocol.AudienceRoom && ev.Until == 0 {
				if invite != nil && invite.Hash() != ev.Hash() {
					return errors.New("group: conflicting membership invites")
				}
				invite = ev
			}
		}
		if invite == nil || invite.Host == nil || invite.Group == nil {
			return errors.New("group: membership carrier needs an original invitation")
		}
		if invite.Group.HostRole == "visitor" {
			c, err := groupProofRecord(tx, invite.Conv, packet.Root.Creator.Fingerprint, invite.Group.Seq)
			if err != nil {
				return err
			}
			if !slices.Contains(c.Admins, invite.Author.Person) {
				return errors.New("group: outside agent invitation needs a group administrator")
			}
		}
		accepted := false
		for _, ev := range events {
			if ev.Type == protocol.EventAccept && ev.Prev == invite.Hash() && ev.Author.Person == invite.Host.Person && ev.Author.Address == invite.Host.Address && ev.Author.Fingerprint == invite.Host.Fingerprint {
				accepted = true
			} else if ev.Type == protocol.EventAccept {
				return errors.New("group: membership carrier has unrelated consent")
			}
		}
		if !accepted {
			return errors.New("group: membership carrier needs exact host consent")
		}
		for _, ev := range events {
			if ev.Type == protocol.EventScope && !ev.Projects(*invite) || ev.Type == protocol.EventShare && (ev.Prev != invite.Hash() || ev.Host == nil || *ev.Host != *invite.Host || ev.Audience != invite.Audience) {
				return errors.New("group: membership carrier scope differs")
			}
			raw, _ := json.Marshal(ev)
			if err := insertParticipationEvent(tx, ev, raw); err != nil {
				return err
			}
			if ev.Type != protocol.EventShare {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO room_membership_events VALUES(?,?,?)`, ev.Hash(), ev.Conv, pid); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
