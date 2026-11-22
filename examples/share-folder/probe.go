package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// probes describe video files with ffprobe, when it is installed: their
// length, bitrate, picture and audio tracks, as MSFP video section 3.2
// lists a copy, exact, with ffmpeg's names as ffprobe gives them (MSFP
// takes those names, open: video section 3.5). Without ffprobe, or for a file it can't read, a copy says
// only what every file says (its size, its container) and its length is
// 0. What ffprobe said is kept by file, size and time, so a file is
// probed once until it changes.
type probes struct {
	mu   sync.Mutex
	done map[string]probed
}

type probed struct {
	size    int64
	modTime time.Time
	copy    wire.Copy
}

// ffprobeOut is what ffprobe's JSON says that a copy uses.
type ffprobeOut struct {
	Format struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Profile      string `json:"profile"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		AvgFrameRate string `json:"avg_frame_rate"`
		Channels     int    `json:"channels"`
		Layout       string `json:"channel_layout"`
		SampleRate   string `json:"sample_rate"`
		Disposition  struct {
			Default int `json:"default"`
			Forced  int `json:"forced"`
		} `json:"disposition"`
		Tags struct {
			Language string `json:"language"`
		} `json:"tags"`
	} `json:"streams"`
}

// describe is what ffprobe says of the file at path: its length,
// bitrate, picture and audio tracks set on a copy; the rest is the
// caller's.
func (p *probes) describe(path string, st os.FileInfo) wire.Copy {
	p.mu.Lock()
	if d, ok := p.done[path]; ok && d.size == st.Size() && d.modTime.Equal(st.ModTime()) {
		p.mu.Unlock()
		return d.copy
	}
	p.mu.Unlock()
	c := ffprobe(path)
	p.mu.Lock()
	if p.done == nil {
		p.done = map[string]probed{}
	}
	p.done[path] = probed{size: st.Size(), modTime: st.ModTime(), copy: c}
	p.mu.Unlock()
	return c
}

func ffprobe(path string) wire.Copy {
	c := wire.Copy{AudioTracks: []wire.CopyAudio{}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "quiet", "-print_format", "json",
		"-show_format", "-show_streams", path).Output()
	var pr ffprobeOut
	if err != nil || json.Unmarshal(out, &pr) != nil {
		return c
	}
	if s, err := strconv.ParseFloat(pr.Format.Duration, 64); err == nil && s > 0 {
		c.DurationMs = int64(s * 1000) // exact, in milliseconds
	}
	if b, err := strconv.ParseInt(pr.Format.BitRate, 10, 64); err == nil && b >= 1000 {
		c.BitrateKbps = b / 1000
	}
	for _, s := range pr.Streams {
		switch s.CodecType {
		case "video":
			// ffprobe's avg_frame_rate is MSFP's fps as it is ("24000/1001";
			// "0/0" for none, which a receiver drops).
			if c.Video == nil && s.Width > 0 && s.Height > 0 {
				c.Video = &wire.VideoStream{Codec: s.CodecName, Profile: s.Profile, Width: s.Width,
					Height: s.Height, FPS: s.AvgFrameRate}
			}
		case "audio":
			a := wire.CopyAudio{ID: "a" + strconv.Itoa(len(c.AudioTracks)), Codec: s.CodecName, Profile: s.Profile,
				Layout: s.Layout, Channels: s.Channels, Default: s.Disposition.Default == 1, Forced: s.Disposition.Forced == 1}
			a.Language, _ = wire.SentTag(s.Tags.Language)
			a.SampleRate, _ = strconv.Atoi(s.SampleRate)
			c.AudioTracks = append(c.AudioTracks, a)
		}
		// Subtitles inside the file are left out: this example serves
		// only the .vtt files beside it.
	}
	return c
}

// muxers are the ffmpeg muxer names (what ffmpeg -f takes) of the
// containers this example shares, by extension: MSFP's container
// (video section 3.5), not ffprobe's list of demuxers ("matroska,webm").
var muxers = map[string]string{"mkv": "matroska", "webm": "webm", "mp4": "mp4", "m4v": "mp4", "mov": "mov", "ts": "mpegts"}
