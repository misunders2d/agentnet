package notify

import (
	"encoding/binary"
	"errors"
	"math"
)

// MaxSoundFile limits local settings input. At most five seconds of mono or
// stereo 16-bit PCM at 48 kHz fits, with room for ordinary WAV metadata.
const MaxSoundFile = 1 << 20

var errWAV = errors.New("Choose a 16-bit PCM WAV file, mono or stereo, up to 5 seconds and 1 MiB.")

type notificationSound struct {
	wave       []byte
	respectDND bool
}

func (s notificationSound) allowed(windowsAllows func() bool) bool {
	return len(s.wave) != 0 && (!s.respectDND || windowsAllows())
}

// PrepareWAV validates the file and builds the canonical memory image that
// WinMM plays. Apply the AgentNet gain here so the Windows mixer keeps its own
// session volume and the notification path only passes prebuilt bytes.
func PrepareWAV(file []byte, volume int) ([]byte, error) {
	if volume < 0 || volume > 100 || len(file) < 12 || len(file) > MaxSoundFile ||
		string(file[:4]) != "RIFF" || string(file[8:12]) != "WAVE" ||
		binary.LittleEndian.Uint32(file[4:]) != uint32(len(file)-8) {
		return nil, errWAV
	}
	var format, pcm []byte
	for pos := 12; pos < len(file); {
		if len(file)-pos < 8 {
			return nil, errWAV
		}
		size := uint64(binary.LittleEndian.Uint32(file[pos+4:]))
		padded := size + size%2
		if padded > uint64(len(file)-pos-8) {
			return nil, errWAV
		}
		data := file[pos+8 : pos+8+int(size)]
		switch string(file[pos : pos+4]) {
		case "fmt ":
			if format != nil {
				return nil, errWAV
			}
			format = data
		case "data":
			if pcm != nil {
				return nil, errWAV
			}
			pcm = data
		}
		pos += 8 + int(padded)
	}
	le := binary.LittleEndian
	if (len(format) != 16 && (len(format) != 18 || le.Uint16(format[16:]) != 0)) ||
		le.Uint16(format) != 1 || le.Uint16(format[14:]) != 16 {
		return nil, errWAV
	}
	channels, rate := int(le.Uint16(format[2:])), int(le.Uint32(format[4:]))
	frameSize := channels * 2
	if channels < 1 || channels > 2 || rate < 8000 || rate > 48000 ||
		int(le.Uint16(format[12:])) != frameSize || int(le.Uint32(format[8:])) != rate*frameSize ||
		len(pcm) == 0 || len(pcm)%frameSize != 0 || len(pcm) > rate*frameSize*5 {
		return nil, errWAV
	}
	wave := pcmWAV(rate, channels, len(pcm))
	for pos := 0; pos < len(pcm); pos += 2 {
		sample := int(int16(le.Uint16(pcm[pos:]))) * volume / 100
		le.PutUint16(wave[44+pos:], uint16(int16(sample)))
	}
	return wave, nil
}

func pcmWAV(rate, channels, size int) []byte {
	wave := make([]byte, 44+size)
	copy(wave, "RIFF\x00\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00")
	copy(wave[36:], "data")
	le := binary.LittleEndian
	le.PutUint32(wave[4:], uint32(size+36))
	le.PutUint16(wave[22:], uint16(channels))
	le.PutUint32(wave[24:], uint32(rate))
	le.PutUint32(wave[28:], uint32(rate*channels*2))
	le.PutUint16(wave[32:], uint16(channels*2))
	le.PutUint16(wave[34:], 16)
	le.PutUint32(wave[40:], uint32(size))
	return wave
}

// ChimeWAV makes AgentNet's short built-in two-note chime when selected.
func ChimeWAV(volume int) []byte {
	const rate = 24000
	const noteFrames = rate * 18 / 100
	wave := pcmWAV(rate, 1, noteFrames*4)
	for note, frequency := range [2]float64{659.25, 880} {
		for frame := range noteFrames {
			t := float64(frame) / rate
			envelope := min(t/.012, 1) * math.Pow(1-float64(frame)/noteFrames, 2)
			sample := int16(math.Sin(2*math.Pi*frequency*t) * envelope * float64(volume) * 180)
			binary.LittleEndian.PutUint16(wave[44+(note*noteFrames+frame)*2:], uint16(sample))
		}
	}
	return wave
}
