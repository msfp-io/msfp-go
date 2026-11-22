package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// probes describe video files as MSFP video section 3.2 lists a copy:
// its media type from its first bytes (sniff.go), its etag from its
// content, and, with ffprobe when it is installed, its exact length, its
// picture and its audio tracks, in the vocabularies MSFP takes (video
// sections 3.5 and 3.6): the registry's codec tokens (ffprobe's
// codec_name), RFC 6381 codecs strings, H.273 colour code points.
// Without ffprobe, or for a file it can't read, a copy says only what
// every file says (its size, its media type, its etag) and its length is
// 0. What a file gave is kept by path, size and time, so a file is read
// once until it changes.
type probes struct {
	mu   sync.Mutex
	done map[string]probed
}

type probed struct {
	size    int64
	modTime time.Time
	copy    wire.Copy
	streams int // the file's streams, numbered from 0
}

// ffprobeStream is what ffprobe's JSON says of a stream that a copy uses.
type ffprobeStream struct {
	Index             int    `json:"index"`
	CodecType         string `json:"codec_type"`
	CodecName         string `json:"codec_name"`
	CodecTag          string `json:"codec_tag_string"`
	Profile           string `json:"profile"`
	Level             int    `json:"level"`
	Width             int    `json:"width"`
	Height            int    `json:"height"`
	PixFmt            string `json:"pix_fmt"`
	BitsPerRawSample  string `json:"bits_per_raw_sample"`
	BitsPerSample     int    `json:"bits_per_sample"`
	ColorRange        string `json:"color_range"`
	ColorSpace        string `json:"color_space"`
	ColorTransfer     string `json:"color_transfer"`
	ColorPrimaries    string `json:"color_primaries"`
	FieldOrder        string `json:"field_order"`
	Refs              int    `json:"refs"`
	SampleAspectRatio string `json:"sample_aspect_ratio"`
	AvgFrameRate      string `json:"avg_frame_rate"`
	RFrameRate        string `json:"r_frame_rate"`
	Channels          int    `json:"channels"`
	SampleRate        string `json:"sample_rate"`
	Disposition       struct {
		Default, Forced, Comment, Dub, Original, VisualImpaired, HearingImpaired, AttachedPic, Descriptions int
	} `json:"disposition"`
	Tags struct {
		Language string `json:"language"`
	} `json:"tags"`
	SideData []ffprobeSide `json:"side_data_list"`
}

// ffprobeSide is a stream's side data: its display matrix, its Dolby
// Vision configuration record.
type ffprobeSide struct {
	Type          string  `json:"side_data_type"`
	Rotation      float64 `json:"rotation"`
	DVProfile     int     `json:"dv_profile"`
	DVLevel       int     `json:"dv_level"`
	ELPresent     int     `json:"el_present_flag"`
	Compatibility int     `json:"dv_bl_signal_compatibility_id"`
}

type ffprobeOut struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

// describe is what the file at path says of itself: a copy with its
// media type, etag, length, picture and audio tracks; and how many
// streams it has. The rest is the caller's.
func (p *probes) describe(path string, st os.FileInfo) (wire.Copy, int) {
	p.mu.Lock()
	if d, ok := p.done[path]; ok && d.size == st.Size() && d.modTime.Equal(st.ModTime()) {
		p.mu.Unlock()
		return d.copy, d.streams
	}
	p.mu.Unlock()
	c, pr, ok := ffprobe(path)
	video := ok && hasPicture(pr.Streams)
	c.MediaType = mediaTypeOf(path, video || !ok)
	c.ETag = contentTag(path)
	p.mu.Lock()
	if p.done == nil {
		p.done = map[string]probed{}
	}
	p.done[path] = probed{size: st.Size(), modTime: st.ModTime(), copy: c, streams: len(pr.Streams)}
	p.mu.Unlock()
	return c, len(pr.Streams)
}

// contentTag is the file's etag: from its content, so that it changes
// whenever its bytes change, and only then (MSFP video section 3.2):
// the first 16 bytes of its SHA-256, base64url. "" when it can't be
// read.
func contentTag(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16])
}

// hasPicture says whether a file has a video stream other than attached
// pictures (cover art).
func hasPicture(streams []ffprobeStream) bool {
	for _, s := range streams {
		if s.CodecType == "video" && s.Disposition.AttachedPic == 0 {
			return true
		}
	}
	return false
}

func ffprobe(path string) (wire.Copy, ffprobeOut, bool) {
	c := wire.Copy{AudioTracks: []wire.CopyAudio{}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "quiet", "-print_format", "json",
		"-show_format", "-show_streams", path).Output()
	var pr ffprobeOut
	if err != nil || json.Unmarshal(out, &pr) != nil {
		return c, pr, false
	}
	if s, err := strconv.ParseFloat(pr.Format.Duration, 64); err == nil && s > 0 {
		c.DurationMs = int64(s * 1000) // exact, in milliseconds
	}
	for _, s := range pr.Streams {
		switch s.CodecType {
		case "video":
			if c.Video == nil && s.Width > 0 && s.Height > 0 && s.Disposition.AttachedPic == 0 {
				c.Video = videoStream(s)
			}
		case "audio":
			c.AudioTracks = append(c.AudioTracks, audioTrack(s))
		}
		// Subtitles inside the file are left out: this example serves
		// only the .vtt files beside it.
	}
	return c, pr, true
}

// videoStream is a copy's picture from ffprobe's stream.
func videoStream(s ffprobeStream) *wire.VideoStream {
	v := &wire.VideoStream{Codec: s.CodecName, Width: s.Width, Height: s.Height, BitDepth: bitDepth(s)}
	v.FPS = reduced(s.AvgFrameRate, "/")
	if v.FPS == "" {
		v.FPS = reduced(s.RFrameRate, "/")
	}
	if sar := reduced(s.SampleAspectRatio, ":"); sar != "" && sar != "1:1" {
		v.SampleAspect = sar
	}
	if s.CodecName == "h264" && s.Refs >= 1 && s.Refs <= 16 {
		v.RefFrames = s.Refs
	}
	v.Interlaced = s.FieldOrder != "" && s.FieldOrder != "progressive" && s.FieldOrder != "unknown"
	v.Colour = colour(s)
	for _, d := range s.SideData {
		switch {
		case strings.HasPrefix(d.Type, "DOVI configuration"):
			el := d.ELPresent == 1
			v.DV = &wire.VideoDV{Profile: d.DVProfile, Level: d.DVLevel, Compatibility: d.Compatibility, EL: &el}
		case d.Type == "Display Matrix":
			// ffprobe gives the angle counterclockwise; MSFP clockwise.
			if r := (360 - int(d.Rotation)%360) % 360; float64(int(d.Rotation)) == d.Rotation && r%90 == 0 {
				v.Rotation = &r
			}
		case strings.Contains(d.Type, "HDR10+") || strings.Contains(d.Type, "SMPTE2094-40"):
			v.HDR = []string{"hdr10plus"}
		}
	}
	v.Codecs = videoCodecs(s, v)
	return v
}

// audioTrack is a copy's audio track from ffprobe's stream: its ID the
// stream's index in the file (MSFP video section 3.2).
func audioTrack(s ffprobeStream) wire.CopyAudio {
	a := wire.CopyAudio{ID: "a" + strconv.Itoa(s.Index), Codec: s.CodecName, Channels: s.Channels,
		Default: s.Disposition.Default == 1, Forced: s.Disposition.Forced == 1, Original: s.Disposition.Original == 1}
	a.Language, _ = wire.SentTag(s.Tags.Language)
	a.SampleRate, _ = strconv.Atoi(s.SampleRate)
	if n, _ := strconv.Atoi(s.BitsPerRawSample); n >= 8 && n <= 32 {
		a.BitDepth = n
	}
	switch {
	case s.Disposition.Comment == 1:
		a.Role = "commentary"
	case s.Disposition.Descriptions == 1 || s.Disposition.VisualImpaired == 1:
		a.Role = "description"
	case s.Disposition.Dub == 1:
		a.Role = "dub"
	}
	switch p := s.Profile; {
	case (s.CodecName == "eac3" || s.CodecName == "truehd") && (strings.Contains(p, "Atmos") || strings.Contains(p, "JOC")):
		a.Objects = "dolby-atmos"
	case s.CodecName == "dts" && strings.Contains(p, "DTS:X"):
		a.Objects = "dts-x"
	}
	a.Codecs = audioCodecs(s.CodecName, s.Profile)
	return a
}

// reduced is "<a><sep><b>" in lowest terms, "" for none ("0/0", "0:1").
func reduced(s, sep string) string {
	x, y, ok := strings.Cut(s, sep)
	a, err1 := strconv.Atoi(x)
	b, err2 := strconv.Atoi(y)
	if !ok || err1 != nil || err2 != nil || a <= 0 || b <= 0 {
		return ""
	}
	g := a
	for m := b; m != 0; g, m = m, g%m {
	}
	return strconv.Itoa(a/g) + sep + strconv.Itoa(b/g)
}

// bitDepth is a picture's bit depth: ffprobe's bits_per_raw_sample, else
// what its pixel format says.
func bitDepth(s ffprobeStream) int {
	if n, _ := strconv.Atoi(s.BitsPerRawSample); n >= 1 && n <= 16 {
		return n
	}
	for _, d := range []string{"16", "14", "12", "10", "9"} {
		if strings.Contains(s.PixFmt, "p"+d) {
			n, _ := strconv.Atoi(d)
			return n
		}
	}
	if s.PixFmt != "" {
		return 8
	}
	return 0
}

// ffmpeg's colour names as ITU-T H.273 code points (the implementers'
// notes' table).
var (
	h273Primaries = map[string]int{"bt709": 1, "bt470m": 4, "bt470bg": 5, "smpte170m": 6, "smpte240m": 7, "film": 8,
		"bt2020": 9, "smpte428": 10, "smpte431": 11, "smpte432": 12, "jedec-p22": 22, "ebu3213": 22}
	h273Transfer = map[string]int{"bt709": 1, "gamma22": 4, "gamma28": 5, "smpte170m": 6, "smpte240m": 7, "linear": 8,
		"log100": 9, "log316": 10, "iec61966-2-4": 11, "bt1361e": 12, "iec61966-2-1": 13, "bt2020-10": 14, "bt2020-12": 15,
		"smpte2084": 16, "smpte428": 17, "arib-std-b67": 18}
	h273Matrix = map[string]int{"gbr": 0, "bt709": 1, "fcc": 4, "bt470bg": 5, "smpte170m": 6, "smpte240m": 7, "ycgco": 8,
		"bt2020nc": 9, "bt2020c": 10, "smpte2085": 11, "chroma-derived-nc": 12, "chroma-derived-c": 13, "ictcp": 14}
)

// colour is a picture's colour as H.273 code points; nil when ffprobe
// says nothing of it.
func colour(s ffprobeStream) *wire.VideoColour {
	pick := func(m map[string]int, name string) *int {
		if n, ok := m[name]; ok {
			return &n
		}
		return nil
	}
	c := wire.VideoColour{Primaries: pick(h273Primaries, s.ColorPrimaries), Transfer: pick(h273Transfer, s.ColorTransfer),
		Matrix: pick(h273Matrix, s.ColorSpace)}
	switch s.ColorRange {
	case "pc":
		t := true
		c.FullRange = &t
	case "tv":
		f := false
		c.FullRange = &f
	}
	if c == (wire.VideoColour{}) {
		return nil
	}
	return &c
}

// chroma is a pixel format's chroma subsampling: "420", "422", "444" or
// "400" (monochrome).
func chroma(pixFmt string) string {
	switch {
	case strings.HasPrefix(pixFmt, "gray"):
		return "400"
	case strings.Contains(pixFmt, "444") || strings.HasPrefix(pixFmt, "gbr"):
		return "444"
	case strings.Contains(pixFmt, "422"):
		return "422"
	}
	return "420"
}

// videoCodecs is a picture's RFC 6381 codecs string (MSFP video section
// 3.5), from what ffprobe says; "" when it can't be made whole (an HEVC
// of a profile from 4, whose constraint bytes ffprobe doesn't give): the
// copy is then one a viewer's server doesn't play as it is. On Dolby
// Vision, the base codec's string.
func videoCodecs(s ffprobeStream, v *wire.VideoStream) string {
	switch s.CodecName {
	case "h264":
		profile := map[string]int{"Baseline": 66, "Constrained Baseline": 66, "Main": 77, "Extended": 88, "High": 100,
			"High 10": 110, "High 4:2:2": 122, "High 4:4:4 Predictive": 244}[s.Profile]
		constraints := 0
		if s.Profile == "Constrained Baseline" {
			constraints = 0x40
		}
		if profile == 0 || s.Level <= 0 || s.Level > 255 {
			return ""
		}
		cc := "avc1"
		if s.CodecTag == "avc3" {
			cc = "avc3"
		}
		return fmt.Sprintf("%s.%02X%02X%02X", cc, profile, constraints, s.Level)
	case "hevc":
		profile := map[string]int{"Main": 1, "Main 10": 2, "Main Still Picture": 3, "Rext": 4}[s.Profile]
		if profile == 0 || profile >= 4 || s.Level <= 0 || s.Level > 255 {
			return ""
		}
		cc := "hvc1"
		if s.CodecTag == "hev1" {
			cc = "hev1"
		}
		// Tier unknown: L; compatibility flags: the profile's own bit.
		return fmt.Sprintf("%s.%d.%X.L%d", cc, profile, 1<<profile, s.Level)
	case "av1":
		profile := map[string]int{"Main": 0, "High": 1, "Professional": 2}[s.Profile]
		level := s.Level
		if level < 0 || level > 31 || v.BitDepth == 0 {
			return ""
		}
		out := fmt.Sprintf("av01.%d.%02dM.%02d", profile, level, v.BitDepth)
		if ch := chroma(s.PixFmt); ch != "420" {
			sub := map[string]string{"444": "0.000", "422": "0.100", "400": "1.110"}[ch]
			out += "." + sub + fmt.Sprintf(".%02d.%02d.%02d.0", 1, 1, 1)
		}
		return out
	case "vp9":
		profile := map[string]int{"Profile 0": 0, "Profile 1": 1, "Profile 2": 2, "Profile 3": 3}[s.Profile]
		if v.BitDepth == 0 {
			return ""
		}
		level := s.Level
		if level < 10 || level > 99 {
			level = 10 // a guess: a consumer ignores VP9's level
		}
		out := fmt.Sprintf("vp09.%02d.%02d.%02d", profile, level, v.BitDepth)
		if profile == 1 || profile == 3 {
			out += map[string]string{"444": ".03", "422": ".02", "420": ".01", "400": ".01"}[chroma(s.PixFmt)]
		}
		return out
	}
	return ""
}

// audioCodecs is a track's RFC 6381 codecs string, from its codec and
// ffprobe's profile: AAC's object type, DTS's flavour, the registry's
// fixed codes; "" when not known.
func audioCodecs(codec, profile string) string {
	switch codec {
	case "aac":
		if ot, ok := map[string]int{"Main": 1, "LC": 2, "SSR": 3, "LTP": 4, "HE-AAC": 5, "HE-AACv2": 29, "LD": 23, "ELD": 39}[profile]; ok {
			return "mp4a.40." + strconv.Itoa(ot)
		}
		return ""
	case "dts":
		switch {
		case strings.HasPrefix(profile, "DTS-HD"):
			return "dtsh"
		case profile == "DTS Express":
			return "dtse"
		case strings.HasPrefix(profile, "DTS"):
			return "dtsc"
		}
		return ""
	}
	return map[string]string{"ac3": "ac-3", "eac3": "ec-3", "flac": "fLaC", "opus": "Opus", "truehd": "mlpa", "alac": "alac"}[codec]
}
