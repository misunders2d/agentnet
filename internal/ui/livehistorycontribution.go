package ui

import (
	"context"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
)

func (l *Live) PreviewHistoryContribution(d client.HistoryContributionRequest) (client.HistoryContributionReview, error) {
	v, err := l.a.PreviewHistoryContribution(d)
	if err != nil {
		return v, Refuse(sentence(err))
	}
	return v, nil
}

func (l *Live) ApplyHistoryContribution(d client.HistoryContributionReview) (client.ConvSent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	v, err := l.a.ApplyHistoryContribution(ctx, d)
	if err != nil {
		return v, Refuse(sentence(err))
	}
	return v, nil
}

func (s *Server) previewHistoryContribution(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(interface {
		PreviewHistoryContribution(client.HistoryContributionRequest) (client.HistoryContributionReview, error)
	})
	if !ok {
		writeErr(w, NotFound("history sharing is not available here"))
		return
	}
	var d client.HistoryContributionRequest
	if !readJSON(w, r, &d) {
		return
	}
	v, err := p.PreviewHistoryContribution(d)
	writeResult(w, v, err)
}

func (s *Server) applyHistoryContribution(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(interface {
		ApplyHistoryContribution(client.HistoryContributionReview) (client.ConvSent, error)
	})
	if !ok {
		writeErr(w, NotFound("history sharing is not available here"))
		return
	}
	var d client.HistoryContributionReview
	if !readJSON(w, r, &d) {
		return
	}
	v, err := p.ApplyHistoryContribution(d)
	writeResult(w, v, err)
}
