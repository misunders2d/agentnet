package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/misunders2d/agentnet/internal/identity"
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
		if !p.Member || !p.Following() && !(p.Held == 0 && p.State == PartDismissed && p.Decision != "" && p.Dismissal != "") {
			continue
		}
		if p.State == PartDismissed {
			var invite, end protocol.ParticipationEvent
			accepted := false
			for _, ev := range evs {
				if ev.Hash() == p.Invite || ev.Hash() == p.Scope && invite.Type == "" {
					invite = ev
				}
				if ev.Hash() == p.Dismissal {
					end = ev
				}
				accepted = accepted || ev.Hash() == p.Decision && ev.Type == protocol.EventAccept
			}
			// A previously counted end can outlive its author's admin role.
			// Do not let unverifiable historical authority poison a new
			// context, or send an acceptance without its counted end.
			if !accepted || !roomMembershipMayDismiss(packet, invite, end) {
				continue
			}
		}
		for _, ev := range evs {
			h := ev.Hash()
			if h == p.Invite || h == p.Scope || h == p.Decision || h == p.Dismissal || slices.Contains(p.Shares, h) {
				proof = append(proof, ev)
			}
		}
	}
	return proof, nil
}

func roomMembershipMayDismiss(packet GroupContext, invite, end protocol.ParticipationEvent) bool {
	if invite.Host == nil || invite.Group == nil || end.Type != protocol.EventDismiss {
		return false
	}
	member, current := packet.State.Member(end.Author.Person)
	host := end.Author.Person == invite.Host.Person && end.Author.Address == invite.Host.Address && end.Author.Fingerprint == invite.Host.Fingerprint
	return host || end.Author.Person == invite.Author.Person || current && !packet.State.Withdrawn(member, packet.Withdrawals) && end.Author.GroupAdmission == member.Admission.Hash() && (invite.Group.HostRole == "member" || member.Admin)
}

// Memberships are extra lifecycle claims, not part of the signed group state.
// Only an effective current admin, or a current own device, may vouch for them.
func roomMembershipSender(q dbq, packet GroupContext, sender identity.Public) (bool, error) {
	if sender.Address == "" || groupTurnCheck(q, packet, sender.Address, sender.Fingerprint()) != nil {
		return false, nil
	}
	person, ok, err := scanPersonIn(q, "person IN (SELECT person FROM person_devices WHERE address=?)", sender.Address)
	if err != nil || !ok {
		return false, err
	}
	member, ok := packet.State.Member(person.info.Person)
	return ok && (member.Admin || person.info.State == personSelf), nil
}

func admitRoomMembershipProof(tx *sql.Tx, packet GroupContext) error {
	if len(packet.Memberships) == 0 {
		return nil
	}
	byPID := map[string][]protocol.ParticipationEvent{}
	for _, ev := range packet.Memberships {
		if ev.Conv != packet.State.Conv || ev.Validate() != nil || ev.Role != "" || ev.Type != protocol.EventInvite && ev.Type != protocol.EventScope && ev.Type != protocol.EventAccept && ev.Type != protocol.EventShare && ev.Type != protocol.EventDismiss {
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
			if now, ok := packet.State.Member(invite.Author.Person); !ok || !now.Admin || !slices.Contains(c.Admins, invite.Author.Person) {
				return errors.New("group: outside agent invitation needs a group administrator")
			}
		}
		accepts := []string{}
		for _, ev := range events {
			if ev.Type == protocol.EventAccept && ev.Prev == invite.Hash() && ev.Author.Person == invite.Host.Person && ev.Author.Address == invite.Host.Address && ev.Author.Fingerprint == invite.Host.Fingerprint {
				accepts = append(accepts, ev.Hash())
			} else if ev.Type == protocol.EventAccept {
				return errors.New("group: membership carrier has unrelated consent")
			}
		}
		if len(accepts) == 0 {
			return errors.New("group: membership carrier needs exact host consent")
		}
		for _, ev := range events {
			if ev.Type == protocol.EventDismiss {
				// A carried end must have the same removal right as a direct
				// event. Marking room_membership_events makes it count later,
				// so the carrier's witness alone must never grant that right.
				if !roomMembershipMayDismiss(packet, *invite, ev) || ev.Prev != invite.Hash() && !slices.Contains(accepts, ev.Prev) {
					return errors.New("group: membership dismissal needs an authorized author and exact invitation or acceptance")
				}
			}
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
