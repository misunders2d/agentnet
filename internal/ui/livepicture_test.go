package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestPersonPictureThroughGuardedHandler(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	a, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	before, err := a.CreatePerson(ctx, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	seed := uint32(1)
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			next := func() uint8 { seed ^= seed << 13; seed ^= seed >> 17; seed ^= seed << 5; return uint8(seed) }
			img.Set(x, y, color.NRGBA{next(), next(), next(), 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	data := b.Bytes()
	raw, _ := json.Marshal(map[string][]byte{"png": data})
	if len(data) > protocol.MaxPictureBytes || len(raw) <= maxBody {
		t.Fatalf("fixture must exceed old JSON bound: PNG %d JSON %d", len(data), len(raw))
	}
	h := New(NewLive(a), "127.0.0.1:8123", testToken).Handler()
	post := func(body []byte, origin string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8123/api/person/picture", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if auth {
			r.Header.Set("Cookie", cookieName+"="+testToken)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		body   []byte
		origin string
		auth   bool
		status int
	}{{raw, "http://127.0.0.1:8123", false, 401}, {raw, "http://foreign.test", true, 403}, {[]byte(`{"png":"","person":"other"}`), "http://127.0.0.1:8123", true, 400}, {[]byte(`{"png":""} {}`), "http://127.0.0.1:8123", true, 400}} {
		if w := post(tc.body, tc.origin, tc.auth); w.Code != tc.status {
			t.Fatalf("guard %d %s", w.Code, w.Body)
		}
	}
	w := post(raw, "http://127.0.0.1:8123", true)
	var after client.PersonInfo
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &after) != nil {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	if after.Picture != protocol.PictureHash(data) || after.Person != before.Person || after.Address != before.Address || after.Fingerprint != before.Fingerprint {
		t.Fatal("picture changed identity", after)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:8123/api/picture/"+after.Picture, nil)
	r.Header.Set("Cookie", cookieName+"="+testToken)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("checked picture fetch", w.Code)
	}
	o, err := NewLive(a).Overview()
	if err != nil || o.Person.Picture != after.Picture || o.Person.PictureURL != pictureURL(after.Picture) {
		t.Fatal("host view", err, o.Person)
	}
}
