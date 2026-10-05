package client

import (
	"bytes"
	"errors"
	"github.com/misunders2d/agentnet/internal/protocol"
	"image"
	"image/png"
	"net/http"
	"testing"
)

func TestPersonPictureAllDevicesMembersCacheAndRemoval(t *testing.T) {
	w, _, _ := dmWithHistory(t)
	before, _, _ := w.alice.Person()
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 32, 32)))
	data := b.Bytes()
	changed, err := w.alice.SetPersonPicture(tctx(t), data)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Person != before.Person || changed.Picture != protocol.PictureHash(data) {
		t.Fatal("wrong person picture")
	}
	phone := linked(t, w.alice) // enrollment after the picture must preserve it
	eventually(t, "all devices and member see signed picture", func() bool {
		p, _, _ := phone.Person()
		remote, ok, _ := w.bob.store.personByID(before.Person)
		return p.Picture == changed.Picture && ok && remote.info.Picture == changed.Picture
	})
	for _, a := range []*Agent{phone, w.bob} {
		got, err := a.PersonPicture(tctx(t), changed.Picture)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("fetch", err)
		}
	}
	old := phone.hub.http.Transport
	phone.hub.http.Transport = personLabelTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	got, err := phone.PersonPicture(tctx(t), changed.Picture)
	phone.hub.http.Transport = old
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("offline cache", err)
	}
	renamed, err := w.alice.RenamePerson(tctx(t), "New name")
	if err != nil || renamed.Picture != changed.Picture {
		t.Fatal("rename cleared picture", err)
	}
	if _, err := w.alice.SetPersonPicture(tctx(t), []byte("<svg/>")); err == nil {
		t.Fatal("invalid upload accepted")
	}
	removed, err := phone.SetPersonPicture(tctx(t), nil)
	if err != nil || removed.Picture != "" {
		t.Fatal("remove", err)
	}
	eventually(t, "removal on every device/member", func() bool {
		p, _, _ := w.alice.Person()
		remote, ok, _ := w.bob.store.personByID(before.Person)
		return p.Picture == "" && ok && remote.info.Picture == ""
	})
}
