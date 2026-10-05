package static

import (
	"encoding/json"
	"regexp"
	"sync"
)

// getapp.json is the one table of the AgentNet app's downloads: the page
// (getapp.mjs) and Go read the same file, and desktop/build.sh names its
// outputs after it.

// Download is one installer of the AgentNet app.
type Download struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Asset string `json:"asset"`
	URL   string `json:"url"`
}

type getAppTable struct {
	Releases  string     `json:"releases"`
	Platforms []Download `json:"platforms"`
}

var getApp = sync.OnceValue(func() getAppTable {
	data, err := files.ReadFile("getapp.json")
	if err != nil {
		panic(err) // a build error
	}
	var t getAppTable
	if err := json.Unmarshal(data, &t); err != nil || t.Releases == "" || len(t.Platforms) == 0 {
		panic("static: getapp.json is malformed") // a build error
	}
	return t
})

var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// Releases is the page listing the app's releases.
func Releases() string { return getApp().Releases }

// Downloads lists the app's installers for version: that release's files
// for a release version (vX.Y.Z), otherwise the latest release's.
func Downloads(version string) []Download {
	t := getApp()
	base := t.Releases + "/latest/download/"
	if releaseTag.MatchString(version) {
		base = t.Releases + "/download/" + version + "/"
	}
	out := make([]Download, len(t.Platforms))
	for i, p := range t.Platforms {
		p.URL = base + p.Asset
		out[i] = p
	}
	return out
}
