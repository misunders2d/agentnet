package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mediaRequest(body []byte) *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1:8123/api/media/video", bytes.NewReader(body))
	r.Header.Set("Cookie", cookieName+"="+testToken)
	r.Header.Set("Origin", "http://127.0.0.1:8123")
	r.Header.Set("Content-Type", "application/octet-stream")
	r.RemoteAddr = "127.0.0.1:45678"
	return r
}

func TestMediaPreparationBoundaries(t *testing.T) {
	s := New(NewFixture(time.Now), "127.0.0.1:8123", testToken)
	h := s.Handler()
	r := mediaRequest([]byte("not a video"))
	r.Header.Del("Cookie")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	r = mediaRequest(nil)
	r.Header.Set("Origin", "https://foreign.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross origin: %d", w.Code)
	}
	r = mediaRequest(nil)
	r.RemoteAddr = "192.0.2.20:3456"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("remote media client: %d", w.Code)
	}
	_, _, available := mediaTools()
	if !available {
		t.Skip("Linux ffmpeg/ffprobe unavailable; authentication checks executed")
	}
	r = mediaRequest(nil)
	r.ContentLength = maxMediaInput + 1
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("size bound: %d %s", w.Code, w.Body)
	}
	mediaSlots <- struct{}{}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, mediaRequest([]byte("x")))
	<-mediaSlots
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrency bound: %d", w.Code)
	}
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, mediaRequest([]byte("#EXTM3U\nhttps://example.invalid/private\n")))
	if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "original") {
		t.Fatalf("unsupported input: %d %s", w.Code, w.Body)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("private preparation files leaked: %v %v", files, err)
	}
	r = mediaRequest([]byte("cancelled"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r = r.WithContext(ctx)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("cancelled job succeeded")
	}
	files, _ = os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("cancelled preparation leaked files")
	}
}

func TestLocalVideoPreparation(t *testing.T) {
	ffmpeg, ffprobe, ok := mediaTools()
	if !ok {
		t.Skip("requires optional Linux ffmpeg and ffprobe")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "source.mp4")
	cmd := exec.CommandContext(t.Context(), ffmpeg, "-nostdin", "-v", "error", "-cpucount", "2", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=60", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "1.5", "-c:v", "libx264", "-threads", "2", "-preset", "ultrafast", "-crf", "10", "-c:a", "aac", "-b:a", "192k", "-shortest", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("deterministic source: %v %s", err, out)
	}
	original, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	s := New(NewFixture(time.Now), "127.0.0.1:8123", testToken)
	w := httptest.NewRecorder()
	started := time.Now()
	s.Handler().ServeHTTP(w, mediaRequest(original))
	if w.Code != 200 {
		t.Fatalf("prepare: %d %s", w.Code, w.Body)
	}
	if w.Body.Len() >= int(float64(len(original))*.8) || w.Body.Len() >= maxMediaOutput {
		t.Fatalf("no worthwhile saving: %d -> %d", len(original), w.Body.Len())
	}
	output := filepath.Join(dir, "received.mp4")
	if err := os.WriteFile(output, w.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	check := exec.CommandContext(t.Context(), ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name,width,height,avg_frame_rate:format=duration", "-of", "json", output)
	b, err := check.Output()
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Streams []struct {
			Type   string `json:"codec_type"`
			Codec  string `json:"codec_name"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		}
		Format struct {
			Duration string `json:"duration"`
		}
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	video, audio := false, false
	for _, stream := range result.Streams {
		if stream.Type == "video" {
			video = stream.Codec == "h264" && stream.Width <= 1280 && stream.Height <= 1280 && stream.Width > 0 && stream.Height > 0
		}
		if stream.Type == "audio" {
			audio = stream.Codec == "aac"
		}
	}
	if !video || !audio {
		t.Fatalf("unusable output: %s", b)
	}
	unchanged, _ := os.ReadFile(input)
	if !bytes.Equal(original, unchanged) {
		t.Fatal("original was changed")
	}
	t.Logf("native media source=%d prepared=%d elapsed=%s probe=%s", len(original), w.Body.Len(), time.Since(started), b)
	t.Run("output cap refuses a partial clip", func(t *testing.T) {
		err := transcodeVideo(t.Context(), ffmpeg, ffprobe, input, filepath.Join(dir, "capped.mp4"), 32<<10)
		if err == nil || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("cap accepted truncated output: %v", err)
		}
	})
	t.Run("playable prefix below cap is incomplete", func(t *testing.T) {
		prefix := filepath.Join(dir, "prefix.mp4")
		makePrefix := exec.CommandContext(t.Context(), ffmpeg, "-nostdin", "-v", "error", "-i", output, "-t", "0.3", "-c", "copy", prefix)
		if out, err := makePrefix.CombinedOutput(); err != nil {
			t.Fatalf("prefix fixture: %v %s", err, out)
		}
		tool := filepath.Join(dir, "prefix-ffmpeg")
		if err := os.WriteFile(tool, []byte("#!/bin/sh\nfor output do :; done\ncp \"$AGENTNET_TEST_PREFIX\" \"$output\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENTNET_TEST_PREFIX", prefix)
		err := transcodeVideo(t.Context(), tool, ffprobe, input, filepath.Join(dir, "short.mp4"), maxMediaOutput)
		if err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("playable prefix accepted as complete: %v", err)
		}
	})
	t.Run("cancel active native preparation and clean temporary files", func(t *testing.T) {
		bin, temp := t.TempDir(), t.TempDir()
		ready := filepath.Join(bin, "ready")
		if err := os.Symlink(ffprobe, filepath.Join(bin, "ffprobe")); err != nil {
			t.Fatal(err)
		}
		tool := "#!/bin/sh\nprintf ready > \"$AGENTNET_TEST_READY\"\nexec /usr/bin/sleep 30\n"
		if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(tool), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		t.Setenv("TMPDIR", temp)
		t.Setenv("AGENTNET_TEST_READY", ready)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		r := mediaRequest(original).WithContext(ctx)
		w := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { s.Handler().ServeHTTP(w, r); close(done) }()
		until := time.Now().Add(2 * time.Second)
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(until) {
				cancel()
				<-done
				t.Fatal("converter never started")
			}
			time.Sleep(5 * time.Millisecond)
		}
		entries, err := os.ReadDir(temp)
		if err != nil || len(entries) != 1 {
			cancel()
			<-done
			t.Fatalf("private work directory: %v %v", entries, err)
		}
		info, err := os.Stat(filepath.Join(temp, entries[0].Name(), "input"))
		if err != nil || info.Mode().Perm()&0077 != 0 {
			cancel()
			<-done
			t.Fatalf("private input permissions: %v %v", info, err)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled converter did not stop")
		}
		if w.Code == 200 {
			t.Fatal("cancelled converter returned success")
		}
		entries, err = os.ReadDir(temp)
		if err != nil || len(entries) != 0 {
			t.Fatalf("cancelled job leaked private files: %v %v", entries, err)
		}
	})
}

func TestMediaSenderRendered(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("synthetic video needs optional local ffmpeg")
	}
	t.Log(browserCheck(t, "testdata/media_sender_rendered.cjs"))
}

func TestLocalVideoPreparationLongDuration(t *testing.T) {
	ffmpeg, ffprobe, ok := mediaTools()
	if !ok {
		t.Skip("requires optional Linux ffmpeg and ffprobe")
	}
	ctx, cancel := context.WithTimeout(t.Context(), mediaDeadline)
	defer cancel()
	dir := t.TempDir()
	input, output := filepath.Join(dir, "long-source.mp4"), filepath.Join(dir, "long-prepared.mp4")
	// Encode 900 small frames without a real-time input clock. The clip's actual
	// three-minute duration exercises the duration-based output budget cheaply.
	makeSource := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "error", "-cpucount", "2", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=5", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "180", "-c:v", "libx264", "-threads", "2", "-preset", "ultrafast", "-crf", "10", "-c:a", "aac", "-b:a", "128k", "-shortest", input)
	if out, err := makeSource.CombinedOutput(); err != nil {
		t.Fatalf("long source: %v %s", err, out)
	}
	started := time.Now()
	if err := transcodeVideo(ctx, ffmpeg, ffprobe, input, output, maxMediaOutput); err != nil {
		t.Fatalf("three-minute preparation: %v", err)
	}
	stat, err := os.Stat(output)
	if err != nil || stat.Size() <= 0 || stat.Size() >= maxMediaOutput {
		t.Fatalf("long output size: %v %v", stat, err)
	}
	probe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name,duration,nb_frames:format=duration", "-of", "json", output)
	data, err := probe.Output()
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Streams []struct {
			Type     string `json:"codec_type"`
			Codec    string `json:"codec_name"`
			Duration string `json:"duration"`
			Frames   string `json:"nb_frames"`
		}
		Format struct {
			Duration string `json:"duration"`
		}
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	complete := func(raw string) bool {
		duration, err := strconv.ParseFloat(raw, 64)
		return err == nil && !math.IsNaN(duration) && math.Abs(duration-180) <= .2
	}
	video, audio := false, false
	for _, stream := range result.Streams {
		if stream.Type == "video" {
			frames, _ := strconv.Atoi(stream.Frames)
			video = stream.Codec == "h264" && frames == 900 && complete(stream.Duration)
		}
		if stream.Type == "audio" {
			audio = stream.Codec == "aac" && complete(stream.Duration)
		}
	}
	if !video || !audio || !complete(result.Format.Duration) {
		t.Fatalf("incomplete long video/audio: %s", data)
	}
	t.Logf("native long media prepared=%d elapsed=%s probe=%s", stat.Size(), time.Since(started), data)
}

func TestMessageInteractionRendered(t *testing.T) {
	t.Log(browserCheck(t, "testdata/interaction_rendered.cjs"))
}

func TestMessageReviewInteractionsRendered(t *testing.T) {
	if engine := os.Getenv("AGENTNET_INTERACTION_ENGINE"); engine != "" && engine != "chromium" {
		t.Skip("real CDP touch input requires Chromium; engine-neutral controls run separately")
	}
	t.Setenv("AGENTNET_INTERACTION_REVIEW", "1")
	t.Log(browserCheck(t, "testdata/interaction_rendered.cjs"))
}

func TestFirstUnreadTimelineRendered(t *testing.T) {
	t.Setenv("AGENTNET_INTERACTION_UNREAD_CONTRACT", "1")
	t.Log(browserCheck(t, "testdata/interaction_rendered.cjs"))
}

func TestTimelineKeyboardRendered(t *testing.T) {
	t.Setenv("AGENTNET_INTERACTION_KEYBOARD", "1")
	t.Log(browserCheck(t, "testdata/interaction_rendered.cjs"))
}

func TestMediaBitrateBudget(t *testing.T) {
	for _, tc := range []struct {
		duration  float64
		audio     bool
		cap, want int64
		fits      bool
	}{
		{1.5, true, maxMediaOutput, 2500000, true}, {120, true, maxMediaOutput, 1885265, true},
		{300, true, maxMediaOutput, 677306, true}, {300, false, maxMediaOutput, 805306, true},
		{301, true, maxMediaOutput, 0, false}, {300, true, 1 << 20, 0, false}, {0, false, maxMediaOutput, 0, false},
	} {
		got, fits := mediaBitrate(tc.duration, tc.audio, tc.cap)
		if got != tc.want || fits != tc.fits {
			t.Errorf("duration=%v audio=%v cap=%d: %d/%v want %d/%v", tc.duration, tc.audio, tc.cap, got, fits, tc.want, tc.fits)
		}
	}
	// The browser executes the same long-duration fixtures using its actual TS.
	if _, err := os.Stat("web/node_modules/esbuild"); err == nil {
		runNodeCheck(t, "testdata/media_models_check.cjs")
	}
}
