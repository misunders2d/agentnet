package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

const appControlsURLFile = "app-ui-url"

// Only the app shell owns this stdin. It checks the invoking native page;
// this dispatcher still uses the same authenticated appAPI and its fences.
type appCallRequest struct {
	Command string          `json:"command"`
	ID      uint64          `json:"id"`
	Action  string          `json:"action"`
	Body    json.RawMessage `json:"body"`
}
type appCallResponse struct {
	header http.Header
	bytes.Buffer
	status int
}

func (w *appCallResponse) Header() http.Header { return w.header }
func (w *appCallResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *appCallResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.Buffer.Write(b)
}

func (r *appRunner) appCall(ctx context.Context, line string) {
	var call appCallRequest
	if len(line) > 4096 || json.Unmarshal([]byte(line), &call) != nil || call.Command != "app-api" || call.ID == 0 {
		return
	}
	path, method := "", http.MethodPost
	switch call.Action {
	case "status":
		path, method = "/api/app/status", http.MethodGet
	case "check":
		path, method = "/api/app/check", http.MethodGet
	case "update":
		path = "/api/app/update"
	case "cli":
		path = "/api/app/cli"
	default:
		r.emit(appEvent{Event: "appreply", CallID: call.ID, Status: 400, Body: "Unknown app action."})
		return
	}
	if len(call.Body) > 1024 || !r.controlsReady.Load() {
		r.emit(appEvent{Event: "appreply", CallID: call.ID, Status: 503, Body: "App controls are not ready."})
		return
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+r.addr+path, bytes.NewReader(call.Body))
	if err != nil {
		return
	}
	req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: r.token})
	req.Header.Set("Origin", "http://"+r.addr)
	req.Header.Set("Content-Type", "application/json")
	reply := &appCallResponse{header: make(http.Header)}
	r.appAPI(reply, req)
	r.emit(appEvent{Event: "appreply", CallID: call.ID, Status: reply.status, Body: reply.String()})
}
