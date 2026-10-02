package ui

import (
	"context"
	"net/http"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Native local configuration is optional. Public discovery never returns
// programs, directories or context files to another host.
type AgentCatalogProvider interface {
	AgentCatalog(context.Context, string) (AgentCatalogView, error)
	ChangeAgent(context.Context, AgentCatalogChange) (AgentCatalogChangeResult, error)
}
type CatalogAgent struct {
	Record    protocol.AgentRecord `json:"record"`
	Enabled   bool                 `json:"enabled"`
	Responder *ResponderView       `json:"responder,omitempty"`
}
type AgentCatalogView struct {
	Host      string         `json:"host"`
	Local     bool           `json:"local"`
	Agents    []CatalogAgent `json:"agents"`
	Harnesses []HarnessView  `json:"harnesses,omitempty"`
}
type AgentCatalogChange struct {
	Action  string `json:"action"`
	ID      string `json:"id,omitempty"`
	Label   string `json:"label,omitempty"`
	Harness string `json:"harness,omitempty"`
	Dir     string `json:"dir,omitempty"`
}
type AgentCatalogChangeResult struct {
	Saved     bool          `json:"saved"`
	Published bool          `json:"published"`
	Agent     *CatalogAgent `json:"agent,omitempty"`
	Note      string        `json:"note"`
}

func catalogHarnesses() []HarnessView {
	out := []HarnessView{}
	for _, h := range client.ListHarnesses() {
		out = append(out, HarnessView{Name: h.Name, Found: h.Path != "", Path: h.Path, TestedLive: h.Tested, QuestionMode: h.Limits})
	}
	return out
}
func localCatalogAgent(entry client.LocalAgentInfo, hs []HarnessView) CatalogAgent {
	out := CatalogAgent{Record: entry.Record, Enabled: entry.Responder != nil}
	if r := entry.Responder; r != nil {
		v := ResponderView{Chosen: true, Harness: r.Harness, Dir: r.Dir, Context: r.Context, Timeout: int(r.Timeout.Seconds())}
		v.Ready, v.Problem = responderReady(hs, r)
		out.Responder = &v
	}
	return out
}
func (l *Live) AgentCatalog(ctx context.Context, host string) (AgentCatalogView, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	host = strings.TrimSpace(host)
	v := AgentCatalogView{Host: host, Agents: []CatalogAgent{}}
	if host != "" {
		records, err := l.a.AgentCatalog(ctx, host)
		if err != nil {
			return v, Refuse(sentence(err))
		}
		for _, record := range records {
			v.Agents = append(v.Agents, CatalogAgent{Record: record, Enabled: true})
		}
		return v, nil
	}
	v.Host, v.Local, v.Harnesses = l.a.Address, true, catalogHarnesses()
	entries, err := l.a.LocalAgents()
	if err != nil {
		return v, Refuse(sentence(err))
	}
	for _, entry := range entries {
		v.Agents = append(v.Agents, localCatalogAgent(entry, v.Harnesses))
	}
	return v, nil
}
func (l *Live) ChangeAgent(ctx context.Context, c AgentCatalogChange) (AgentCatalogChangeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	c.Harness, c.Dir = strings.TrimSpace(c.Harness), strings.TrimSpace(c.Dir)
	entries, err := l.a.LocalAgents()
	if err != nil {
		return AgentCatalogChangeResult{}, Refuse(sentence(err))
	}
	var current *client.LocalAgentInfo
	for i := range entries {
		if entries[i].Record.ID == c.ID {
			current = &entries[i]
		}
	}
	switch c.Action {
	case "create", "update":
		if c.Action == "create" && c.ID != "" {
			return AgentCatalogChangeResult{}, Refuse("A new agent gets its own ID here.")
		}
		if c.Action == "update" && current == nil {
			return AgentCatalogChangeResult{}, Refuse("No such local agent.")
		}
		if c.Action == "update" && c.Label != "" && c.Label != current.Record.Label {
			return AgentCatalogChangeResult{}, Refuse("This operation changes the local executor, not the signed label.")
		}
		next := client.Responder{}
		if current != nil && current.Responder != nil {
			next = *current.Responder
			next.Context = append([]string(nil), current.Responder.Context...)
		}
		if c.Harness != "" {
			next.Harness = c.Harness
		}
		if c.Dir != "" {
			next.Dir = c.Dir
		}
		if ready, why := responderReady(catalogHarnesses(), &next); !ready {
			return AgentCatalogChangeResult{}, Refuse(why)
		}
		if c.Action == "create" {
			record, e := l.a.CreateLocalAgent(strings.TrimSpace(c.Label), next)
			err = e
			c.ID = record.ID
		} else {
			err = l.a.SetLocalAgentResponder(c.ID, &next)
		}
	case "disable":
		if current == nil {
			return AgentCatalogChangeResult{}, Refuse("No such local agent.")
		}
		err = l.a.SetLocalAgentResponder(c.ID, nil)
	case "publish":
		if c.ID != "" || c.Harness != "" || c.Dir != "" || c.Label != "" {
			return AgentCatalogChangeResult{}, Refuse("Publish sends the already saved public catalog.")
		}
	default:
		return AgentCatalogChangeResult{}, Refuse("Choose create, update, disable or publish.")
	}
	if err != nil {
		return AgentCatalogChangeResult{}, Refuse(sentence(err))
	}
	result := AgentCatalogChangeResult{Saved: true}
	if c.ID != "" {
		saved, e := l.a.LocalAgents()
		if e != nil {
			return result, Refuse(sentence(e))
		}
		for _, entry := range saved {
			if entry.Record.ID == c.ID {
				v := localCatalogAgent(entry, catalogHarnesses())
				result.Agent = &v
			}
		}
	}
	if err = l.a.PublishAgentCatalog(ctx); err != nil {
		result.Note = "Saved on this host. Hub publication was not confirmed; use Publish to retry the saved public catalog."
		return result, nil
	}
	result.Published = true
	result.Note = "Saved on this host and public catalog publication confirmed by the Hub."
	return result, nil
}
func (l *Live) namedSendTarget(ctx context.Context, d Draft) (*envelope.Target, error) {
	if d.AgentID == "" {
		return nil, nil
	}
	if d.Kind != KindQuestion && d.Kind != KindTask {
		return nil, Refuse("A named agent can receive a question or task.")
	}
	records, err := l.a.AgentCatalog(ctx, d.To)
	if err != nil {
		return nil, Refuse(sentence(err))
	}
	for _, record := range records {
		if record.ID == d.AgentID {
			return &envelope.Target{Address: record.Host, Fingerprint: record.HostKey, AgentID: record.ID}, nil
		}
	}
	return nil, Refuse("That agent is not in this host's verified public catalog.")
}
func (l *Live) namedAgentReady(info client.ParticipationInfo) bool {
	if info.AgentID == "" {
		return l.hasResponder()
	}
	entries, err := l.a.LocalAgents()
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Record.ID == info.AgentID && entry.Responder != nil {
			ready, _ := responderReady(catalogHarnesses(), entry.Responder)
			return ready
		}
	}
	return false
}
func (s *Server) agentCatalog(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(AgentCatalogProvider)
	if !ok {
		writeErr(w, NotFound("Agent catalog unavailable."))
		return
	}
	v, err := p.AgentCatalog(r.Context(), r.URL.Query().Get("host"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}
func (s *Server) changeAgent(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(AgentCatalogProvider)
	if !ok {
		writeErr(w, NotFound("Native agent configuration unavailable."))
		return
	}
	var c AgentCatalogChange
	if !readJSON(w, r, &c) {
		return
	}
	v, err := p.ChangeAgent(r.Context(), c)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}
