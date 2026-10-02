package ui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/gdrive"
)

type DriveSpaces interface {
	Drive(context.Context, client.DriveRequest) (client.DriveView, error)
	DriveUpload(context.Context, string, string, io.Reader, int64, bool) (gdrive.File, error)
}

func (l *Live) Drive(ctx context.Context, r client.DriveRequest) (client.DriveView, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.DriveCommand(ctx, r)
}
func (l *Live) DriveUpload(ctx context.Context, conv, name string, r io.Reader, n int64, confirm bool) (gdrive.File, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.DriveUpload(ctx, conv, name, r, n, confirm)
}

type DriveAttachmentSpaces interface {
	DriveSaveAttachment(context.Context, string, string, string, int, bool) (gdrive.File, error)
}

func (l *Live) DriveSaveAttachment(ctx context.Context, conv, dir, id string, index int, confirm bool) (gdrive.File, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.DriveSaveAttachment(ctx, conv, dir, id, index, confirm)
}
func (s *Server) drive(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(DriveSpaces)
	if !ok {
		writeErr(w, NotFound("Google Drive not available on this client"))
		return
	}
	var req client.DriveRequest
	if r.Method == "GET" {
		req.Conv = r.URL.Query().Get("conv")
		req.Action = "status"
	} else if r.Method == "POST" {
		if !readJSON(w, r, &req) {
			return
		}
	} else {
		w.WriteHeader(405)
		return
	}
	if req.Action == "save-attachment" {
		ap, ok := s.p.(DriveAttachmentSpaces)
		if !ok {
			writeErr(w, NotFound("attachment saving not available on this client"))
			return
		}
		f, e := ap.DriveSaveAttachment(r.Context(), req.Conv, req.Dir, req.Message, req.Index, req.ConfirmOutside)
		if e != nil {
			driveError(w, e)
			return
		}
		writeJSON(w, client.DriveView{File: &f, Notice: gdrive.OutsideE2EE})
		return
	}
	v, e := p.Drive(r.Context(), req)
	if e != nil {
		driveError(w, e)
		return
	}
	writeJSON(w, v)
}
func (s *Server) driveUpload(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(DriveSpaces)
	if !ok {
		writeErr(w, NotFound("Google Drive not available on this client"))
		return
	}
	q := r.URL.Query()
	n, e := strconv.ParseInt(q.Get("size"), 10, 64)
	if e != nil || n < 0 || n > 32<<20 {
		http.Error(w, "Drive uploads limited to 32 MiB", 400)
		return
	}
	f, e := p.DriveUpload(r.Context(), q.Get("conv"), q.Get("name"), io.LimitReader(r.Body, n+1), n, q.Get("confirm_outside_e2ee") == "true")
	if e != nil {
		driveError(w, e)
		return
	}
	writeJSON(w, f)
}
func driveError(w http.ResponseWriter, e error) {
	code := http.StatusBadRequest
	if errors.Is(e, gdrive.ErrConsent) {
		code = http.StatusUnauthorized
	}
	if errors.Is(e, gdrive.ErrDenied) || errors.Is(e, gdrive.ErrCapability) {
		code = http.StatusForbidden
	}
	if errors.Is(e, gdrive.ErrOffline) {
		code = http.StatusServiceUnavailable
	} // Adapter/local errors are bounded and never contain provider response bodies.
	http.Error(w, e.Error(), code)
}

type DriveSetupProvider interface {
	DriveSetup(context.Context, client.DriveSetupRequest) (client.DriveSetupView, error)
}

func (l *Live) DriveSetup(ctx context.Context, r client.DriveSetupRequest) (client.DriveSetupView, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.DriveSetup(ctx, r)
}
func (s *Server) driveSetup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(DriveSetupProvider)
	if !ok {
		writeErr(w, NotFound("file storage options unavailable"))
		return
	}
	req := client.DriveSetupRequest{Action: "status"}
	if r.Method == "POST" && !readJSON(w, r, &req) {
		return
	}
	v, e := p.DriveSetup(r.Context(), req)
	if e != nil {
		driveError(w, e)
		return
	}
	writeJSON(w, v)
}
