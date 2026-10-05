package client

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Supported contract, not a release pin: Claude Code on Linux that marks the
// processes it starts (CLAUDECODE=1) and exports their session's own ID
// (CLAUDE_CODE_SESSION_ID, Claude Code 2.1.224 or later) to hooks, tools and
// stdio MCP servers. Each registration is bound to the exact running claude
// executable (process, start time, path and digest) and fails when any of
// them changes; an updated Claude registers anew through its own hook.
// Local process provenance is not a signature, remote grant or model authority.
const claudeChannelSource = "agentnet"

type claudeNativeRoute struct {
	PID      int    `json:"pid"`
	Boot     string `json:"boot"`
	Ticks    uint64 `json:"ticks"`
	Binary   string `json:"binary"`
	SHA256   string `json:"sha256"`
	Projects string `json:"projects"`
	CWD      string `json:"cwd"`
	Source   string `json:"source"`
}

func (r claudeNativeRoute) verify(ancestry bool) error {
	if runtime.GOOS != "linux" || r.PID < 1 || r.Binary == "" || r.Source != claudeChannelSource {
		return errors.New("qualified native Claude route unavailable")
	}
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil || strings.TrimSpace(string(boot)) != r.Boot {
		return errors.New("native Claude boot identity changed")
	}
	_, ticks, binary, e := codexProcess(r.PID)
	if e != nil || ticks != r.Ticks || binary != r.Binary {
		return errors.New("native Claude process identity changed")
	}
	digest, e := codexBinaryDigest(binary)
	if e != nil || digest != r.SHA256 {
		return errors.New("native Claude executable changed")
	}
	cwd, e := os.Readlink(fmt.Sprintf("/proc/%d/cwd", r.PID))
	if e != nil || cwd != r.CWD {
		return errors.New("native Claude working directory changed")
	}
	if ancestry {
		pid, found := os.Getpid(), false
		for range 32 {
			if pid == r.PID {
				found = true
				break
			}
			parent, _, _, e := codexProcess(pid)
			if e != nil || parent < 1 || parent == pid {
				break
			}
			pid = parent
		}
		if !found {
			return errors.New("Claude channel/hook is outside registered native process")
		}
	}
	return nil
}

func captureClaudeRoute(sid string) (claudeNativeRoute, error) {
	var r claudeNativeRoute
	if runtime.GOOS != "linux" || sid == "" {
		return r, errors.New("native Claude route is qualified on Linux only")
	}
	// Claude Code exports both to every hook, tool and stdio MCP server it
	// starts. They are checked again against the running claude process and
	// the registered physical file; inherited text alone cannot create a claim.
	if os.Getenv("CLAUDECODE") != "1" {
		return r, errors.New("not started by Claude Code (CLAUDECODE is not 1)")
	}
	if fromNative := os.Getenv("CLAUDE_CODE_SESSION_ID"); fromNative != sid {
		return r, errors.New("native Claude session identity absent or changed (Claude Code 2.1.224 or later exports CLAUDE_CODE_SESSION_ID)")
	}
	pid := os.Getpid()
	for range 32 {
		parent, ticks, binary, e := codexProcess(pid)
		if e != nil {
			return r, errors.New("native Claude ancestry unavailable")
		}
		if filepath.Base(binary) == "claude" {
			digest, e := codexBinaryDigest(binary) // this registration's exact executable
			if e != nil {
				return r, errors.New("native Claude executable unreadable")
			}
			r.PID, r.Ticks, r.Binary, r.SHA256 = pid, ticks, binary, digest
			break
		}
		if parent < 1 || parent == pid {
			break
		}
		pid = parent
	}
	if r.PID == 0 {
		return r, errors.New("native Claude hook/channel ancestor absent")
	}
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return r, e
	}
	r.Boot = strings.TrimSpace(string(boot))
	r.CWD, e = os.Readlink(fmt.Sprintf("/proc/%d/cwd", r.PID))
	if e != nil {
		return r, e
	}
	r.Projects, e = claudeProjectsDir()
	if e != nil {
		return r, e
	}
	r.Source = claudeChannelSource
	return r, r.verify(true)
}

// claudeProjectsDir is the projects directory of the caller's canonical
// Claude profile: CLAUDE_CONFIG_DIR, or the normal default ~/.claude.
func claudeProjectsDir() (string, error) {
	config := os.Getenv("CLAUDE_CONFIG_DIR")
	if config == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		config = filepath.Join(home, ".claude")
	}
	// Do not set CLAUDE_CONFIG_DIR: its absence changes global config lookup.
	config, e := filepath.EvalSymlinks(config)
	if e != nil {
		return "", e
	}
	return filepath.Abs(filepath.Join(config, "projects"))
}

func (r claudeNativeRoute) checkFile(file, sid string) error {
	if sid == "" || filepath.Base(file) != sid+".jsonl" {
		return errors.New("native Claude physical session name changed")
	}
	rel, e := filepath.Rel(r.Projects, file)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("native Claude transcript outside registered profile")
	}
	physical, e := canonicalNativeFile(file, true)
	if e != nil || physical != file {
		return errors.New("native Claude physical path changed")
	}
	// SessionStart precedes creation of projects and its transcript parent. Root
	// confines existing/symlink components without creating any vendor directory.
	profile := filepath.Dir(r.Projects)
	real, e := filepath.EvalSymlinks(profile)
	if e != nil || real != profile {
		return errors.New("native Claude profile path changed")
	}
	root, e := os.OpenRoot(profile)
	if e != nil {
		return e
	}
	defer root.Close()
	projects, e := root.OpenRoot(filepath.Base(r.Projects))
	if errors.Is(e, os.ErrNotExist) {
		// Missing projects is lazy; an existing dangling link is not absence.
		if _, e = root.Lstat(filepath.Base(r.Projects)); errors.Is(e, os.ErrNotExist) {
			return nil
		}
		return errors.New("native Claude projects path unavailable")
	}
	if e != nil {
		return e
	}
	defer projects.Close()
	real, e = filepath.EvalSymlinks(r.Projects)
	if e != nil || real != r.Projects {
		return errors.New("native Claude projects path changed")
	}
	if _, e = projects.Stat(rel); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return nil
}

// ClaudeChannelNotification is the official SDK's content/meta payload, not a
// second queue. Claims, trust checks, selection and retry ownership stay in Go.
type ClaudeChannelNotification struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta"`
}

func ClaudeReplyNotification(sid string, d *ReplyReceiverDelivery) ClaudeChannelNotification {
	return ClaudeChannelNotification{Content: codexInputText(d), Meta: map[string]string{
		"session_id": sid, "binding_id": d.BindingID, "input_id": d.InputID,
		"claim_id": d.ClaimID, "input_token": d.InputToken,
	}}
}

func claudeChannelTuple(content string) (map[string]string, bool) {
	// Inspect only the vendor-created outer tag, not a tag echoed inside remote
	// body text. The native producer escapes attribute values using XML entities.
	if !strings.HasPrefix(content, "<channel ") {
		return nil, false
	}
	i := strings.Index(content, ">\n")
	if i < 0 || !strings.HasSuffix(content, "\n</channel>") {
		return nil, false
	}
	decoder := xml.NewDecoder(strings.NewReader(content[:i+1] + "</channel>"))
	token, e := decoder.Token()
	start, ok := token.(xml.StartElement)
	if e != nil || !ok || start.Name.Local != "channel" || start.Name.Space != "" {
		return nil, false
	}
	values := map[string]string{}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			return nil, false
		}
		if _, duplicate := values[attr.Name.Local]; duplicate {
			return nil, false
		}
		values[attr.Name.Local] = attr.Value
	}
	if len(values) != 6 {
		return nil, false
	}
	return values, true
}

// Read complete physical native records only. No model echoes, native output
// writes or SDK receipts can substitute for one exact user/channel-origin tuple.
// The scan starts at offset, the transcript's size when the input was
// claimed (liveInputClaim.Offset): its token is fresh at the claim, so no
// record before that can hold it.
func claudeNativeReceipt(file, sid, source string, ack ReplyReceiverAck, offset int64) (bool, error) {
	return claudeNativeScan(file, sid, false, offset, func(raw json.RawMessage) bool {
		var row struct {
			Type, UUID, SessionID string
			IsMeta, IsSidechain   bool
			Origin                struct{ Kind string }
			Message               struct {
				Role    string
				Content json.RawMessage
			}
		}
		if json.Unmarshal(raw, &row) != nil || row.Type != "user" || row.Message.Role != "user" || !row.IsMeta || row.IsSidechain || row.UUID == "" || row.SessionID != sid || row.Origin.Kind != "channel" {
			return false
		}
		var content string
		if json.Unmarshal(row.Message.Content, &content) != nil {
			var blocks []struct{ Type, Text string }
			if json.Unmarshal(row.Message.Content, &blocks) != nil || len(blocks) != 1 || blocks[0].Type != "text" {
				return false
			}
			content = blocks[0].Text
		}
		values, ok := claudeChannelTuple(content)
		return ok && values["source"] == source && values["session_id"] == sid && values["binding_id"] == ack.BindingID && values["input_id"] == ack.InputID && values["claim_id"] == ack.ClaimID && values["input_token"] == ack.InputToken && ack.BindingID != "" && ack.InputID != "" && ack.ClaimID != "" && ack.InputToken != ""
	})
}

// claudeNativeScan validates a Claude transcript from offset on, record by
// record, and counts the records match accepts (more than one is an error).
// Its size is never a refusal: a record over nativeRecordMax is read
// through and skipped (nativescan.go).
func claudeNativeScan(file, sid string, missingOK bool, offset int64, match func(json.RawMessage) bool) (bool, error) {
	found := false
	err := nativeRecords(file, offset, func(rec nativeRecord) error {
		if !rec.Complete {
			return errors.New("native Claude record is not completely persisted")
		}
		if rec.Oversize {
			return nil
		}
		var row struct{ SessionID, Version, Type string }
		if json.Unmarshal(rec.Line, &row) != nil {
			return errors.New("native Claude transcript record is invalid")
		}
		if row.SessionID != "" && row.SessionID != sid {
			return errors.New("native Claude transcript contains another session")
		}
		// A resumed session keeps the records earlier Claude versions wrote:
		// a version is typed, never required to be one release.
		if row.Version != "" && !nativeVersion.MatchString(row.Version) {
			return errors.New("native Claude transcript version is malformed")
		}
		if match != nil && match(json.RawMessage(rec.Line)) {
			if found {
				return errors.New("duplicate native Claude input receipt")
			}
			found = true
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) && missingOK {
		return false, nil // native Claude may create its first JSONL lazily
	}
	if err != nil {
		return false, err
	}
	return found, nil
}
