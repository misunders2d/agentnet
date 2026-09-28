package main

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"golang.org/x/sys/windows"
)

// On Windows a process cannot put another program in its own place, and a
// daemon started by the scheduled task \agentnet must stay under that task.
// So a daemon that is exactly the task's running instance switches like
// this: before it stops, it starts a helper (the updated program) and waits
// until the helper holds it; it then stops; the helper waits for it to be
// gone, checks the task again and has Task Scheduler start the task, which
// runs the updated program with the same arguments as the same user. The
// program that starts records the outcome (client.settleUpdate); if none
// does, the helper records the failure. A daemon started any other way is
// never stopped for an update.

// switchHooks: see restart_unix.go.
func switchHooks(home, exe string) (func() (bool, string), func(client.UpdateRequest) error) {
	canSwitch := func() (bool, string) {
		if err := checkTaskInstance(exe, os.Args[1:]); err != nil {
			return false, err.Error() + "; restart it yourself when no job runs (schtasks /end, then /run /tn agentnet)"
		}
		return true, ""
	}
	prepare := func(r client.UpdateRequest) error {
		if err := checkTaskInstance(exe, os.Args[1:]); err != nil {
			return err
		}
		return startHelper(home, exe, r)
	}
	return canSwitch, prepare
}

// checkTaskInstance reports why this process cannot be switched through the
// scheduled task, or nil: the task starts exactly this program with exactly
// these arguments as this user, this process is its running instance, and
// no job would end a helper together with this process.
func checkTaskInstance(exe string, args []string) error {
	if err := checkTaskDefinition(exe, args); err != nil {
		return err
	}
	state, pids, err := taskInstances()
	if err != nil {
		return fmt.Errorf("cannot ask Task Scheduler about %s: %w", winTaskName, err)
	}
	if state != taskStateRunning || !slices.Contains(pids, os.Getpid()) {
		return fmt.Errorf("this daemon is not the running instance of the scheduled task %s (it was started some other way)", winTaskName)
	}
	return checkJob()
}

const (
	taskStateRunning = 4   // TASK_STATE_RUNNING
	stillActive      = 259 // STILL_ACTIVE
)

func checkTaskDefinition(exe string, args []string) error {
	out, err := runTask(30*time.Second, 1<<20, "schtasks", "/query", "/tn", winTaskName, "/xml", "ONE")
	if err != nil {
		return fmt.Errorf("no scheduled task %s to restart this daemon (agentnet help startup): %v", winTaskName, err)
	}
	def, err := parseTaskDef(out)
	if err != nil {
		return err
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	return checkTaskDef(def, taskEnv{exe: exe, args: args, user: u.Username, sid: u.Uid, sameFile: sameFile, split: windows.DecomposeCommandLine})
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// checkJob refuses when this process is in a job that kills its processes
// when it closes: a helper started from here would belong to that job and
// could end with it. Only the immediate job can be asked.
func checkJob() error {
	var in int32
	if r, _, err := procIsProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&in))); r == 0 {
		return fmt.Errorf("cannot tell whether this daemon runs in a job: %v", err)
	}
	if in == 0 {
		return nil
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return fmt.Errorf("cannot read this daemon's job: %v", err)
	}
	f := info.BasicLimitInformation.LimitFlags
	if f&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE != 0 && f&windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK == 0 {
		return errors.New("this daemon runs in a job that ends its processes when it closes, so a helper could not outlive it")
	}
	return nil
}

// taskInstances asks Task Scheduler (its COM interface, through Windows
// PowerShell) for the task's state and the process id of each running
// instance.
func taskInstances() (int, []int, error) {
	script := `$ErrorActionPreference='Stop';$s=New-Object -ComObject Schedule.Service;$s.Connect();` +
		`$t=$s.GetFolder('\').GetTask('agentnet');$p=@();foreach($i in $t.GetInstances(0)){$p+=$i.EnginePID};` +
		`"$($t.State)|$($p -join ',')"`
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	out, err := runTask(60*time.Second, 64<<10, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	if err != nil {
		return 0, nil, err
	}
	state, list, ok := strings.Cut(strings.TrimSpace(string(out)), "|")
	if !ok {
		return 0, nil, fmt.Errorf("unexpected answer %q", out)
	}
	n, err := strconv.Atoi(state)
	if err != nil || n < 0 || n > 10 {
		return 0, nil, fmt.Errorf("unexpected state %q", state)
	}
	var pids []int
	for f := range strings.SplitSeq(list, ",") {
		if f == "" {
			continue
		}
		p, err := strconv.Atoi(f)
		if err != nil || p <= 0 {
			return 0, nil, fmt.Errorf("unexpected process id %q", f)
		}
		pids = append(pids, p)
	}
	return n, pids, nil
}

func init() {
	quietAttr = func(cmd *exec.Cmd) {
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	}
}

// runTask runs schtasks and PowerShell for the switch, always bounded
// (runQuiet); tests replace it.
var runTask = runQuiet

// helperReady is where the helper says it holds the daemon's process.
func helperReady(home, id string) string { return filepath.Join(home, "update-helper-"+id+".ready") }

// startHelper starts the helper (the updated program) and returns once it
// holds this process, so it will see this daemon exit. On any error the
// helper is stopped and the daemon does not stop.
func startHelper(home, exe string, r client.UpdateRequest) error {
	ready := helperReady(home, r.ID)
	os.Remove(ready)
	args := append([]string{"--home", home, updateHelperCmd, "--id", r.ID, "--pid", strconv.Itoa(os.Getpid()), "--"}, os.Args[1:]...)
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS, HideWindow: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start the update helper: %w", err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	deadline := time.After(20 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return nil
		}
		select {
		case <-exited:
			return errors.New("the update helper stopped before it was ready")
		case <-deadline:
			cmd.Process.Kill()
			return errors.New("the update helper did not get ready")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// restartForUpdate: the helper started by prepare takes over once this
// process exits, which it now does.
func restartForUpdate(home, exe string, r client.UpdateRequest) error {
	return nil
}

// runUpdateHelper is the hidden `update-helper` command: wait for the
// daemon to exit, check that nothing changed, have Task Scheduler start the
// task again, and see the updated daemon settle the request. Every wait is
// bounded; if the updated daemon does not come, the failure is recorded.
func runUpdateHelper(home string, args []string) error {
	fs := flag.NewFlagSet(updateHelperCmd, flag.ContinueOnError)
	id := fs.String("id", "", "")
	pid := fs.Int("pid", 0, "")
	if err := fs.Parse(args); err != nil || *id == "" || *pid <= 0 {
		return errors.New("usage: update-helper --id ID --pid PID -- DAEMON-ARGS")
	}
	daemonArgs := fs.Args()
	fail := func(detail string) error {
		if pendingID, to := client.PendingUpdate(home); pendingID == *id {
			client.RecordUpdateActivation(home, client.UpdateActivation{ID: *id, To: to, Result: client.ActivationFailed, Detail: detail})
		}
		return errors.New(detail)
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(*pid))
	if err != nil {
		return fmt.Errorf("cannot hold the daemon's process: %w", err)
	}
	defer windows.CloseHandle(h)
	ready := helperReady(home, *id)
	if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return err
	}
	defer os.Remove(ready)
	if ev, _ := windows.WaitForSingleObject(h, 2*60*1000); ev != windows.WAIT_OBJECT_0 {
		return fail("the daemon did not stop within 2 minutes to switch; once it stops, start it: schtasks /run /tn agentnet")
	}
	// The daemon's lock is free once everything it held is closed.
	for end := time.Now().Add(time.Minute); ; time.Sleep(200 * time.Millisecond) {
		if release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock")); err == nil {
			release()
			break
		}
		if time.Now().After(end) {
			return fail("another daemon holds this home; nothing was started")
		}
	}
	if pendingID, _ := client.PendingUpdate(home); pendingID != *id {
		return nil // settled or replaced meanwhile
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return fail("cannot find the updated program: " + err.Error())
	}
	if err := checkTaskDefinition(exe, daemonArgs); err != nil {
		return fail(err.Error() + "; start the daemon yourself")
	}
	// IgnoreNew drops a run while Task Scheduler still shows the old
	// instance; so wait for it to end, and try a few times.
	for attempt := 0; attempt < 3; attempt++ {
		for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(250 * time.Millisecond) {
			if state, pids, err := taskInstances(); err == nil && (state != taskStateRunning || len(pids) == 0) {
				break
			}
		}
		runTask(30*time.Second, 64<<10, "schtasks", "/run", "/tn", winTaskName)
		for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(250 * time.Millisecond) {
			if act, ok, _ := client.ReadUpdateActivation(home); ok && act.ID == *id {
				return nil // the task's new instance settled it (running, or its own failure)
			}
		}
	}
	return fail("the scheduled task did not start the updated daemon; start it: schtasks /run /tn agentnet")
}

// processAlive reports whether process pid exists.
func processAlive(pid int) (alive, known bool) {
	if pid <= 0 {
		return false, false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false, true
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == stillActive, true
}
