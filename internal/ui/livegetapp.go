package ui

import (
	"net/http"
	"runtime"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/ui/static"
)

// getAppView is where to get the AgentNet app for this program's version
// (static/getapp.json), with the platform this daemon runs on.
func getAppView() GetAppView {
	v := GetAppView{Version: protocol.Version, Detected: map[string]string{"windows": "windows", "darwin": "macos", "linux": "linux"}[runtime.GOOS]}
	for _, d := range static.Downloads(protocol.Version) {
		v.Platforms = append(v.Platforms, GetAppPlatform{ID: d.ID, Label: d.Label, URL: d.URL})
	}
	return v
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request) { writeJSON(w, getAppView()) }

func (s *Server) invitations(w http.ResponseWriter) (Invitations, bool) {
	p, ok := s.p.(Invitations)
	if !ok {
		writeErr(w, NotFound("invitations are not made here"))
	}
	return p, ok
}

func (s *Server) invite(w http.ResponseWriter, r *http.Request) {
	var v InviteRequest
	if !readJSON(w, r, &v) {
		return
	}
	if p, ok := s.invitations(w); ok {
		out, err := p.Invite(v)
		writeResult(w, out, err)
	}
}

func (s *Server) invites(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.invitations(w); ok {
		out, err := p.Invites()
		writeResult(w, out, err)
	}
}

func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request) {
	var v struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if p, ok := s.invitations(w); ok {
		writeResult(w, map[string]bool{"revoked": true}, p.RevokeInvite(v.ID))
	}
}

func (s *Server) folders(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Folders)
	if !ok {
		writeErr(w, NotFound("folders are chosen in the AgentNet app on a computer"))
		return
	}
	out, err := p.Folders(r.URL.Query().Get("path"))
	writeResult(w, out, err)
}
