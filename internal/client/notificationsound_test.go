package client

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/notify"
)

func TestNotificationSoundSettingsKeepCustomAudio(t *testing.T) {
	defaultSound := &soundState{NotificationSound: NotificationSound{Source: "system", Volume: 100}}
	wav := notify.ChimeWAV(100)
	half := 50
	custom, err := defaultSound.prepare(NotificationSoundChange{Source: "custom", FileName: "local.WAV", File: wav, Volume: &half})
	if err != nil || custom.FileName != "local.WAV" || custom.Volume != 50 || !bytes.Equal(custom.file, wav) || bytes.Equal(custom.wave, wav) {
		t.Fatalf("prepare custom sound: %v", err)
	}
	system, err := custom.prepare(NotificationSoundChange{Source: "system", Volume: &half})
	if err != nil || len(system.wave) != 0 || len(system.file) == 0 {
		t.Fatalf("switch back to system or retain picked file: %v", err)
	}
	restored, err := system.prepare(NotificationSoundChange{Source: "custom", Volume: &half})
	if err != nil || !bytes.Equal(custom.wave, restored.wave) {
		t.Fatalf("reuse saved custom sound: %v", err)
	}
	for _, invalid := range []NotificationSoundChange{
		{Source: "custom", Volume: &half},
		{Source: "chime"},
		{Source: "custom", Volume: &half, FileName: `C:\private.wav`, File: wav},
		{Source: "custom", Volume: &half, FileName: "corrupt.wav", File: []byte("invalid")},
	} {
		if result, err := defaultSound.prepare(invalid); err == nil || result != nil || defaultSound.Source != "system" || defaultSound.file != nil {
			t.Fatal("invalid input replaced the current sound")
		}
	}
}

func TestNotificationSoundStoreRestoresPreferencesAndRejectsStaleSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.db.Close() })
	ax := &Agent{store: st, Logf: t.Logf}
	// Run the settings/store logic on any test OS without requesting audio.
	initial := ax.readNotificationSound()
	if initial.Source != "system" || initial.Volume != 100 || initial.Revision != 0 || initial.RespectDND || len(initial.wave) != 0 {
		t.Fatalf("older store default: %+v", initial.NotificationSound)
	}
	ax.sound.live.Store(initial)
	half := 50
	change := NotificationSoundChange{Source: "custom", Volume: &half, FileName: "tone.wav", File: notify.ChimeWAV(100), RespectDND: true}
	saved, err := ax.ChangeNotificationSound(change, false)
	if err != nil || saved.Revision != 1 || !saved.RespectDND {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if _, err := ax.ChangeNotificationSound(change, false); err == nil {
		t.Fatal("accepted stale save")
	}
	if err := st.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.db.Close() })
	ax.store = reopened
	restarted := ax.readNotificationSound()
	if restarted.NotificationSound != saved || !bytes.Equal(restarted.wave, ax.sound.live.Load().wave) {
		t.Fatalf("restart readback: %+v", restarted.NotificationSound)
	}
}
