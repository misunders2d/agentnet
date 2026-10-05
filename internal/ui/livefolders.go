package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Folders implements Folders: the folder picker of Connect an agent, on the
// computer the daemon runs on. It only lists; nothing is created.
func (l *Live) Folders(path string) (FoldersView, error) { return listFolders(path) }

// listFolders lists path's subfolders (the person's home when path is
// empty), hidden ones left out, at most MaxFolders, sorted by name.
func listFolders(path string) (FoldersView, error) {
	home, _ := os.UserHomeDir()
	if path == "" {
		path = home
	}
	if !filepath.IsAbs(path) {
		return FoldersView{}, Refuse("Choose a folder from the list.")
	}
	path = filepath.Clean(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		return FoldersView{}, NotFound("That folder cannot be opened here.")
	}
	v := FoldersView{Path: path, Home: home, Roots: driveRoots(), Dirs: []FolderView{}}
	if parent := filepath.Dir(path); parent != path {
		v.Parent = parent
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		if !e.IsDir() {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if st, err := os.Stat(full); err != nil || !st.IsDir() { // a link to a folder is a folder
				continue
			}
		}
		if len(v.Dirs) == MaxFolders {
			v.Truncated = true
			break
		}
		v.Dirs = append(v.Dirs, FolderView{Name: name, Path: full})
	}
	sort.Slice(v.Dirs, func(i, j int) bool { return strings.ToLower(v.Dirs[i].Name) < strings.ToLower(v.Dirs[j].Name) })
	return v, nil
}

// driveRoots are the drives on Windows (C:\ …); nil elsewhere.
func driveRoots() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if _, err := os.Stat(root); err == nil {
			out = append(out, root)
		}
	}
	return out
}
