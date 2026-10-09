package client

import (
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// File recovery uses the existing own-linked PID history authority. These
// sources never become selectable cross-person historical group turns.
func (a *Agent) groupParticipationFileSources(q dbq, conv, lid, author string, limit int) ([]groupHistorySource, error) {
	packet, err := groupTurnPacketIn(q, conv)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`SELECT * FROM (
 SELECT id,'in',sender,coalesce(verified_by,claimed_fp,'') author,ts,kind,body,coalesce(reply_to,''),coalesce(status,''),coalesce(origin,''),coalesce(emotion,''),lid,coalesce(received_ms,received_at*1000) ms,coalesce(group_admission,''),coalesce(target,''),pid,CASE WHEN kind IN ('question','task') THEN '' ELSE coalesce(agent_id,'') END FROM inbox WHERE conv=? AND kind IN ('question','task','answer','result') AND coalesce(sub,'')='' AND pid IS NOT NULL AND ref_id IS NULL AND local=0
 UNION ALL
 SELECT id,'out',?,?,json_extract(envelope,'$.ts'),kind,body,coalesce(reply_to,''),coalesce(status,''),coalesce(origin,''),coalesce(emotion,''),lid,coalesce(created_ms,created_at*1000),coalesce(group_admission,''),coalesce(target,''),pid,coalesce(agent_id,'') FROM outbox WHERE conv=? AND kind IN ('question','task','answer','result') AND coalesce(sub,'')='' AND pid IS NOT NULL AND ref_id IS NULL)
 WHERE lid=? AND author=? ORDER BY ms DESC,id DESC LIMIT ?`, conv, a.Address, a.Self().Fingerprint(), conv, lid, author, limit)
	if err != nil {
		return nil, err
	}
	var sources []groupHistorySource
	for rows.Next() {
		var s groupHistorySource
		var target string
		s.item.V = 1
		err = rows.Scan(&s.item.ID, &s.dir, &s.item.From, &s.item.FromKey, &s.item.TS, &s.item.Kind, &s.item.Body, &s.item.ReplyTo, &s.item.Status, &s.item.Origin, &s.item.Emotion, &s.item.LID, &s.item.At, &s.stamp, &target, &s.item.PID, &s.item.AgentID)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if target != "" {
			if err = json.Unmarshal([]byte(target), &s.item.Target); err != nil {
				rows.Close()
				return nil, err
			}
		}
		sources = append(sources, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range sources {
		s := &sources[i]
		s.item.ReceiverRoute, err = receiverStoredRoute(q, s.dir, s.item.ID)
		if err != nil {
			return nil, err
		}
		s.item.Human, err = storedHuman(q, s.dir, s.item.ID)
		if err != nil {
			return nil, err
		}
		if retractedRef(q, conv, s.item.LID, s.item.FromKey) {
			return nil, ErrGroupHistoryUnavailable
		}
		table, column := "attachments", "message_id"
		if s.dir == "out" {
			table = "sent_attachments"
		}
		files, e := q.Query("SELECT name,size,sha256 FROM "+table+" WHERE "+column+"=? ORDER BY rowid", s.item.ID)
		if e != nil {
			return nil, e
		}
		for files.Next() {
			var f envelope.Attachment
			if e = files.Scan(&f.Name, &f.Size, &f.SHA256); e != nil {
				break
			}
			s.item.Attachments = append(s.item.Attachments, f)
		}
		if e == nil {
			e = files.Err()
		}
		files.Close()
		if e != nil {
			return nil, e
		}
		s.item, e = a.groupParticipationHistorySource(q, packet, s.item)
		if e != nil {
			return nil, e
		}
		s.stamp = s.item.GroupAdmission
	}
	return sources, nil
}

func (a *Agent) groupFileSources(q dbq, conv, lid, author string, limit int) ([]groupHistorySource, error) {
	sources, err := a.groupHistorySources(q, conv, lid, author, 0, limit)
	if err != nil || len(sources) != 0 {
		return sources, err
	}
	return a.groupParticipationFileSources(q, conv, lid, author, limit)
}

func (a *Agent) groupFileSourceIn(q dbq, conv string, ref protocol.GroupHistoryRef) (groupHistorySource, error) {
	sources, err := a.groupFileSources(q, conv, ref.LID, ref.Author, 3)
	if err != nil {
		return groupHistorySource{}, err
	}
	if len(sources) == 0 {
		return groupHistorySource{}, ErrGroupHistoryUnavailable
	}
	for _, s := range sources {
		if historyRef(conv, s.item) != ref {
			return groupHistorySource{}, errors.Join(ErrGroupHistoryUnavailable, errors.New("group: conflicting exact file source"))
		}
	}
	return sources[0], nil
}
