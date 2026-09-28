//go:build windows

package itest

// A native probe of the Windows Task Scheduler facts that switching a
// task-started daemon to an updated program would rely on (agentnet update).
// It uses a private task with a unique name and a stand-in program (a copy of
// this test binary), never a real agent home, and removes exactly what it
// created. It changes the machine's task list while it runs, so it is opt-in:
// AGENTNET_WINTASK=1.
//
// What it checks, in order:
//   - the task's action keeps the daemon's arguments exactly (--home with a
//     space, daemon, --ui), as stored and as each started program sees them;
//   - the running instance's EnginePID (RegisteredTask.GetInstances) is the
//     started program's own process id, seen from outside and from inside;
//   - the task instance's job, and whether a helper started by the instance
//     (DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP, no breakaway flag) is
//     outside that job and still runs after the instance has ended;
//   - the helper's schtasks /run starts a new instance of the same task, with
//     the same arguments, which schtasks /end then stops.
// The task's default settings (batteries, time limit, instances policy) and
// principal are logged, not judged.

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	winProbeMarker    = ".agentnet-winprobe" // in the stand-in home: this is a probe fixture
	winProbeHelperCmd = "winprobe-helper"
	winProbeTaskFile  = "task-name"
	taskStateRunning  = 4 // TASK_STATE_RUNNING
	stillActive       = 259
)

// winProbeRecord is what each stand-in process writes about itself.
type winProbeRecord struct {
	PID       int      `json:"pid"`
	Args      []string `json:"args"`
	InJob     bool     `json:"in_job"`
	JobFlags  uint32   `json:"job_flags,omitempty"`
	JobPIDs   []int    `json:"job_pids,omitempty"` // instance: its job's processes after starting the helper
	JobErr    string   `json:"job_err,omitempty"`
	Engine    string   `json:"engine,omitempty"` // instance: "state|pids" from its own GetInstances query
	HelperPID int      `json:"helper_pid,omitempty"`
	RunExit   int      `json:"run_exit"`
	RunOut    string   `json:"run_out,omitempty"`
	Err       string   `json:"err,omitempty"`
}

// A copy of this test binary started as `--home HOME daemon ...` or
// `--home HOME winprobe-helper PID`, with HOME holding the probe marker, is
// the stand-in program; nothing else ever passes those arguments to a test
// binary.
func init() {
	a := os.Args
	if len(a) < 4 || a[1] != "--home" {
		return
	}
	if _, err := os.Stat(filepath.Join(a[2], winProbeMarker)); err != nil {
		return
	}
	switch a[3] {
	case "daemon":
		os.Exit(winProbeInstance(a[2]))
	case winProbeHelperCmd:
		os.Exit(winProbeHelper(a[2], a[4:]))
	}
}

// winProbeInstance is one run of the task. The first run (old.json) starts
// the helper once the test says go and exits; the next (new.json) waits
// like a daemon until the task is ended.
func winProbeInstance(home string) int {
	rec := winProbeSelf()
	name, _ := os.ReadFile(filepath.Join(home, winProbeTaskFile))
	if state, pids, err := winTaskInstances(string(name)); err != nil {
		rec.Engine = "error: " + err.Error()
	} else {
		rec.Engine = fmt.Sprintf("%d|%v", state, pids)
	}
	f, err := os.OpenFile(filepath.Join(home, "old.claim"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		winProbeWrite(home, "new.json", rec)
		time.Sleep(2 * time.Minute) // until schtasks /end
		return 0
	}
	f.Close()
	winProbeWrite(home, "old.json", rec)
	if !winProbeWait(home, "go", time.Minute) {
		return 1
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "--home", home, winProbeHelperCmd, strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS, HideWindow: true}
	if err := cmd.Start(); err != nil {
		rec.Err = "start helper: " + err.Error()
		winProbeWrite(home, "old-done.json", rec)
		return 1
	}
	rec.HelperPID = cmd.Process.Pid
	if rec.InJob {
		rec.JobPIDs, err = winProbeJobPIDs()
		if err != nil {
			rec.JobErr = err.Error()
		}
	}
	if !winProbeWait(home, "helper-ready", 20*time.Second) {
		rec.Err = "helper not ready"
	}
	winProbeWrite(home, "old-done.json", rec)
	return 0 // the instance ends; the helper must outlive it
}

// winProbeHelper holds the old instance's process, waits for it to exit and
// for the test's go-ahead, then asks Task Scheduler to run the task again.
func winProbeHelper(home string, args []string) int {
	rec := winProbeSelf()
	if len(args) != 1 {
		rec.Err = "usage"
		winProbeWrite(home, "helper.json", rec)
		return 2
	}
	old, _ := strconv.Atoi(args[0])
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(old))
	if err != nil {
		rec.Err = "open old instance: " + err.Error()
		winProbeWrite(home, "helper.json", rec)
		return 1
	}
	defer windows.CloseHandle(h)
	winProbeWrite(home, "helper-ready", rec)
	if ev, _ := windows.WaitForSingleObject(h, 60_000); ev != windows.WAIT_OBJECT_0 {
		rec.Err = "old instance did not exit"
	}
	if !winProbeWait(home, "run", time.Minute) {
		rec.Err += "; no go-ahead to run"
		winProbeWrite(home, "helper.json", rec)
		return 1
	}
	name, _ := os.ReadFile(filepath.Join(home, winProbeTaskFile))
	run := exec.Command("schtasks", "/run", "/tn", string(name))
	run.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	out, err := run.CombinedOutput()
	rec.RunOut = strings.TrimSpace(string(out))
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		rec.RunExit = exit.ExitCode()
	} else if err != nil {
		rec.RunExit, rec.Err = -1, err.Error()
	}
	winProbeWrite(home, "helper.json", rec)
	return 0
}

func winProbeSelf() winProbeRecord {
	rec := winProbeRecord{PID: os.Getpid(), Args: os.Args[1:]}
	in, err := winProbeInJob()
	if err != nil {
		rec.JobErr = "IsProcessInJob: " + err.Error()
		return rec
	}
	rec.InJob = in
	if in {
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
			rec.JobErr = "job limits: " + err.Error()
		} else {
			rec.JobFlags = info.BasicLimitInformation.LimitFlags
		}
	}
	return rec
}

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// winProbeInJob reports whether this process belongs to any job.
func winProbeInJob() (bool, error) {
	var in int32
	if r, _, err := procIsProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&in))); r == 0 {
		return false, err
	}
	return in != 0, nil
}

// winProbeJobPIDs lists the processes of this process's own (immediate) job.
func winProbeJobPIDs() ([]int, error) {
	var list struct {
		Assigned, InList uint32
		IDs              [512]uintptr
	}
	if err := windows.QueryInformationJobObject(0, windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)), uint32(unsafe.Sizeof(list)), nil); err != nil {
		return nil, err
	}
	pids := make([]int, 0, list.InList)
	for _, id := range list.IDs[:list.InList] {
		pids = append(pids, int(id))
	}
	return pids, nil
}

// winTaskInstances asks Task Scheduler (COM, through Windows PowerShell) for
// the task's state and the EnginePID of each running instance.
func winTaskInstances(name string) (int, []int, error) {
	script := `$ErrorActionPreference='Stop';$s=New-Object -ComObject Schedule.Service;$s.Connect();` +
		`$t=$s.GetFolder('\').GetTask('` + name + `');$p=@();foreach($i in $t.GetInstances(0)){$p+=$i.EnginePID};` +
		`"$($t.State)|$($p -join ',')"`
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return 0, nil, fmt.Errorf("%v: %s", err, bytes.TrimSpace(out))
	}
	state, list, ok := strings.Cut(strings.TrimSpace(string(out)), "|")
	if !ok {
		return 0, nil, fmt.Errorf("unexpected %q", out)
	}
	n, err := strconv.Atoi(state)
	if err != nil {
		return 0, nil, fmt.Errorf("state %q", state)
	}
	var pids []int
	for f := range strings.SplitSeq(list, ",") {
		if f == "" {
			continue
		}
		p, err := strconv.Atoi(f)
		if err != nil {
			return 0, nil, fmt.Errorf("pid %q", f)
		}
		pids = append(pids, p)
	}
	return n, pids, nil
}

func winProbeWrite(home, name string, v any) {
	data, _ := json.Marshal(v)
	tmp := filepath.Join(home, name+".tmp")
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, filepath.Join(home, name))
	}
}

// winProbeWait waits for a file the other side writes (a local fixture
// handshake, bounded).
func winProbeWait(home, name string, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(home, name)); err == nil {
			return true
		}
	}
	return false
}

func winProbeRead(t *testing.T, home, name string, d time.Duration) winProbeRecord {
	t.Helper()
	var rec winProbeRecord
	if !winProbeWait(home, name, d) {
		t.Fatalf("no %s within %v", name, d)
	}
	data, err := os.ReadFile(filepath.Join(home, name))
	if err == nil {
		err = json.Unmarshal(data, &rec)
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	t.Logf("%s: %s", name, data)
	return rec
}

// winTaskXML is the part of a task definition the switch would verify.
type winTaskXML struct {
	LogonType string `xml:"Principals>Principal>LogonType"`
	UserID    string `xml:"Principals>Principal>UserId"`
	Settings  struct {
		MultipleInstancesPolicy    string
		DisallowStartIfOnBatteries string
		StopIfGoingOnBatteries     string
		ExecutionTimeLimit         string
	} `xml:"Settings"`
	Exec []struct {
		Command   string
		Arguments string
	} `xml:"Actions>Exec"`
}

func winQueryTaskXML(t *testing.T, name string) winTaskXML {
	t.Helper()
	out, err := exec.Command("schtasks", "/query", "/tn", name, "/xml", "ONE").Output()
	if err != nil {
		t.Fatalf("schtasks /query /xml: %v", err)
	}
	if len(out) >= 2 && (out[0] == 0xff && out[1] == 0xfe || len(out) > 1 && out[1] == 0) { // UTF-16LE
		out = bytes.TrimPrefix(out, []byte{0xff, 0xfe})
		u := make([]uint16, len(out)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(out[2*i:])
		}
		out = []byte(string(utf16.Decode(u)))
	}
	var def winTaskXML
	dec := xml.NewDecoder(bytes.NewReader(out))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil } // already UTF-8
	if err := dec.Decode(&def); err != nil {
		t.Fatalf("task XML: %v\n%s", err, out)
	}
	return def
}

func winSchtasks(args ...string) (string, error) {
	out, err := exec.Command("schtasks", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func winOpen(t *testing.T, pid int) windows.Handle {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatalf("open process %d: %v", pid, err)
	}
	t.Cleanup(func() { windows.CloseHandle(h) })
	return h
}

func winAlive(h windows.Handle) bool {
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == stillActive
}

func winImage(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "(" + err.Error() + ")"
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "(" + err.Error() + ")"
	}
	return windows.UTF16ToString(buf[:n])
}

func TestWindowsTaskSwitchProbe(t *testing.T) {
	if os.Getenv("AGENTNET_WINTASK") != "1" {
		t.Skip("creates and runs a private scheduled task; set AGENTNET_WINTASK=1")
	}
	// Short paths: /tr is limited to 262 characters. Spaces on purpose.
	root, err := os.MkdirTemp("", "anp")
	if err != nil {
		t.Fatal(err)
	}
	bin, home := filepath.Join(root, "bin dir"), filepath.Join(root, "agent home")
	for _, d := range []string{bin, home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "agentnet.exe")
	if data, err := os.ReadFile(self); err != nil || os.WriteFile(exe, data, 0o700) != nil {
		t.Fatalf("copy the stand-in program: %v", err)
	}
	rnd := make([]byte, 6)
	rand.Read(rnd)
	name := "agentnet-itest-" + hex.EncodeToString(rnd)
	for f, data := range map[string]string{winProbeMarker: "", winProbeTaskFile: name} {
		if err := os.WriteFile(filepath.Join(home, f), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	argv := []string{"--home", home, "daemon", "--ui", "127.0.0.1:0"}
	tr := syscall.EscapeArg(exe)
	for _, a := range argv {
		tr += " " + syscall.EscapeArg(a)
	}
	if len(tr) > 262 {
		t.Fatalf("/tr is %d characters (limit 262): %s", len(tr), tr)
	}

	// Cleanup touches only this task and processes running this copy.
	var pids []int
	t.Cleanup(func() {
		winSchtasks("/end", "/tn", name)
		if out, err := winSchtasks("/delete", "/tn", name, "/f"); err != nil {
			t.Errorf("delete task %s: %v: %s", name, err, out)
		}
		for _, pid := range pids {
			if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)); err == nil {
				if winAlive(h) && strings.EqualFold(winImage(pid), exe) {
					windows.TerminateProcess(h, 1)
					windows.WaitForSingleObject(h, 5000)
				}
				windows.CloseHandle(h)
			}
		}
		os.RemoveAll(root)
	})

	// The task as documented (agentnet help daemon), with the full daemon
	// arguments. A runner without an interactive logon may not start an
	// "only when logged on" task; then the same task is made with a
	// stored-less non-interactive logon (/ru USER /np) and that is reported.
	principal := "documented (schtasks /create /sc onlogon, no /ru)"
	create := []string{"/create", "/sc", "onlogon", "/tn", name, "/tr", tr}
	if out, err := winSchtasks(create...); err != nil {
		t.Fatalf("schtasks /create: %v: %s", err, out)
	}
	start := func() bool {
		if out, err := winSchtasks("/run", "/tn", name); err != nil {
			t.Fatalf("schtasks /run: %v: %s", err, out)
		}
		return winProbeWait(home, "old.json", 45*time.Second)
	}
	if !start() {
		out, _ := winSchtasks("/query", "/tn", name, "/v", "/fo", "list")
		t.Logf("%s task did not start here:\n%s", principal, out)
		u, err := user.Current()
		if err != nil {
			t.Fatal(err)
		}
		winSchtasks("/end", "/tn", name)
		if out, err := winSchtasks("/delete", "/tn", name, "/f"); err != nil {
			t.Fatalf("delete before retry: %v: %s", err, out)
		}
		principal = "non-interactive (/ru " + u.Username + " /np)"
		if out, err := winSchtasks(append(create, "/ru", u.Username, "/np")...); err != nil {
			t.Fatalf("schtasks /create %s: %v: %s", principal, err, out)
		}
		if !start() {
			out, _ := winSchtasks("/query", "/tn", name, "/v", "/fo", "list")
			t.Fatalf("%s task did not start either:\n%s", principal, out)
		}
	}
	t.Logf("principal measured: %s", principal)

	def := winQueryTaskXML(t, name)
	t.Logf("task settings: logon %s, user %s, instances %q, no start on batteries %q, stop on batteries %q, time limit %q",
		def.LogonType, def.UserID, def.Settings.MultipleInstancesPolicy, def.Settings.DisallowStartIfOnBatteries,
		def.Settings.StopIfGoingOnBatteries, def.Settings.ExecutionTimeLimit)
	if len(def.Exec) != 1 {
		t.Fatalf("task has %d exec actions", len(def.Exec))
	}
	t.Logf("stored action: Command %q Arguments %q", def.Exec[0].Command, def.Exec[0].Arguments)
	if a, b := filepath.Clean(strings.Trim(def.Exec[0].Command, `"`)), exe; !strings.EqualFold(a, b) {
		t.Errorf("stored Command %q, want %q", a, b)
	}
	// The first word of a command line follows program-name rules, so a
	// placeholder takes its place when splitting the stored arguments.
	if got, err := windows.DecomposeCommandLine("x " + def.Exec[0].Arguments); err != nil || !slices.Equal(got[1:], argv) {
		t.Errorf("stored Arguments split to %q (%v), want %q", got, err, argv)
	}

	// The first instance: arguments and EnginePID, from outside and inside.
	old := winProbeRead(t, home, "old.json", time.Second)
	pids = append(pids, old.PID)
	oldH := winOpen(t, old.PID)
	if !slices.Equal(old.Args, argv) {
		t.Errorf("instance started with %q, want %q", old.Args, argv)
	}
	state, engine, err := winTaskInstances(name)
	if err != nil {
		t.Fatalf("GetInstances: %v", err)
	}
	t.Logf("while the first instance runs: state %d, EnginePID %v (instance pid %d), inside view %q", state, engine, old.PID, old.Engine)
	if state != taskStateRunning || !slices.Equal(engine, []int{old.PID}) {
		for _, p := range engine {
			t.Logf("EnginePID %d is %s", p, winImage(p))
		}
		t.Errorf("EnginePID %v is not the started program's pid %d", engine, old.PID)
	}
	if want := fmt.Sprintf("%d|%v", taskStateRunning, []int{old.PID}); old.Engine != want {
		t.Errorf("inside view %q, want %q", old.Engine, want)
	}
	t.Logf("instance job: in job %v, limit flags %#x %s", old.InJob, old.JobFlags, old.JobErr)

	// The helper, started by the instance, and the instance's end.
	os.WriteFile(filepath.Join(home, "go"), nil, 0o600)
	done := winProbeRead(t, home, "old-done.json", 45*time.Second)
	if done.Err != "" || done.HelperPID == 0 {
		t.Fatalf("instance could not start the helper: %s", done.Err)
	}
	pids = append(pids, done.HelperPID)
	helperH := winOpen(t, done.HelperPID)
	if old.InJob {
		if done.JobErr != "" {
			t.Errorf("instance could not list its job: %s", done.JobErr)
		} else if slices.Contains(done.JobPIDs, done.HelperPID) {
			t.Errorf("helper %d is in the task instance's job %v", done.HelperPID, done.JobPIDs)
		}
	}
	if ev, _ := windows.WaitForSingleObject(oldH, 20_000); ev != windows.WAIT_OBJECT_0 {
		t.Fatal("first instance did not exit")
	}
	ended := false
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(250 * time.Millisecond) {
		if state, engine, err := winTaskInstances(name); err == nil && state != taskStateRunning && len(engine) == 0 {
			ended = true
			break
		}
	}
	if !ended {
		t.Fatal("Task Scheduler still reports the first instance running")
	}
	time.Sleep(2 * time.Second) // time for the scheduler to close the instance's job
	if !winAlive(helperH) {
		t.Fatal("helper did not outlive the task instance")
	}

	// The helper runs the task again; /end controls the new instance.
	os.WriteFile(filepath.Join(home, "run"), nil, 0o600)
	helper := winProbeRead(t, home, "helper.json", 45*time.Second)
	t.Logf("helper job: in job %v, limit flags %#x %s", helper.InJob, helper.JobFlags, helper.JobErr)
	if helper.RunExit != 0 || helper.Err != "" {
		t.Fatalf("helper's schtasks /run: exit %d %q %s", helper.RunExit, helper.RunOut, helper.Err)
	}
	next := winProbeRead(t, home, "new.json", 45*time.Second)
	pids = append(pids, next.PID)
	nextH := winOpen(t, next.PID)
	if next.PID == old.PID || !slices.Equal(next.Args, argv) {
		t.Errorf("new instance pid %d args %q, want a new pid and %q", next.PID, next.Args, argv)
	}
	state, engine, err = winTaskInstances(name)
	if err != nil || state != taskStateRunning || !slices.Equal(engine, []int{next.PID}) {
		t.Errorf("after /run: state %d EnginePID %v (%v), want running and [%d]", state, engine, err, next.PID)
	}
	if out, err := winSchtasks("/end", "/tn", name); err != nil {
		t.Fatalf("schtasks /end: %v: %s", err, out)
	}
	if ev, _ := windows.WaitForSingleObject(nextH, 15_000); ev != windows.WAIT_OBJECT_0 {
		t.Error("schtasks /end did not stop the new instance")
	}
	if ev, _ := windows.WaitForSingleObject(helperH, 15_000); ev != windows.WAIT_OBJECT_0 {
		t.Error("helper did not exit after its run")
	}
}
