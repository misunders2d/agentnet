package hub

import (
	"bytes"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/protocol"
	"image"
	"image/png"
	"net/http/httptest"
	"testing"
)

func TestPublicPictureBlobAndSignedReference(t *testing.T) {
	h, ownerID, addr := testHub(t)
	owner := member{ownerID, addr}
	prev := personOf(t, h, owner)
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 32, 32)))
	data := b.Bytes()
	hash := protocol.PictureHash(data)
	if c, raw := owner.call(t, h, "PUT", "/v1/pictures/"+hash, data); c != 204 {
		t.Fatalf("store %d %s", c, raw)
	}
	req := httptest.NewRequest("GET", "/v1/pictures/"+hash, nil)
	response := httptest.NewRecorder()
	h.routes().ServeHTTP(response, req)
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), data) || response.Header().Get("Content-Type") != "image/png" {
		t.Fatal("public blob", response.Code, response.Body.String())
	}
	next := prev
	next.Seq++
	next.Prev = prev.Hash()
	next.By = ownerID.Public(owner.addr).Fingerprint()
	next.Picture = hash
	next.Sign(ownerID.Sign)
	raw, _ := json.Marshal(next)
	if c, body := owner.call(t, h, "PUT", "/v1/person", raw); c != 204 {
		t.Fatalf("signed reference %d %s", c, body)
	}
	bad := []byte("<svg/>")
	if c, _ := owner.call(t, h, "PUT", "/v1/pictures/"+protocol.PictureHash(bad), bad); c != 400 {
		t.Fatal("accepted type", c)
	}
	if c, _ := owner.call(t, h, "PUT", "/v1/pictures/"+hash, bad); c != 400 {
		t.Fatal("accepted wrong hash", c)
	}
	next.Seq++
	next.Prev = next.Hash()
	next.Picture = protocol.PictureHash([]byte("missing"))
	next.Sign(ownerID.Sign)
	raw, _ = json.Marshal(next)
	if c, _ := owner.call(t, h, "PUT", "/v1/person", raw); c != 400 {
		t.Fatal("accepted missing blob", c)
	}
}
