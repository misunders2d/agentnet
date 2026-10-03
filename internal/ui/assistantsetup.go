package ui

import (
	"context"
	"encoding/json"
	"net/http"
)

// AssistantSetup uses the existing native installers. It never chooses a
// responder, grants permissions or fabricates a registered native context.
type AssistantSetupRequest struct {
	Action    string   `json:"action,omitempty"`
	Harnesses []string `json:"harnesses,omitempty"`
	ReviewID  string   `json:"review_id,omitempty"`
}
type AssistantSetupHarness struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Detected   bool   `json:"detected"`
	Configured bool   `json:"configured"`
	Registered bool   `json:"registered"`
	Supported  bool   `json:"supported"`
	State      string `json:"state"`
	Note       string `json:"note"`
	Change     string `json:"change,omitempty"`
	Next       string `json:"next,omitempty"`
	Target     string `json:"target,omitempty"`
}
type AssistantSetupView struct {
	Local     bool                    `json:"local"`
	Harnesses []AssistantSetupHarness `json:"harnesses"`
	ReviewID  string                  `json:"review_id,omitempty"`
	Note      string                  `json:"note,omitempty"`
}
type AssistantSetupFunc func(context.Context, AssistantSetupRequest) (AssistantSetupView, error)
type AssistantSetupProvider interface {
	AssistantSetup(context.Context, AssistantSetupRequest) (AssistantSetupView, error)
}

// SetAssistantSetup is called before exposing a provider. The CLI owns the
// installer callback so the UI does not maintain a second hook installer.
func (l *Live) SetAssistantSetup(f AssistantSetupFunc) { l.assistantSetup = f }
func (l *Live) AssistantSetup(ctx context.Context, request AssistantSetupRequest) (AssistantSetupView, error) {
	if l.assistantSetup == nil {
		return AssistantSetupView{}, NotFound("Set up harnesses on a native AgentNet computer.")
	}
	return l.assistantSetup(ctx, request)
}
func (s *Server) setupAssistants(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(AssistantSetupProvider)
	if !ok {
		writeJSON(w, AssistantSetupView{Harnesses: []AssistantSetupHarness{}, Note: "This browser cannot inspect or install tools. Open Settings on your AgentNet computer."})
		return
	}
	request := AssistantSetupRequest{}
	if r.Method == http.MethodPost {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || (request.Action != "review" && request.Action != "apply") {
			writeErr(w, Refuse("Choose tools and review their setup changes first."))
			return
		}
	}
	view, err := p.AssistantSetup(r.Context(), request)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, view)
}
