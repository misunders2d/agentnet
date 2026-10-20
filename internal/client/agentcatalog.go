package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Append this new named schema step only; existing rows/IDs are retained.
const agentIdentitySchema = `
ALTER TABLE inbox ADD COLUMN agent_id TEXT;
ALTER TABLE outbox ADD COLUMN agent_id TEXT;
ALTER TABLE inbox ADD COLUMN executor TEXT;
`
const agentCatalogConfig = "agent_catalog_v1"

var ErrUnknownAgent = errors.New("selected agent is unknown, removed or belongs to another host")
var ErrAgentCatalogFull = errors.New("local agent catalog is full; choose an existing configured agent as the default")

// LocalAgentInfo contains private host configuration. Only Record is public.
// A disabled identity remains here for historical authorship; it cannot run.
type LocalAgentInfo struct {
	Record           protocol.AgentRecord `json:"record"`
	Responder        *Responder           `json:"responder,omitempty"`
	DefaultResponder bool                 `json:"default_responder,omitempty"` // migrated unnamed configuration, not a named twin
}

// ExecutorStamp is immutable admitted local execution input. It never comes
// from the sender and contains no other harness's native session reference.
type ExecutorStamp struct {
	AgentID   string                `json:"agent_id,omitempty"`
	Record    *protocol.AgentRecord `json:"record,omitempty"`
	Responder Responder             `json:"responder"`
}

func (a *Agent) localAgentsIn(q dbq) ([]LocalAgentInfo, error) {
	var raw string
	if err := q.QueryRow(`SELECT v FROM config WHERE k=?`, agentCatalogConfig).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var entries []LocalAgentInfo
	if err := decodeStrict([]byte(raw), &entries); err != nil {
		return nil, errors.New("invalid local agent catalog")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.Record.ID] || entry.Record.Verify(a.Self()) != nil {
			return nil, errors.New("invalid local agent catalog identity")
		}
		seen[entry.Record.ID] = true
	}
	if len(entries) > protocol.MaxAgentCatalog {
		return nil, errors.New("local agent catalog exceeds limit")
	}
	return entries, nil
}
func (a *Agent) LocalAgents() ([]LocalAgentInfo, error) {
	entries, err := a.localAgentsIn(a.store.db)
	for i := range entries {
		if entries[i].Responder != nil {
			entries[i].Record.Harness = entries[i].Responder.Harness
			entries[i].Record.Sign(a.id.Sign)
		}
	}
	return entries, err
}

func copyResponder(r Responder) Responder { r.Context = append([]string(nil), r.Context...); return r }
func (a *Agent) CreateLocalAgent(label string, r Responder) (protocol.AgentRecord, error) {
	r = copyResponder(r)
	if err := validateResponder(&r); err != nil {
		return protocol.AgentRecord{}, err
	}
	if err := checkOMPResponderSetup(&r); err != nil {
		return protocol.AgentRecord{}, err
	}
	record := protocol.AgentRecord{V: 1, ID: protocol.NewID(), Host: a.Address, HostKey: a.Self().Fingerprint(), Label: label, Harness: r.Harness, TS: time.Now().Unix()}
	record.Sign(a.id.Sign)
	if err := record.Verify(a.Self()); err != nil {
		return protocol.AgentRecord{}, err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return protocol.AgentRecord{}, err
	}
	defer tx.Rollback()
	entries, err := a.localAgentsIn(tx)
	if err != nil {
		return protocol.AgentRecord{}, err
	}
	if len(entries) >= protocol.MaxAgentCatalog {
		return protocol.AgentRecord{}, errors.New("local agent catalog is full")
	}
	entries = append(entries, LocalAgentInfo{Record: record, Responder: &r})
	raw, _ := json.Marshal(entries)
	if _, err = tx.Exec(`INSERT INTO config(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, agentCatalogConfig, string(raw)); err != nil {
		return protocol.AgentRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return protocol.AgentRecord{}, err
	}
	a.store.changed()
	notifyDaemon(a.home)
	return record, nil
}
func (a *Agent) SetLocalAgentResponder(id string, r *Responder) error {
	var normalized *Responder
	if r != nil {
		value := copyResponder(*r)
		if err := validateResponder(&value); err != nil {
			return err
		}
		if err := checkOMPResponderSetup(&value); err != nil {
			return err
		}
		normalized = &value
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	entries, err := a.localAgentsIn(tx)
	if err != nil {
		return err
	}
	found := false
	for i := range entries {
		if entries[i].Record.ID == id {
			entries[i].Responder = normalized
			if normalized != nil {
				entries[i].Record.Harness = normalized.Harness
				entries[i].Record.Sign(a.id.Sign)
			}
			found = true
			break
		}
	}
	if !found {
		return ErrUnknownAgent
	}
	raw, _ := json.Marshal(entries)
	if _, err = tx.Exec(`UPDATE config SET v=? WHERE k=?`, string(raw), agentCatalogConfig); err != nil {
		return err
	}
	var defaultID string
	_ = tx.QueryRow(`SELECT v FROM config WHERE k=?`, defaultAgentConfig).Scan(&defaultID)
	if defaultID == id {
		if normalized == nil {
			if err = a.saveDefaultIn(tx, nil, ""); err != nil {
				return err
			}
		} else {
			data, _ := json.Marshal(normalized)
			if _, err = tx.Exec(`UPDATE config SET v=? WHERE k='responder'`, string(data)); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.store.changed()
	notifyDaemon(a.home)
	return nil
}

// ResolveExecutorIn must be called in the existing authority claim transaction.
// A named target cannot fall back to the default, even after removal.
func (a *Agent) ResolveExecutorIn(q dbq, id string, defaultResponder *Responder) (*ExecutorStamp, error) {
	if id == "" {
		if defaultResponder == nil {
			return nil, nil
		}
		return &ExecutorStamp{Responder: copyResponder(*defaultResponder)}, nil
	}
	entries, err := a.localAgentsIn(q)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Record.ID == id && entry.Responder != nil {
			r := copyResponder(*entry.Responder)
			if err := validateResponder(&r); err != nil {
				return nil, fmt.Errorf("selected local agent unavailable: %w", err)
			}
			record := entry.Record
			record.Harness = r.Harness
			record.Sign(a.id.Sign)
			return &ExecutorStamp{AgentID: id, Record: &record, Responder: r}, nil
		}
	}
	return nil, ErrUnknownAgent
}

func (a *Agent) PublicAgentCatalog() ([]protocol.AgentRecord, error) {
	entries, err := a.LocalAgents()
	if err != nil {
		return nil, err
	}
	records := make([]protocol.AgentRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.Responder != nil {
			records = append(records, entry.Record)
		}
	}
	return records, nil
}
func (a *Agent) PublishAgentCatalog(ctx context.Context) error {
	records, err := a.PublicAgentCatalog()
	if err != nil {
		return err
	}
	features, err := a.relayFeatures(ctx)
	if err != nil {
		return err
	}
	modern := false
	for _, feature := range features {
		modern = modern || feature == protocol.FeatureAgentHarness
	}
	if !modern {
		for i := range records {
			records[i].Harness, records[i].HarnessSig = "", nil
		}
	}
	return a.hub.do(ctx, "PUT", "/v1/agent-catalog", records, nil)
}
func (a *Agent) AgentCatalog(ctx context.Context, host string) ([]protocol.AgentRecord, error) {
	key, err := a.sendKey(ctx, host)
	if err != nil {
		return nil, err
	}
	label, name, err := protocol.SplitAddress(host)
	if err != nil {
		return nil, err
	}
	profile, err := a.peerProfile(ctx, host)
	if err != nil {
		return nil, err
	}
	if !profile.Supports(host, key.SignKey, protocol.CapAgentIdentity) {
		return nil, errors.New("host cannot select stable agents yet; update all its active AgentNet sessions")
	}
	var records []protocol.AgentRecord
	if err = a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/agent-catalog", nil, &records); err != nil {
		return nil, err
	}
	if len(records) > protocol.MaxAgentCatalog {
		return nil, errors.New("agent catalog exceeds limit")
	}
	seen := map[string]bool{}
	for _, record := range records {
		if seen[record.ID] || record.Verify(key) != nil {
			return nil, errors.New("agent catalog host identity invalid")
		}
		seen[record.ID] = true
	}
	return records, nil
}
