package ui

import (
	"context"
	"net/http"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// ReplyReceiverSelection accepts local intent only. Derived executor/preset,
// existing binding IDs, native paths and session owner credentials stay absent.
type ReplyReceiverSelection struct {
	Host          *client.ReplyReceiverHost   `json:"host,omitempty"`
	OnClose       *client.ManagedReplyHandoff `json:"on_close,omitempty"`
	Kind          string                      `json:"kind"`
	SessionHandle string                      `json:"session_handle,omitempty"`
	AgentID       string                      `json:"agent_id,omitempty"`
	Instructions  string                      `json:"instructions,omitempty"`
	Mode          string                      `json:"mode,omitempty"`
}

// Safe local registration metadata; active is not a native process health check.
type ReplySessionChoice struct {
	Handle  string `json:"handle"`
	Harness string `json:"harness"`
	Label   string `json:"label"`
	Active  bool   `json:"active"`
}
type ReplySessionCatalogView struct {
	Host     string               `json:"host"`
	Local    bool                 `json:"local"`
	Sessions []ReplySessionChoice `json:"sessions"`
}
type ReplySessionCatalogProvider interface {
	ReplySessions() (ReplySessionCatalogView, error)
}

// Optional additive remote branch; local provider shape remains unchanged.
type RemoteReplySessionCatalogProvider interface {
	ReplySessionsAt(context.Context, client.ReplyReceiverHost) (client.ReplyReceiverCatalog, error)
}

func (l *Live) ReplySessionsAt(ctx context.Context, host client.ReplyReceiverHost) (client.ReplyReceiverCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	v, e := l.a.ReplyReceiverCatalog(ctx, host)
	if e != nil {
		return v, Refuse(sentence(e))
	}
	return v, nil
}

type ReplyReceivers interface {
	ReplyReceiverBindings() ([]ReplyReceiverBindingView, error)
}
type ReplyReceiverBindingView struct {
	ID           string                   `json:"id"`
	Conv         string                   `json:"conv,omitempty"`
	RequestRef   string                   `json:"request_ref"`
	Receiver     ReplyReceiverSelection   `json:"receiver"`
	Label        string                   `json:"label"`
	Host         string                   `json:"host"`
	State        string                   `json:"state"`
	Detail       string                   `json:"detail,omitempty"`
	HandoffState string                   `json:"handoff_state,omitempty"`
	HandoffLabel string                   `json:"handoff_label,omitempty"`
	Inputs       []ReplyReceiverInputView `json:"inputs,omitempty"`
}
type ReplyReceiverInputView struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	NativeState string `json:"native_state,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// Reject unavailable/invalid receiver intent before consuming staged files.
// The client's transactional receiver binding remains the final authority.
func (l *Live) selectedReplyReceiver(d *ReplyReceiverSelection) (*client.ReplyReceiver, error) {
	if d == nil {
		return nil, nil
	}
	if d.OnClose != nil && d.Kind != "live_session" {
		return nil, Refuse("Closed-session backup requires an exact native reply session.")
	}
	r := &client.ReplyReceiver{Kind: d.Kind, AgentID: d.AgentID, SessionHandle: d.SessionHandle, Instructions: strings.TrimSpace(d.Instructions), Mode: d.Mode, Host: d.Host}
	if d.Host != nil {
		choice := envelope.ReceiverChoice{Kind: r.Kind, AgentID: r.AgentID, SessionHandle: r.SessionHandle, Instructions: r.Instructions, Mode: r.Mode}
		if d.OnClose != nil {
			choice.OnClose = &envelope.ReceiverOnClose{AgentID: d.OnClose.AgentID, Instructions: strings.TrimSpace(d.OnClose.Instructions), Mode: d.OnClose.Mode}
			r.OnClose = &client.ManagedReplyHandoff{AgentID: choice.OnClose.AgentID, Instructions: choice.OnClose.Instructions, Mode: choice.OnClose.Mode}
		}
		if err := choice.Validate(); err != nil {
			return nil, Refuse(sentence(err))
		}
		ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
		defer cancel()
		catalog, err := l.a.ReplyReceiverCatalog(ctx, *d.Host)
		if err != nil {
			return nil, Refuse(sentence(err))
		}
		if catalog.Local {
			local := *d
			local.Host = nil
			return l.selectedReplyReceiver(&local)
		}
		if catalog.Status == "unavailable" {
			return nil, Refuse(catalog.Detail)
		}
		if r.Kind == "live_session" {
			if catalog.Status != "ready" {
				return nil, Refuse("Selected host session catalog is not available yet. Nothing sent.")
			}
			for _, session := range catalog.Sessions {
				if session.Handle == r.SessionHandle {
					return r, nil
				}
			}
			return nil, Refuse("Selected native reply session is not registered on that host. Nothing sent; no default was chosen.")
		}
		return r, nil // selected host resolves its own enabled preset at local approval
	}
	if d.OnClose != nil {
		// Reuse managed intent validation before any send path consumes files.
		backup, err := l.selectedReplyReceiver(&ReplyReceiverSelection{Kind: "managed_agent", AgentID: d.OnClose.AgentID, Instructions: d.OnClose.Instructions, Mode: d.OnClose.Mode})
		if err != nil {
			return nil, err
		}
		r.OnClose = &client.ManagedReplyHandoff{AgentID: backup.AgentID, Instructions: backup.Instructions, Mode: backup.Mode}
	}
	switch r.Kind {
	case "human":
		if r.AgentID != "" || r.SessionHandle != "" || r.Instructions != "" || r.Mode != "" {
			return nil, Refuse("Me receives replies without assistant instructions or mode.")
		}
	case "managed_agent":
		if r.SessionHandle != "" || !protocol.ValidID(r.AgentID) || r.Instructions == "" || len(r.Instructions) > 4<<10 || (r.Mode != envelope.KindQuestion && r.Mode != envelope.KindTask) {
			return nil, Refuse("Choose an enabled local assistant, original continuation instructions and question or task mode.")
		}
		entries, err := l.a.LocalAgents()
		if err != nil {
			return nil, Refuse(sentence(err))
		}
		for _, entry := range entries {
			if entry.Record.ID == r.AgentID && entry.Responder != nil {
				if ready, why := responderReady(catalogHarnesses(), entry.Responder); !ready {
					return nil, Refuse(why)
				}
				return r, nil
			}
		}
		return nil, Refuse("Selected reply assistant is no longer enabled on this host. Nothing sent; no default was chosen.")
	case "live_session":
		if r.SessionHandle == "" || r.AgentID != "" || r.Instructions != "" || r.Mode != "" {
			return nil, Refuse("Choose an exact registered native session, without assistant mode or instructions.")
		}
		entries, err := l.a.ReplySessions()
		if err != nil {
			return nil, Refuse(sentence(err))
		}
		for _, entry := range entries {
			if entry.Handle == r.SessionHandle {
				return r, nil
			}
		}
		return nil, Refuse("Selected native reply session is not registered here. Nothing sent; no default was chosen.")
	default:
		return nil, Refuse("Choose Me, an enabled local assistant or an exact registered native session as reply receiver.")
	}
	return r, nil
}

func (l *Live) ReplyReceiverBindings() ([]ReplyReceiverBindingView, error) {
	rows, err := l.a.ReplyReceiverBindings()
	if err != nil {
		return nil, Refuse(sentence(err))
	}
	agents, err := l.a.LocalAgents()
	if err != nil {
		return nil, Refuse(sentence(err))
	}
	sessions, err := l.a.ReplySessions()
	if err != nil {
		return nil, Refuse(sentence(err))
	}
	inbox, err := l.a.Inbox(false, false)
	if err != nil {
		return nil, Refuse(sentence(err))
	}
	messages := map[string]client.Message{}
	for _, m := range inbox {
		messages[m.ID] = m
	}
	out := []ReplyReceiverBindingView{}
	for _, b := range rows {
		v := ReplyReceiverBindingView{ID: b.ID, Conv: b.Conv, RequestRef: b.RequestRef, Receiver: ReplyReceiverSelection{Kind: b.Receiver.Kind, AgentID: b.Receiver.AgentID, SessionHandle: b.Receiver.SessionHandle, Instructions: b.Receiver.Instructions, Mode: b.Receiver.Mode, OnClose: b.Receiver.OnClose, Host: b.Receiver.Host}, Host: l.a.Address, State: b.State, Detail: b.Detail, HandoffState: b.HandoffState, Label: "Me (human)"}
		if b.Receiver.Host != nil {
			v.Host = b.Receiver.Host.Address
		}
		if b.Receiver.OnClose != nil {
			v.HandoffLabel = "Selected assistant"
			for _, a := range agents {
				if a.Record.ID == b.Receiver.OnClose.AgentID {
					v.HandoffLabel = a.Record.Label
				}
			}
		}
		if b.Receiver.Kind == "live_session" {
			v.Label = "Selected native session"
			for _, session := range sessions {
				if session.Handle == b.Receiver.SessionHandle {
					v.Label = session.Label + " (" + session.Harness + ")"
				}
			}
		}
		if b.Receiver.Kind == "managed_agent" {
			v.Label = "Selected assistant"
			for _, a := range agents {
				if a.Record.ID == b.Receiver.AgentID {
					v.Label = a.Record.Label
				}
			}
		}
		uncertain := false
		for _, in := range b.Inputs {
			m := messages[in.ID]
			v.Inputs = append(v.Inputs, ReplyReceiverInputView{ID: in.ID, State: in.State, NativeState: m.State, Detail: in.Detail})
			if b.Receiver.Kind == "managed_agent" && b.State != "canceled" && in.State == "accepted" {
				if m.State == "running" {
					v.State = "running"
				} else {
					uncertain = true
				}
			}
		}
		if uncertain {
			v.State = "uncertain"
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) replyReceivers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(ReplyReceivers)
	if !ok {
		writeErr(w, NotFound("Local reply receiver continuation unavailable on this provider."))
		return
	}
	v, err := p.ReplyReceiverBindings()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}

func (l *Live) ReplySessions() (ReplySessionCatalogView, error) {
	v := ReplySessionCatalogView{Host: l.a.Address, Local: true, Sessions: []ReplySessionChoice{}}
	rows, err := l.a.ReplySessions()
	if err != nil {
		return v, Refuse(sentence(err))
	}
	for _, row := range rows {
		v.Sessions = append(v.Sessions, ReplySessionChoice{Handle: row.Handle, Harness: row.Harness, Label: row.Label, Active: row.Active})
	}
	return v, nil
}
func (s *Server) replySessions(w http.ResponseWriter, r *http.Request) {
	host, key := r.URL.Query().Get("host"), r.URL.Query().Get("host_key")
	if host != "" || key != "" {
		if host == "" || key == "" {
			writeErr(w, Refuse("Exact receiver host and key required."))
			return
		}
		p, ok := s.p.(RemoteReplySessionCatalogProvider)
		if !ok {
			writeErr(w, NotFound("Linked receiver catalog unavailable on this provider."))
			return
		}
		v, e := p.ReplySessionsAt(r.Context(), client.ReplyReceiverHost{Address: host, Fingerprint: key})
		if e != nil {
			writeErr(w, e)
			return
		}
		writeJSON(w, v)
		return
	}
	p, ok := s.p.(ReplySessionCatalogProvider)
	if !ok {
		writeErr(w, NotFound("Native reply sessions unavailable on this provider."))
		return
	}
	v, err := p.ReplySessions()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}
