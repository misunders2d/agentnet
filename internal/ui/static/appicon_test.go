package static

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

// TestWriteAppIcon writes the app icon at 1024x1024 to the file named by
// AGENTNET_APP_ICON_OUT, the source of the desktop app's icons
// (desktop/build.sh icons runs `tauri icon` on it). Without the variable it
// only checks the large icon is a square PNG.
func TestWriteAppIcon(t *testing.T) {
	b := AppIcon(1024)
	m, err := png.Decode(bytes.NewReader(b))
	if err != nil || m.Bounds().Dx() != 1024 || m.Bounds().Dy() != 1024 {
		t.Fatalf("1024 icon: %v %v", m.Bounds(), err)
	}
	if out := os.Getenv("AGENTNET_APP_ICON_OUT"); out != "" {
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
