package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// baseline makes the baseline of a copy outside it (MSFP playback
// section 2.7), as req.Served says: the picture to H.264 High 4.1, 8-bit
// 4:2:0, tagged BT.709, unless it is copied; the audio track played to
// AAC-LC stereo at 48 kHz, unless it is copied; as complete HLS with
// MPEG-TS segments, in a temporary folder the playback's Stop removes.
// ffmpeg makes the whole copy before the answer: simple, and fine for
// short files; a real host transcodes as the viewer plays.
func (f *folder) baseline(ctx context.Context, req msfp.StartRequest, path string) (msfp.Started, error) {
	if _, err := req.Admit(wire.MethodTranscode); err != nil {
		return msfp.Started{}, err
	}
	dir, err := os.MkdirTemp("", "share-folder-baseline-")
	if err != nil {
		return msfp.Started{}, err // the engine releases what Admit counted
	}
	args := []string{"-nostdin", "-v", "error", "-i", path, "-map_metadata", "-1", "-sn", "-dn"}
	switch {
	case req.AudioOnly || req.Served.Video == "":
		args = append(args, "-vn")
	case req.Served.Video == wire.CodecCopy:
		args = append(args, "-map", "0:v:0", "-c:v", "copy")
	default:
		args = append(args, "-map", "0:v:0", "-c:v", "libx264", "-preset", "veryfast", "-profile:v", "high",
			"-level:v", "4.1", "-pix_fmt", "yuv420p", "-color_primaries", "bt709", "-color_trc", "bt709",
			"-colorspace", "bt709")
	}
	if track := strings.TrimPrefix(req.AudioTrack, "a"); track != "" {
		args = append(args, "-map", "0:"+track)
		if req.Served.Audio == wire.CodecCopy {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-c:a", "aac", "-profile:a", "aac_low", "-ac", "2", "-ar", "48000")
		}
	}
	args = append(args, "-f", "hls", "-hls_time", "6", "-hls_playlist_type", "vod",
		"-hls_segment_filename", filepath.Join(dir, "seg%d.ts"), "-master_pl_name", "master.m3u8",
		filepath.Join(dir, "v0.m3u8"))
	if out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return msfp.Started{}, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	f.mu.Lock()
	if f.made == nil {
		f.made = map[string]string{}
	}
	f.n++
	handle := fmt.Sprintf("made%d %s", f.n, req.Copy)
	f.made[handle] = dir
	f.mu.Unlock()
	return msfp.Started{Handle: handle, Method: wire.MethodTranscode, Protocol: wire.ProtocolHLS, PositionMs: req.StartMs}, nil
}
