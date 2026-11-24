package notify

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"
)

func TestNotificationSoundQueriesDNDOnlyWhenOptedIn(t *testing.T) {
	open := false
	queries := 0
	query := func() bool { queries++; return open }
	wave := pcmWAV(8000, 1, 2)
	if !(notificationSound{wave: wave}).allowed(query) || (notificationSound{respectDND: true}).allowed(query) || queries != 0 {
		t.Fatal("default or system sound queried DND")
	}
	optedIn := notificationSound{wave: wave, respectDND: true}
	if optedIn.allowed(query) || queries != 1 {
		t.Fatal("quiet or unknown Windows state played an opted-in sound")
	}
	open = true
	if !optedIn.allowed(query) || queries != 2 {
		t.Fatal("clear Windows state did not allow an opted-in sound")
	}
}

func TestPrepareWAVNormalizesAndAppliesGain(t *testing.T) {
	le := binary.LittleEndian
	input := pcmWAV(44100, 2, 4)
	le.PutUint16(input[44:], 32767)
	le.PutUint16(input[46:], 32768) // -32768
	// Odd metadata length with RIFF padding, dropped from the playback image.
	withMetadata := slices.Concat(input[:36], []byte("JUNK\x01\x00\x00\x00x\x00"), input[36:])
	le.PutUint32(withMetadata[4:], uint32(len(withMetadata)-8))
	for _, tt := range []struct {
		volume      int
		left, right int16
	}{
		{0, 0, 0}, {50, 16383, -16384}, {100, 32767, -32768},
	} {
		wave, err := PrepareWAV(withMetadata, tt.volume)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(wave[:44], input[:44]) || int16(le.Uint16(wave[44:])) != tt.left || int16(le.Uint16(wave[46:])) != tt.right {
			t.Fatalf("volume %d produced invalid PCM: %x", tt.volume, wave)
		}
	}
	if !bytes.Equal(withMetadata[46:], input[36:]) {
		t.Fatal("gain changed the source bytes")
	}
	chime := ChimeWAV(100)
	if reencoded, err := PrepareWAV(chime, 100); err != nil || !bytes.Equal(chime, reencoded) {
		t.Fatalf("built-in sound is not canonical WAV: %v", err)
	}
}

func TestPrepareWAVRejectsMalformedAndOutOfBoundsFiles(t *testing.T) {
	valid := pcmWAV(8000, 1, 2)
	badFormat := slices.Clone(valid)
	badFormat[20] = 3 // IEEE float
	badChunk := slices.Clone(valid)
	binary.LittleEndian.PutUint32(badChunk[40:], 0xffff_ffff)
	duplicate := slices.Concat(valid, valid[36:])
	binary.LittleEndian.PutUint32(duplicate[4:], uint32(len(duplicate)-8))
	for name, input := range map[string][]byte{
		"empty": nil, "mp3": []byte("ID3"), "truncated": valid[:len(valid)-1], "float": badFormat,
		"chunk overflow": badChunk, "duplicate data": duplicate, "no frames": pcmWAV(8000, 1, 0),
		"partial frame": pcmWAV(8000, 2, 2), "rate": pcmWAV(96000, 1, 2),
		"too long": pcmWAV(8000, 1, 8000*2*5+2), "too large": make([]byte, MaxSoundFile+1),
	} {
		t.Run(name, func(t *testing.T) {
			if wave, err := PrepareWAV(input, 100); err == nil || wave != nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}
