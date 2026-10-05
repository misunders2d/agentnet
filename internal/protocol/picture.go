package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image/png"
)

const MaxPictureBytes = 64 << 10
const MaxPicturePixels = 256

// ValidatePicture accepts bounded square PNGs, without identifying metadata.
// Skins convert user-selected raster images through canvas before publishing.
func ValidatePicture(b []byte) error {
	if len(b) == 0 || len(b) > MaxPictureBytes {
		return errors.New("picture: choose a PNG up to 64 KB")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return errors.New("picture: invalid PNG")
	}
	if cfg.Width < 1 || cfg.Width != cfg.Height || cfg.Width > MaxPicturePixels {
		return errors.New("picture: must be square and at most 256 pixels")
	}
	// Permit only pixels and colour information, not text, EXIF or animation.
	for off := 8; ; {
		if off+12 > len(b) {
			return errors.New("picture: incomplete PNG")
		}
		n := int(binary.BigEndian.Uint32(b[off : off+4]))
		typ := string(b[off+4 : off+8])
		if n > len(b)-off-12 {
			return errors.New("picture: incomplete PNG")
		}
		switch typ {
		case "IHDR", "PLTE", "IDAT", "IEND", "tRNS", "sRGB", "gAMA", "cHRM", "pHYs":
		default:
			return errors.New("picture: remove metadata by cropping the image first")
		}
		off += n + 12
		if typ == "IEND" {
			if off != len(b) {
				return errors.New("picture: trailing data")
			}
			break
		}
	}
	if _, err = png.Decode(bytes.NewReader(b)); err != nil {
		return errors.New("picture: invalid PNG pixels")
	}
	return nil
}
func PictureHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
