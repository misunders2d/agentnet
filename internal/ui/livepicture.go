package ui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type PersonPictureProvider interface {
	SetPersonPicture(context.Context, []byte) (client.PersonInfo, error)
	PersonPicture(context.Context, string) ([]byte, error)
}

func pictureURL(hash string) string {
	if !protocol.ValidHash(hash) {
		return ""
	}
	return "/api/picture/" + hash
}
func (l *Live) SetPersonPicture(ctx context.Context, b []byte) (client.PersonInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.SetPersonPicture(ctx, b)
}
func (l *Live) PersonPicture(ctx context.Context, hash string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.PersonPicture(ctx, hash)
}
func (s *Server) setPicture(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(PersonPictureProvider)
	if !ok {
		writeErr(w, NotFound("profile pictures unavailable"))
		return
	}
	var c struct {
		PNG []byte `json:"png"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, (protocol.MaxPictureBytes*4/3)+1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(c.PNG) > 0 {
		if err := protocol.ValidatePicture(c.PNG); err != nil {
			writeErr(w, Refuse(sentence(err)))
			return
		}
	}
	v, err := p.SetPersonPicture(r.Context(), c.PNG)
	if err != nil {
		writeErr(w, Refuse("Picture change was not confirmed. Refresh your profile before retrying."))
		return
	}
	writeJSON(w, v)
}
func (s *Server) getPicture(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(PersonPictureProvider)
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, err := p.PersonPicture(r.Context(), r.PathValue("hash"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Write(b)
}
