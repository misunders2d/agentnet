package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// A request made from inside an assistant session returns to that session.
// The harness names its own session to the commands it runs (Claude Code:
// CLAUDE_CODE_SESSION_ID; Codex: CODEX_THREAD_ID), and that name counts only
// as the exact, active registration its own hooks made in this home, for the
// caller's own native profile, its session file still that session's. Nothing
// else names it: no root or parent session, scan, label or default. Pi and OMP
// export their own handle instead (CurrentReplySession).
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
// assistant session named by the harness environment: nil when none is
// named (a plain command, answered in this device's inbox), or an error that
// says why the answer cannot return to the session that is asking. It never
// falls back to the inbox for a named session.
func (a *Agent) NativeOriginReceiver(claudeSID, codexThread string) (*ReplyReceiver, error) {
	switch {
	case claudeSID == "" && codexThread == "":
		return nil, nil
	case claudeSID != "" && codexThread != "":
		return nil, errOriginAmbiguous
	case claudeSID != "":
		// Claude Code marks what it starts and names that session's own ID.
		if os.Getenv("CLAUDECODE") != "1" || os.Getenv(ClaudeSessionEnv) != claudeSID {
			return nil, claudeOriginError(claudeSID, errors.New("not started by Claude Code in this session (CLAUDECODE, CLAUDE_CODE_SESSION_ID)"))
		}
		projects, e := claudeProjectsDir()
		if e != nil {
			return nil, claudeOriginError(claudeSID, e)
		}
		return a.claudeOrigin(claudeSID, projects)
	default:
		home, e := codexHomeDir()
		if e != nil {
			return nil, codexOriginError(codexThread, e)
		}
		return a.codexOrigin(codexThread, home)
	}
}

// Both names may be inherited (one harness run inside the other); no route
// here proves which of them is the one asking.
var errOriginAmbiguous = errors.New("this command runs with both a Claude Code session and a Codex thread in its environment, so AgentNet cannot tell which assistant is asking; " +
	"choose where the answer goes with --reply-receiver session:HANDLE (agentnet receivers --sessions), or --reply-receiver human for this device's inbox")

func claudeOriginError(sid string, e error) error {
	return fmt.Errorf("this command runs in Claude Code session %s, but AgentNet cannot return the answer to it (%v); "+
		"a Claude session receives replies only through AgentNet's Claude channel: run agentnet hooks install claude --channel, "+
		"start Claude with the steps it prints (the MCP fragment, admitting server:agentnet, native channel consent) and ask from that session, "+
		"or pass --reply-receiver human to get the answer in this device's inbox", sid, e)
}

func codexOriginError(thread string, e error) error {
	return fmt.Errorf("this command runs in Codex thread %s, but AgentNet cannot return the answer to it (%v); "+
		"a Codex thread receives replies only when it runs on Codex's app-server daemon with AgentNet's Codex hooks installed and trusted "+
		"(agentnet hooks install codex, then trust them in /hooks) and was started after that, "+
		"or pass --reply-receiver human to get the answer in this device's inbox", thread, e)
}

// claudeOrigin is the receiver for Claude session sid asked from the profile
// whose projects directory is projects: its one active registration in this
// home, under that profile, its transcript still that session's. Its owner
// token is not kept.
func (a *Agent) claudeOrigin(sid, projects string) (*ReplyReceiver, error) {
	r, e := a.originRegistration("claude", sid)
	if e != nil {
		return nil, claudeOriginError(sid, e)
	}
	if r.Claude == nil || r.Claude.Source != claudeChannelSource || r.Claude.Projects != projects {
		return nil, claudeOriginError(sid, errors.New("this session is registered under another Claude profile"))
	}
	if e = r.Claude.checkFile(r.File, sid); e != nil {
		return nil, claudeOriginError(sid, e)
	}
	if _, e = claudeNativeScan(r.File, sid, true, nil); e != nil {
		return nil, claudeOriginError(sid, e)
	}
	return &ReplyReceiver{Kind: "live_session", SessionHandle: r.Handle}, nil
}

// codexOrigin is the receiver for Codex thread sid asked from Codex home
// home: its one active registration in this home, made on that home's
// default daemon, which its own record still names, the rollout still that
// thread's in that home. Its owner token is not kept.
func (a *Agent) codexOrigin(sid, home string) (*ReplyReceiver, error) {
	r, e := a.originRegistration("codex", sid)
	if e != nil {
		return nil, codexOriginError(sid, e)
	}
	if r.Codex == nil || r.Codex.Home != home || r.Codex.Endpoint != codexEndpoint(home) {
		return nil, codexOriginError(sid, errors.New("this thread is registered under another Codex home"))
	}
	p, e := codexDaemonRecordIn(home)
	if e != nil || p.PID != r.Codex.PID || p.Identity.Boot != r.Codex.Boot || p.Identity.Ticks != r.Codex.Ticks {
		return nil, codexOriginError(sid, errors.New("the Codex daemon that registered this thread is no longer the one recorded in its home"))
	}
	if file, e := codexRolloutIn(*r.Codex, r.File); e != nil || file != r.File {
		return nil, codexOriginError(sid, errors.New("registered rollout is not in this Codex home"))
	}
	if _, e = codexNativeEntries(r.File, sid); e != nil {
		return nil, codexOriginError(sid, e)
	}
	return &ReplyReceiver{Kind: "live_session", SessionHandle: r.Handle}, nil
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
