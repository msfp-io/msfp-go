package describe

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/msfp-io/msfp-go/wire"
)

// External is a subtitle file beside the video (MSFP Media video 3.3,
// external): what the host knows of it. Shape numbers it above the
// file's streams, in the order given.
type External struct {
	Language        string // a media server's code; sent as wire.SentTag gives it
	Format          string // the registry's token (subrip, webvtt, ass…)
	Role            string // a DASH role (commentary, easyreader…), or ""
	Default         bool
	Forced          bool
	HearingImpaired bool
}

// Describe probes the file at path (Prober.Probe) and shapes the copy
// (Shape). The copy's ID and etag are the host's to set.
func (p *Prober) Describe(ctx context.Context, path string, external []External) (wire.Copy, *Probe, error) {
	pr, err := p.Probe(ctx, path)
	if err != nil {
		return wire.Copy{}, nil, err
	}
	return Shape(pr, external), pr, nil
}

// Shape is the copy a probe describes (MSFP Media video 3.2 to 3.6), every
// value checked against its field's form and bounds, nothing else from
// the tags (no title, no file name, no encoder):
//
//   - mediaType: the sniffed type, under the audio-or-video rule;
//   - size and durationMs (the format's duration, rounded to the
//     millisecond);
//   - video: the first picture stream (attached pictures and still
//     images left out), its codec the registry's token (ffprobe's
//     codec_name), codecs (VideoCodecs), bitDepth, colour as H.273 code
//     points with fullRange (the frame read's transfer, the alternative
//     transfer SEI's, preferred), dv from the Dolby Vision configuration
//     record, hdr (hdr10plus, from the stream's or the frames' side
//     data), fps, sampleAspect, rotation, refFrames (H.264, from its
//     SPS), interlaced;
//   - threeD, from the stream's stereo 3D side data (Matroska's
//     StereoMode);
//   - audio and subtitle tracks, a<n> and s<n> by stream index, their
//     language, codec or format token, flags and DASH role from the
//     dispositions, then from the track's title where a flag is missing
//     (the implementers' notes' table), objects from the profile
//     ("+ Dolby Atmos", "DTS:X"), sample rate, bit depth;
//   - external subtitles numbered above the file's streams;
//   - chapters: their start, and their name cleaned, dropped when it
//     looks like a file name, an address or a site.
//
// The copy's id and etag are left empty: the host's to set. A copy whose
// codecs couldn't be built where required keeps the stream without it
// (a viewer's server then doesn't play it as it is).
func Shape(pr *Probe, external []External) wire.Copy {
	c := wire.Copy{Size: pr.Size, AudioTracks: []wire.CopyAudio{}}
	if ms, ok := seconds(pr.Format.Duration); ok && ms <= wire.MaxCopyDurationMs {
		c.DurationMs = ms
	}
	pic, picture := pr.picture()
	c.MediaType = AudioOrVideo(CanonicalType(pr.MediaType), picture)
	if picture {
		c.Video = videoStream(pic, pr.Frames)
		c.ThreeD = threeD(pic)
	}
	streams := pr.Format.Streams
	for _, s := range pr.Streams {
		streams = max(streams, s.Index+1)
		switch s.CodecType {
		case "audio":
			if len(c.AudioTracks) < wire.MaxAudioTracks {
				if a, ok := audioTrack(s); ok {
					c.AudioTracks = append(c.AudioTracks, a)
				}
			}
		case "subtitle":
			if len(c.SubtitleTracks) < wire.MaxSubtitleTracks {
				c.SubtitleTracks = append(c.SubtitleTracks, subtitleTrack(s))
			}
		}
	}
	for i, e := range external {
		if len(c.SubtitleTracks) == wire.MaxSubtitleTracks {
			break
		}
		t := wire.CopySubtitle{ID: "s" + strconv.Itoa(streams+i), Default: e.Default, Forced: e.Forced,
			HearingImpaired: e.HearingImpaired, External: true}
		t.Language, _ = wire.SentTag(e.Language)
		if wire.Token(e.Format) {
			t.Format = e.Format
		}
		if wire.Token(e.Role) {
			t.Role = e.Role
		}
		c.SubtitleTracks = append(c.SubtitleTracks, t)
	}
	c.Chapters = chapters(pr.Chapters, c.DurationMs)
	return c
}

// codecToken is a codec or format name as the registry's token: the
// producer-side aliases mapped (section 4.4), "" for one that isn't a
// token.
func codecToken(name string) string {
	switch name {
	case "dca":
		name = "dts"
	case "srt", "text":
		name = "subrip"
	case "none", "unknown":
		return ""
	}
	if !wire.Token(name) {
		return ""
	}
	return name
}

// VideoBitDepth is a picture's bit depth: ffprobe's bits_per_raw_sample,
// else what its pixel format says; 0 when neither does.
func VideoBitDepth(s Stream) int {
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

func videoStream(s Stream, frames []Frame) *wire.VideoStream {
	codec := codecToken(s.CodecName)
	if codec == "" || s.Width > wire.MaxVideoSide || s.Height > wire.MaxVideoSide {
		return nil
	}
	v := &wire.VideoStream{Codec: codec, Width: s.Width, Height: s.Height, BitDepth: VideoBitDepth(s)}
	v.FPS = fpsOf(s)
	if sar := reduced(s.SampleAspectRatio, ":", wire.MaxSampleAspect); sar != "" && sar != "1:1" {
		v.SampleAspect = sar
	}
	if codec == "h264" {
		if r, err := ParseAVC(s.Record); err == nil && r.RefFrames >= 1 && r.RefFrames <= wire.MaxRefFrames {
			v.RefFrames = r.RefFrames
		}
	}
	v.Interlaced = s.FieldOrder != "" && s.FieldOrder != "progressive" && s.FieldOrder != "unknown"
	// The frame read: the transfer the decoder applied (the alternative
	// transfer characteristics SEI's), HDR10+ in the frames' side data.
	hdr10plus := false
	for _, f := range frames {
		if _, ok := h273Transfer[f.ColorTransfer]; ok && f.ColorTransfer != s.ColorTransfer {
			s.ColorTransfer = f.ColorTransfer
		}
		for _, d := range f.SideData {
			hdr10plus = hdr10plus || isHDR10Plus(d.Type)
		}
	}
	v.Colour = colour(s)
	for _, d := range s.SideData {
		switch {
		case strings.HasPrefix(d.Type, "DOVI configuration"):
			if d.DVProfile >= 0 && d.DVProfile <= 127 && d.DVLevel >= 0 && d.DVLevel <= 63 && d.Compatibility >= 0 && d.Compatibility <= 15 {
				el := d.ELPresent == 1
				v.DV = &wire.VideoDV{Profile: d.DVProfile, Level: d.DVLevel, Compatibility: d.Compatibility, EL: &el}
			}
		case d.Type == "Display Matrix":
			v.Rotation = rotation(d)
		case isHDR10Plus(d.Type):
			hdr10plus = true
		}
	}
	if hdr10plus {
		v.HDR = []string{"hdr10plus"}
	}
	v.Codecs = VideoCodecs(s, v.BitDepth)
	if v.Codecs != "" {
		if _, err := wire.ReadCodecs(v.Codec, v.Codecs, v.BitDepth, v.DV); err != nil {
			v.Codecs = ""
		}
	}
	return v
}

func isHDR10Plus(t string) bool {
	return strings.Contains(t, "HDR10+") || strings.Contains(t, "SMPTE2094-40")
}

// rotation is a display matrix's turn, clockwise (ffprobe gives it
// counterclockwise); nil for none, a mirror, or an angle that isn't a
// multiple of 90.
func rotation(d SideData) *int {
	if m := matrix(d.DisplayMatrix); m != nil && m[0]*m[4]-m[1]*m[3] < 0 {
		return nil // mirrored
	}
	if d.Rotation != math.Trunc(d.Rotation) || int(d.Rotation)%90 != 0 {
		return nil
	}
	r := (360 - int(d.Rotation)%360) % 360
	if r == 0 {
		return nil
	}
	return &r
}

// matrix reads ffprobe's display matrix (three lines of three numbers
// after an offset); nil when it isn't of that form.
func matrix(s string) []float64 {
	var out []float64
	for line := range strings.SplitSeq(s, "\n") {
		_, nums, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		for _, f := range strings.Fields(nums) {
			n, err := strconv.ParseFloat(f, 64)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
	}
	if len(out) != 9 {
		return nil
	}
	return out
}

// threeD is a picture's 3D layout from its stereo 3D side data, else
// from Matroska's stereo_mode tag; "" for 2D or a layout MSFP doesn't
// name.
func threeD(s Stream) string {
	for _, d := range s.SideData {
		if d.Type != "Stereo 3D" {
			continue
		}
		t := map[string]string{"side by side": "sbs", "top and bottom": "tab"}[d.StereoType]
		if t != "" && d.Inverted == 1 {
			t += "-rl"
		}
		return t
	}
	return map[string]string{"left_right": "sbs", "right_left": "sbs-rl", "top_bottom": "tab", "bottom_top": "tab-rl"}[s.Tags.Get("stereo_mode")]
}

// fpsOf is a stream's frame rate, reduced, each term in bounds: the
// declared rate (r_frame_rate) when the average agrees with it within
// 0.1%, else the average (a variable rate), else whichever is valid.
func fpsOf(s Stream) string {
	nominal := reduced(s.RFrameRate, "/", wire.MaxFPSTerm)
	avg := reduced(s.AvgFrameRate, "/", wire.MaxFPSTerm)
	value := func(r string) float64 {
		a, b, _ := strings.Cut(r, "/")
		x, _ := strconv.ParseFloat(a, 64)
		y, _ := strconv.ParseFloat(b, 64)
		return x / y
	}
	switch {
	case nominal != "" && avg != "" && math.Abs(value(nominal)-value(avg)) <= value(nominal)/1000:
		return nominal
	case avg != "":
		return avg
	}
	return nominal
}

// reduced is "<a><sep><b>" in lowest terms, each term 1 to top; "" for
// none ("0/0", "0:1", "N/A") or out of bounds.
func reduced(s, sep string, top int) string {
	x, y, ok := strings.Cut(s, sep)
	a, err1 := strconv.Atoi(x)
	b, err2 := strconv.Atoi(y)
	if !ok || err1 != nil || err2 != nil || a <= 0 || b <= 0 {
		return ""
	}
	g := a
	for m := b; m != 0; g, m = m, g%m {
	}
	a, b = a/g, b/g
	if a > top || b > top {
		return ""
	}
	return strconv.Itoa(a) + sep + strconv.Itoa(b)
}

// ffmpeg's colour names as ITU-T H.273 code points (the implementers'
// notes' table), the other spellings ffmpeg reads included.
var (
	h273Primaries = map[string]int{"bt709": 1, "bt470m": 4, "bt470bg": 5, "smpte170m": 6, "smpte240m": 7, "film": 8,
		"bt2020": 9, "smpte428": 10, "smpte428_1": 10, "smpte431": 11, "smpte432": 12, "jedec-p22": 22, "ebu3213": 22}
	h273Transfer = map[string]int{"bt709": 1, "bt470m": 4, "gamma22": 4, "bt470bg": 5, "gamma28": 5, "smpte170m": 6,
		"smpte240m": 7, "linear": 8, "log100": 9, "log": 9, "log316": 10, "log_sqrt": 10, "iec61966-2-4": 11,
		"bt1361e": 12, "iec61966-2-1": 13, "srgb": 13, "bt2020-10": 14, "bt2020-12": 15, "smpte2084": 16,
		"smpte428": 17, "smpte428_1": 17, "arib-std-b67": 18}
	h273Matrix = map[string]int{"gbr": 0, "rgb": 0, "bt709": 1, "fcc": 4, "bt470bg": 5, "smpte170m": 6, "smpte240m": 7,
		"ycgco": 8, "bt2020nc": 9, "bt2020c": 10, "smpte2085": 11, "chroma-derived-nc": 12, "chroma-derived-c": 13,
		"ictcp": 14, "ipt-c2": 15, "ycgco-re": 16, "ycgco-ro": 17}
)

// colour is a picture's colour as H.273 code points; nil when ffprobe
// says nothing of it.
func colour(s Stream) *wire.VideoColour {
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

func audioTrack(s Stream) (wire.CopyAudio, bool) {
	codec := codecToken(s.CodecName)
	if codec == "" {
		return wire.CopyAudio{}, false
	}
	d := s.Disposition
	a := wire.CopyAudio{ID: "a" + strconv.Itoa(s.Index), Codec: codec, Default: d.Default == 1, Forced: d.Forced == 1,
		Original: d.Original == 1}
	a.Language, _ = wire.SentTag(s.Tags.Get("language"))
	if s.Channels >= 1 && s.Channels <= 64 {
		a.Channels = s.Channels
	}
	if n, _ := strconv.Atoi(s.SampleRate); n >= 8_000 && n <= 768_000 {
		a.SampleRate = n
	}
	if n, _ := strconv.Atoi(s.BitsPerRawSample); n >= 8 && n <= 32 {
		a.BitDepth = n
	} else if strings.HasPrefix(codec, "pcm_") && s.BitsPerSample >= 8 && s.BitsPerSample <= 32 {
		a.BitDepth = s.BitsPerSample
	}
	switch {
	case d.Comment == 1:
		a.Role = "commentary"
	case d.Descriptions == 1 || d.VisualImpaired == 1:
		a.Role = "description"
	case d.Dub == 1:
		a.Role = "dub"
	default:
		t := titleFlags(s.Tags.Get("title"))
		a.Role = t.audioRole
		a.Forced = a.Forced || t.forced
	}
	switch p := s.Profile; {
	case (codec == "eac3" || codec == "truehd") && (strings.Contains(p, "Atmos") || strings.Contains(p, "JOC")):
		a.Objects = "dolby-atmos"
	case codec == "dts" && strings.Contains(p, "DTS:X"):
		a.Objects = "dts-x"
	}
	a.Codecs = AudioCodecs(s)
	if a.Codecs != "" && (!wire.CodecsString(a.Codecs) || readFails(codec, a.Codecs)) {
		a.Codecs = ""
	}
	return a, true
}

func readFails(codec, codecs string) bool {
	_, err := wire.ReadCodecs(codec, codecs, 0, nil)
	return err != nil
}

func subtitleTrack(s Stream) wire.CopySubtitle {
	d := s.Disposition
	t := wire.CopySubtitle{ID: "s" + strconv.Itoa(s.Index), Format: codecToken(s.CodecName), Default: d.Default == 1,
		Forced: d.Forced == 1, HearingImpaired: d.HearingImpaired == 1 || d.Captions == 1}
	t.Language, _ = wire.SentTag(s.Tags.Get("language"))
	switch {
	case d.Comment == 1:
		t.Role = "commentary"
	case d.Descriptions == 1:
		t.Role = "description"
	}
	// The title, where a flag is missing.
	f := titleFlags(s.Tags.Get("title"))
	if t.Role == "" {
		t.Role = f.subtitleRole
	}
	t.Forced = t.Forced || f.forced
	t.HearingImpaired = t.HearingImpaired || f.hearingImpaired
	return t
}

// fromTitle is what a track's title says, read only where a flag is
// missing (the implementers' notes, "Track titles to roles and flags").
type fromTitle struct {
	audioRole, subtitleRole string
	forced, hearingImpaired bool
}

var (
	titleCommentary  = regexp.MustCompile(`(?i)\bcommentary\b|\bcommentaire\b`)
	titleDescription = regexp.MustCompile(`(?i)\baudio[ -]description\b|\bdescriptive\b|\bdescribed video\b`)
	titleAD          = regexp.MustCompile(`\bAD\b`)
	titleSDH         = regexp.MustCompile(`(?i)\bSDH\b|\bhearing[ -]impaired\b`)
	titleCC          = regexp.MustCompile(`\bCC\b`)
	titleForced      = regexp.MustCompile(`(?i)\bforced\b|(^|[^a-z])forc(é|ée|és|ées)([^a-z]|$)|\bforeign parts only\b`)
	titleEasyReader  = regexp.MustCompile(`(?i)\beasy[ -]?reader\b`)
)

func titleFlags(title string) fromTitle {
	var f fromTitle
	if title == "" {
		return f
	}
	switch {
	case titleCommentary.MatchString(title):
		f.audioRole, f.subtitleRole = "commentary", "commentary"
	case titleDescription.MatchString(title) || titleAD.MatchString(title):
		f.audioRole = "description"
	}
	if titleEasyReader.MatchString(title) && f.subtitleRole == "" {
		f.subtitleRole = "easyreader"
	}
	f.forced = titleForced.MatchString(title)
	f.hearingImpaired = titleSDH.MatchString(title) || titleCC.MatchString(title)
	return f
}

// chapters are a copy's chapters: in order, each after the one before
// and within the copy, the first MaxChapters; a name only when it is
// plain text that doesn't look like a file name, an address or a site.
func chapters(list []Chapter, duration int64) []wire.CopyChapter {
	var out []wire.CopyChapter
	for _, ch := range list {
		if len(out) == wire.MaxChapters {
			break
		}
		start, ok := seconds(ch.StartTime)
		if !ok || duration > 0 && start > duration || len(out) > 0 && start <= out[len(out)-1].StartMs {
			continue
		}
		c := wire.CopyChapter{StartMs: start}
		if name := wire.CleanText(ch.Tags.Get("title"), wire.MaxChapterName); name != "" && !looksLikeLocation(name) {
			c.Name = name
		}
		out = append(out, c)
	}
	return out
}

var (
	locationScheme = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://|\bwww\.|@[a-z0-9-]+\.`)
	locationSite   = regexp.MustCompile(`(?i)\b[a-z0-9-]+\.(com|net|org|info|io|tv|me|cc|co|to|ws|biz|xyz|ru|fr|de|uk|nl|se|es|it|pl|eu|in|cn|jp|kr)\b`)
	locationFile   = regexp.MustCompile(`(?i)\.(mkv|mp4|m4v|avi|ts|m2ts|mov|wmv|webm|mpg|mpeg|vob|iso|srt|ass|ssa|sub|idx|sup|vtt|nfo|txt|jpg|png|mp3|flac|aac|ac3|dts)$`)
)

// looksLikeLocation says whether a chapter name looks like a file name,
// a path, an address or a site rather than a chapter's name.
func looksLikeLocation(s string) bool {
	return strings.ContainsAny(s, `/\`) || locationScheme.MatchString(s) || locationSite.MatchString(s) || locationFile.MatchString(s)
}
