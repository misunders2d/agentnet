package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/secfile"
)

func ownedCommandTarget(t *testing.T, home, path string) {
	t.Helper()
	sum, e := fileSum(path)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(appCommandRecord{Path: path, Sum: hex.EncodeToString(sum)})
	if e = secfile.Write(filepath.Join(home, "app-command.json"), b); e != nil {
		t.Fatal(e)
	}
}

func TestAppCommandRegisteredPATHShadowRefreshesWithCanonical(t *testing.T) {
	home, user, bin := t.TempDir(), t.TempDir(), t.TempDir()
	name := "agentnet"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	shadow := filepath.Join(bin, name)
	os.WriteFile(shadow, []byte("previous app CLI"), 0755)
	ownedCommandTarget(t, home, shadow)
	if e := registerAppCommandTarget(context.Background(), home, shadow); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin)
	src := filepath.Join(t.TempDir(), "bundled")
	os.WriteFile(src, []byte("new app CLI"), 0755)
	status := installAppCommand(context.Background(), src, home, user, false)
	if status.State != "installed" {
		t.Fatal(status)
	}
	if s := checkAppCommand(home, src); s.State != "installed" {
		t.Fatal(s)
	}
	canonical := appCommandPath(user)
	if canonical == "" {
		canonical = src
	}
	for _, path := range []string{shadow, canonical} {
		b, e := os.ReadFile(path)
		if e != nil || string(b) != "new app CLI" {
			t.Fatalf("unreconciled %s: %q %v", path, b, e)
		}
	}
	r, e := readAppCommandRecord(home)
	if e != nil || r.Path != canonical || len(r.Targets) == 0 {
		t.Fatalf("ownership lost: %+v %v", r, e)
	}
	if e := registerAppCommandTarget(context.Background(), home, canonical); e != nil {
		t.Fatal(e)
	}
	// A later app startup must update registered and canonical targets again.
	os.WriteFile(src, []byte("next app CLI"), 0755)
	if s := installAppCommand(context.Background(), src, home, user, false); s.State != "installed" {
		t.Fatal(s)
	}
	if b, _ := os.ReadFile(shadow); string(b) != "next app CLI" {
		t.Fatal("recorded shadow did not refresh")
	}
}

func TestAppCommandChangedRegisteredTargetPreservedAndIncomplete(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "replace canonical"}[replace], func(t *testing.T) {
			testAppCommandChangedRegisteredTarget(t, replace)
		})
	}
}

func testAppCommandChangedRegisteredTarget(t *testing.T, replace bool) {
	t.Helper()
	home, user, bin := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PATH", bin)
	name := "agentnet"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	shadow := filepath.Join(bin, name)
	os.WriteFile(shadow, []byte("owned"), 0755)
	ownedCommandTarget(t, home, shadow)
	if e := registerAppCommandTarget(context.Background(), home, shadow); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(shadow, []byte("custom replacement"), 0755)
	before, _ := secfile.Read(filepath.Join(home, "app-command.json"))
	if s := checkAppCommand(home, shadow); s.State != "error" || s.Path != shadow {
		t.Fatal("read-only check accepted changed recorded bytes")
	}
	after, _ := secfile.Read(filepath.Join(home, "app-command.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("read-only check mutated ownership")
	}
	src := filepath.Join(t.TempDir(), "bundled")
	os.WriteFile(src, []byte("new app CLI"), 0755)
	s := installAppCommand(context.Background(), src, home, user, replace)
	if s.State != "error" || s.Path != shadow || strings.Contains(s.Problem, "Replace command") {
		t.Fatalf("unsafe action offered for a separate registered target: %+v", s)
	}
	if b, _ := os.ReadFile(shadow); string(b) != "custom replacement" {
		t.Fatal("custom target overwritten")
	}
	canonical := appCommandPath(user)
	if canonical == "" {
		canonical = src
	}
	if b, _ := os.ReadFile(canonical); string(b) != "new app CLI" {
		t.Fatal("canonical copy not refreshed")
	}
	if s := checkAppCommand(home, src); s.State != "error" || s.Path != shadow {
		t.Fatalf("read-only check must report the changed target: %+v", s)
	}
}

func TestAppCommandCustomPATHShadowNeverOffersCanonicalReplacement(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "replace canonical"}[replace], func(t *testing.T) {
			home, user, bin := t.TempDir(), t.TempDir(), t.TempDir()
			name := "agentnet"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			shadow := filepath.Join(bin, name)
			original := []byte("custom command; must never execute or overwrite")
			if e := os.WriteFile(shadow, original, 0755); e != nil {
				t.Fatal(e)
			}
			t.Setenv("PATH", bin)
			src := filepath.Join(t.TempDir(), "bundled")
			if e := os.WriteFile(src, []byte("new app CLI"), 0755); e != nil {
				t.Fatal(e)
			}
			s := installAppCommand(context.Background(), src, home, user, replace)
			if s.State != "error" || s.Path != shadow || !strings.Contains(s.Problem, "off PATH") || strings.Contains(s.Problem, "Replace command") {
				t.Fatalf("unsafe action offered for an earlier PATH command: %+v", s)
			}
			if b, e := os.ReadFile(shadow); e != nil || !bytes.Equal(b, original) {
				t.Fatalf("custom PATH command changed: %q %v", b, e)
			}
			if s := checkAppCommand(home, src); s.State != "error" || s.Path != shadow {
				t.Fatalf("read-only check must report the PATH shadow: %+v", s)
			}
		})
	}
}

func TestAppCommandBundledSourceOnPATHIsNotAnExtraTarget(t *testing.T) {
	home, user, bin := t.TempDir(), t.TempDir(), t.TempDir()
	name := "agentnet"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	src := filepath.Join(bin, name)
	if e := os.WriteFile(src, []byte("bundled app CLI"), 0755); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin)
	if s := installAppCommand(context.Background(), src, home, user, false); s.State != "installed" {
		t.Fatal(s)
	}
	r, e := readAppCommandRecord(home)
	if e != nil || len(r.Targets) != 0 {
		t.Fatalf("bundled source registered as a separate target: %+v %v", r, e)
	}
	primary := appCommandPath(user)
	if primary == "" {
		primary = src // Windows installed executable is the primary command.
	}
	if r.Path != primary {
		t.Fatalf("wrong primary command: %+v", r)
	}
}

func TestAppCommandAdoptsOnlyExactUnrecordedBundledPATHCopy(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact private copy", true: "changed private copy"}[changed], func(t *testing.T) {
			home, user := t.TempDir(), t.TempDir()
			name := "agentnet"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			src := filepath.Join(t.TempDir(), name)
			private := filepath.Join(home, appStableExeDir, name)
			original := []byte("current bundled app command")
			if err := os.WriteFile(src, original, 0755); err != nil {
				t.Fatal(err)
			}
			if changed {
				original = append(original, []byte(" with arbitrary changes")...)
			}
			if err := os.MkdirAll(filepath.Dir(private), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(private, original, 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", filepath.Dir(private))
			// External CLI registration still has no trusted source. Even the
			// exact copy needs a saved record or an official release checksum.
			if err := registerAppCommandTarget(context.Background(), home, private); err == nil {
				t.Fatal("requesting terminal adopted unrecorded arbitrary bytes")
			}
			status := installAppCommand(context.Background(), src, home, user, false)
			got, err := os.ReadFile(private)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("initial adoption replaced PATH bytes: %q %v", got, err)
			}
			record, err := readAppCommandRecord(home)
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				if status.State != "error" || status.Path != private || len(record.Targets) != 0 {
					t.Fatalf("changed unrecorded copy was adopted: %+v %+v", status, record)
				}
				return
			}
			if status.State != "installed" || len(record.Targets) != 1 || record.Targets[0].Path != private {
				t.Fatalf("exact bundle copy not adopted: %+v %+v", status, record)
			}
			// Its captured ownership now permits subsequent app versions to
			// refresh both the inherited private PATH and primary command.
			canonical := appCommandPath(user)
			if canonical == "" {
				canonical = src
			}
			for _, next := range []string{"next bundled app command", "next bundled app command"} {
				if err := os.WriteFile(src, []byte(next), 0755); err != nil {
					t.Fatal(err)
				}
				if status := installAppCommand(context.Background(), src, home, user, false); status.State != "installed" {
					t.Fatalf("repeated app update rejected owned private PATH: %+v", status)
				}
				for _, path := range []string{private, canonical} {
					got, err := os.ReadFile(path)
					if err != nil || string(got) != next {
						t.Fatalf("repeated update missed %s: %q %v", path, got, err)
					}
				}
			}
			if record, err := readAppCommandRecord(home); err != nil || len(record.Targets) != 1 {
				t.Fatalf("repeated adoption duplicated ownership: %+v %v", record, err)
			}
		})
	}
}

func TestAppCommandReadCheckReportsFailingRegisteredTarget(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("PATH", bin)
	src := filepath.Join(t.TempDir(), "bundled")
	if e := os.WriteFile(src, []byte("bundled app CLI"), 0755); e != nil {
		t.Fatal(e)
	}
	ownedCommandTarget(t, home, src)
	missing := filepath.Join(t.TempDir(), "missing")
	r, e := readAppCommandRecord(home)
	if e != nil {
		t.Fatal(e)
	}
	r.Targets = []appCommandTarget{{Path: missing, Sum: r.Sum}}
	b, _ := json.Marshal(r)
	if e := secfile.Write(filepath.Join(home, "app-command.json"), b); e != nil {
		t.Fatal(e)
	}
	if s := checkAppCommand(home, src); s.State != "error" || s.Path != missing {
		t.Fatalf("wrong failing command reported: %+v", s)
	}
	if s := installAppCommand(context.Background(), src, home, t.TempDir(), true); s.State != "error" || s.Path != missing {
		t.Fatalf("wrong invalid registered command reported: %+v", s)
	}
}

func TestRegisterAppCommandTargetRejectsUnknownSymlinkAndReadOnly(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "target")
	os.WriteFile(path, []byte("unknown"), 0755)
	if e := registerAppCommandTarget(context.Background(), home, path); e == nil {
		t.Fatal("unknown command adopted")
	}
	ownedCommandTarget(t, home, path)
	os.Chmod(path, 0444)
	if e := registerAppCommandTarget(context.Background(), home, path); e == nil {
		t.Fatal("read-only command adopted")
	}
	os.Chmod(path, 0755)
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "link")
		os.Symlink(path, link)
		if e := registerAppCommandTarget(context.Background(), home, link); e == nil {
			t.Fatal("symlink adopted")
		}
	}
	if e := registerAppCommandTarget(context.Background(), home, "relative"); e == nil {
		t.Fatal("relative target adopted")
	}
}
