package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

const defaultAgentConfig = "responder_agent_id"

func HarnessLabel(h string) string {
	switch h {
	case "pi":
		return "Pi"
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "omp":
		return "OMP"
	case "agy":
		return "Antigravity"
	}
	return h
}

// DefaultAgentID names future explicit traffic only. Retained untargeted
// requests and admitted executor snapshots are deliberately never rebound.
func (a *Agent) DefaultAgentID() (string, error) {
	if r, err := a.Responder(); err != nil || r == nil {
		return "", err
	}
	id, err := a.store.config(defaultAgentConfig)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (a *Agent) saveDefaultIn(tx *sql.Tx, r *Responder, selectID string) error {
	if r == nil {
		if _, err := tx.Exec(`DELETE FROM config WHERE k='responder'`); err != nil {
			return err
		}
		// Manual handling of untargeted requests does not disconnect a harness.
		_, err := tx.Exec(`INSERT INTO config(k,v) VALUES('responder_manual','1') ON CONFLICT(k) DO UPDATE SET v='1'`)
		return err
	}
	entries, err := a.localAgentsIn(tx)
	if err != nil {
		return err
	}
	var id string
	if selectID != "" {
		id = selectID
	} else {
		_ = tx.QueryRow(`SELECT v FROM config WHERE k=?`, defaultAgentConfig).Scan(&id)
	}
	selected := -1
	for i := range entries {
		if entries[i].Record.ID == id && (entries[i].Responder != nil && (selectID != "" || entries[i].Responder.Harness == r.Harness) || selectID == "" && entries[i].DefaultResponder && entries[i].Record.Harness == r.Harness) {
			selected = i
			break
		}
	}
	if selectID != "" && selected < 0 {
		return ErrUnknownAgent
	}
	if selected < 0 && selectID == "" {
		// A harness has one active identity on this device, regardless of name
		// or working folder. Never mint a parallel default identity.
		for i := range entries {
			if entries[i].Responder != nil && entries[i].Responder.Harness == r.Harness {
				if selected >= 0 {
					return ErrAgentHarnessAmbiguous
				}
				selected = i
			}
		}
		if selected < 0 {
			for i := range entries {
				if entries[i].DefaultResponder && entries[i].Record.Harness == r.Harness {
					if selected >= 0 {
						return ErrAgentHarnessAmbiguous
					}
					selected = i
				}
			}
		}
	}
	if selected < 0 {
		if len(entries) >= protocol.MaxAgentCatalog {
			return ErrAgentCatalogFull
		}
		record := protocol.AgentRecord{V: 1, ID: protocol.NewID(), Host: a.Address, HostKey: a.Self().Fingerprint(), Label: HarnessLabel(r.Harness), Harness: r.Harness, TS: time.Now().Unix()}
		record.Sign(a.id.Sign)
		entries = append(entries, LocalAgentInfo{Record: record, Responder: r, DefaultResponder: true})
		selected = len(entries) - 1
	} else if selectID == "" {
		entries[selected].Responder = r
	}
	r = entries[selected].Responder
	entries[selected].Record.Harness = r.Harness
	entries[selected].Record.Sign(a.id.Sign)
	// An exact saved pointer or an explicit selection proves the current
	// identity. Retire only duplicate configurations of that same harness;
	// all records, original conversations and admitted executors stay intact.
	for i := range entries {
		if i != selected && entries[i].Responder != nil && entries[i].Responder.Harness == r.Harness {
			entries[i].Responder = nil
		}
	}
	raw, _ := json.Marshal(entries)
	if _, err = tx.Exec(`INSERT INTO config(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, agentCatalogConfig, string(raw)); err != nil {
		return err
	}
	data, _ := json.Marshal(r)
	if _, err = tx.Exec(`INSERT INTO config(k,v) VALUES('responder',?),(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, string(data), defaultAgentConfig, entries[selected].Record.ID); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM config WHERE k='responder_manual'`)
	return err
}

// Migration reads only saved configuration. It never changes an old request,
// admitted executor or native session, and never guesses among legacy twins.
func (a *Agent) ensureDefaultAgent() error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := responderIn(tx)
	if err != nil || r == nil {
		return err
	}
	var id string
	if err = tx.QueryRow(`SELECT v FROM config WHERE k=?`, defaultAgentConfig).Scan(&id); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	entries, err := a.localAgentsIn(tx)
	if err != nil {
		return err
	}
	if enabledHarnessCount(entries, r.Harness) == 1 {
		want, _ := json.Marshal(r)
		for _, entry := range entries {
			if entry.Record.ID == id && entry.Record.Harness == r.Harness {
				saved, _ := json.Marshal(entry.Responder)
				if string(saved) == string(want) {
					return nil // ordinary CLI/hook opens do not rewrite canonical settings
				}
			}
		}
	}
	if err = a.saveDefaultIn(tx, r, ""); err != nil {
		if errors.Is(err, ErrAgentCatalogFull) || errors.Is(err, ErrAgentHarnessAmbiguous) || errors.Is(err, ErrUnknownAgent) {
			return nil
		} // preserve readable old profiles; no identity is guessed
		return err
	}
	return tx.Commit()
}

func (a *Agent) SetDefaultAgent(id string) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = a.saveDefaultIn(tx, &Responder{}, id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.store.changed()
	notifyDaemon(a.home)
	return nil
}
