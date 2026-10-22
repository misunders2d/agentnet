package ui

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// This deterministic native process selects marked references from its actual
// supplied prompt. It proves transport/action/report plumbing, not a model's
// semantic selection or acceptance in the owner's live conversation.
func TestAgentOrganizationJoinedProcess(t *testing.T) {
	if os.Getenv("AGENTNET_ORGANIZATION_STANDIN") != "1" {
		t.Skip("owned native stand-in subprocess only")
	}
	fail := func(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(2) }
	starts, err := os.OpenFile(os.Getenv("AGENTNET_ORGANIZATION_STARTS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fail(err.Error())
	}
	fmt.Fprintln(starts, "start")
	starts.Close()
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var prompt, link, operation string
	var ids []string
	tool := func(n int) {
		enc.Encode(map[string]any{"id": fmt.Sprint("organize-", n), "method": "item/tool/call", "params": map[string]any{
			"threadId": "organization-thread", "turnId": "organization-turn", "tool": "agentnet_room", "callId": fmt.Sprint("call-", n),
			"arguments": map[string]any{"action": "organize", "new": true, "title": "Fixture organization", "ids": ids},
		}})
	}
	for {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Input []struct{ Text string } `json:"input"`
			} `json:"params"`
			Result struct {
				Success bool                    `json:"success"`
				Items   []struct{ Text string } `json:"contentItems"`
			} `json:"result"`
		}
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF && link != "" {
				os.Exit(0)
			}
			fail("native input ended before the exact tool result")
		}
		switch m.Method {
		case "initialize":
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{}})
		case "thread/start":
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]string{"id": "organization-thread"}, "approvalPolicy": "on-request"}})
		case "turn/start":
			if len(m.Params.Input) != 1 {
				fail("expected one production native prompt")
			}
			prompt = m.Params.Input[0].Text
			refs := regexp.MustCompile(`(?m)^\[message ([a-f0-9]{32}); author key [^\]]+\] [^\n]*ORG_SELECTED_[AB]`).FindAllStringSubmatch(prompt, -1)
			for _, ref := range refs {
				ids = append(ids, ref[1])
			}
			if len(ids) != 2 {
				fail("the granted prompt did not supply both selected originals")
			}
			file := regexp.MustCompile(`Selected file "original-report.txt"[^\n]*read-only at "([^"]+)"`).FindStringSubmatch(prompt)
			if len(file) != 2 {
				fail("the granted original attachment was not supplied to the native run")
			}
			if data, err := os.ReadFile(file[1]); err != nil || !bytes.Equal(data, []byte("EXACT_ORGANIZATION_ATTACHMENT\n")) {
				fail("native received different original attachment bytes")
			}
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]string{"id": "organization-turn"}}})
			tool(1)
		case "":
			if string(m.ID) != `"organize-1"` && string(m.ID) != `"organize-2"` {
				continue
			}
			if !m.Result.Success || len(m.Result.Items) != 1 {
				fail(fmt.Sprintf("organization tool refused: %+v", m.Result))
			}
			text := m.Result.Items[0].Text
			result := regexp.MustCompile(`topic move ([a-f0-9]{32}) stored`).FindStringSubmatch(text)
			destination := regexp.MustCompile(`Destination: (agentnet://open#[^\s]+)`).FindStringSubmatch(text)
			if len(result) != 2 || len(destination) != 2 {
				fail("organization tool did not report its stored result and destination")
			}
			if link == "" {
				operation, link = result[1], destination[1]
				tool(2) // A lost-response retry must keep the same operation and link.
				continue
			}
			if result[1] != operation || destination[1] != link {
				fail("same native request retry changed the operation or destination")
			}
			evidence, _ := json.Marshal(map[string]any{"prompt": prompt, "ids": ids, "link": link, "operation": operation, "calls": 2, "request": os.Getenv(client.AgentRequestEnv)})
			if err := os.WriteFile(os.Getenv("AGENTNET_ORGANIZATION_EVIDENCE"), evidence, 0600); err != nil {
				fail(err.Error())
			}
			enc.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "organization-thread", "turnId": "organization-turn", "item": map[string]string{"id": "report", "type": "agentMessage", "text": "Organized the supplied related discussion.\n\n[Open organized topic](" + link + ")"}}})
			enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "organization-thread", "turn": map[string]string{"id": "organization-turn", "status": "completed"}}})
		}
	}
}

type joinedOrganization struct {
	a, b                   *client.Agent
	db                     *sql.DB
	conv, pid, link, topic string
	selected, omitted      []string
	originals, grant       string
	work, starts           string
	report, move           DMMessage
}

func joinedOrganizationFixture(t *testing.T) *joinedOrganization {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix native stand-in launcher")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	dir, bin, hubHome, aHome, bHome := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexec '"+strings.ReplaceAll(exe, "'", "'\"'\"'")+"' -test.run '^TestAgentOrganizationJoinedProcess$' -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENTNET_ORGANIZATION_STANDIN", "1")
	t.Setenv("AGENTNET_ORGANIZATION_STARTS", filepath.Join(dir, "starts"))
	t.Setenv("AGENTNET_ORGANIZATION_EVIDENCE", filepath.Join(dir, "native.json"))
	testhub.Start(t, hubHome, "127.0.0.1:0", "")
	a, err := client.Join(t.Context(), aHome, testhub.BootstrapCode(t, hubHome), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	code, err := a.Invite(t.Context(), "briar", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.Join(t.Context(), bHome, code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	runDaemon(t, a)
	runDaemon(t, b)
	for _, entry := range []struct {
		a    *client.Agent
		name string
	}{{a, "Aster"}, {b, "Briar"}} {
		if _, err = entry.a.CreatePerson(t.Context(), entry.name); err != nil {
			t.Fatal(err)
		}
	}
	f := &joinedOrganization{a: a, b: b, starts: filepath.Join(dir, "starts")}
	waitFor(t, "joined organization DM", func() bool { f.conv, err = a.CreateDM(t.Context(), b.Address); return err == nil })
	file := filepath.Join(dir, "original-report.txt")
	if err = os.WriteFile(file, []byte("EXACT_ORGANIZATION_ATTACHMENT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	send := func(out client.ConvOutgoing) client.ConvSent {
		t.Helper()
		sent, err := a.SendConv(t.Context(), f.conv, out)
		if err != nil {
			t.Fatal(err)
		}
		return sent
	}
	parent := send(client.ConvOutgoing{Body: "ORG_HIDDEN_PARENT original omitted parent"})
	first := send(client.ConvOutgoing{Body: "ORG_SELECTED_A related original with file", Files: []client.OutgoingFile{{Path: file}}})
	unrelated := send(client.ConvOutgoing{Body: "ORG_HIDDEN_UNRELATED_TASK keep this task in place", Kind: envelope.KindTask})
	second := send(client.ConvOutgoing{Body: "ORG_SELECTED_B related resolution and confirmation", ReplyTo: parent.LID})
	f.selected, f.omitted = []string{first.LID, second.LID}, []string{parent.LID, unrelated.LID}
	waitFor(t, "all interleaved originals at host", func() bool {
		messages, _ := b.ConversationMessages(f.conv)
		return len(messages) == 4
	})
	f.db, err = sql.Open("sqlite", "file:"+filepath.Join(bHome, "agent.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Close() })
	f.originals = f.originalRows(t)
	if err = b.Approve(a.Address); err != nil {
		t.Fatal(err)
	}
	organizer, err := b.CreateLocalAgent("Organizer", client.Responder{Harness: "codex", Dir: dir, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "exact organizer catalog", func() bool {
		if err := b.PublishAgentCatalog(t.Context()); err != nil {
			return false
		}
		records, err := a.AgentCatalog(t.Context(), b.Address)
		return err == nil && len(records) == 1 && records[0].ID == organizer.ID
	})
	p, err := a.InviteNamedAgent(t.Context(), f.conv, b.Address, organizer.ID, f.selected, nil, "Only these two earlier originals")
	if err != nil {
		t.Fatal(err)
	}
	f.pid = p.PID
	waitFor(t, "exact narrow invitation at host", func() bool { p, _ := b.Participation(f.pid); return p.State == client.PartInvited })
	if _, err = b.AcceptParticipation(t.Context(), f.pid); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "signed narrow acceptance at sender", func() bool { p, _ := a.Participation(f.pid); return p.Claimable() })
	p, err = b.Participation(f.pid)
	if err != nil || len(p.Grant) != 2 {
		t.Fatal("narrow history grant missing", err)
	}
	grant, _ := json.Marshal(p.Grant)
	f.grant = string(grant)
	request, err := a.AskAgent(t.Context(), f.pid, envelope.KindQuestion, "Organize the supplied related discussion into a clearly named topic and report its link.")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "native organization and exact linked report", func() bool {
		view, _ := NewLive(a).DM(f.conv)
		for _, m := range view.Messages {
			if m.VerifiedAgent && m.Kind == envelope.KindAnswer && strings.Contains(m.Body, "[Open organized topic](agentnet://open#") {
				f.report = m
			}
			if m.VerifiedAgent && m.Origin == envelope.OriginAgentOrganization {
				f.move = m
			}
		}
		return f.report.ID != "" && f.move.ID != ""
	})
	f.topic = f.move.Topic
	realm, err := a.RealmID()
	if err != nil {
		t.Fatal(err)
	}
	f.link, err = protocol.ConversationLink(realm, f.conv, f.topic)
	if err != nil || !strings.Contains(f.report.Body, "[Open organized topic]("+f.link+")") || f.report.AgentID != organizer.ID || f.move.AgentID != organizer.ID {
		t.Fatal("native report differs from its exact stored destination or executor", err)
	}
	var evidence struct {
		Prompt, Link, Operation, Request string
		IDs                              []string
		Calls                            int
	}
	raw, err := os.ReadFile(filepath.Join(dir, "native.json"))
	if err != nil || json.Unmarshal(raw, &evidence) != nil || !reflect.DeepEqual(evidence.IDs, f.selected) || evidence.Link != f.link || evidence.Operation != f.move.LID || evidence.Calls != 2 {
		t.Fatal("actual native process lost selection, retry or result", err)
	}
	for _, hidden := range append([]string{"ORG_HIDDEN_PARENT", "ORG_HIDDEN_UNRELATED_TASK"}, f.omitted...) {
		if strings.Contains(evidence.Prompt, hidden) {
			t.Fatal("narrow native context disclosed omitted history", hidden)
		}
	}
	if err = f.db.QueryRow(`SELECT json_array(state,attempts,executor,session_ref,result_id) FROM inbox WHERE id=? AND lid=?`, evidence.Request, request.LID).Scan(&f.work); err != nil || !strings.Contains(f.work, `"answered",1,`) {
		t.Fatal("native request did not complete exactly once", f.work, err)
	}
	f.requirePreserved(t)
	f.requirePlacement(t, f.topic)
	return f
}

func (f *joinedOrganization) requirePlacement(t *testing.T, topic string) {
	t.Helper()
	waitFor(t, "both participants project the exact selected placement", func() bool {
		for _, a := range []*client.Agent{f.a, f.b} {
			messages, err := a.ConversationMessages(f.conv)
			if err != nil {
				return false
			}
			assigned := client.ChatTopicAssignments(messages)
			for _, id := range f.selected {
				if assigned[id] != topic {
					return false
				}
			}
			for _, id := range f.omitted {
				if assigned[id] != "" {
					return false
				}
			}
		}
		return true
	})
}

func (f *joinedOrganization) originalRows(t *testing.T) string {
	t.Helper()
	rows, err := f.db.Query(`SELECT json_array(id,lid,sender,verified_by,body,kind,ts,topic,reply_to,target,state,attempts,executor,session_ref,agent_id)
		FROM inbox WHERE conv=? AND lid IN (?,?,?,?) ORDER BY lid,id`, f.conv, f.selected[0], f.selected[1], f.omitted[0], f.omitted[1])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var originals []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		originals = append(originals, raw)
	}
	if err := rows.Err(); err != nil || len(originals) != 4 {
		t.Fatal("original identity set changed", len(originals), err)
	}
	return strings.Join(originals, "\n")
}

func (f *joinedOrganization) requirePreserved(t *testing.T) {
	t.Helper()
	if f.originalRows(t) != f.originals {
		t.Fatal("organization changed an original ID, author, time, body, reply, task or native state")
	}
	p, err := f.b.Participation(f.pid)
	grant, _ := json.Marshal(p.Grant)
	if err != nil || string(grant) != f.grant || p.Topic != nil {
		t.Fatal("organization widened the earlier history or future topic grant", err)
	}
	starts, err := os.ReadFile(f.starts)
	if err != nil || string(starts) != "start\n" {
		t.Fatal("native action or historical work repeated", string(starts), err)
	}
	var work string
	if err = f.db.QueryRow(`SELECT json_array(state,attempts,executor,session_ref,result_id) FROM inbox WHERE pid=? AND kind='question'`, f.pid).Scan(&work); err != nil || work != f.work {
		t.Fatal("completed execution stamp or attempt changed", err)
	}
	var moves, ordinary int
	if err = f.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND origin=?`, f.conv, envelope.OriginAgentOrganization).Scan(&moves); err != nil || moves != 0 {
		// The host keeps its single organization in outbox, not a new input.
		t.Fatal("organization fabricated another native input", moves, err)
	}
	if err = f.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND origin=?`, f.conv, envelope.OriginAgentOrganization).Scan(&moves); err != nil || moves != 1 {
		t.Fatal("native retry duplicated the organization", moves, err)
	}
	if err = f.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND kind='message' AND sub IS NULL AND topic_event IS NULL`, f.conv).Scan(&ordinary); err != nil || ordinary != 0 {
		t.Fatal("organization resent originals as ordinary messages", ordinary, err)
	}
	view, err := NewLive(f.a).DM(f.conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range view.Messages {
		if m.LID == f.selected[0] {
			stream, _, err := f.a.OpenFileFrom(t.Context(), m.Dir, m.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(stream)
			stream.Close()
			if err != nil || !bytes.Equal(data, []byte("EXACT_ORGANIZATION_ATTACHMENT\n")) {
				t.Fatal("original selected attachment changed", err)
			}
			return
		}
	}
	t.Fatal("original attachment message disappeared")
}

func TestAgentOrganizationNativeJoined(t *testing.T) {
	f := joinedOrganizationFixture(t)
	view, err := NewLive(f.a).DM(f.conv)
	if err != nil {
		t.Fatal(err)
	}
	var title string
	for _, topic := range view.Topics {
		if topic.ID == f.topic {
			title = topic.Title
		}
	}
	if title != "Fixture organization" {
		t.Fatalf("requester topic title=%q, want the supplied new title %q", title, "Fixture organization")
	}
	var notice string
	for _, m := range view.Messages {
		if m.LID == f.move.LID {
			notice = m.Body
		}
	}
	if notice != client.TopicOrganizationNotice(f.topic, "Fixture organization") {
		t.Fatalf("requester notice=%q", notice)
	}
	if _, err := f.a.UndoTopicOrganization(t.Context(), client.TopicOrganizationUndo{Conv: f.conv, LID: f.move.LID, Author: f.b.Self().Fingerprint(), ID: protocol.NewID()}); err != nil {
		t.Fatal(err)
	}
	f.requirePlacement(t, "")
	f.requirePreserved(t)
	t.Log("deterministic native stand-in PASS: real action/retry, exact destination report, narrow context, original attachment/work and Undo; no semantic-model/live-chat claim")
}

func TestAgentOrganizationNativeComicRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: installed Playwright required")
	}
	for _, width := range []int{1280, 390} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			f := joinedOrganizationFixture(t)
			realm, err := f.a.RealmID()
			if err != nil {
				t.Fatal(err)
			}
			set := NewWorkspaceProviders()
			if _, err = set.Bind(client.Workspace{ID: "default", Name: "Synthetic organization", Realm: realm, Address: f.a.Address, State: "enrolled"}, NewLive(f.a)); err != nil {
				t.Fatal(err)
			}
			ts := httptest.NewUnstartedServer(nil)
			ts.Config.Handler = New(NewLive(f.a), ts.Listener.Addr().String(), testToken).WorkspaceHandler(set)
			ts.Start()
			t.Cleanup(ts.Close)
			config, _ := json.Marshal(map[string]any{"url": ts.URL + "/?t=" + testToken, "conv": f.conv, "realm": realm, "link": f.link, "topic": f.topic, "report": f.report.ID, "move": f.move.ID, "selected": f.selected, "omitted": f.omitted, "width": width})
			file := filepath.Join(t.TempDir(), "browser.json")
			if err = os.WriteFile(file, config, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "node", "testdata/agentorganization_joined.cjs", file)
			cmd.Env = os.Environ()
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "joined native organization Comic PASS") {
				t.Fatalf("%v\n%s", err, out)
			}
			t.Log(string(out))
			f.requirePlacement(t, "")
			f.requirePreserved(t)
		})
	}
}
