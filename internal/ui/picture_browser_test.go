package ui

import (
	"encoding/base64"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/protocol"
	"os/exec"
	"testing"
)

func TestBrowserPictureSignedFieldCacheParity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node missing")
	}
	out, err := exec.Command(node, "testdata/picture_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var v struct {
		Checks    int
		Steps     []json.RawMessage
		Hash, PNG string
		Puts      int
	}
	if err = json.Unmarshal(out, &v); err != nil || v.Checks < 20 {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := base64.StdEncoding.DecodeString(v.PNG)
	if err != nil || protocol.ValidatePicture(b) != nil || protocol.PictureHash(b) != v.Hash {
		t.Fatal("PNG/hash differs from Go", err)
	}
	var previous protocol.PersonRoster
	seen := false
	for i, raw := range v.Steps {
		r, err := protocol.ParsePersonRoster(raw)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			err = r.VerifyFirst()
		} else {
			_, err = r.VerifyNext(previous)
		}
		if err != nil {
			t.Fatal("Go refuses browser picture step", err)
		}
		if r.Picture == v.Hash {
			seen = true
		}
		previous = r
	}
	if !seen || previous.Picture != "" {
		t.Fatal("set/remove missing")
	}
	t.Logf("%d browser checks, %d Go-verified roster steps", v.Checks, len(v.Steps))
}
