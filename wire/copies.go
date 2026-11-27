package wire

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
)

// The copies of a film, an episode or an extra (MSFP Media video 3.2
// to 3.8): what an owning server sends, and what a viewer's server keeps
// of them. The types are both: their JSON is the wire's, written with
// Marshal; a copy read with ReadCopy is in bounds, and written again
// unchanged.

// Copy is one version of a film, an episode or an extra (MSFP Media video
// 3.2). AudioTracks is required, and may be empty, never nil (Marshal
// would leave a nil list out). A pointer field, and a zero number where
// 0 is out of range, is absent. A copy carries no name, no bitrate and
// no date: a viewer's server builds a label from its facts, the overall
// rate is Size × 8 / DurationMs, and "recently added" counts from the
// day it first saw the copy.
type Copy struct {
	ID             string         `json:"id"`
	Edition        string         `json:"edition,omitempty"`
	DurationMs     int64          `json:"durationMs"`
	StartMs        *int64         `json:"startMs,omitempty"`
	EndMs          *int64         `json:"endMs,omitempty"`
	MediaType      string         `json:"mediaType"`
	Size           int64          `json:"size"`
	ETag           string         `json:"etag"`
	Parts          int            `json:"parts,omitempty"`
	Video          *VideoStream   `json:"video,omitempty"`
	AudioTracks    []CopyAudio    `json:"audioTracks"`
	SubtitleTracks []CopySubtitle `json:"subtitleTracks,omitempty"`
	Chapters       []CopyChapter  `json:"chapters,omitempty"`
	Segments       []CopySegment  `json:"segments,omitempty"`
	PreviewLayout  *PreviewLayout `json:"previewLayout,omitempty"`
	ThreeD         string         `json:"threeD,omitempty"`
}

// VideoStream is a copy's picture (MSFP Media video 3.3). Codec is the
// registry's token, open; Codecs the stream's RFC 6381 codecs string
// (required for h264, hevc, av1 and vp9: RequiresCodecs; ReadCodecs
// reads it); FPS "<num>/<den>" reduced; SampleAspect "<w>:<h>", absent
// for square pixels; Rotation 0, 90, 180 or 270 degrees clockwise, nil
// (absent) meaning 0; RefFrames, on h264 only, its max_num_ref_frames.
// The dynamic range is Colour's (its Transfer: 16 PQ, 18 HLG); HDR lists
// only enhancement layers and dynamic metadata (hdr10plus), tokens
// sorted in byte order; Dolby Vision is DV alone.
type VideoStream struct {
	Codec        string       `json:"codec"`
	Codecs       string       `json:"codecs,omitempty"`
	Width        int          `json:"width"`
	Height       int          `json:"height"`
	SampleAspect string       `json:"sampleAspect,omitempty"`
	Rotation     *int         `json:"rotation,omitempty"`
	BitDepth     int          `json:"bitDepth,omitempty"`
	Colour       *VideoColour `json:"colour,omitempty"`
	HDR          []string     `json:"hdr,omitempty"`
	DV           *VideoDV     `json:"dv,omitempty"`
	FPS          string       `json:"fps,omitempty"`
	RefFrames    int          `json:"refFrames,omitempty"`
	Interlaced   bool         `json:"interlaced,omitempty"`
}

// VideoDV is a Dolby Vision picture's configuration record (MSFP Media video
// 3.6), sent whenever the copy has Dolby Vision: the profile (0
// to 127), the level (0 to 63), the base-layer compatibility ID (0 to
// 15), and whether an enhancement layer is present (nil: not said).
type VideoDV struct {
	Profile       int   `json:"profile"`
	Level         int   `json:"level"`
	Compatibility int   `json:"compatibility"`
	EL            *bool `json:"el,omitempty"`
}

// VideoColour is a picture's colour description (MSFP Media video 3.6):
// ITU-T H.273 code points, each 0 to 255 and never 2 ("unspecified"),
// nil when absent; FullRange H.273's full-range flag, nil when unknown.
// Transfer is the preferred transfer (the alternative transfer
// characteristics SEI's when the stream has one).
type VideoColour struct {
	Primaries *int  `json:"primaries,omitempty"`
	Transfer  *int  `json:"transfer,omitempty"`
	Matrix    *int  `json:"matrix,omitempty"`
	FullRange *bool `json:"fullRange,omitempty"`
}

// H.273's transfer characteristics that make a picture's range (MSFP Media video
// 3.6).
const (
	TransferBT709 = 1
	TransferPQ    = 16
	TransferHLG   = 18
)

// CopyAudio is a copy's audio track (MSFP Media video 3.3), with the a<n>
// ID play takes. Codec is the registry's token, Role a value of the DASH
// role scheme (main, dub, commentary, description…), Objects the
// object-based format it carries (dolby-atmos, dts-x), all open; Codecs
// its RFC 6381 codecs string (required for aac and dts).
type CopyAudio struct {
	ID         string `json:"id"`
	Language   string `json:"language,omitempty"`
	Channels   int    `json:"channels,omitempty"`
	Role       string `json:"role,omitempty"`
	Default    bool   `json:"default,omitempty"`
	Forced     bool   `json:"forced,omitempty"`
	Codec      string `json:"codec"`
	Codecs     string `json:"codecs,omitempty"`
	Objects    string `json:"objects,omitempty"`
	SampleRate int    `json:"sampleRate,omitempty"`
	BitDepth   int    `json:"bitDepth,omitempty"`
	Original   bool   `json:"original,omitempty"`
}

// CopySubtitle is a copy's subtitle track (MSFP Media video 3.3), with its
// s<n> ID. Format is the registry's token (subrip, ass,
// hdmv_pgs_subtitle, dvd_subtitle…), open, "" when not known; Role a
// value of the DASH role scheme (subtitle, commentary, easyreader…).
type CopySubtitle struct {
	ID              string `json:"id"`
	Language        string `json:"language,omitempty"`
	Role            string `json:"role,omitempty"`
	Default         bool   `json:"default,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	HearingImpaired bool   `json:"hearingImpaired,omitempty"`
	Format          string `json:"format,omitempty"`
	External        bool   `json:"external,omitempty"`
}

// CopyChapter is a chapter of a copy (MSFP Media video 3.4).
type CopyChapter struct {
	StartMs int64  `json:"startMs"`
	Name    string `json:"name,omitempty"`
}

// CopySegment is a skip marker of a copy (MSFP Media video 3.4): Kind is
// open (intro, outro, recap, preview, commercial…); a viewer's server
// shows a kind it doesn't know as a plain "Skip". The last outro is the
// final credits.
type CopySegment struct {
	Kind    string `json:"kind"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

// The bounds of MSFP Media video 3.8.
const (
	MaxCopies         = 16
	MaxCopyDurationMs = 8_640_000_000 // 100 days
	MaxCopySize       = 999_999_999_999_999
	MaxCopyParts      = 16
	MaxAudioTracks    = 32
	MaxSubtitleTracks = 64
	MaxChapters       = 256
	MaxSegments       = 32
	MaxHDR            = 8
	MaxChapterName    = 64
	MaxBitrateKbps    = 10_000_000
	MaxVideoSide      = 100_000
	MaxRefFrames      = 16
)

var (
	copyCodecsRe = regexp.MustCompile(`^[A-Za-z0-9.+-]{1,64}$`)
	copyRatioRe  = regexp.MustCompile(`^([1-9][0-9]{0,6})([:/])([1-9][0-9]{0,6})$`)
)

// CodecsString says whether s is of a codecs string's form (MSFP Media video
// 3.5): 1 to 64 characters of A–Z a–z 0–9 . + -.
func CodecsString(s string) bool { return copyCodecsRe.MatchString(s) }

// ReadCopy reads one entry of a title's copies as MSFP Media video 3.8
// says: an error, to skip it, when it isn't an object, when its id or
// etag isn't of an ID's form, when durationMs or size isn't an integer
// in range, when mediaType isn't a media type (MSFP Media base 3), or
// when audioTracks isn't an array. Open values (mediaType, the codecs,
// edition, role, format, objects, segment kinds, hdr, threeD) are kept
// as they are when well formed, known or not. Its other members, out of
// range or of the wrong type, count as absent alone; a bad entry of one
// of its lists is skipped alone; members of earlier drafts (name,
// container, bitrateKbps, added, previews, a track's title…) are
// ignored. Whether its id repeats an earlier copy's is the title's to
// check (ReadVideoTitle does).
func ReadCopy(raw json.RawMessage) (Copy, error) {
	tree, err := strictTree(raw, true)
	if err != nil {
		return Copy{}, err
	}
	return readCopy(tree)
}

// readCopies keeps of a title's copies the first MaxCopies good ones,
// each id once (MSFP Media video 3.8).
func readCopies(list []any) []Copy {
	var out []Copy
	for _, e := range list {
		if len(out) == MaxCopies {
			break
		}
		c, err := readCopy(e)
		if err != nil || slices.ContainsFunc(out, func(o Copy) bool { return o.ID == c.ID }) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func readCopy(v any) (Copy, error) {
	o, ok := v.(map[string]any)
	if !ok {
		return Copy{}, errors.New("not an object")
	}
	id, _ := o["id"].(string)
	if !ID(id) {
		return Copy{}, errors.New("id isn't 1 to 64 characters of A-Z a-z 0-9 _ -")
	}
	duration, ok := videoInt(o["durationMs"], 0, MaxCopyDurationMs)
	if !ok {
		return Copy{}, errors.New("durationMs isn't an integer from 0 to 8,640,000,000")
	}
	mediaType := mediaTypeOf(o["mediaType"])
	if mediaType == "" {
		return Copy{}, errors.New("mediaType missing or not a media type")
	}
	size, ok := videoInt(o["size"], 1, MaxCopySize)
	if !ok {
		return Copy{}, errors.New("size isn't an integer from 1 to 999,999,999,999,999")
	}
	etag, _ := o["etag"].(string)
	if !ID(etag) {
		return Copy{}, errors.New("etag isn't 1 to 64 characters of A-Z a-z 0-9 _ -")
	}
	audio, ok := o["audioTracks"].([]any)
	if !ok {
		return Copy{}, errors.New("audioTracks isn't an array")
	}
	c := Copy{ID: id, DurationMs: duration, MediaType: mediaType, Size: size, ETag: etag}
	c.Edition = tokenOf(o["edition"])
	from := int64(0)
	if n, ok := videoInt(o["startMs"], 0, duration); ok {
		c.StartMs, from = &n, n
	}
	if n, ok := videoInt(o["endMs"], from+1, duration); ok {
		c.EndMs = &n
	}
	if n, ok := videoInt(o["parts"], 2, MaxCopyParts); ok {
		c.Parts = int(n)
	}
	c.Video = readVideoStream(o["video"])
	c.AudioTracks = readAudioTracks(audio)
	c.SubtitleTracks = readSubtitleTracks(o["subtitleTracks"])
	c.Chapters = readChapters(o["chapters"], duration)
	c.Segments = readSegments(o["segments"], duration)
	c.PreviewLayout = readPreviewLayout(o["previewLayout"])
	c.ThreeD = tokenOf(o["threeD"])
	return c, nil
}

// FileCount is how many files the copy is made of: Parts, 1 when absent.
func (c Copy) FileCount() int { return max(c.Parts, 1) }

// readVideoStream: nil, absent, unless it is an object whose codec is a
// token and whose width and height are integers in range; its other
// members count as absent alone.
func readVideoStream(v any) *VideoStream {
	o, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	codec := tokenOf(o["codec"])
	w, okW := videoInt(o["width"], 1, MaxVideoSide)
	h, okH := videoInt(o["height"], 1, MaxVideoSide)
	if codec == "" || !okW || !okH {
		return nil
	}
	s := &VideoStream{Codec: codec, Width: int(w), Height: int(h)}
	s.Codecs = videoForm(o["codecs"], copyCodecsRe)
	s.SampleAspect = ratio(o["sampleAspect"], ":", MaxSampleAspect, false)
	if r, ok := videoInt(o["rotation"], 0, 270); ok && r%90 == 0 {
		n := int(r)
		s.Rotation = &n
	}
	if b, ok := videoInt(o["bitDepth"], 1, 16); ok {
		s.BitDepth = int(b)
	}
	if c, ok := o["colour"].(map[string]any); ok {
		col := VideoColour{Primaries: h273(c["primaries"]), Transfer: h273(c["transfer"]), Matrix: h273(c["matrix"]),
			FullRange: boolOf(c["fullRange"])}
		if col != (VideoColour{}) {
			s.Colour = &col
		}
	}
	if _, ok := o["hdr"].([]any); ok {
		s.HDR = CleanHDR(videoStrings(o["hdr"]))
	}
	if dv, ok := o["dv"].(map[string]any); ok {
		p, ok1 := videoInt(dv["profile"], 0, 127)
		l, ok2 := videoInt(dv["level"], 0, 63)
		c, ok3 := videoInt(dv["compatibility"], 0, 15)
		if ok1 && ok2 && ok3 {
			s.DV = &VideoDV{Profile: int(p), Level: int(l), Compatibility: int(c), EL: boolOf(dv["el"])}
		}
	}
	s.FPS = ratio(o["fps"], "/", MaxFPSTerm, true)
	if codec == "h264" {
		s.RefFrames = int(videoIntOr0(o["refFrames"], 1, MaxRefFrames))
	}
	s.Interlaced = o["interlaced"] == true
	return s
}

// h273 is an H.273 code point, 0 to 255, 2 ("unspecified") counting as
// absent (MSFP Media video 3.6); nil when absent.
func h273(v any) *int {
	n, ok := videoInt(v, 0, 255)
	if !ok || n == 2 {
		return nil
	}
	i := int(n)
	return &i
}

// The bounds of a picture's ratios (MSFP Media video 3.3): each side of a
// sampleAspect, each term of an fps.
const (
	MaxSampleAspect = 65_535
	MaxFPSTerm      = 1_000_000
)

// ratio is v when it is a string "<a><sep><b>", each a number from 1 to
// top without leading zeros, and, when reduced is asked, a fraction in
// lowest terms; else "".
func ratio(v any, sep string, top int, reduced bool) string {
	s, _ := v.(string)
	m := copyRatioRe.FindStringSubmatch(s)
	if m == nil || m[2] != sep {
		return ""
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[3])
	if a > top || b > top || reduced && gcd(a, b) != 1 {
		return ""
	}
	return s
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// boolOf is v when it is a boolean, nil otherwise.
func boolOf(v any) *bool {
	if b, ok := v.(bool); ok {
		return &b
	}
	return nil
}

// CleanHDR keeps of a stream's hdr entries the tokens, each once, sorted
// in byte order, the first MaxHDR (MSFP Media video 3.8); none when nothing
// is left.
func CleanHDR(entries []string) []string {
	var out []string
	for _, e := range entries {
		if Token(e) && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	slices.Sort(out)
	if len(out) > MaxHDR {
		out = out[:MaxHDR]
	}
	return out
}

// HighDynamicRange says whether a picture is of high dynamic range
// (MSFP Media video 3.6): a transfer of 16 (PQ) or 18 (HLG), or Dolby
// Vision. A picture with no colour says nothing of its range: false.
func (v VideoStream) HighDynamicRange() bool {
	if v.DV != nil {
		return true
	}
	return v.Colour != nil && v.Colour.Transfer != nil && (*v.Colour.Transfer == TransferPQ || *v.Colour.Transfer == TransferHLG)
}

// readAudioTracks keeps the first MaxAudioTracks good tracks; never nil.
func readAudioTracks(list []any) []CopyAudio {
	out := []CopyAudio{}
	for _, e := range list {
		o, ok := e.(map[string]any)
		if !ok || len(out) == MaxAudioTracks {
			continue
		}
		id, _ := o["id"].(string)
		codec := tokenOf(o["codec"])
		if _, okID := TrackID(id, 'a'); !okID || codec == "" || slices.ContainsFunc(out, func(t CopyAudio) bool { return t.ID == id }) {
			continue
		}
		t := CopyAudio{
			ID:       id,
			Language: videoTag(o["language"]),
			Role:     tokenOf(o["role"]),
			Default:  o["default"] == true,
			Forced:   o["forced"] == true,
			Codec:    codec,
			Codecs:   videoForm(o["codecs"], copyCodecsRe),
			Objects:  tokenOf(o["objects"]),
			Original: o["original"] == true,
		}
		t.Channels = int(videoIntOr0(o["channels"], 1, 64))
		t.SampleRate = int(videoIntOr0(o["sampleRate"], 8_000, 768_000))
		t.BitDepth = int(videoIntOr0(o["bitDepth"], 8, 32))
		out = append(out, t)
	}
	return out
}

// readSubtitleTracks keeps the first MaxSubtitleTracks good tracks; none
// when v isn't an array.
func readSubtitleTracks(v any) []CopySubtitle {
	list, _ := v.([]any)
	var out []CopySubtitle
	for _, e := range list {
		o, ok := e.(map[string]any)
		if !ok || len(out) == MaxSubtitleTracks {
			continue
		}
		id, _ := o["id"].(string)
		if _, ok := TrackID(id, 's'); !ok || slices.ContainsFunc(out, func(t CopySubtitle) bool { return t.ID == id }) {
			continue
		}
		out = append(out, CopySubtitle{
			ID:              id,
			Language:        videoTag(o["language"]),
			Role:            tokenOf(o["role"]),
			Default:         o["default"] == true,
			Forced:          o["forced"] == true,
			HearingImpaired: o["hearingImpaired"] == true,
			Format:          tokenOf(o["format"]),
			External:        o["external"] == true,
		})
	}
	return out
}

// readChapters keeps, in order, the chapters whose start is in the copy
// and after the one before, the first MaxChapters.
func readChapters(v any, duration int64) []CopyChapter {
	list, _ := v.([]any)
	var out []CopyChapter
	for _, e := range list {
		o, ok := e.(map[string]any)
		if !ok || len(out) == MaxChapters {
			continue
		}
		start, ok := videoInt(o["startMs"], 0, duration)
		if !ok || len(out) > 0 && start <= out[len(out)-1].StartMs {
			continue
		}
		c := CopyChapter{StartMs: start}
		if s, ok := o["name"].(string); ok {
			c.Name = CleanText(s, MaxChapterName)
		}
		out = append(out, c)
	}
	return out
}

// readSegments keeps the segments whose kind is a token, known or not,
// and whose times are in the copy and in order, the first MaxSegments.
func readSegments(v any, duration int64) []CopySegment {
	list, _ := v.([]any)
	var out []CopySegment
	for _, e := range list {
		o, ok := e.(map[string]any)
		if !ok || len(out) == MaxSegments {
			continue
		}
		kind := tokenOf(o["kind"])
		start, ok1 := videoInt(o["startMs"], 0, duration)
		end, ok2 := videoInt(o["endMs"], 0, duration)
		if kind == "" || !ok1 || !ok2 || start >= end {
			continue
		}
		out = append(out, CopySegment{Kind: kind, StartMs: start, EndMs: end})
	}
	return out
}

// videoIntOr0 is videoInt, 0 for absent (lo is above 0).
func videoIntOr0(v any, lo, hi int64) int64 {
	n, ok := videoInt(v, lo, hi)
	if !ok {
		return 0
	}
	return n
}

// videoForm is v when it is a string matching re, else "".
func videoForm(v any, re *regexp.Regexp) string {
	if s, ok := v.(string); ok && re.MatchString(s) {
		return s
	}
	return ""
}

// videoTag is v when it is a valid tag (LanguageTag), never converted;
// else "".
func videoTag(v any) string {
	if s, ok := v.(string); ok && LanguageTag(s) {
		return s
	}
	return ""
}
