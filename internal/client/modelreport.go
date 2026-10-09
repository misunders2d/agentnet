package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const modelReportSchema = `
CREATE TABLE agent_models(host_key TEXT NOT NULL,agent_id TEXT NOT NULL,body TEXT NOT NULL,revision INTEGER NOT NULL,PRIMARY KEY(host_key,agent_id));
CREATE TABLE agent_model_copies(recipient_fp TEXT NOT NULL,agent_id TEXT NOT NULL,revision INTEGER NOT NULL,carrier TEXT NOT NULL,PRIMARY KEY(recipient_fp,agent_id));
`
const modelReportMarker = "AGENTNET-MODEL: "
const modelReportPrompt = "\nOptional private runtime metadata: only if your current native context already identifies your exact active model, append one final line AGENTNET-MODEL: NAME after all other output. Otherwise omit it. Do not guess, read configuration, call tools or change settings to obtain this metadata. It is shown only to your owner as an agent-reported value.\n"

func reportedModel(body string) (string, string) {
	text := strings.TrimSpace(body)
	i := strings.LastIndexByte(text, '\n')
	line := text[i+1:]
	if !strings.HasPrefix(line, modelReportMarker) {
		return body, ""
	}
	name := strings.TrimPrefix(line, modelReportMarker)
	// Invalid metadata is never interpreted as a setting or retained as a model.
	if !protocol.ValidReportedModel(name) {
		return body, ""
	}
	if i < 0 {
		return "", name
	}
	return strings.TrimSpace(text[:i]), name
}
func modelExecutor(r Responder) string {
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (a *Agent) recordModelReport(j job, r *Responder, name string) error {
	if r == nil || j.Executor == nil || j.Receiver != nil || !protocol.ValidReportedModel(name) {
		return nil
	}
	tx, e := a.store.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var state, executor string
	if e = tx.QueryRow(`SELECT state,coalesce(executor,'') FROM inbox WHERE id=? AND replica=0`, j.ID).Scan(&state, &executor); e != nil {
		return e
	}
	var stamp ExecutorStamp
	if state != stateRunning || json.Unmarshal([]byte(executor), &stamp) != nil || stamp.AgentID != j.AgentID || modelExecutor(stamp.Responder) != modelExecutor(*r) {
		return nil
	}
	fp := a.Self().Fingerprint()
	var n int64
	if e = tx.QueryRow(`SELECT coalesce(max(revision),0) FROM agent_models WHERE host_key=? AND agent_id=?`, fp, j.AgentID).Scan(&n); e != nil {
		return e
	}
	m := protocol.AgentModel{AgentID: j.AgentID, Model: name, Harness: r.Harness, Executor: modelExecutor(*r), At: time.Now().Unix(), Revision: n + 1}
	if n > 0 {
		var raw string
		if e = tx.QueryRow(`SELECT body FROM agent_models WHERE host_key=? AND agent_id=?`, fp, j.AgentID).Scan(&raw); e != nil {
			return e
		}
		var previous protocol.AgentModel
		if json.Unmarshal([]byte(raw), &previous) == nil && previous.Model == m.Model && previous.Harness == m.Harness && previous.Executor == m.Executor {
			return nil
		}
	}
	if n >= protocol.MaxTopicTitleRevision {
		return errors.New("model report revision exhausted")
	}
	if e = saveModelReport(tx, fp, m); e != nil {
		return e
	}
	if e = a.store.done(tx.Commit()); e == nil {
		a.convWork.due(convHistory)
		a.kickNow()
	}
	return e
}
func saveModelReport(tx *sql.Tx, fp string, m protocol.AgentModel) error {
	raw, _ := json.Marshal(m)
	var previous string
	var rev int64
	e := tx.QueryRow(`SELECT body,revision FROM agent_models WHERE host_key=? AND agent_id=?`, fp, m.AgentID).Scan(&previous, &rev)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if e == nil && rev >= m.Revision {
		if rev == m.Revision && previous != string(raw) {
			return errors.New("model report: conflicting revision")
		}
		return nil
	}
	_, e = tx.Exec(`INSERT INTO agent_models VALUES(?,?,?,?) ON CONFLICT(host_key,agent_id) DO UPDATE SET body=excluded.body,revision=excluded.revision`, fp, m.AgentID, string(raw), m.Revision)
	return e
}

// PrivateModelReport is projected to own-human readers for current verified own hosts.
type PrivateModelReport struct {
	protocol.AgentModel
	Host    string `json:"host"`
	HostKey string `json:"host_key"`
}

func (a *Agent) ModelReports() ([]PrivateModelReport, error) {
	me, ok, e := a.store.selfPerson(a.Address)
	if e != nil || !ok || me.info.State != personSelf || !me.has(a.Address, a.Self().Fingerprint()) || !me.roster.Human(a.Self().Fingerprint()) {
		return nil, e
	}
	current := map[string]string{}
	if r, e := a.Responder(); e != nil {
		return nil, e
	} else if r != nil {
		current[""] = modelExecutor(*r)
	}
	entries, e := a.LocalAgents()
	if e != nil {
		return nil, e
	}
	for _, entry := range entries {
		if entry.Responder != nil {
			current[entry.Record.ID] = modelExecutor(*entry.Responder)
		}
	}
	out := []PrivateModelReport{}
	for _, dev := range me.roster.Devices {
		fp := dev.Fingerprint()
		if dev.Address != a.Address {
			pin, pending, found, e := a.store.peer(dev.Address)
			if e != nil {
				return nil, e
			}
			if !found || pending != nil || pin.Fingerprint() != fp {
				continue
			}
		}
		rows, e := a.store.db.Query(`SELECT body FROM agent_models WHERE host_key=? ORDER BY agent_id LIMIT 64`, fp)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				break
			}
			var m protocol.AgentModel
			if json.Unmarshal([]byte(raw), &m) == nil && (dev.Address != a.Address || current[m.AgentID] == m.Executor) {
				out = append(out, PrivateModelReport{m, dev.Address, fp})
			}
		}
		re := rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if re != nil {
			return nil, re
		}
	}
	return out, nil
}

// modelSyncAuthority permits a current own host to report only its own metadata
// to a current own-human reader. It grants no enrollment or execution authority.
func modelSyncAuthority(q dbq, r protocol.ModelSync, from, fromFP, to, toFP string) error {
	me, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return err
	}
	if !ok || r.Person != me.info.Person || from == to || !me.has(from, fromFP) || !me.has(to, toFP) || !me.roster.Human(toFP) {
		return errors.New("model sync: only current own hosts may report to own-human devices")
	}
	for _, endpoint := range []struct{ address, fp string }{{from, fromFP}, {to, toFP}} {
		var raw string
		var pending sql.NullString
		e := q.QueryRow(`SELECT public,pending FROM peers WHERE address=?`, endpoint.address).Scan(&raw, &pending)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		var key identity.Public
		if pending.Valid || json.Unmarshal([]byte(raw), &key) != nil || key.Fingerprint() != endpoint.fp {
			return errors.New("model sync: device key changed or pending")
		}
	}
	bound, err := inChainIn(q, r.Person, r.Roster)
	if err != nil {
		return err
	}
	if !bound {
		return errors.New("model sync: unverified roster")
	}
	return nil
}
func (a *Agent) syncModelReports() (bool, error) {
	release, e := lockfile.Wait(a.spoolLockPath())
	if e != nil {
		return false, e
	}
	defer release()
	tx, e := a.store.db.Begin()
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	me, ok, e := scanPersonIn(tx, "state = ?", personSelf)
	if e != nil || !ok {
		return false, e
	}
	fp := a.Self().Fingerprint()
	if !me.has(a.Address, fp) {
		return false, nil
	}
	var copies []outCopy
	for _, dev := range me.roster.Devices {
		if dev.Address == a.Address || !me.roster.Human(dev.Fingerprint()) {
			continue
		}
		r := protocol.ModelSync{V: 1, Person: me.info.Person, Roster: me.info.Roster}
		if modelSyncAuthority(tx, r, a.Address, fp, dev.Address, dev.Fingerprint()) != nil {
			continue
		}
		rows, e := tx.Query(`SELECT body FROM agent_models m WHERE host_key=? AND NOT EXISTS(SELECT 1 FROM agent_model_copies c JOIN outbox o ON o.id=c.carrier WHERE c.recipient_fp=? AND c.agent_id=m.agent_id AND c.revision=m.revision AND o.state IN ('queued','waiting','custody','delivered','quarantined')) ORDER BY agent_id LIMIT 64`, fp, dev.Fingerprint())
		if e != nil {
			return false, e
		}
		for rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				break
			}
			var m protocol.AgentModel
			if e = json.Unmarshal([]byte(raw), &m); e != nil {
				break
			}
			r.Reports = append(r.Reports, m)
		}
		re := rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		if re != nil {
			return false, re
		}
		if len(r.Reports) == 0 {
			continue
		}
		raw, _ := json.Marshal(r)
		recipient, e := dev.Recipient()
		if e != nil {
			return false, e
		}
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubModelSync, Replica: true, Body: string(raw)}
		env, e := envelope.Seal(in, a.id.Sign, recipient)
		if e != nil {
			return false, e
		}
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapModelSync, recipientFP: dev.Fingerprint()})
		if len(copies) == historyPage {
			break
		}
	}
	if len(copies) == 0 {
		return false, nil
	}
	if e = insertCopies(tx, copies); e != nil {
		return false, e
	}
	for _, c := range copies {
		r, _ := protocol.ParseModelSync([]byte(c.in.Body))
		for _, m := range r.Reports {
			if _, e = tx.Exec(`INSERT OR REPLACE INTO agent_model_copies VALUES(?,?,?,?)`, c.recipientFP, m.AgentID, m.Revision, c.env.ID); e != nil {
				return false, e
			}
		}
	}
	return len(copies) == historyPage, a.store.done(tx.Commit())
}
func (a *Agent) admitModelSync(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	r, e := protocol.ParseModelSync([]byte(in.Body))
	if e != nil {
		return hold(reasonInvalid, e.Error())
	}
	// A newly enrolled own host may not be listed in the reader's older local
	// roster yet. Refresh the already trusted own person, never a body claim.
	me, own, e := a.store.selfPerson(a.Address)
	if e != nil {
		return e
	}
	if !own {
		return hold(reasonInvalid, "model sync: no current own person")
	}
	if _, e = a.refreshPerson(ctx, me.info.Person, false); e != nil {
		return e
	}
	tx, e := a.store.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = modelSyncAuthority(tx, r, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint()); e != nil {
		tx.Rollback()
		return hold(reasonInvalid, e.Error())
	}
	for _, m := range r.Reports {
		if e = saveModelReport(tx, sender.Fingerprint(), m); e != nil {
			tx.Rollback()
			return hold(reasonInvalid, e.Error())
		}
	}
	if held {
		if _, e = tx.Exec(`DELETE FROM quarantine WHERE id=?`, env.ID); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(`INSERT INTO history_receipts(id) VALUES(?) ON CONFLICT(id) DO UPDATE SET acked=0`, env.ID); e != nil {
		return e
	}
	return a.store.done(tx.Commit())
}
func (a *Agent) mayDeliverModelSync(env envelope.Envelope) (bool, bool, error) {
	var sub, body, fp, state string
	e := a.store.db.QueryRow(`SELECT coalesce(sub,''),body,coalesce(recipient_fp,''),state FROM outbox WHERE id=?`, env.ID).Scan(&sub, &body, &fp, &state)
	if errors.Is(e, sql.ErrNoRows) || e == nil && sub != envelope.SubModelSync {
		return false, false, nil
	}
	if e != nil {
		return true, false, e
	}
	if state != stateQueued {
		return true, false, nil
	}
	if e = a.refreshRecipientPerson(context.Background(), env.To, map[string]error{}); e != nil {
		return true, false, e
	}
	r, parse := protocol.ParseModelSync([]byte(body))
	check := func() (identity.Public, bool, error) {
		key, pending, found, e := a.store.peer(env.To)
		if e != nil {
			return key, false, e
		}
		ok := parse == nil && found && pending == nil && key.Fingerprint() == fp && modelSyncAuthority(a.store.db, r, env.From, a.Self().Fingerprint(), env.To, fp) == nil
		if !ok {
			e = a.store.setOutboxState(env.ID, stateNotDelivered, "model report owner or host key changed", "")
		}
		return key, ok, e
	}
	key, ok, e := check()
	if e != nil || !ok {
		return true, false, e
	}
	if e = a.requireParticipationCaps(context.Background(), key, protocol.CapModelSync); e != nil {
		if errors.Is(e, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+e.Error(), "")
		}
		return true, false, e
	}
	_, ok, e = check()
	return true, ok, e
}
