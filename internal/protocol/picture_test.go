package protocol

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"strings"
	"testing"
)

func picturePNG(w, h int) []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, w, h)))
	return b.Bytes()
}
func TestPictureBoundsAndMetadata(t *testing.T) {
	good := picturePNG(256, 256)
	if err := ValidatePicture(good); err != nil {
		t.Fatal(err)
	}
	if len(PictureHash(good)) != 64 {
		t.Fatal("not a hash")
	}
	for name, b := range map[string][]byte{"empty": nil, "type": []byte("<svg/>"), "rectangle": picturePNG(256, 128), "large dimensions": picturePNG(257, 257), "large bytes": []byte(strings.Repeat("x", MaxPictureBytes+1)), "trailing": append(append([]byte{}, good...), 1), "truncated": good[:len(good)-1]} {
		if ValidatePicture(b) == nil {
			t.Fatal("accepted", name)
		}
	}
	chunk := make([]byte, 12+6)
	binary.BigEndian.PutUint32(chunk, 6)
	copy(chunk[4:], "tEXt")
	copy(chunk[8:], "a\x00name")
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	withMetadata := append(append(append([]byte{}, good[:33]...), chunk...), good[33:]...)
	if ValidatePicture(withMetadata) == nil {
		t.Fatal("stored identifying metadata")
	}
}
func TestPictureSignedAndOldCanonicalUnchanged(t *testing.T) {
	prev := vecRoster()
	desk, pub := vecDesk()
	next := PersonRoster{Person: prev.Person, Label: prev.Label, Seq: 1, Prev: prev.Hash(), Devices: prev.Devices, By: pub.Fingerprint(), Picture: PictureHash(picturePNG(16, 16))}
	next.Sign(desk.Sign)
	if _, err := next.VerifyNext(prev); err != nil {
		t.Fatal(err)
	}
	forged := next
	forged.Picture = PictureHash(picturePNG(32, 32))
	if _, err := forged.VerifyNext(prev); err == nil {
		t.Fatal("unsigned picture change accepted")
	}
	invalid := next
	invalid.Picture = "bad"
	invalid.Sign(desk.Sign)
	if _, err := invalid.VerifyNext(prev); err == nil {
		t.Fatal("invalid hash accepted")
	}
	if bytes.Contains(prev.Canonical(), []byte("picture")) {
		t.Fatal("empty field changes old signatures")
	}
}
