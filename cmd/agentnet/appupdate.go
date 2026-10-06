package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

const appUpdateHelperCmd = "app-update-helper"
const appUpdateResultFile = "app-update-result"

type appUpdatePlan struct {
	App     string `json:"app"`
	Asset   string `json:"asset"`
	Kind    string `json:"kind"`
	Sum     string `json:"sum"`
	Version string `json:"version"`
	Backup  string `json:"backup,omitempty"`
}

func releaseChecksums(ctx context.Context, tag string) (map[string]string, error) {
	var b bytes.Buffer
	if _, ok := parseRelease(tag); !ok {
		return nil, errors.New("invalid release")
	}
	if err := fetch(ctx, releaseBase+"/download/"+tag+"/SHA256SUMS", maxSums, &b); err != nil {
		return nil, err
	}
	out := map[string]string{}
	sc := bufio.NewScanner(&b)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		sum := strings.ToLower(f[0])
		decoded, err := hex.DecodeString(sum)
		if err != nil || len(decoded) != 32 {
			continue
		}
		if _, exists := out[name]; exists {
			return nil, errors.New("duplicate release checksum")
		}
		out[name] = sum
	}
	return out, sc.Err()
}

func appUpdateAsset(goos, app string) (string, string, error) {
	switch goos {
	case "linux":
		if strings.HasSuffix(strings.ToLower(app), ".appimage") {
			return "AgentNet-linux-x86_64.AppImage", "appimage", nil
		}
		switch linuxPackageKind(app) {
		case "deb":
			return "AgentNet-linux-amd64.deb", "deb", nil
		case "rpm":
			return "AgentNet-linux-x86_64.rpm", "rpm", nil
		}
		return "", "", errors.New("This app is not from an installed AgentNet package. Install the AgentNet app to update it.")
	case "windows":
		return "AgentNet-windows-x64-setup.exe", "nsis", nil
	case "darwin":
		if strings.Contains(app, "/AppTranslocation/") {
			return "", "", errors.New("Move AgentNet into Applications first, then update again.")
		}
		if strings.Contains(app, ".app/Contents/MacOS/") && !strings.Contains(app, "/AppTranslocation/") {
			return "AgentNet-macos-universal.dmg", "dmg", nil
		}
	}
	return "", "", errors.New("Open the installed AgentNet app to update it.")
}

// Downloads are complete and checked before the shell is asked to quit.
func (r *appRunner) stageAppUpdate(ctx context.Context) (string, string, error) {
	return r.stageAppUpdateTo(ctx, "")
}

func (r *appRunner) stageAppUpdateTo(ctx context.Context, tag string) (string, string, error) {
	name, kind, err := appUpdateAsset(runtime.GOOS, r.exe)
	if err != nil {
		return "", "", err
	}
	if tag == "" {
		tag, err = latestRelease(ctx)
		if err != nil {
			return "", "", err
		}
	}
	if _, ok := parseRelease(tag); !ok {
		return "", "", errors.New("invalid release")
	}
	current, known := parseRelease(protocol.Version)
	if base, ok := devBase(protocol.Version); ok {
		current, known = base, true
	}
	if !known {
		return "", "", errors.New("This development app cannot be updated safely. Install an official AgentNet app first.")
	}
	target, _ := parseRelease(tag)
	if !olderRelease(current, target) {
		return "", "", errors.New("AgentNet is already up to date.")
	}
	release, err := lockfile.Acquire(filepath.Join(r.home, "app-update.lock"))
	if err != nil {
		return "", "", errors.New("An update is already being prepared.")
	}
	defer release()
	dir, err := os.MkdirTemp(r.home, "app-update-")
	if err != nil {
		return "", "", err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(dir)
		}
	}()
	sums, err := releaseChecksums(ctx, tag)
	if err != nil {
		return "", "", err
	}
	want := sums[name]
	if want == "" {
		return "", "", errors.New("This release has no verified app for this computer.")
	}
	asset := filepath.Join(dir, name)
	f, err := os.OpenFile(asset, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	err = fetch(ctx, releaseBase+"/download/"+tag+"/"+name, 512<<20, io.MultiWriter(f, h))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return "", "", errors.New("The download did not pass its safety check. Your current app is unchanged.")
	}
	// Keep an independent helper: a Windows installer or AppImage replacement
	// removes the old bundle that contains this currently running program.
	src, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	helper, err := stableCopy(src, dir)
	if err != nil {
		return "", "", err
	}
	plan := appUpdatePlan{App: r.exe, Asset: asset, Kind: kind, Sum: want, Version: tag}
	if kind == "nsis" {
		plan.Backup = filepath.Join(dir, "previous-app")
		if err = copyAppTree(filepath.Dir(r.exe), plan.Backup); err != nil {
			return "", "", err
		}
	}
	if kind == "deb" || kind == "rpm" {
		if _, ok := parseRelease(protocol.Version); !ok {
			return "", "", errors.New("This development package cannot be updated safely. Install an official AgentNet package first.")
		}
		plan.Backup, err = stageAppAsset(ctx, dir, protocol.Version, name+".previous", name)
		if err != nil {
			return "", "", errors.New("Could not prepare your previous package for recovery. Your current app is unchanged.")
		}
		if err = installLinuxPackage(plan); err != nil {
			return "", "", err
		}
		plan.Kind = "installed-package"
	}
	b, _ := json.Marshal(plan)
	path := filepath.Join(dir, "plan.json")
	if err = secfile.Write(path, b); err != nil {
		return "", "", err
	}
	keep = true
	return helper, path, nil
}

func (r *appRunner) appAPI(w http.ResponseWriter, req *http.Request) bool {
	if !strings.HasPrefix(req.URL.Path, "/api/app/") {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	cookie, err := req.Cookie("agentnet_ui")
	if req.Host != r.addr || err != nil || r.token == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(r.token)) != 1 {
		http.Error(w, "Open AgentNet from your apps.", http.StatusUnauthorized)
		return true
	}
	reply := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	if req.Method == http.MethodGet && req.URL.Path == "/api/app/status" {
		r.commandMu.Lock()
		status := r.command
		r.commandMu.Unlock()
		_, _, updateErr := appUpdateAsset(runtime.GOOS, r.exe)
		problem := ""
		if updateErr != nil {
			problem = updateErr.Error()
		}
		result, _ := secfile.Read(filepath.Join(r.home, appUpdateResultFile))
		reply(struct {
			appCommandStatus
			Version   string `json:"version"`
			Supported bool   `json:"app_update_supported"`
			Problem   string `json:"problem,omitempty"`
			Result    string `json:"update_result,omitempty"`
		}{status, protocol.Version, updateErr == nil, problem, string(result)})
		return true
	}
	mt, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if req.Method != http.MethodPost || req.Header.Get("Origin") != "http://"+r.addr || mt != "application/json" || (req.Header.Get("Sec-Fetch-Site") != "" && req.Header.Get("Sec-Fetch-Site") != "same-origin") {
		http.Error(w, "This action must come from the app.", http.StatusForbidden)
		return true
	}
	switch req.URL.Path {
	case "/api/app/cli":
		var choice struct {
			Replace bool `json:"replace"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, req.Body, 1024)).Decode(&choice) != nil || !choice.Replace {
			http.Error(w, "Choose Replace command first.", 400)
			return true
		}
		r.commandMu.Lock()
		r.command = r.installCommand(true)
		status := r.command
		r.commandMu.Unlock()
		reply(status)
	case "/api/app/update":
		var choice struct {
			Version string `json:"version"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, req.Body, 1024)).Decode(&choice) != nil {
			http.Error(w, "Invalid update request.", 400)
			return true
		}
		if !r.updating.CompareAndSwap(false, true) {
			http.Error(w, "An update is already being prepared.", 409)
			return true
		}
		resume, err := r.pauseForAppUpdate()
		if err != nil {
			r.updating.Store(false)
			http.Error(w, err.Error(), 409)
			return true
		}
		keepPaused := false
		defer func() {
			if !keepPaused {
				resume()
			}
		}()
		helper, plan, err := r.stageAppUpdateTo(req.Context(), choice.Version)
		if err != nil {
			r.updating.Store(false)
			http.Error(w, err.Error(), 400)
			return true
		}
		if err = r.handoffAppUpdate(req.Context(), helper, plan); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return true
		}
		keepPaused = true
		reply(map[string]string{"state": "restarting", "message": "Restarting AgentNet with the update…"})
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	default:
		http.NotFound(w, req)
	}
	return true
}

// Shell holds stdin open until its old program has stopped. No production
// service is touched: restarting the app restarts the daemon it owns.
func runAppUpdateHelper(home string, args []string, stdin io.Reader) error {
	if len(args) != 1 {
		return errors.New("invalid app update handoff")
	}
	b, err := secfile.Read(args[0])
	if err != nil {
		return err
	}
	var p appUpdatePlan
	if err = json.Unmarshal(b, &p); err != nil {
		return err
	}
	dir := filepath.Dir(args[0])
	if !filepath.IsAbs(dir) || filepath.Dir(dir) != filepath.Clean(home) || !strings.HasPrefix(filepath.Base(dir), "app-update-") || filepath.Dir(p.Asset) != dir {
		return errors.New("invalid app update location")
	}
	_, err = io.Copy(io.Discard, stdin)
	if err != nil {
		return err
	}
	err = applyAppUpdate(p)
	if err != nil {
		secfile.Write(filepath.Join(home, appUpdateResultFile), []byte("Update failed. Your previous app is still available: "+err.Error()))
	} else {
		secfile.Write(filepath.Join(home, appUpdateResultFile), []byte("Updated to "+p.Version))
	}
	// Clear AppImage mount variables before opening either version.
	cmd := exec.Command(p.App)
	if p.Kind == "dmg" {
		cmd = exec.Command("open", macAppBundle(p.App))
	}
	cmd.Env = appRestartEnv(os.Environ())
	startErr := cmd.Start()
	if startErr == nil {
		cmd.Process.Release()
	}
	if runtime.GOOS != "windows" && err == nil {
		os.RemoveAll(dir)
	} // Windows cannot remove the running helper.
	if err != nil {
		return err
	}
	return startErr
}

func appRestartEnv(env []string) []string {
	out := []string{}
	mount := os.Getenv("APPDIR")
	for _, v := range env {
		key := strings.SplitN(v, "=", 2)[0]
		if key == "APPIMAGE" || key == "APPDIR" || key == "AGENTNET_APP" || key == "AGENTNET_APP_EXE" || (mount != "" && strings.Contains(v, mount)) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func macAppBundle(app string) string {
	i := strings.Index(app, ".app/Contents/MacOS/")
	if i < 0 {
		return ""
	}
	return app[:i+4]
}

func applyAppUpdate(p appUpdatePlan) error {
	sum, err := fileSum(p.Asset)
	if err != nil {
		return err
	}
	if hex.EncodeToString(sum) != p.Sum {
		return errors.New("download changed after verification")
	}
	switch p.Kind {
	case "appimage":
		// Copy beside the installed file, so replacement is atomic across disks.
		b, err := os.ReadFile(p.Asset)
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(p.App), ".agentnet-app-update-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		_, err = tmp.Write(b)
		if err == nil {
			err = tmp.Chmod(0755)
		}
		if err == nil {
			err = tmp.Sync()
		}
		closeErr := tmp.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		return replaceExecutable(p.App, tmp.Name())
	case "nsis":
		return installWindowsApp(p)
	case "installed-package":
		return nil
	case "dmg":
		return installMacApp(p)
	}
	return fmt.Errorf("unsupported app update kind %q", p.Kind)
}

func installMacApp(p appUpdatePlan) error {
	bundle := macAppBundle(p.App)
	if bundle == "" {
		return errors.New("cannot find the installed Mac app")
	}
	// Translocated unsigned apps cannot safely be replaced in their read-only
	// mount. Keep the running app rather than guessing its original location.
	if strings.Contains(bundle, "/AppTranslocation/") {
		return errors.New("Move AgentNet into Applications first, then update again.")
	}
	mount, err := os.MkdirTemp(filepath.Dir(p.Asset), "mount-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mount)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err = exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-mountpoint", mount, p.Asset).Run(); err != nil {
		return err
	}
	defer exec.Command("hdiutil", "detach", mount).Run()
	staged := bundle + ".new"
	if _, err = os.Lstat(staged); !errors.Is(err, os.ErrNotExist) {
		return errors.New("an earlier Mac update needs recovery")
	}
	if err = exec.CommandContext(ctx, "ditto", filepath.Join(mount, "AgentNet.app"), staged).Run(); err != nil {
		os.RemoveAll(staged)
		return err
	}
	defer os.RemoveAll(staged)
	if p.Version != "" {
		line, versionErr := fileVersion(ctx, filepath.Join(staged, "Contents", "MacOS", "agentnet"))
		if versionErr != nil || !strings.HasPrefix(line, "agentnet "+p.Version+" (protocol ") {
			return errors.New("The downloaded app does not contain the new AgentNet program.")
		}
	}
	old := bundle + ".old"
	if _, err = os.Lstat(old); err == nil {
		if _, markerErr := os.Stat(filepath.Join(old, ".agentnet-update-backup")); markerErr != nil {
			return errors.New("A previous app backup needs to be kept safe before updating.")
		}
		if err = os.RemoveAll(old); err != nil {
			return err
		}
	}
	if err = os.Rename(bundle, old); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(old, ".agentnet-update-backup"), []byte("AgentNet app update backup\n"), 0600)
	if err = os.Rename(staged, bundle); err != nil {
		if restore := os.Rename(old, bundle); restore != nil {
			return fmt.Errorf("install failed: %v; previous app saved at %s", err, old)
		}
		return err
	}
	return nil
}

func stageAppAsset(ctx context.Context, dir, tag, filename, asset string) (string, error) {
	sums, err := releaseChecksums(ctx, tag)
	if err != nil {
		return "", err
	}
	want := sums[asset]
	if want == "" {
		return "", errors.New("release has no package checksum")
	}
	path := filepath.Join(dir, filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	err = fetch(ctx, releaseBase+"/download/"+tag+"/"+asset, 512<<20, io.MultiWriter(f, h))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		os.Remove(path)
		return "", errors.New("previous package did not pass its safety check")
	}
	return path, nil
}

func (r *appRunner) handoffAppUpdate(ctx context.Context, helper, plan string) error {
	for len(r.updateReplies) > 0 {
		<-r.updateReplies
	}
	r.emit(appEvent{Event: "update", Helper: helper, Plan: plan, Home: r.home})
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case ready := <-r.updateReplies:
		if ready {
			return nil
		}
		r.updating.Store(false)
		os.RemoveAll(filepath.Dir(plan))
		return errors.New("The update could not start. Your current app is unchanged. Try again.")
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("The app did not answer the update request. Close and reopen AgentNet before trying again.")
	}
}

func (r *appRunner) pauseForAppUpdate() (func(), error) {
	if a := r.activeAgent.Load(); a != nil {
		return a.PauseForAppUpdate()
	}
	release, err := lockfile.Acquire(filepath.Join(r.home, "daemon.lock"))
	if err != nil {
		return nil, errors.New("Another daemon owns this home. Stop it when idle, then update from the app.")
	}
	return release, nil
}
