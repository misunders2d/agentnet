package main

import (
	"os"
	"path/filepath"
	"strings"
)

// mergePath returns current with the entries of login it lacks, then the
// existing directories among extra it still lacks, put in front, without
// duplicates: a program started from the app launcher gets a bare PATH,
// while the person's coding agents are on the PATH of their login shell or
// in well-known per-user directories.
func mergePath(current, login string, extra []string) string {
	seen := map[string]bool{}
	var out []string
	add := func(dir string, mustExist bool) {
		if dir == "" || !filepath.IsAbs(dir) {
			return
		}
		key := filepath.Clean(dir)
		if seen[key] {
			return
		}
		if mustExist {
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				return
			}
		}
		seen[key] = true
		out = append(out, dir)
	}
	have := map[string]bool{}
	for _, d := range filepath.SplitList(current) {
		have[filepath.Clean(d)] = true
	}
	for _, d := range filepath.SplitList(login) {
		if !have[filepath.Clean(d)] {
			add(d, false)
		}
	}
	for _, d := range extra {
		if !have[filepath.Clean(d)] {
			add(d, true)
		}
	}
	for _, d := range filepath.SplitList(current) {
		if d == "" {
			continue
		}
		if key := filepath.Clean(d); !seen[key] {
			seen[key] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}

// userBinDirs are per-user directories where coding agents are commonly
// installed, looked at when the login shell does not name them.
func userBinDirs(home string) []string {
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".cargo", "bin"),
		filepath.Join(home, ".local", "share", "mise", "shims"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
}
