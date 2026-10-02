package client

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Private local SDK/CLI response; not picker metadata and never channel content.
type ClaudeReplyChannelOwner struct {
	ReplySessionCall
	Source string `json:"source"`
}

// Only a genuine native hook can publish physical session registration. SDK
// initialization is not native acceptance and cannot create a second receiver.
func (a *Agent) ClaudeReplySessionHook(event, sid, file string) (string, error) {
	route, e := captureClaudeRoute(sid)
	if e != nil {
		return "", e
	}
	return a.registerClaudeReplySession(event, sid, file, route)
}

func (a *Agent) registerClaudeReplySession(event, sid, file string, route claudeNativeRoute) (string, error) {
	if sid == "" || len(sid) > 128 || strings.ContainsAny(sid, "/\\\x00") {
		return "", errors.New("invalid native Claude session identity")
	}
	if event != "SessionStart" && event != "SessionEnd" && event != "UserPromptSubmit" && event != "PostToolUse" && event != "Stop" {
		return "", errors.New("unsupported native Claude hook")
	}
	if e := route.verify(true); e != nil {
		return "", e
	}
	file, e := canonicalNativeFile(file, true)
	if e != nil {
		return "", e
	}
	if e = route.checkFile(file, sid); e != nil {
		return "", e
	}
	if _, e = claudeNativeScan(file, sid, true, nil); e != nil {
		return "", e
	}
	tx, e := a.store.db.Begin()
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	rows, e := tx.Query(`SELECT record FROM reply_sessions WHERE json_extract(record,'$.harness')='claude' AND json_extract(record,'$.session_id')=?`, sid)
	if e != nil {
		return "", e
	}
	var r replySessionRecord
	count := 0
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			break
		}
		e = json.Unmarshal([]byte(raw), &r)
		count++
		if e != nil {
			break
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return "", e
	}
	if rowErr != nil {
		return "", rowErr
	}
	if count > 1 {
		return "", errors.New("ambiguous native Claude registration")
	}
	if count == 1 {
		if e = a.checkReplySession(tx, r); e != nil {
			return "", e
		}
		if r.File != file || r.Claude == nil {
			return "", errors.New("native Claude physical session changed")
		}
	}
	if event == "SessionStart" {
		if count == 0 {
			var realm string
			if e = tx.QueryRow(`SELECT v FROM config WHERE k='realm_id'`).Scan(&realm); e != nil {
				return "", e
			}
			r = replySessionRecord{SessionID: sid, File: file, Key: a.Self().Fingerprint(), Realm: realm}
			r.Handle, r.Harness = protocol.NewID(), "claude"
			r.Label = "claude " + sid
		}
		r.Claude = &route
		r.Generation++
		r.OwnerToken = protocol.NewID()
		r.Active = true
		r.CloseReason = ""
		r.CloseGeneration = 0
	} else {
		if count == 0 || !r.Active || r.Claude == nil || *r.Claude != route {
			return "", errors.New("native Claude hook has no exact current registration")
		}
		if event == "SessionEnd" {
			r.Active = false
			r.Generation++
			r.OwnerToken = ""
			// A native SessionEnd can also mean detach/clear/restart. Its exact clean
			// shutdown meaning is NOT qualified; never infer on-close task authority.
			r.CloseReason = "detached"
			r.CloseGeneration = r.Generation
		}
	}
	if e = saveReplySession(tx, r); e == nil {
		e = tx.Commit()
	}
	if e != nil {
		return "", e
	}
	a.store.changed()
	notifyDaemon(a.home)
	return r.Handle, nil
}

// SDK-owned CLI calls may only recover the currently registered local lease.
// No default responder, fallback, file scan or implicit registration is allowed.
func (a *Agent) ClaudeReplyChannelOwner(sid string) (ClaudeReplyChannelOwner, error) {
	if osSID := os.Getenv("CLAUDE_CODE_SESSION_ID"); osSID == "" || osSID != sid {
		return ClaudeReplyChannelOwner{}, errors.New("native Claude SDK session identity absent")
	}
	route, e := captureClaudeRoute(sid)
	if e != nil {
		return ClaudeReplyChannelOwner{}, e
	}
	return a.claudeReplyChannelOwner(sid, route)
}

func (a *Agent) claudeReplyChannelOwner(sid string, route claudeNativeRoute) (ClaudeReplyChannelOwner, error) {
	if e := route.verify(true); e != nil {
		return ClaudeReplyChannelOwner{}, e
	}
	rows, e := a.store.db.Query(`SELECT record FROM reply_sessions WHERE json_extract(record,'$.harness')='claude' AND json_extract(record,'$.session_id')=?`, sid)
	if e != nil {
		return ClaudeReplyChannelOwner{}, e
	}
	defer rows.Close()
	var r replySessionRecord
	count := 0
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return ClaudeReplyChannelOwner{}, e
		}
		if e = json.Unmarshal([]byte(raw), &r); e != nil {
			return ClaudeReplyChannelOwner{}, e
		}
		count++
	}
	if e = rows.Err(); e != nil {
		return ClaudeReplyChannelOwner{}, e
	}
	if count != 1 || !r.Active || r.Claude == nil || *r.Claude != route || r.OwnerToken == "" {
		return ClaudeReplyChannelOwner{}, errors.New("native Claude SDK has no exact registered receiver")
	}
	if e = a.checkReplySession(a.store.db, r); e != nil {
		return ClaudeReplyChannelOwner{}, e
	}
	if e = route.checkFile(r.File, sid); e != nil {
		return ClaudeReplyChannelOwner{}, e
	}
	if _, e = claudeNativeScan(r.File, sid, true, nil); e != nil {
		return ClaudeReplyChannelOwner{}, e
	}
	return ClaudeReplyChannelOwner{ReplySessionCall: ReplySessionCall{
		Handle: r.Handle, Generation: r.Generation, OwnerToken: r.OwnerToken,
		SessionID: r.SessionID, File: r.File,
	}, Source: route.Source}, nil
}
