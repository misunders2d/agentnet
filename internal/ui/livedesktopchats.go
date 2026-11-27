package ui

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"runtime"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
)

// Desktop chats (MEL-829, MEL-830): the open Claude chats and recent Codex
// Desktop chats on this computer, which the person may connect as named
// agents from Settings as well as with `agentnet claude-chat|codex-chat
// connect`. Connecting reuses the same client calls; a connected chat stays
// person-only (only the owner's verified human devices reach it), is never
// a program or the default agent, and answers with its own settings and
// approvals. The routes are the page's own (UI token, same origin), the same
// authority as creating an agent here.
type DesktopChatProvider interface {
	DesktopChats(context.Context) (DesktopChatsView, error)
	ConnectDesktopChat(context.Context, DesktopChatConnect) (AgentCatalogChangeResult, error)
}

type DesktopChat struct {
	Kind       string `json:"kind"` // claude or codex
	ID         string `json:"id"`   // Claude session ID or Codex thread ID
	Name       string `json:"name,omitempty"`
	Dir        string `json:"dir"`
	Ready      bool   `json:"ready"`
	Updated    int64  `json:"updated,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	AgentLabel string `json:"agent_label,omitempty"`
}

type DesktopChatsView struct {
	Supported bool          `json:"supported"`
	Note      string        `json:"note,omitempty"`
	Chats     []DesktopChat `json:"chats"`
}

type DesktopChatConnect struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

const desktopChatsLinuxOnly = "Connecting a Claude or Codex Desktop chat works on Linux only for now."

// desktopChatLimit bounds the recent Codex Desktop chats offered.
const desktopChatLimit = 20

func (l *Live) DesktopChats(ctx context.Context) (DesktopChatsView, error) {
	v := DesktopChatsView{Chats: []DesktopChat{}}
	if runtime.GOOS != "linux" {
		v.Note = desktopChatsLinuxOnly
		return v, nil
	}
	v.Supported = true
	entries, err := l.a.LocalAgents()
	if err != nil {
		return v, Refuse(sentence(err))
	}
	claudeAgent, codexAgent := map[string]client.LocalAgentInfo{}, map[string]client.LocalAgentInfo{}
	for _, e := range entries {
		if r := e.Responder; r != nil && r.Chat != nil {
			claudeAgent[r.Chat.SessionID] = e
		} else if r != nil && r.CodexChat != nil {
			codexAgent[r.CodexChat.Thread] = e
		}
	}
	claude, err := l.a.ClaudeChatCandidates()
	if err != nil {
		return v, Refuse(sentence(err))
	}
	for _, c := range claude {
		d := DesktopChat{Kind: "claude", ID: c.SessionID, Dir: c.Dir, Ready: c.Inbox}
		if e, ok := claudeAgent[c.SessionID]; ok {
			d.AgentID, d.AgentLabel = e.Record.ID, e.Record.Label
		}
		v.Chats = append(v.Chats, d)
	}
	codex, err := client.CodexChatCandidates(desktopChatLimit)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		v.Note = "Codex Desktop chats could not be read: " + sentence(err)
	}
	for _, c := range codex {
		d := DesktopChat{Kind: "codex", ID: c.Thread, Name: c.Name, Dir: c.Dir, Ready: true}
		if !c.Updated.IsZero() {
			d.Updated = c.Updated.Unix()
		}
		if e, ok := codexAgent[c.Thread]; ok {
			d.AgentID, d.AgentLabel = e.Record.ID, e.Record.Label
		}
		v.Chats = append(v.Chats, d)
	}
	return v, nil
}

func (l *Live) ConnectDesktopChat(ctx context.Context, c DesktopChatConnect) (AgentCatalogChangeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	if runtime.GOOS != "linux" {
		return AgentCatalogChangeResult{}, Refuse(desktopChatsLinuxOnly)
	}
	id, label := strings.TrimSpace(c.ID), strings.TrimSpace(c.Label)
	if id == "" || label == "" {
		return AgentCatalogChangeResult{}, Refuse("Choose a chat and give it a name.")
	}
	var agentID string
	var err error
	switch c.Kind {
	case "claude":
		agentID, err = l.a.ConnectClaudeChat(id, label)
	case "codex":
		agentID, err = l.a.ConnectCodexChat(id, label)
	default:
		return AgentCatalogChangeResult{}, Refuse("Choose a Claude or Codex chat.")
	}
	if err != nil {
		return AgentCatalogChangeResult{}, Refuse(sentence(err))
	}
	result := AgentCatalogChangeResult{Saved: true}
	saved, err := l.a.LocalAgents()
	if err != nil {
		return result, Refuse(sentence(err))
	}
	for _, entry := range saved {
		if entry.Record.ID == agentID {
			agent := localCatalogAgent(entry, catalogHarnesses())
			result.Agent = &agent
		}
	}
	if err = l.a.PublishAgentCatalog(ctx); err != nil {
		result.Note = "Connected on this computer. Hub publication was not confirmed; use Publish to retry the saved public catalog."
		return result, nil
	}
	result.Published = true
	result.Note = "Connected. Requests from your own devices that name this agent go into that chat."
	return result, nil
}

func (s *Server) desktopChats(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(DesktopChatProvider)
	if !ok {
		writeErr(w, NotFound("Desktop chats are unavailable here."))
		return
	}
	v, err := p.DesktopChats(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}

func (s *Server) connectDesktopChat(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(DesktopChatProvider)
	if !ok {
		writeErr(w, NotFound("Desktop chats are unavailable here."))
		return
	}
	var c DesktopChatConnect
	if !readJSON(w, r, &c) {
		return
	}
	v, err := p.ConnectDesktopChat(r.Context(), c)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}
