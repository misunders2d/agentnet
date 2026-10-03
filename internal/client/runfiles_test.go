package client

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The run folders each harness is given, as whole argument slices (ROOM_V1
// §6): claude reads in/ and writes a task's out/; codex gets only a task's
// out/, never a sandbox change or bypass, and nothing when it resumes; pi
// gets none; a run without files gets nothing.
func TestRunArgsGolden(t *testing.T) {
	path := filepath.Join("home", "runs", "R")
	in, out := filepath.Join(path, "in"), filepath.Join(path, "out")
	task := &runDir{path: path, in: []string{filepath.Join(in, "01-0123abcd-a.txt")}, out: &fileKey{}}
	question := &runDir{path: path, in: task.in}
	outOnly := &runDir{path: path, out: &fileKey{}}
	none := &runDir{path: path}
	claude, codex, pi := Harnesses["claude"], Harnesses["codex"], Harnesses["pi"]
	argv := func(base []string, r *runDir, h harness, resume bool) []string {
		return append(slices.Clone(base), r.args(h, resume)...) // as runJob composes them (no lookups)
	}
	for _, c := range []struct {
		name      string
		got, want []string
	}{
		{"claude question", argv(claude.question, question, claude, false),
			[]string{"-p", "--output-format", "text", "--no-session-persistence", "--permission-mode", "dontAsk", "--disallowedTools", "Edit,Write,NotebookEdit", "--add-dir", in}},
		{"claude question without files", argv(claude.question, none, claude, false),
			[]string{"-p", "--output-format", "text", "--no-session-persistence", "--permission-mode", "dontAsk", "--disallowedTools", "Edit,Write,NotebookEdit"}},
		{"claude task", argv(claude.task, task, claude, false),
			[]string{"-p", "--output-format", "text", "--no-session-persistence", "--add-dir", in, "--add-dir", out}},
		{"claude task without received files", argv(claude.task, outOnly, claude, false),
			[]string{"-p", "--output-format", "text", "--no-session-persistence", "--add-dir", out}},
		{"claude task resumed", argv(resumeArgs(claude, "task", claude.task, "S"), task, claude, true),
			[]string{"-p", "--output-format", "text", "--resume", "S", "--add-dir", in, "--add-dir", out}},
		{"codex question", argv(codex.question, question, codex, false),
			[]string{"exec", "--ephemeral", "--sandbox", "read-only", "--skip-git-repo-check", "--color", "never", "-c", `approval_policy="never"`}},
		{"codex task", argv(codex.task, task, codex, false),
			[]string{"exec", "--ephemeral", "--skip-git-repo-check", "--color", "never", "--add-dir", out}},
		{"codex task resumed", argv(resumeArgs(codex, "task", codex.task, "T"), task, codex, true),
			[]string{"exec", "resume", "T", "--json", "--skip-git-repo-check"}},
		{"pi question", argv(pi.question, question, pi, false),
			[]string{"-p", "--no-session", "--exclude-tools", "bash,edit,write,powershell"}},
		{"pi task", argv(pi.task, task, pi, false), []string{"-p", "--no-session"}},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
	for _, name := range HarnessNames() {
		for _, arg := range task.args(Harnesses[name], false) {
			if strings.Contains(arg, "sandbox") || strings.Contains(arg, "dangerously") || strings.Contains(arg, "bypass") {
				t.Errorf("%s run arguments change the sandbox: %q", name, arg)
			}
		}
	}
	var nilRun *runDir
	if nilRun.args(claude, false) != nil || outboxPrompt(nilRun) != "" || outboxPrompt(question) != "" {
		t.Fatal("a job without a run folder or outbox is given one")
	}
}
