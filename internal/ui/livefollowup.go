package ui

import (
	"context"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

type RequestFollowupDraft struct {
	Conv  string       `json:"conv,omitempty"`
	Ref   envelope.Ref `json:"ref"`
	ID    string       `json:"id"`
	Body  string       `json:"body"`
	Files []string     `json:"files,omitempty"`
}

func (l *Live) RequestFollowup(d RequestFollowupDraft) (string, error) {
	files, cleanup, err := l.takeStaged(d.Files)
	if err != nil {
		return "", err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	note, err := l.a.QueueRequestFollowup(ctx, client.RequestFollowup{Conv: d.Conv, Ref: d.Ref, ID: d.ID, Body: d.Body, Files: files})
	if err != nil {
		return "", Refuse(sentence(err))
	}
	return note, nil
}

func (s *Server) requestFollowup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(interface {
		RequestFollowup(RequestFollowupDraft) (string, error)
	})
	if !ok {
		writeErr(w, NotFound("request follow-ups are not available here"))
		return
	}
	var d RequestFollowupDraft
	if !readJSON(w, r, &d) {
		return
	}
	note, err := p.RequestFollowup(d)
	writeResult(w, map[string]string{"note": note}, err)
}
