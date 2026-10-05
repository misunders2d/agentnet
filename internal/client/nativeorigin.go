package client

import (
	"encoding/json"
	"errors"
	"os"
)

// A request made from inside an assistant session returns to that session.
// The harness names its own session to the commands it runs (Claude Code:
// CLAUDE_CODE_SESSION_ID; Codex: CODEX_THREAD_ID), and that name counts only
// as the exact, active registration its own hooks made in this home, for the
// caller's own native profile, its session file path still that session's.
// Nothing else names it: no root or parent session, label or default. Pi and
// OMP export their own handle instead (CurrentReplySession). When the named
// session cannot receive the answer itself, the answer goes to this
// computer's inbox with a note; asking is never refused.
//
// This only chooses where the answer goes, like --reply-receiver
// session:HANDLE: it needs no view of the native process, which a harness
// sandbox hides from the commands it runs. Ownership of the native session,
// its lease and every receipt are proven again where the hooks register it
// and where AgentNet delivers to it.
const (
	ClaudeSessionEnv = "CLAUDE_CODE_SESSION_ID"
	CodexThreadEnv   = "CODEX_THREAD_ID"
)

// NativeOriginReceiver is the reply receiver for a request made from the
// assistant session named by the harness environment, and a plain note to
// show the person when the answer cannot go to that session itself. It never
// refuses (MEL-537): a named session that is not a live receiver gets no
// receiver, so its answer lands in this computer's plain inbox, where every
// session's hooks announce it (attention.go), the app shows it and the
// asking command's wait prints it (AwaitReply). A plain command (no session
// named) gets neither a receiver nor a note.
//
// Claude Code is a receiver only while AgentNet's Claude channel drains this
// exact registration (it attached in this generation); without it the
// ordinary hooks announce the answer instead. Codex is one when its
// app-server registration still holds. Neither reads the session's file
// here: selection only chooses where the answer goes; ownership is proven at
// registration and at delivery.
func (a *Agent) NativeOriginReceiver(claudeSID, codexThread string) (*ReplyReceiver, string) {
	switch {
	case claudeSID == "" && codexThread == "":
		return nil, ""
	case claudeSID != "" && codexThread != "":
		// Both names may be inherited (one harness run inside the other); no
		// route here proves which of them is the one asking.
		return nil, InboxNote("both a Claude Code session and a Codex thread are named here, so AgentNet cannot tell which one asked")
	case claudeSID != "":
		// Claude Code marks what it starts and names that session's own ID.
		if os.Getenv("CLAUDECODE") != "1" || os.Getenv(ClaudeSessionEnv) != claudeSID {
			return nil, InboxNote("this command was not started by Claude Code in that session")
		}
		projects, e := claudeProjectsDir()
		if e != nil {
			return nil, InboxNote("Claude Code's profile cannot be read: " + e.Error())
		}
		return a.claudeOrigin(claudeSID, projects)
	default:
		home, e := codexHomeDir()
		if e != nil {
			return nil, InboxNote("Codex's home cannot be read: " + e.Error())
		}
		return a.codexOrigin(codexThread, home)
	}
}

// InboxNote says where an answer goes when no session receives it, and why.
func InboxNote(why string) string {
	return "the answer will come to this computer's AgentNet inbox: your agent sessions' hooks announce it and `agentnet conversation ID` shows it (" + why + ")"
}

// claudeOrigin is the receiver for Claude session sid asked from the profile
// whose projects directory is projects: its one active registration in this
// home, under that profile, its transcript path still that session's, with
// AgentNet's Claude channel draining it in this generation. Its owner token
// is not kept. Anything less is the inbox, with a note.
func (a *Agent) claudeOrigin(sid, projects string) (*ReplyReceiver, string) {
	r, e := a.originRegistration("claude", sid)
	if e != nil {
		return nil, InboxNote("this session has no exact AgentNet registration: " + e.Error())
	}
	if r.Claude == nil || r.Claude.Source != claudeChannelSource || r.Claude.Projects != projects {
		return nil, InboxNote("this session is registered under another Claude profile")
	}
	if e = r.Claude.checkFile(r.File, sid); e != nil {
		return nil, InboxNote(e.Error())
	}
	if r.ChannelGeneration != r.Generation {
		return nil, InboxNote("this session has no AgentNet Claude channel; the hooks announce the answer instead")
	}
	return &ReplyReceiver{Kind: "live_session", SessionHandle: r.Handle}, ""
}

// codexOrigin is the receiver for Codex thread sid asked from Codex home
// home: its one active registration in this home, made on that home's
// default daemon, which its own record still names, the rollout path still
// in that home. Its owner token is not kept. Anything less is the inbox,
// with a note.
func (a *Agent) codexOrigin(sid, home string) (*ReplyReceiver, string) {
	r, e := a.originRegistration("codex", sid)
	if e != nil {
		return nil, InboxNote("this thread has no exact AgentNet registration: " + e.Error())
	}
	if r.Codex == nil || r.Codex.Home != home || r.Codex.Endpoint != codexEndpoint(home) {
		return nil, InboxNote("this thread is registered under another Codex home")
	}
	p, e := codexDaemonRecordIn(home)
	if e != nil || p.PID != r.Codex.PID || p.Identity.Boot != r.Codex.Boot || p.Identity.Ticks != r.Codex.Ticks {
		return nil, InboxNote("the Codex daemon that registered this thread is no longer the one recorded in its home")
	}
	if file, e := codexRolloutIn(*r.Codex, r.File); e != nil || file != r.File {
		return nil, InboxNote("the registered rollout is not in this Codex home")
	}
	return &ReplyReceiver{Kind: "live_session", SessionHandle: r.Handle}, ""
}

// originRegistration is the one active registration of native session sid
// in this home, under this home's key and realm.
func (a *Agent) originRegistration(harness, sid string) (replySessionRecord, error) {
	var r replySessionRecord
	rows, e := a.store.db.Query(`SELECT record FROM reply_sessions WHERE json_extract(record,'$.harness')=? AND json_extract(record,'$.session_id')=?`, harness, sid)
	if e != nil {
		return r, e
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return r, e
		}
		if e = json.Unmarshal([]byte(raw), &r); e != nil {
			return r, e
		}
		count++
	}
	if e = rows.Err(); e != nil {
		return r, e
	}
	if count != 1 || !r.Active || r.OwnerToken == "" {
		return r, errors.New("no exact active registration of this session in this AgentNet home")
	}
	return r, a.checkReplySession(a.store.db, r)
}
