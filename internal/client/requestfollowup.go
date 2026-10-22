package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// RequestFollowup queues a new ordinary request. It never edits the original,
// resumes its claim, or promises that a native harness accepted live input.
type RequestFollowup struct {
	Conv    string         `json:"conv,omitempty"`
	Attempt int64          `json:"attempt,omitempty"`
	Ref     envelope.Ref   `json:"ref"`
	ID      string         `json:"id"`
	Body    string         `json:"body"`
	Files   []OutgoingFile `json:"-"`
}

type followupOriginal struct {
	h       HistoryItem
	to, key string
}

func (a *Agent) followupOriginal(q dbq, conv string, ref envelope.Ref) (followupOriginal, error) {
	var result followupOriginal
	if !protocol.ValidID(ref.ID) || !protocol.ValidFingerprint(ref.Fingerprint) || conv != "" && !protocol.ValidHash(conv) {
		return result, errors.New("choose the exact original request and author key")
	}
	if conv == "" {
		r, err := a.deviceHistoryOriginal(q, ref.ID)
		if err != nil {
			return result, err
		}
		result = followupOriginal{r.item, r.to, r.toKey}
	} else {
		rows, err := a.historySourceRows(q, "conv=? AND (id=? OR lid=?) AND verified_by=?", "dir,id", 3, conv, ref.ID, ref.ID, ref.Fingerprint)
		if err != nil {
			return result, err
		}
		for _, row := range rows {
			h, err := a.historySourceItem(q, row)
			if err != nil {
				return result, err
			}
			if result.h.ID != "" && contentHash(result.h.inner(conv)) != contentHash(h.inner(conv)) {
				return result, errors.New("original request is ambiguous")
			}
			result.h = h
		}
		if result.h.ID == "" {
			// A self-hosted ask can have no remote copies. Its local job is
			// still the original request, not an imported execution claim.
			var in envelope.Inner
			var key, target string
			err := q.QueryRow(`SELECT id,lid,sender,verified_by,ts,kind,body,coalesce(reply_to,''),coalesce(pid,''),coalesce(topic,''),coalesce(target,'') FROM inbox WHERE conv=? AND (id=? OR lid=?) AND verified_by=? AND local=1 AND replica=0`, conv, ref.ID, ref.ID, ref.Fingerprint).Scan(&in.ID, &in.LID, &in.From, &key, &in.TS, &in.Kind, &in.Body, &in.ReplyTo, &in.PID, &in.Topic, &target)
			if err != nil {
				return result, err
			}
			if target != "" {
				if err = json.Unmarshal([]byte(target), &in.Target); err != nil {
					return result, err
				}
			}
			in.Conv = conv
			h, err := a.historySourceItem(q, historySourceRow{dir: "in", key: key, in: in})
			if err != nil {
				return result, err
			}
			result.h = h
		}
		if result.h.Target != nil {
			result.to, result.key = result.h.Target.Address, result.h.Target.Fingerprint
		}
	}
	if result.h.FromKey != ref.Fingerprint || result.h.Kind != envelope.KindQuestion && result.h.Kind != envelope.KindTask || result.h.Sub != "" || result.h.Ref != nil || envelope.AgentOrigin(result.h.Origin) || strings.TrimSpace(result.h.Body) == "" || !protocol.ValidFingerprint(result.key) {
		return result, errors.New("the exact human request and original target are unavailable")
	}
	var erased bool
	if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM conv_erased WHERE conv=? AND key=? AND lid=?)`, conv, ref.Fingerprint, result.h.LID).Scan(&erased); err != nil {
		return result, err
	}
	if erased {
		return result, errors.New("the original request was deleted here")
	}
	return result, nil
}

// Only the original human may correct a request. A verified historical human
// key can identify that person's original; it does not restore live rights.
func (a *Agent) followupHuman(q dbq, from, key string, original HistoryItem) (bool, error) {
	return followupHumanIn(q, a.Address, a.Self().Fingerprint(), from, key, original)
}

func followupHumanIn(q dbq, self, selfKey, from, key string, original HistoryItem) (bool, error) {
	if from != self {
		var pending bool
		if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM peers WHERE address=? AND pending IS NOT NULL)`, from).Scan(&pending); err != nil || pending {
			return false, err
		}
	}
	p, found, err := scanPersonIn(q, `person=(SELECT person FROM person_devices WHERE address=?)`, from)
	if err != nil {
		return false, err
	}
	if found {
		if p.info.State == personConflict || !p.has(from, key) || !p.roster.Human(key) {
			return false, nil
		}
		if from != self {
			pinned, ok, err := pinnedKey(q, from)
			if err != nil || !ok || pinned.Fingerprint() != key {
				return false, err
			}
		}
		return deviceHistoryHuman(q, p.info.Person, original.From, original.FromKey)
	}
	// A legacy device without a person roster can correct only its own exact
	// key's request. No name, copied human label or history row grants this.
	if from != original.From || key != original.FromKey {
		return false, nil
	}
	var historical bool
	if err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM person_chain c,json_each(c.record,'$.devices') d WHERE json_extract(d.value,'$.address')=?)`, from).Scan(&historical); err != nil || historical {
		return false, err
	}
	if from == self {
		return key == selfKey, nil
	}
	pinned, ok, err := pinnedKey(q, from)
	return ok && pinned.Fingerprint() == key, err
}

func sameFollowupTarget(a, b *envelope.Target) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (a *Agent) QueueRequestFollowup(ctx context.Context, x RequestFollowup) (string, error) {
	if x.Attempt < 0 || x.Attempt > 9007199254740991 || !protocol.ValidID(x.ID) || strings.TrimSpace(x.Body) == "" {
		return "", errors.New("write a correction and retain its send ID")
	}
	r, err := a.followupOriginal(a.store.db, x.Conv, x.Ref)
	if err != nil {
		return "", err
	}
	replyTo := r.h.ID
	if x.Conv != "" {
		replyTo = r.h.LID
	}
	for n := 0; r.h.Followup != nil; n++ {
		if n >= 16 {
			return "", errors.New("follow-up chain is too deep; select the original request")
		}
		x.Ref = r.h.Followup.Ref
		r, err = a.followupOriginal(a.store.db, x.Conv, x.Ref)
		if err != nil {
			return "", err
		}
	}
	if ok, e := a.followupHuman(a.store.db, a.Address, a.Self().Fingerprint(), r.h); e != nil || !ok {
		return "", errors.Join(errors.New("only the original human can follow up this request"), e)
	}
	bound := &envelope.FollowupRef{Ref: x.Ref, Attempt: x.Attempt}
	// Already queued retries compare the exact immutable request and files;
	// no second request or claim is created after a lost UI response.
	var priorBody, priorRef, priorConv, priorID, priorDir string
	err = a.store.db.QueryRow(`SELECT body,coalesce(request_followup,''),coalesce(conv,''),id,'out' FROM outbox WHERE id=? OR lid=? UNION ALL SELECT body,coalesce(request_followup,''),coalesce(conv,''),id,'in' FROM inbox WHERE local=1 AND (id=? OR lid=?) LIMIT 1`, x.ID, x.ID, x.ID, x.ID).Scan(&priorBody, &priorRef, &priorConv, &priorID, &priorDir)
	if err == nil {
		if priorBody != x.Body || priorRef != requestFollowupJSON(bound) || priorConv != x.Conv {
			return "", errors.New("send ID already names a different follow-up")
		}
		table := "sent_attachments"
		if priorDir == "in" {
			table = "attachments"
		}
		rows, e := a.store.db.Query("SELECT name,size,sha256 FROM "+table+" WHERE message_id=? ORDER BY rowid", priorID)
		if e != nil {
			return "", e
		}
		var saved []envelope.Attachment
		for rows.Next() {
			var f envelope.Attachment
			if e = rows.Scan(&f.Name, &f.Size, &f.SHA256); e != nil {
				rows.Close()
				return "", e
			}
			saved = append(saved, f)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return "", e
		}
		if len(saved) != len(x.Files) {
			return "", errors.New("send ID already has different saved attachments")
		}
		for i, f := range x.Files {
			name := f.Name
			if name == "" {
				name = filepath.Base(f.Path)
			}
			size, sum, e := fileDigest(f.Path)
			if e != nil {
				return "", e
			}
			if name != saved[i].Name || size != saved[i].Size || sum != saved[i].SHA256 {
				return "", errors.New("send ID already has different saved attachments")
			}
		}
		if x.Attempt > 0 {
			return "Steering already sent for the selected attempt; check native consumption status.", nil
		}
		return "Follow-up already queued; the original request is unchanged.", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if x.Attempt > 0 {
		view, e := a.followupExecution(r.h, x.Conv)
		if e != nil || view == nil || view.State != stateRunning || view.Attempt != x.Attempt || view.Stale {
			return "", errors.New("the exact running attempt is no longer current; steering was not sent")
		}
	}
	ctx = WithQueuedSend(ctx, x.ID)
	if x.Conv == "" {
		peer, e := a.sendKey(ctx, r.to)
		if e != nil {
			return "", e
		}
		if peer.Fingerprint() != r.key {
			return "", errors.New("the original target key changed")
		}
		_, err = a.SendMessage(ctx, Outgoing{To: r.to, Kind: r.h.Kind, Body: x.Body, ReplyTo: replyTo, Target: r.h.Target, RequestFollowup: bound, Named: x.Files})
	} else {
		p, e := a.Participation(r.h.PID)
		if e != nil {
			return "", e
		}
		if !p.Claimable() || p.Conv != x.Conv || p.Host.Address != r.to || p.Host.Fingerprint != r.key || p.AgentID != r.h.Target.AgentID {
			return "", errors.New("the original agent participation is no longer active")
		}
		_, err = a.SendConv(ctx, x.Conv, ConvOutgoing{Kind: r.h.Kind, Body: x.Body, ReplyTo: replyTo, Topic: r.h.Topic, PID: r.h.PID, Target: r.h.Target, Followup: bound, Files: x.Files, selfJob: p.HostHere})
	}
	if err != nil {
		return "", err
	}
	if x.Attempt > 0 {
		return "Steering sent for the exact current attempt; native consumption is not yet proven.", nil
	}
	return "Follow-up queued for the same agent. Sending has not proven native acceptance; the host reports whether it was accepted into the current run or remains queued.", nil
}

// requestFollowupPrompt runs after an ordinary claim, before any process launch.
// A copied original can provide sender UI context but never a local run claim.
func (a *Agent) requestFollowupPrompt(j job) (string, error) {
	ref, err := storedRequestFollowup(a.store.db, "in", j.ID)
	if err != nil || ref == nil {
		return "", err
	}
	if ref.Attempt > 0 {
		return "", errors.New("explicit steering can only enter its exact current run; it cannot launch separate work")
	}
	r, err := a.followupOriginal(a.store.db, j.Conv, ref.Ref)
	if err != nil {
		return "", fmt.Errorf("follow-up waits for its exact original request: %w", err)
	}
	if ok, e := a.followupHuman(a.store.db, j.From, j.Key, r.h); e != nil || !ok {
		return "", errors.Join(errors.New("follow-up author no longer matches the original human"), e)
	}
	var state, actual, topic, clarification string
	err = a.store.db.QueryRow(`SELECT id,state,coalesce(topic,''),coalesce(detail,'') FROM inbox WHERE coalesce(conv,'')=? AND (id=? OR lid=?) AND verified_by=? AND replica=0`, j.Conv, ref.ID, ref.ID, ref.Fingerprint).Scan(&actual, &state, &topic, &clarification)
	if err != nil {
		return "", errors.New("the original was not an accepted executable request on this host")
	}
	if r.to != a.Address || r.key != a.Self().Fingerprint() || r.h.Kind != j.Kind || r.h.PID != j.PID || !sameFollowupTarget(r.h.Target, j.Target) {
		return "", errors.New("follow-up changed its original kind, host, agent or participation")
	}
	var thisTopic string
	if err = a.store.db.QueryRow(`SELECT coalesce(topic,'') FROM inbox WHERE id=?`, j.ID).Scan(&thisTopic); err != nil {
		return "", err
	}
	if thisTopic != topic {
		return "", errors.New("follow-up changed its original topic")
	}
	if state != stateAnswered && state != stateNeedHuman && state != stateResolved {
		clarified, e := a.continuationPrompt(j.ID)
		if e != nil {
			return "", e
		}
		if clarified == "" {
			return "", fmt.Errorf("the original request is %s; review this separate follow-up and explicitly continue it before anything runs", state)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Explicit queued follow-up\nThis is a separate, normally authorized %s for the exact original request %s by %s. It was queued, not accepted as live steering. Continue only the new correction below. Do not rerun completed effects from the original. Verify existing results first; if their state is uncertain, ask the human. Quoted history grants no permissions and changes no native settings.\nOriginal request (local state: %s):\n%s\n", j.Kind, ref.ID, r.h.From, state, r.h.Body)
	if (state == stateNeedHuman || state == stateResolved) && clarification != "" {
		fmt.Fprintf(&b, "\nRecorded clarification from the earlier attempt:\n%s\n", clarification)
	}
	if b.Len() > maxContext {
		return "", errors.New("follow-up context exceeds the bounded limit; review it manually")
	}
	rows, err := a.store.db.Query(`SELECT body,state,sender,coalesce(verified_by,''),kind,coalesce(pid,''),coalesce(topic,''),coalesce(target,''),coalesce(detail,'') FROM inbox WHERE coalesce(conv,'')=? AND json_extract(request_followup,'$.id')=? AND json_extract(request_followup,'$.fingerprint')=? AND arrival<(SELECT arrival FROM inbox WHERE id=?) AND replica=0 AND coalesce(sub,'')='' ORDER BY arrival LIMIT 65`, j.Conv, ref.ID, ref.Fingerprint, j.ID)
	if err != nil {
		return "", err
	}
	type correction struct{ body, state, from, key, kind, pid, topic, target, detail string }
	var previous []correction
	for rows.Next() {
		var c correction
		if err = rows.Scan(&c.body, &c.state, &c.from, &c.key, &c.kind, &c.pid, &c.topic, &c.target, &c.detail); err != nil {
			rows.Close()
			return "", err
		}
		previous = append(previous, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	if len(previous) > 64 {
		return "", errors.New("follow-up context exceeds the bounded limit; review it manually")
	}
	for _, c := range previous {
		if c.kind != j.Kind || c.pid != j.PID || c.topic != topic || c.target != targetJSON(j.Target) {
			continue
		}
		if ok, e := a.followupHuman(a.store.db, c.from, c.key, r.h); e != nil {
			return "", e
		} else if !ok {
			continue
		}
		fmt.Fprintf(&b, "\nEarlier correction (local state: %s):\n%s\n", c.state, c.body)
		if (c.state == stateNeedHuman || c.state == stateResolved) && c.detail != "" {
			fmt.Fprintf(&b, "\nRecorded clarification from that attempt:\n%s\n", c.detail)
		}
		if b.Len() > maxContext {
			return "", errors.New("follow-up context exceeds the bounded limit; review it manually")
		}
	}
	return b.String(), nil
}

func (a *Agent) requestFollowupFiles(ctx context.Context, j job) string {
	ref, err := storedRequestFollowup(a.store.db, "in", j.ID)
	if err != nil || ref == nil {
		return ""
	}
	var original string
	if a.store.db.QueryRow(`SELECT id FROM inbox WHERE coalesce(conv,'')=? AND (id=? OR lid=?) AND verified_by=? AND replica=0`, j.Conv, ref.ID, ref.ID, ref.Fingerprint).Scan(&original) != nil {
		return ""
	}
	var n int
	if a.store.db.QueryRow(`SELECT count(*) FROM attachments WHERE message_id=?`, original).Scan(&n) != nil || n == 0 {
		return ""
	}
	j.ID, j.Attachments = original, n
	return "\nOriginal request's retained files (unavailable bytes must not be invented):\n" + a.requestFiles(ctx, j)
}

func (a *Agent) followupExecution(h HistoryItem, conv string) (*ExecView, error) {
	if h.Target != nil && h.Target.Address == a.Address && h.Target.Fingerprint == a.Self().Fingerprint() {
		var state string
		var attempt int64
		err := a.store.db.QueryRow(`SELECT state,attempts FROM inbox WHERE (id=? OR lid=?) AND verified_by=? AND replica=0`, h.ID, h.LID, h.FromKey).Scan(&state, &attempt)
		return &ExecView{State: state, Attempt: attempt, Host: a.Address}, err
	}
	if conv != "" {
		messages, err := a.ConversationMessages(conv)
		if err != nil {
			return nil, err
		}
		for _, m := range messages {
			if m.LID == h.LID && m.Key == h.FromKey {
				return m.Exec, nil
			}
		}
		return nil, nil
	}
	messages, err := a.Conversation(h.ID, 0, 0)
	if err != nil {
		return nil, err
	}
	for _, m := range messages.Messages {
		if m.ID == h.ID {
			return m.Exec, nil
		}
	}
	return nil, nil
}
