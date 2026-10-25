package ui

import (
	"context"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
)

func (l *Live) PreviewTopicTransfer(d client.TopicTransferRequest) (client.TopicTransferReview, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	v, e := l.a.PreviewTopicTransfer(ctx, d)
	if e != nil {
		return v, Refuse(sentence(e))
	}
	return v, nil
}
func (l *Live) TopicTransferPage(d client.TopicTransferAction) (client.TopicTransferPage, error) {
	v, e := l.a.TopicTransferPage(d)
	if e != nil {
		return v, Refuse(sentence(e))
	}
	return v, nil
}
func (l *Live) ApplyTopicTransfer(d client.TopicTransferAction) (client.TopicTransferReview, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	v, e := l.a.ApplyTopicTransfer(ctx, d)
	if e != nil {
		return v, Refuse(sentence(e))
	}
	return v, nil
}
func (l *Live) TopicTransferStatus(d client.TopicTransferAction) (*client.TopicTransferReview, error) {
	v, e := l.a.TopicTransferStatus(d)
	if e != nil {
		return v, Refuse(sentence(e))
	}
	return v, nil
}
func (l *Live) CancelTopicTransfer(d client.TopicTransferAction) (client.TopicTransferReview, error) {
	v, e := l.a.CancelTopicTransfer(d)
	if e != nil {
		return v, Refuse(sentence(e))
	}
	return v, nil
}
func (l *Live) TopicTransferLocation(d client.TopicTransferRequest) (*client.TopicTransferLocation, error) {
	v, e := l.a.TopicTransferLocation(d)
	if e != nil {
		return v, Refuse(sentence(e))
	}
	return v, nil
}

type topicTransferProvider interface {
	PreviewTopicTransfer(client.TopicTransferRequest) (client.TopicTransferReview, error)
	TopicTransferPage(client.TopicTransferAction) (client.TopicTransferPage, error)
	ApplyTopicTransfer(client.TopicTransferAction) (client.TopicTransferReview, error)
	TopicTransferStatus(client.TopicTransferAction) (*client.TopicTransferReview, error)
	CancelTopicTransfer(client.TopicTransferAction) (client.TopicTransferReview, error)
	TopicTransferLocation(client.TopicTransferRequest) (*client.TopicTransferLocation, error)
}

func (s *Server) topicTransfer(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(topicTransferProvider)
	if !ok {
		writeErr(w, NotFound("whole-topic continuation is not available here"))
		return
	}
	switch r.PathValue("action") {
	case "preview", "location":
		var d client.TopicTransferRequest
		if !readJSON(w, r, &d) {
			return
		}
		if r.PathValue("action") == "preview" {
			v, e := p.PreviewTopicTransfer(d)
			writeResult(w, v, e)
		} else {
			v, e := p.TopicTransferLocation(d)
			writeResult(w, v, e)
		}
	case "page", "apply", "status", "cancel":
		var d client.TopicTransferAction
		if !readJSON(w, r, &d) {
			return
		}
		switch r.PathValue("action") {
		case "page":
			v, e := p.TopicTransferPage(d)
			writeResult(w, v, e)
		case "apply":
			v, e := p.ApplyTopicTransfer(d)
			writeResult(w, v, e)
		case "status":
			v, e := p.TopicTransferStatus(d)
			writeResult(w, v, e)
		case "cancel":
			v, e := p.CancelTopicTransfer(d)
			writeResult(w, v, e)
		}
	default:
		writeErr(w, NotFound("no such topic transfer action"))
	}
}
