package client

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/notify"
)

const (
	soundPrefsKey = "notification-sound"
	soundFileKey  = "notification-sound-wav"
)

// NotificationSound is this workspace's device-local Windows sound preference.
// The saved waveform is never included in the view or sent to the Hub.
type NotificationSound struct {
	Source     string `json:"source"`
	Volume     int    `json:"volume"`
	FileName   string `json:"file_name,omitempty"`
	Revision   uint64 `json:"revision"`
	RespectDND bool   `json:"respect_dnd"`
}

type NotificationSoundChange struct {
	Source     string `json:"source"`
	Volume     *int   `json:"volume"`
	FileName   string `json:"file_name,omitempty"`
	File       []byte `json:"file,omitempty"`
	Revision   uint64 `json:"revision"`
	RespectDND bool   `json:"respect_dnd"`
}

type soundState struct {
	NotificationSound
	file []byte // canonical WAV at full volume, for a future settings change
	wave []byte // ready to play with the selected gain, immutable after publishing
}

type desktopSound struct {
	save sync.Mutex // settings writes only, never taken by a notification
	live atomic.Pointer[soundState]
}

func (a *Agent) HasNotificationSound() bool {
	return runtime.GOOS == "windows" && a.sound.live.Load() != nil
}

func (a *Agent) loadNotificationSound() {
	if runtime.GOOS == "windows" {
		if os.Getenv("AGENTNET_NOTIFY") == "off" {
			a.sound.live.Store(nil)
		} else {
			a.sound.live.Store(a.readNotificationSound())
		}
	}
}

func (a *Agent) readNotificationSound() *soundState {
	defaults := &soundState{NotificationSound: NotificationSound{Source: "system", Volume: 100}}
	stored, err := a.store.config(soundPrefsKey)
	if err != nil {
		return defaults // earlier installs have no row
	}
	var prefs NotificationSound
	if err := json.Unmarshal([]byte(stored), &prefs); err != nil {
		a.Logf("notification sound: saved settings could not be read")
		return defaults
	}
	next := NotificationSoundChange{Source: prefs.Source, Volume: &prefs.Volume, FileName: prefs.FileName, RespectDND: prefs.RespectDND}
	if prefs.FileName != "" {
		encoded, err := a.store.config(soundFileKey)
		if err != nil || len(encoded) > base64.StdEncoding.EncodedLen(notify.MaxSoundFile) {
			a.Logf("notification sound: saved WAV could not be read")
			return defaults
		}
		next.File, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(next.File) == 0 {
			a.Logf("notification sound: saved WAV could not be decoded")
			return defaults
		}
	}
	saved, err := defaults.prepare(next)
	if err != nil {
		a.Logf("notification sound: saved WAV or preference is invalid")
		return defaults
	}
	saved.Revision = prefs.Revision
	return saved
}

func (a *Agent) NotificationSound() NotificationSound {
	if s := a.sound.live.Load(); s != nil {
		return s.NotificationSound
	}
	return NotificationSound{}
}

func (a *Agent) ChangeNotificationSound(change NotificationSoundChange, preview bool) (NotificationSound, error) {
	if !preview {
		a.sound.save.Lock()
		defer a.sound.save.Unlock()
	}
	current := a.sound.live.Load()
	if current == nil {
		return NotificationSound{}, errors.New("Windows notification sound is unavailable on this host.")
	}
	if current.Revision != change.Revision {
		return NotificationSound{}, errors.New("Sound settings changed in another window. Reopen Notifications and try again.")
	}
	next, err := current.prepare(change)
	if err != nil {
		return NotificationSound{}, err
	}
	if preview {
		if len(next.wave) == 0 {
			return NotificationSound{}, errors.New("Choose an AgentNet sound to preview it.")
		}
		return current.NotificationSound, notify.PreviewSound(next.wave)
	}
	next.Revision++
	meta, err := json.Marshal(next.NotificationSound)
	if err != nil {
		return NotificationSound{}, err
	}
	settings := map[string]string{soundPrefsKey: string(meta)}
	if len(change.File) != 0 {
		settings[soundFileKey] = base64.StdEncoding.EncodeToString(next.file)
	}
	if err := a.store.setConfig(settings); err != nil {
		return NotificationSound{}, errors.New("Could not save notification sound. Try again.")
	}
	a.sound.live.Store(next)
	return next.NotificationSound, nil
}

func (s *soundState) prepare(change NotificationSoundChange) (*soundState, error) {
	if change.Volume == nil || *change.Volume < 0 || *change.Volume > 100 {
		return nil, errors.New("Choose a notification volume between 0 and 100.")
	}
	next := *s
	next.Source, next.Volume, next.RespectDND = change.Source, *change.Volume, change.RespectDND
	if change.FileName != "" || len(change.File) != 0 {
		name := change.FileName
		if len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, `/\:`) ||
			strings.ContainsFunc(name, unicode.IsControl) || !strings.HasSuffix(strings.ToLower(name), ".wav") {
			return nil, errors.New("Choose a WAV file with a valid file name.")
		}
		file, err := notify.PrepareWAV(change.File, 100)
		if err != nil {
			return nil, err
		}
		next.FileName, next.file = name, file
	}
	switch next.Source {
	case "system":
		next.wave = nil
	case "chime":
		next.wave = notify.ChimeWAV(next.Volume)
	case "custom":
		if len(next.file) == 0 {
			return nil, errors.New("Choose a WAV file first.")
		}
		next.wave = next.file
		if next.Volume != 100 {
			wave, err := notify.PrepareWAV(next.file, next.Volume)
			if err != nil {
				return nil, err
			}
			next.wave = wave
		}
	default:
		return nil, errors.New("Choose Windows default, AgentNet Chime, or Custom WAV.")
	}
	return &next, nil
}
