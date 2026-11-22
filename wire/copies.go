package wire

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// The copies of a film, an episode or an extra (MSFP video sections 3.2
// to 3.8): what an owning server sends, and what a viewer's server keeps
// of them. The types are both: their JSON is the wire's, written with
// Marshal; a copy read with ReadCopy is in bounds, and written again
// unchanged.

// Copy is one version of a film, an episode or an extra (video section
// 3.2). AudioTracks is required, and may be empty, never nil (Marshal
// would leave a nil list out). A pointer field, and a zero number where
// 0 is out of range, is absent.
type Copy struct {
	ID             string         `json:"id"`
	Name           string         `json:"name,omitempty"`
	Edition        string         `json:"edition,omitempty"`
	DurationMs     int64          `json:"durationMs"`
	StartMs        *int64         `json:"startMs,omitempty"`
	Container      string         `json:"container"`
	Size           int64          `json:"size"`
	ETag           string         `json:"etag"`
	Parts          []CopyPart     `json:"parts,omitempty"`
	BitrateKbps    int64          `json:"bitrateKbps,omitempty"`
	Video          *VideoStream   `json:"video,omitempty"`
	AudioTracks    []CopyAudio    `json:"audioTracks"`
	SubtitleTracks []CopySubtitle `json:"subtitleTracks,omitempty"`
	Chapters       []CopyChapter  `json:"chapters,omitempty"`
	Segments       []CopySegment  `json:"segments,omitempty"`
	Previews       bool           `json:"previews,omitempty"`
	Added          string         `json:"added,omitempty"`
	ThreeD         string         `json:"threeD,omitempty"`
}

// CopyPart is one file of a copy made of several (video section 3.2).
type CopyPart struct {
	Size       int64 `json:"size"`
	DurationMs int64 `json:"durationMs"`
}

// VideoStream is a copy's picture (video section 3.3).
type VideoStream struct {
	Codec      string       `json:"codec"`
	CodecTag   string       `json:"codecTag,omitempty"`
	Profile    string       `json:"profile,omitempty"`
	Level      *float64     `json:"level,omitempty"`
	Width      int          `json:"width"`
	Height     int          `json:"height"`
	BitDepth   int          `json:"bitDepth,omitempty"`
	HDR        []string     `json:"hdr,omitempty"`
	DV         *VideoDV     `json:"dv,omitempty"`
	FPS        float64      `json:"fps,omitempty"`
	Interlaced bool         `json:"interlaced,omitempty"`
	Colour     *VideoColour `json:"colour,omitempty"`
}

// VideoDV is a Dolby Vision picture's profile (0 to 10), level (0 to
// 13) and base-layer compatibility ID (0 to 15), with dv in its hdr.
type VideoDV struct {
	Profile       int `json:"profile"`
	Level         int `json:"level"`
	Compatibility int `json:"compatibility"`
}

// VideoColour is a picture's colour description, as ffmpeg names it;
// Range is tv or pc.
type VideoColour struct {
	Primaries string `json:"primaries,omitempty"`
	Transfer  string `json:"transfer,omitempty"`
	Matrix    string `json:"matrix,omitempty"`
	Range     string `json:"range,omitempty"`
}

// CopyAudio is a copy's audio track (video section 3.3), with the a<n>
// ID play gives it.
type CopyAudio struct {
	ID         string `json:"id"`
	Language   string `json:"language,omitempty"`
	Channels   int    `json:"channels,omitempty"`
	Role       string `json:"role,omitempty"`
	Default    bool   `json:"default,omitempty"`
	Forced     bool   `json:"forced,omitempty"`
	FileIndex  *int   `json:"fileIndex,omitempty"`
	Codec      string `json:"codec"`
	Profile    string `json:"profile,omitempty"`
	Layout     string `json:"layout,omitempty"`
	SampleRate int    `json:"sampleRate,omitempty"`
	BitDepth   int    `json:"bitDepth,omitempty"`
	Title      string `json:"title,omitempty"`
	Original   bool   `json:"original,omitempty"`
}

// CopySubtitle is a copy's subtitle track (video section 3.3), with the
// s<n> ID play gives it.
type CopySubtitle struct {
	ID              string `json:"id"`
	Language        string `json:"language,omitempty"`
	Default         bool   `json:"default,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	HearingImpaired bool   `json:"hearingImpaired,omitempty"`
	Format          string `json:"format"`
	External        bool   `json:"external,omitempty"`
	FileIndex       *int   `json:"fileIndex,omitempty"`
	Title           string `json:"title,omitempty"`
}

// CopyChapter is a chapter of a copy (video section 3.4).
type CopyChapter struct {
	StartMs int64  `json:"startMs"`
	Name    string `json:"name,omitempty"`
}

// CopySegment is a skip marker of a copy (video section 3.4): Kind is
// one of SegmentKinds.
type CopySegment struct {
	Kind    string `json:"kind"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
	Final   bool   `json:"final,omitempty"`
}

// The bounds of video section 3.8.
const (
	MaxCopies         = 8
	MaxCopyDurationMs = 8_640_000_000 // 100 days
	MaxCopySize       = 999_999_999_999_999
	MaxCopyParts      = 16
	MaxAudioTracks    = 32
	MaxSubtitleTracks = 64
	MaxChapters       = 256
	MaxSegments       = 32
	MaxCopyName       = 32 // a copy's name, a track's title, in code points
	MaxChapterName    = 64
	MaxBitrateKbps    = 10_000_000
	MaxVideoSide      = 100_000
)

// The other closed lists of a copy (video sections 3.2 to 3.4).
var (
	ThreeDModes  = []string{"sbs", "tab", "mvc"}
	AudioRoles   = []string{"commentary", "description"}
	SegmentKinds = []string{"intro", "outro", "recap", "preview", "commercial"}
)

var (
	copyCodecTagRe = regexp.MustCompile(`^[A-Za-z0-9]{1,8}$`)
	copyLayoutRe   = regexp.MustCompile(`^[A-Za-z0-9()._-]{1,32}$`)
	copyColourRe   = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	copyDateRe     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

// ReadCopy reads one entry of a title's copies as video section 3.8
// says: an error, to skip it, when it isn't an object, when its id or
// etag isn't of an ID's form, when durationMs or size isn't an integer
// in range, when container isn't a string, or when audioTracks isn't an
// array. Its other members, out of range or of the wrong type, count as
// absent alone; a bad entry of one of its lists is skipped alone.
// Whether its id repeats an earlier copy's is the title's to check
// (ReadVideoTitle does).
func ReadCopy(raw json.RawMessage) (Copy, error) {
	tree, err := strictTree(raw, true)
	if err != nil {
		return Copy{}, err
	}
	return readCopy(tree)
}

// readCopies keeps of a title's copies the first MaxCopies good ones,
// each id once (video section 3.8).
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
	container, ok := o["container"].(string)
	if !ok {
		return Copy{}, errors.New("container isn't a string")
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
	c := Copy{ID: id, DurationMs: duration, Container: NameOrOther(Containers, container), Size: size, ETag: etag}
	if s, ok := o["name"].(string); ok {
		c.Name = CleanText(s, MaxCopyName)
	}
	if s, ok := o["edition"].(string); ok {
		c.Edition = NameOrOther(Editions, s)
	}
	if n, ok := videoInt(o["startMs"], 0, duration); ok {
		c.StartMs = &n
	}
	c.Parts = readParts(o["parts"], size)
	if n, ok := videoInt(o["bitrateKbps"], 500, MaxBitrateKbps); ok {
		c.BitrateKbps = n / 500 * 500
	}
	c.Video = readVideoStream(o["video"])
	c.AudioTracks = readAudioTracks(audio)
	c.SubtitleTracks = readSubtitleTracks(o["subtitleTracks"])
	c.Chapters = readChapters(o["chapters"], duration)
	c.Segments = readSegments(o["segments"], duration)
	c.Previews = o["previews"] == true
	if s, ok := o["added"].(string); ok && copyDateRe.MatchString(s) {
		if _, err := time.Parse(time.DateOnly, s); err == nil {
			c.Added = s
		}
	}
	c.ThreeD = videoOneOf(o["threeD"], ThreeDModes)
	return c, nil
}

// readParts: none unless there are 1 to MaxCopyParts parts, each in
// range, whose sizes add up to size.
func readParts(v any, size int64) []CopyPart {
	list, _ := v.([]any)
	if len(list) == 0 || len(list) > MaxCopyParts {
		return nil
	}
	var out []CopyPart
	var sum int64
	for _, e := range list {
		o, _ := e.(map[string]any)
		s, ok1 := videoInt(o["size"], 1, MaxCopySize)
		d, ok2 := videoInt(o["durationMs"], 0, MaxCopyDurationMs)
		if !ok1 || !ok2 {
			return nil
		}
		sum += s
		out = append(out, CopyPart{Size: s, DurationMs: d})
	}
	if sum != size {
		return nil
	}
	return out
}

// readVideoStream: nil, absent, unless it is an object whose codec is a
// string and whose width and height are integers in range; its other
// members count as absent alone.
func readVideoStream(v any) *VideoStream {
	o, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	codec, ok := o["codec"].(string)
	w, okW := videoInt(o["width"], 1, MaxVideoSide)
	h, okH := videoInt(o["height"], 1, MaxVideoSide)
	if !ok || !okW || !okH {
		return nil
	}
	s := &VideoStream{Codec: NameOrOther(VideoCodecs, codec), Width: int(w), Height: int(h)}
	s.CodecTag = videoForm(o["codecTag"], copyCodecTagRe)
	if p, ok := o["profile"].(string); ok && p != "" && len([]rune(p)) <= 32 {
		s.Profile = p
	}
	if f, ok := videoNumber(o["level"]); ok && f >= 0 && f <= 1000 {
		s.Level = &f
	}
	if b, ok := videoInt(o["bitDepth"], 8, 12); ok && b%2 == 0 {
		s.BitDepth = int(b)
	}
	if _, ok := o["hdr"].([]any); ok {
		s.HDR = CleanHDR(videoStrings(o["hdr"]))
	}
	if dv, ok := o["dv"].(map[string]any); ok && slices.Contains(s.HDR, "dv") {
		p, ok1 := videoInt(dv["profile"], 0, 10)
		l, ok2 := videoInt(dv["level"], 0, 13)
		c, ok3 := videoInt(dv["compatibility"], 0, 15)
		if ok1 && ok2 && ok3 {
			s.DV = &VideoDV{Profile: int(p), Level: int(l), Compatibility: int(c)}
		}
	}
	if f, ok := videoNumber(o["fps"]); ok {
		if r := math.Round(f*100) / 100; r > 0 && r <= 1000 {
			s.FPS = r
		}
	}
	s.Interlaced = o["interlaced"] == true
	if c, ok := o["colour"].(map[string]any); ok {
		col := VideoColour{
			Primaries: videoForm(c["primaries"], copyColourRe),
			Transfer:  videoForm(c["transfer"], copyColourRe),
			Matrix:    videoForm(c["matrix"], copyColourRe),
			Range:     videoOneOf(c["range"], []string{"tv", "pc"}),
		}
		if col != (VideoColour{}) {
			s.Colour = &col
		}
	}
	return s
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
		codec, ok := o["codec"].(string)
		if _, okID := TrackID(id, 'a'); !okID || !ok || slices.ContainsFunc(out, func(t CopyAudio) bool { return t.ID == id }) {
			continue
		}
		t := CopyAudio{
			ID:       id,
			Language: videoTag(o["language"]),
			Role:     videoOneOf(o["role"], AudioRoles),
			Default:  o["default"] == true,
			Forced:   o["forced"] == true,
			Codec:    NameOrOther(AudioCodecs, codec),
			Profile:  videoOneOf(o["profile"], AudioProfiles),
			Layout:   videoForm(o["layout"], copyLayoutRe),
			Title:    videoTitleText(o["title"]),
			Original: o["original"] == true,
		}
		t.Channels = int(videoIntOr0(o["channels"], 1, 64))
		t.FileIndex = videoIndex(o["fileIndex"])
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
		format, _ := o["format"].(string)
		out = append(out, CopySubtitle{
			ID:              id,
			Language:        videoTag(o["language"]),
			Default:         o["default"] == true,
			Forced:          o["forced"] == true,
			HearingImpaired: o["hearingImpaired"] == true,
			Format:          NameOrOther(SubtitleFormats, format),
			External:        o["external"] == true,
			FileIndex:       videoIndex(o["fileIndex"]),
			Title:           videoTitleText(o["title"]),
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

// readSegments keeps the segments of a known kind whose times are in the
// copy and in order, the first MaxSegments.
func readSegments(v any, duration int64) []CopySegment {
	list, _ := v.([]any)
	var out []CopySegment
	for _, e := range list {
		o, ok := e.(map[string]any)
		if !ok || len(out) == MaxSegments {
			continue
		}
		kind := videoOneOf(o["kind"], SegmentKinds)
		start, ok1 := videoInt(o["startMs"], 0, duration)
		end, ok2 := videoInt(o["endMs"], 0, duration)
		if kind == "" || !ok1 || !ok2 || start >= end {
			continue
		}
		out = append(out, CopySegment{Kind: kind, StartMs: start, EndMs: end, Final: o["final"] == true})
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

// videoIndex is a track's fileIndex, 0 to 999; nil when absent.
func videoIndex(v any) *int {
	n, ok := videoInt(v, 0, 999)
	if !ok {
		return nil
	}
	i := int(n)
	return &i
}

// videoNumber reads any JSON number, as a float.
func videoNumber(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(n), 64)
	return f, err == nil
}

// videoForm is v when it is a string matching re, else "".
func videoForm(v any, re *regexp.Regexp) string {
	if s, ok := v.(string); ok && re.MatchString(s) {
		return s
	}
	return ""
}

// videoOneOf is v when it is a string of list, else "".
func videoOneOf(v any, list []string) string {
	if s, ok := v.(string); ok && slices.Contains(list, s) {
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

// videoTitleText is a track's title made plain text and cut (video
// section 3.6); "" when it isn't a string or nothing is left.
func videoTitleText(v any) string {
	s, _ := v.(string)
	return CleanText(s, MaxCopyName)
}

// BetterCopy says whether a comes before b in a title's copies (video
// section 3.7): of a greater height, a picture wider than 16:9 counted as
// a 16:9 one as wide (3840 × 1600 as 2160), then with HDR when b has
// none, then with more hdr formats; a copy without video after those
// with one. Copies equal on all three keep the owning server's order:
// sort with slices.SortStableFunc (SortCopies).
func BetterCopy(a, b Copy) bool { return copyRank(a, b) < 0 }

// SortCopies puts a title's copies in the order of video section 3.7,
// keeping the owning server's own order between equal ones.
func SortCopies(copies []Copy) { slices.SortStableFunc(copies, copyRank) }

func copyRank(a, b Copy) int {
	switch {
	case a.Video == nil || b.Video == nil:
		return boolRank(a.Video != nil, b.Video != nil)
	case copyHeight(*a.Video) != copyHeight(*b.Video):
		return copyHeight(*b.Video) - copyHeight(*a.Video)
	case (len(a.Video.HDR) > 0) != (len(b.Video.HDR) > 0):
		return boolRank(len(a.Video.HDR) > 0, len(b.Video.HDR) > 0)
	}
	return len(b.Video.HDR) - len(a.Video.HDR)
}

// boolRank orders true before false.
func boolRank(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

// copyHeight is the height a picture counts as in the order of copies.
func copyHeight(v VideoStream) int {
	if v.Width*9 > v.Height*16 {
		return v.Width * 9 / 16
	}
	return v.Height
}
