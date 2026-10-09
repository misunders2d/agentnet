package ui

import (
	"context"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
)

func (l *Live) PreviewTopicOrganization(d client.TopicOrganizationRequest) (client.TopicOrganizationReview, error) {
	v, err := l.a.PreviewTopicOrganization(d)
	if err != nil {
		return v, Refuse(sentence(err))
	}
	return v, nil
}

func (l *Live) ApplyTopicOrganization(d client.TopicOrganizationReview) (client.ConvSent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	v, err := l.a.ApplyTopicOrganization(ctx, d)
	if err != nil {
		return v, Refuse(sentence(err))
	}
	return v, nil
}

func (s *Server) previewTopicOrganization(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(interface {
		PreviewTopicOrganization(client.TopicOrganizationRequest) (client.TopicOrganizationReview, error)
	})
	if !ok {
		writeErr(w, NotFound("topic organization is not available here"))
		return
	}
	var d client.TopicOrganizationRequest
	if !readJSON(w, r, &d) {
		return
	}
	v, err := p.PreviewTopicOrganization(d)
	writeResult(w, v, err)
}

func (s *Server) applyTopicOrganization(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(interface {
		ApplyTopicOrganization(client.TopicOrganizationReview) (client.ConvSent, error)
	})
	if !ok {
		writeErr(w, NotFound("topic organization is not available here"))
		return
	}
	var d client.TopicOrganizationReview
	if !readJSON(w, r, &d) {
		return
	}
	v, err := p.ApplyTopicOrganization(d)
	writeResult(w, v, err)
}
