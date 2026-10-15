package client

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// The run folders each harness is given, as whole argument slices (ROOM_V1
// §6): claude reads in/ and writes a task's out/; codex gets only a task's
// out/, never a sandbox change or bypass, and no outbox at all when it
// resumes; pi gets none; a run without files gets nothing. Where the
// outbox's file checks cannot be made (Windows), no task gets an outbox.
func TestRunArgsGolden(t *testing.T) {
	path := filepath.Join("home", "runs", "R")
	in, out := filepath.Join(path, "in"), filepath.Join(path, "out")
	task := &runDir{path: path, in: []string{filepath.Join(in, "01-0123abcd-a.txt")}, out: &fileKey{}}
	question := &runDir{path: path, in: task.in}
	outOnly := &runDir{path: path, out: &fileKey{}}
	none := &runDir{path: path}
	claude, codex, pi := Harnesses["claude"], Harnesses["codex"], Harnesses["pi"]
	argv := func(base []string, r *runDir, h harness, resume bool) []string {
		if r.out != nil && !outboxFor(job{ID: "R", Kind: envelope.KindTask}, h, resume) {
			r = &runDir{path: r.path, in: r.in} // as runJob makes it: no outbox
		}
		return append(slices.Clone(base), r.args(h)...) // as runJob composes them (no lookups)
	}
	// withOut: the outbox argument, where this platform gives a task one.
	withOut := func(args ...string) []string {
		if outboxSupported {
			return append(args, "--add-dir", out)
		}
		return args
	}
	for _, c := range []struct {
		name      string
		got, want []string
	}{
		{"claude question", argv(claude.question, question, claude, false),
			[]string{"-p", "--output-format", "text", "--no-session-persistence", "--permission-mode", "auto", "--add-dir", in}},
		{"claude question without files", argv(claude.question, none, claude, false),
			[]string{"-p", "--output-format", "text", "--no-session-persistence", "--permission-mode", "auto"}},
		{"claude task", argv(claude.task, task, claude, false),
			withOut("-p", "--output-format", "text", "--no-session-persistence", "--permission-mode", "auto", "--add-dir", in)},
		{"claude task without received files", argv(claude.task, outOnly, claude, false),
			withOut("-p", "--output-format", "text", "--no-session-persistence", "--permission-mode", "auto")},
		{"claude task resumed", argv(resumeArgs(claude, "task", claude.task, "S"), task, claude, true),
			withOut("-p", "--output-format", "text", "--permission-mode", "auto", "--resume", "S", "--add-dir", in)},
		{"codex question", argv(codex.question, question, codex, false),
			[]string{"app-server"}},
		{"codex task", argv(codex.task, task, codex, false),
			[]string{"app-server"}},
		{"codex task resumed", argv(resumeArgs(codex, "task", codex.task, "T"), task, codex, true),
			[]string{"app-server"}},
		{"pi question", argv(pi.question, question, pi, false),
			[]string{"-p", "--no-session"}},
		{"pi task", argv(pi.task, task, pi, false), []string{"-p", "--no-session"}},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
	for _, name := range HarnessNames() {
		for _, arg := range task.args(Harnesses[name]) {
			if strings.Contains(arg, "sandbox") || strings.Contains(arg, "dangerously") || strings.Contains(arg, "bypass") {
				t.Errorf("%s run arguments change the sandbox: %q", name, arg)
			}
		}
	}
	var nilRun *runDir
	if nilRun.args(claude) != nil || outboxPrompt(nilRun) != "" || outboxPrompt(question) != "" {
		t.Fatal("a job without a run folder or outbox is given one")
	}
	device := job{ID: "R", Kind: envelope.KindTask}
	if outboxFor(device, codex, true) != outboxSupported || outboxFor(device, claude, false) != outboxSupported || outboxFor(device, codex, false) != outboxSupported || outboxFor(device, pi, false) != outboxSupported {
		t.Fatal("outbox given or withheld wrongly")
	}
}

func gone(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

// Harness descriptions must match the actual native transport, not promise
// writable permissions merely because an output path was supplied.
func TestHarnessLimitsTellRunFolders(t *testing.T) {
	codex := Harnesses["codex"].limits
	for _, want := range []string{"app-server", "native automatic approval review", "inherits your sandbox policy", "grants no additional writable folders"} {
		if !strings.Contains(codex, want) {
			t.Errorf("codex limits lack %q", want)
		}
	}

	for name, want := range map[string]string{"claude": "added with --add-dir", "pi": "no folder is added for Pi"} {
		if !strings.Contains(Harnesses[name].limits, want) {
			t.Errorf("%s limits lack %q", name, want)
		}
	}
}
