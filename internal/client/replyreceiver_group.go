package client

import (
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Private immutable admission snapshots, one per original logical request.
// Current membership alone cannot revive an old selected-return obligation.
type groupReceiverRequest struct {
	From      string            `json:"from"`
	Key       string            `json:"key"`
	Admission string            `json:"admission"`
	Replies   map[string]string `json:"replies"` // exact signing key -> original member admission; visitor host has none
}
type groupReceiverScopes map[string]groupReceiverRequest

func groupReceiverCurrent(q dbq, conv string, scope groupReceiverRequest) (dmMembers, error) {
	m, err := membersIn(q, conv)
	if err != nil {
		return m, err
	}
	if m.group == nil || !m.device(scope.From, scope.Key) || scope.Admission == "" || m.keyEpoch(scope.Key) != scope.Admission {
		return m, errors.New("selected group requester's original admission no longer holds")
	}
	return m, nil
}
func groupReceiverBinding(q dbq, b ReplyReceiverBinding) error {
	if err := remoteReceiverAuthority(q, b); err != nil {
		return err
	}
	if b.Conv == "" {
		return nil
	}
	root, _, found, err := conversationIn(q, b.Conv)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("selected reply conversation is unavailable")
	}
	if root.Kind != protocol.ConvKindGroup {
		return nil
	}
	scope, ok := b.group[b.RequestRef]
	if !ok {
		return errors.New("selected group request has no original admission snapshot")
	}
	m, err := groupReceiverCurrent(q, b.Conv, scope)
	if err == nil && b.remote != nil && !m.device(b.remote.Route.Host, b.remote.Route.HostKey) {
		return errors.New("selected receiver host is no longer a current group member device")
	}
	return err
}

func snapshotGroupReceiver(q dbq, first envelope.Inner, copies []outCopy, old ReplyReceiverBinding) (groupReceiverScopes, error) {
	root, err := protocol.ParseConvRoot(first.Root)
	if err != nil || root.Kind != protocol.ConvKindGroup {
		return old.group, nil
	}
	m, err := membersIn(q, first.Conv)
	if err != nil {
		return nil, err
	}
	if old.ID != "" {
		if err = groupReceiverBinding(q, old); err != nil {
			return nil, err
		}
	}
	var key string
	for _, p := range m.persons {
		if d, ok := p.device(first.From); ok {
			key = d.Fingerprint()
			break
		}
	}
	scope := groupReceiverRequest{From: first.From, Key: key, Admission: m.keyEpoch(key), Replies: map[string]string{}}
	if _, err = groupReceiverCurrent(q, first.Conv, scope); err != nil {
		return nil, err
	}
	for _, c := range copies {
		if c.in.Replica || c.in.Target != nil && (c.env.To != c.in.Target.Address || c.recipientFP != c.in.Target.Fingerprint) {
			continue
		}
		epoch := m.keyEpoch(c.recipientFP)
		if epoch == "" && (first.PID == "" || first.Target == nil || first.Target.Fingerprint != c.recipientFP) {
			return nil, errors.New("selected group reply source lacks original admission")
		}
		scope.Replies[c.recipientFP] = epoch
	}
	scopes := groupReceiverScopes{}
	for ref, s := range old.group {
		scopes[ref] = s
	}
	scopes[first.LID] = scope
	return scopes, nil
}

// This guard supplements admission, never grants room or execution authority.
func groupReceiverInput(q dbq, b ReplyReceiverBinding, id string) error {
	if err := receiverInputRetraction(q, b, id); err != nil {
		return err
	}
	if err := groupReceiverBinding(q, b); err != nil {
		return err
	}
	if b.group == nil {
		return nil
	} // legacy direct/DM behavior
	var in envelope.Inner
	var fp, target, stamp string
	var replica bool
	err := q.QueryRow(`SELECT sender,coalesce(verified_by,''),conv,lid,kind,body,coalesce(reply_to,''),coalesce(pid,''),coalesce(agent_id,''),coalesce(target,''),coalesce(sub,''),replica,coalesce(group_admission,'') FROM inbox WHERE id=?`, id).Scan(&in.From, &fp, &in.Conv, &in.LID, &in.Kind, &in.Body, &in.ReplyTo, &in.PID, &in.AgentID, &target, &in.Sub, &replica, &stamp)
	if err != nil {
		return err
	}
	if replica || in.Conv != b.Conv || in.Sub != "" {
		return errors.New("selected group input is not a direct turn of this conversation")
	}
	scope, ok := b.group[in.ReplyTo]
	if !ok { // physical references must resolve one known logical original.
		var ref string
		err = q.QueryRow(`SELECT lid FROM outbox WHERE id=? AND conv=? AND reply_receiver=?`, in.ReplyTo, b.Conv, b.ID).Scan(&ref)
		if err != nil {
			return errors.New("selected group input lacks exact original request")
		}
		scope, ok = b.group[ref]
	}
	if !ok {
		return errors.New("selected group input lacks original admission snapshot")
	}
	m, err := groupReceiverCurrent(q, b.Conv, scope)
	if err != nil {
		return err
	}
	epoch, ok := scope.Replies[fp]
	if !ok {
		return errors.New("selected group reply key was not an original recipient")
	}
	if epoch != "" && (!m.device(in.From, fp) || m.keyEpoch(fp) != epoch) {
		return errors.New("selected group reply source admission changed")
	}
	if stamp == "" || stamp != scope.Admission {
		return errors.New("selected group input recipient admission changed")
	}
	if target != "" {
		if err = json.Unmarshal([]byte(target), &in.Target); err != nil {
			return err
		}
	}
	if in.PID != "" {
		info, e := participationIn(q, b.Conv, in.PID, m, scope.From)
		if e != nil {
			return e
		}
		if !info.Claimable() {
			return errors.New("selected group participation is no longer active")
		}
		if e = externalTurn(in, info, m, in.From, fp, q); e != nil {
			return e
		}
		if in.Kind == envelope.KindAnswer || in.Kind == envelope.KindResult {
			_, e = externalOutputRequest(q, in, info, m, scope.From, scope.Key)
			return e
		}
		// Q/T clarification is data only; the original host/PID and epoch must
		// still match, never automatic acceptance of this remote request.
		var raw, kind string
		if b.remote != nil && b.remote.Role == "imported" {
			raw = targetJSON(b.remote.Request.Target)
			kind = b.remote.Request.Kind
		} else {
			if e = q.QueryRow(`SELECT coalesce(target,''),kind FROM outbox WHERE reply_receiver=? AND conv=? AND (id=? OR lid=?) AND pid=? ORDER BY id LIMIT 1`, b.ID, b.Conv, in.ReplyTo, in.ReplyTo, in.PID).Scan(&raw, &kind); e != nil {
				return e
			}
		}
		var original envelope.Target
		if json.Unmarshal([]byte(raw), &original) != nil || original.Address != in.From || original.Fingerprint != fp || original.AgentID != info.AgentID || !m.requestEpoch(scope.From, scope.Key, &original) || kind != envelope.KindQuestion && kind != envelope.KindTask {
			return errors.New("selected group clarification differs from original host/PID/requester epoch")
		}
	}
	return nil
}
