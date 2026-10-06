package ui

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Synthetic local provider. No accounts, real agents or Hub operations.
type pictureFixture struct {
	*Fixture
	lock    sync.Mutex
	picture string
	blobs   map[string][]byte
	sample  []byte
}

func newPictureFixture() *pictureFixture {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.NRGBA{uint8(x * 4), uint8(y * 4), 180, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	data := b.Bytes()
	hash := protocol.PictureHash(data)
	return &pictureFixture{Fixture: NewFixture(time.Now), blobs: map[string][]byte{hash: data}, sample: data}
}
func (f *pictureFixture) Overview() (Overview, error) {
	o, err := f.Fixture.Overview()
	f.lock.Lock()
	defer f.lock.Unlock()
	o.Persons = true
	o.Role = RolePerson
	o.Files = &FileLimits{MaxFile: 20 << 20, MaxCount: 10}
	o.Me.Address = "alice/laptop"
	o.Person = &PersonView{Person: "alice", Label: "Alice", Address: o.Me.Address, State: "self", Published: true, Picture: f.picture, PictureURL: pictureURL(f.picture), Devices: []DeviceView{{Address: o.Me.Address, Name: "laptop", This: true}}}
	hash := protocol.PictureHash(f.sample)
	peer := PersonView{Person: "bob", Label: "Bob", Address: "bob/desk", State: "pinned", Picture: hash, PictureURL: pictureURL(hash)}
	o.People = []PersonView{*o.Person, peer}
	o.Threads = []ThreadSummary{}
	o.Review = []ReviewItem{}
	o.NeedsYou = nil
	o.Held = nil
	o.DMs = []DMSummary{{ID: "picture-chat", Peer: peer, Title: "Picture chat", Created: f.now(), LastAt: f.now(), Count: 1, Last: "Here is your picture"}}
	return o, err
}
func (f *pictureFixture) DM(id string) (DMThread, error) {
	o, _ := f.Overview()
	return DMThread{ID: id, Peer: o.DMs[0].Peer, Created: f.now(), Mine: true, Messages: []DMMessage{{ID: "picture-message", LID: "picture-message", Dir: "in", From: "bob/desk", Kind: "message", Body: "Here is your picture", At: f.now(), State: "received", Attachments: []FileView{{Name: "avatar.png", Size: int64(len(f.sample)), Openable: true}}}}}, nil
}
func (f *pictureFixture) SetPersonPicture(_ context.Context, b []byte) (client.PersonInfo, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	hash := ""
	if len(b) > 0 {
		if err := protocol.ValidatePicture(b); err != nil {
			return client.PersonInfo{}, err
		}
		hash = protocol.PictureHash(b)
		f.blobs[hash] = append([]byte{}, b...)
	}
	f.picture = hash
	return client.PersonInfo{Person: "alice", Label: "Alice", Picture: hash}, nil
}
func (f *pictureFixture) PersonPicture(_ context.Context, hash string) ([]byte, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	b, ok := f.blobs[hash]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte{}, b...), nil
}
func (f *pictureFixture) StageFile(string, io.Reader) (string, error) { return "", ErrRefused }
func (f *pictureFixture) DiscardFiles([]string)                       {}
func (f *pictureFixture) OpenFile(context.Context, string, string, int) (io.ReadCloser, string, error) {
	return io.NopCloser(bytes.NewReader(f.sample)), "avatar.png", nil
}
func (f *pictureFixture) RequestFile(context.Context, string, int) error { return nil }

func TestPersonPictureRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("Claude owns opt-in rendered check")
	}
	f := newPictureFixture()
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	defer ts.Close()
	cmd := exec.Command("node", "testdata/picture_rendered_check.cjs", ts.URL+"/?t="+testToken)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "P22 rendered PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
