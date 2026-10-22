package ui

// Optional Linux sender preparation. This is the authenticated loopback UI,
// never a relay operation: bytes enter and leave this machine before staging
// or encryption. No path, URL, filter or command argument comes from the page.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxMediaInput = 1 << 30
const maxMediaOutput = 32 << 20
const mediaDeadline = 2 * time.Minute

// Keep this budget identical to web/src/features/Composer.media-budget.ts.
func mediaBitrate(duration float64, audio bool, cap int64) (int64, bool) {
	if !(duration > 0 && duration <= 300) || cap <= 0 {
		return 0, false
	}
	budget := math.Floor(float64(cap) * 8 * .9 / duration)
	if audio {
		budget -= 128000
	}
	budget = math.Min(2500000, budget)
	if budget < 650000 {
		return 0, false
	}
	return int64(budget), true
}

// Shared across local memberships: one preparation at a time per process.
var mediaSlots = make(chan struct{}, 1)

func mediaTools() (string, string, bool) {
	if runtime.GOOS != "linux" {
		return "", "", false
	}
	f, e := exec.LookPath("ffmpeg")
	p, pe := exec.LookPath("ffprobe")
	return f, p, e == nil && pe == nil
}

func (s *Server) mediaCapabilities(w http.ResponseWriter, r *http.Request) {
	_, _, ok := mediaTools()
	writeJSON(w, map[string]any{"video": ok && !s.mobile && s.localMediaRequest(r), "max_input": maxMediaInput, "max_output": maxMediaOutput})
}

func (s *Server) localMediaRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(s.host)
	remote, _, remoteErr := net.SplitHostPort(r.RemoteAddr)
	return err == nil && remoteErr == nil && net.ParseIP(host).IsLoopback() && net.ParseIP(remote).IsLoopback()
}

func (s *Server) prepareVideo(w http.ResponseWriter, r *http.Request) {
	if !s.localMediaRequest(r) {
		http.Error(w, "Video preparation is available only on this device's loopback UI.", http.StatusForbidden)
		return
	}
	ffmpeg, ffprobe, available := mediaTools()
	if !available || s.mobile {
		http.Error(w, "Video preparation is unavailable here. Send the original file or prepare it on another device.", http.StatusNotImplemented)
		return
	}
	if r.ContentLength > maxMediaInput {
		http.Error(w, "This video exceeds the 1 GiB preparation limit. Send the original file.", http.StatusRequestEntityTooLarge)
		return
	}
	select {
	case mediaSlots <- struct{}{}:
		defer func() { <-mediaSlots }()
	default:
		http.Error(w, "Another video is being prepared. Wait for it or send the original file.", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), mediaDeadline)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = r.Body.Close() })
	defer stop()
	dir, err := os.MkdirTemp("", "agentnet-media-")
	if err != nil {
		writeErr(w, Refuse("Could not create private temporary storage for this video. Send the original file."))
		return
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "input")
	file, err := os.OpenFile(input, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		writeErr(w, Refuse("Could not create a private temporary video. Send the original file."))
		return
	}
	n, err := io.Copy(file, io.LimitReader(r.Body, maxMediaInput+1))
	closeErr := file.Close()
	if errors.Is(err, syscall.ENOSPC) || errors.Is(closeErr, syscall.ENOSPC) {
		writeErr(w, Refuse("This device is out of temporary storage. Free disk space or send the original file."))
		return
	}
	if err != nil || closeErr != nil || n == 0 || n > maxMediaInput || ctx.Err() != nil {
		http.Error(w, "Video preparation was cancelled or the input exceeds the 1 GiB limit. The original file is kept.", http.StatusBadRequest)
		return
	}
	output := filepath.Join(dir, "prepared.mp4")
	if err := transcodeVideo(ctx, ffmpeg, ffprobe, input, output, maxMediaOutput); err != nil {
		writeErr(w, Refuse(err.Error()))
		return
	}
	prepared, err := os.Open(output)
	if err != nil {
		writeErr(w, Refuse("Could not read the prepared video. The original file is kept."))
		return
	}
	defer prepared.Close()
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "prepared.mp4", time.Time{}, prepared)
}

type mediaProbe struct {
	Streams []struct {
		Type   string `json:"codec_type"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Rate   string `json:"avg_frame_rate"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// boundedProbe prevents malformed input from retaining unbounded tool output.
type boundedProbe struct{ bytes.Buffer }

func (b *boundedProbe) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16<<10 {
		return 0, errors.New("media metadata exceeds limit")
	}
	return b.Buffer.Write(p)
}

func transcodeVideo(ctx context.Context, ffmpeg, ffprobe, input, output string, limit int64) error {
	// Restrict demuxers and protocols. Playlists, remote references and arbitrary
	// local-file demuxers are outside this operation; MOV external drefs stay off.
	probe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-protocol_whitelist", "file", "-format_whitelist", "mov,matroska,webm", "-show_entries", "format=duration:stream=codec_type,width,height,avg_frame_rate", "-of", "json", input)
	var out boundedProbe
	probe.Stdout = &out
	probe.Stderr = io.Discard
	var info mediaProbe
	if err := probe.Run(); err != nil || json.Unmarshal(out.Bytes(), &info) != nil {
		return errors.New("This video format cannot be prepared here. Send the original file.")
	}
	duration, err := strconv.ParseFloat(info.Format.Duration, 64)
	if err != nil || !(duration > 0 && duration <= 300) {
		return errors.New("Video preparation supports clips up to five minutes. Send the original file.")
	}
	rate := 30.0
	video := false
	audio := false
	for _, stream := range info.Streams {
		if stream.Type == "audio" {
			audio = true
		}
		if stream.Type != "video" {
			continue
		}
		if video || stream.Width <= 0 || stream.Height <= 0 || stream.Width > 4096 || stream.Height > 4096 || int64(stream.Width)*int64(stream.Height) > 3840*2160 {
			return errors.New("This video's dimensions or tracks exceed the preparation limit. Send the original file.")
		}
		video = true
		parts := strings.Split(stream.Rate, "/")
		if len(parts) == 2 {
			a, _ := strconv.ParseFloat(parts[0], 64)
			b, _ := strconv.ParseFloat(parts[1], 64)
			if a > 0 && b > 0 && a/b < rate {
				rate = a / b
			}
		}
	}
	if !video {
		return errors.New("This file has no supported video track. Send the original file.")
	}
	bitrate, fits := mediaBitrate(duration, audio, limit)
	if !fits {
		return errors.New("This video's duration cannot fit the output limit at usable quality. Send the original file.")
	}
	filter := fmt.Sprintf("scale=w='min(1280,iw)':h='min(1280,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,setsar=1,fps=%g", rate)
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-cpucount", "2", "-threads", "2", "-filter_threads", "1", "-filter_complex_threads", "1", "-protocol_whitelist", "file", "-format_whitelist", "mov,matroska,webm", "-i", input,
		"-map", "0:v:0", "-map", "0:a:0?", "-map_metadata", "-1", "-map_chapters", "-1", "-vf", filter, "-c:v", "libx264", "-preset", "fast", "-threads", "2", "-pix_fmt", "yuv420p", "-b:v", strconv.FormatInt(bitrate, 10), "-maxrate", strconv.FormatInt(bitrate*12/10, 10), "-bufsize", strconv.FormatInt(bitrate*2, 10), "-c:a", "aac", "-b:a", "128k", "-ac", "2", "-fs", strconv.FormatInt(limit, 10), "-movflags", "+faststart", "-f", "mp4", output)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("Video preparation failed or was cancelled. The original file is kept.")
	}
	stat, err := os.Stat(output)
	if err != nil || stat.Size() == 0 || stat.Size() >= limit {
		return errors.New("The prepared video exceeds the 32 MiB output limit. Send the original file.")
	}
	// -fs may finish successfully with only a prefix of a clip. A playable
	// prefix is still an incomplete send; validate the actual completed output.
	complete := exec.CommandContext(ctx, ffprobe, "-v", "error", "-protocol_whitelist", "file", "-format_whitelist", "mov", "-show_entries", "format=duration:stream=codec_type,width,height", "-of", "json", output)
	out.Reset()
	complete.Stdout, complete.Stderr = &out, io.Discard
	var produced mediaProbe
	if complete.Run() != nil || json.Unmarshal(out.Bytes(), &produced) != nil {
		return errors.New("The prepared video is incomplete. Send the original file.")
	}
	finished, err := strconv.ParseFloat(produced.Format.Duration, 64)
	hasVideo, hasAudio := false, false
	for _, stream := range produced.Streams {
		if stream.Type == "video" {
			hasVideo = stream.Width > 0 && stream.Height > 0 && stream.Width <= 1280 && stream.Height <= 1280
		}
		if stream.Type == "audio" {
			hasAudio = true
		}
	}
	if err != nil || math.IsNaN(finished) || math.Abs(finished-duration) > .2 || !hasVideo || audio && !hasAudio {
		return errors.New("The prepared video is incomplete or exceeds the output limit. Send the original file.")
	}
	return nil
}
