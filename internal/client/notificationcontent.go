package client

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// NativeNotification is locally decrypted display data. It never goes to the
// relay or a push provider. The platform owns lock-screen privacy and consent.
type NativeNotification struct{ Fragment, Title, Body string }

func (a *Agent) hasNativeNotifier() bool {
	return a.nativeNotify != nil || a.nativeNotifyContent != nil
}

func notificationText(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > limit {
		s = string(r[:limit-1]) + "…"
	}
	return s
}

func (a *Agent) nativeNotification(fragment, id, title, body string) {
	if a.nativeNotifyContent == nil {
		if a.nativeNotify != nil {
			a.nativeNotify(fragment)
		}
		return
	}
	n := NativeNotification{Fragment: fragment, Title: title, Body: body}
	if protocol.ValidID(id) {
		a.notificationMessage(&n, id)
	}
	n.Title, n.Body = notificationText(n.Title, 100), notificationText(n.Body, 240)
	a.nativeNotifyContent(n)
}

// Read one exact stored target and its controls, not a conversation/overview.
// Nothing in this display lookup fetches keys, history, files or network data.
func (a *Agent) notificationMessage(n *NativeNotification, id string) {
	m, err := a.store.inboxMessage(id)
	if err != nil {
		return
	}
	var key, conv, lid, topic, origin, pid, human string
	if m == nil {
		// A reminder may name our exact sent copy. Use its own plaintext and
		// sealed outer kind; never look through the conversation for a match.
		var raw string
		m = &Message{ID: id, From: a.Address}
		err = a.store.db.QueryRow(`SELECT envelope,body,coalesce(conv,''),coalesce(lid,''),coalesce(topic,''),coalesce(origin,''),coalesce(pid,''),coalesce(human,''),coalesce(agent_id,''),coalesce(sub,''),coalesce(reply_to,''),coalesce(status,'') FROM outbox WHERE id=?`, id).Scan(&raw, &m.Body, &conv, &lid, &topic, &origin, &pid, &human, &m.AgentID, &m.Sub, &m.ReplyTo, &m.Status)
		var sealed envelope.Envelope
		if err != nil || json.Unmarshal([]byte(raw), &sealed) != nil {
			return
		}
		m.Kind, key = sealed.Kind, a.Self().Fingerprint()
	} else {
		err = a.store.db.QueryRow(`SELECT coalesce(verified_by,''), coalesce(conv,''), coalesce(lid,''), coalesce(topic,''), coalesce(origin,''), coalesce(pid,''), coalesce(human,'') FROM inbox WHERE id=?`, id).Scan(&key, &conv, &lid, &topic, &origin, &pid, &human)
	}
	if err != nil || key == "" {
		return
	}
	// The exact stored conversation is already known here. Carry it in the
	// existing route so a tap needs one local view, not every conversation.
	if protocol.ValidHash(conv) {
		if direction, ok := strings.CutPrefix(n.Fragment, "msg="+id+"&dir="); ok && (direction == "in" || direction == "out") {
			n.Fragment = "msg=" + id + "&conv=" + conv + "&dir=" + direction
		}
	}
	sender := a.senderHere(m.From, key)
	name := m.From
	if sender.Relation != SenderUnverified && sender.Label != "" {
		name = sender.Label
	}
	agent := m.Kind == envelope.KindAnswer || m.Kind == envelope.KindResult
	if conv != "" {
		row := ConvMessage{From: m.From, Key: key, Kind: m.Kind, Sub: m.Sub, ReplyTo: m.ReplyTo, PID: pid, AgentID: m.AgentID, Origin: origin, status: m.Status}
		if human != "" {
			if err := json.Unmarshal([]byte(human), &row.Human); err != nil {
				return
			}
		}
		rows := []ConvMessage{row}
		a.verifyAgents(conv, rows)
		agent = rows[0].VerifiedAgent
	}
	if agent {
		name = "Agent on " + sender.Device
		// Only our own catalog is available here as verified local identity
		// data. A remote person's label never names the agent on their host.
		if m.AgentID != "" && m.From == a.Address && key == a.Self().Fingerprint() {
			if entries, err := a.LocalAgents(); err == nil {
				for _, entry := range entries {
					if entry.Record.ID == m.AgentID {
						name = entry.Record.Label + " · Agent on " + sender.Device
						break
					}
				}
			}
		}
	}
	if n.Title != "AgentNet" {
		name = n.Title + " · " + name
	}
	if packet, e := groupTurnPacketIn(a.store.db, conv); e == nil && packet.State.Title != "" {
		name += " · " + packet.State.Title
	}
	if topic != "" {
		var title string
		if a.store.db.QueryRow(`SELECT coalesce(title,'') FROM topic_state WHERE peer=? AND topic=?`, conv, topic).Scan(&title) == nil && title != "" {
			name += " · " + title
		}
	}
	n.Title = name
	if m.Sub != "" {
		if m.Sub == "group-invite" {
			n.Body = "Group invitation needs your decision."
		}
		return
	}
	ref := ControlRef{Conv: conv, ID: id, Fingerprint: key}
	if conv != "" {
		ref.ID = lid
	}
	controls, err := a.notificationControls(ref, m.From)
	if err != nil {
		return
	} // no stale plaintext if current controls cannot be checked
	if controls.Deleted {
		n.Body = "Message deleted."
		return
	}
	preview := notificationText(controls.Shown(m.Body), 240)
	if preview == "" {
		if len(m.Attachments) > 0 {
			preview = "Attachment: " + m.Attachments[0].Name
		} else {
			preview = "New message"
		}
	}
	n.Body = preview
}

// Restrict the existing signed-control resolver to this exact message. Its
// current author/participant checks remain the same as the messenger's.
func (a *Agent) notificationControls(ref ControlRef, peer string) (Controls, error) {
	self, selfFP := a.Address, a.Self().Fingerprint()
	rows, err := a.store.db.Query(`SELECT coalesce(lid,id),sub,sender,coalesce(verified_by,claimed_fp,''),ref_id,ref_fp,body,coalesce(received_ms,received_at*1000),coalesce(pid,''),coalesce(agent_id,''),coalesce(origin,''),coalesce(human,'')
 FROM inbox WHERE ref_id=? AND ref_fp=? AND coalesce(conv,'')=? AND local=0
 UNION ALL SELECT coalesce(o.lid,o.id),o.sub,?,?,o.ref_id,o.ref_fp,o.body,coalesce(o.created_ms,o.created_at*1000),coalesce(o.pid,''),coalesce(o.agent_id,''),coalesce(o.origin,''),coalesce(o.human,'')
 FROM outbox o WHERE o.ref_id=? AND o.ref_fp=? AND coalesce(o.conv,'')=? AND (o.lid IS NULL OR o.rowid=(SELECT min(rowid) FROM outbox f WHERE f.conv=o.conv AND f.lid=o.lid))`, ref.ID, ref.Fingerprint, ref.Conv, self, selfFP, ref.ID, ref.Fingerprint, ref.Conv)
	if err != nil {
		return Controls{}, err
	}
	records, err := scanControls(rows)
	if err != nil {
		return Controls{}, err
	}
	target := ControlRef{ID: ref.ID, Fingerprint: ref.Fingerprint}
	if ref.Conv == "" {
		return resolveControls(records, legacyAuthority(peer, self, selfFP))[target], nil
	}
	members, err := controlMembers(a.store.db, ref.Conv)
	if err != nil {
		return Controls{}, err
	}
	me, _, err := a.store.selfPerson(self)
	if err != nil {
		return Controls{}, err
	}
	auth, _ := a.convAuthority(ref.Conv, members, me)
	return resolveControls(records, auth)[target], nil
}
