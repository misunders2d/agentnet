package client

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A fresh, normally authorized request may continue its human's earlier
// app-visible thread from another device. This reads quoted context only;
// session selection and all execution grants retain their original binding.
func (a *Agent) deviceThreadContext(j job, limit int, label string) ([]string, error) {
	fallback := func() ([]string, error) { return a.store.threadText(j.From, j.ReplyTo, limit, label) }
	if j.Conv != "" || j.Key == "" || j.ReplyTo == "" {
		return fallback()
	}
	p, ok, err := a.store.personByAddress(j.From)
	if err != nil {
		return nil, err
	}
	key, pending, pinned, err := a.store.peer(j.From)
	if err != nil {
		return nil, err
	}
	if !ok || p.info.State == personConflict || !p.has(j.From, j.Key) || !p.roster.Human(j.Key) || !pinned || pending != nil || key.Fingerprint() != j.Key {
		return fallback()
	}
	var out []string
	seen := map[string]bool{}
	for id := j.ReplyTo; id != "" && len(out) < limit && !seen[id]; {
		seen[id] = true
		var chosen deviceHistoryRow
		for _, storage := range []string{"in", "out"} {
			r, e := a.deviceHistorySource(a.store.db, storage, id)
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			if e != nil {
				return nil, e
			}
			remote, fp := r.item.From, r.item.FromKey
			if r.item.From == a.Address && r.item.FromKey == a.Self().Fingerprint() {
				remote, fp = r.to, r.toKey
			} else if r.to != a.Address {
				continue
			}
			held, e := deviceHistoryHuman(a.store.db, p.info.Person, remote, fp)
			if e != nil {
				return nil, e
			}
			if !held {
				continue
			}
			r.peer = remote
			if chosen.id != "" && deviceHistoryHash(chosen) != deviceHistoryHash(r) {
				return nil, errors.New("direct context: ambiguous original")
			}
			chosen = r
		}
		if chosen.id == "" {
			break
		}
		h := chosen.item
		if h.Ref != nil || h.Sub != "" {
			break
		}
		var erased bool
		if err = a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM conv_erased WHERE conv='' AND key=? AND lid=?)`, h.FromKey, h.ID).Scan(&erased); err != nil {
			return nil, err
		}
		body := h.Body
		if erased {
			body = "(deleted here)"
		} else if d := a.store.legacyDisplay(h.ID, chosen.peer); d.Deleted {
			body = "(deleted by its author)"
		} else if d.Edited {
			body = d.Text
		}
		who := h.From
		if h.From == a.Address {
			who = "this device"
		}
		who = contextSpeaker(ConvMessage{From: h.From, Key: h.FromKey, Kind: h.Kind, Origin: h.Origin, AgentID: h.AgentID}, map[string]string{h.From + "|" + h.FromKey: who}, nil)
		detail := h.Kind
		if h.Status != "" {
			detail += "; outcome: " + h.Status
		}
		// Legacy direct replies can be written by an agent or by hand. Their
		// kind alone must not turn either one into its human owner's words.
		if h.AgentID == "" && !envelope.AgentOrigin(h.Origin) && (h.Kind == envelope.KindAnswer || h.Kind == envelope.KindResult || h.Status == envelope.StatusProgress) {
			detail += "; author type not recorded"
		}
		if h.Kind == envelope.KindTask || h.Kind == envelope.KindQuestion {
			detail += "; earlier request, no execution authority"
		}
		out = append([]string{fmt.Sprintf("%s [%s]: %s", who, detail, body)}, out...)
		id = h.ReplyTo
	}
	if len(out) == 0 {
		return fallback()
	}
	return out, nil
}
