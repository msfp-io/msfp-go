package wire

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Music titles (MSFP Media music 2 and 3): their keys, and what a
// server keeps of a music title a peer lists in titles, its copies
// included (MSFP Media listing 3.3, MSFP Media music 3.2 and 3.3). The queue
// of a play is in music_queue.go, the lyrics call in music_lyrics.go.

// mbidRe is a MusicBrainz ID in its one form: lowercase 8-4-4-4-12 hex.
var mbidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// MBID says whether s is a MusicBrainz ID in the form MSFP Media music 2.1
// gives: 36 characters, lowercase. An AcoustID has the same form.
func MBID(s string) bool { return mbidRe.MatchString(s) }

// MusicKey is a music title key, parsed (MSFP Media music 2): its kind
// (artist, album or track), its scheme, and the scheme's part. Under mb,
// ID is an MBID: an artist's, a release's for an album, a release
// track's for a track. Under local, it is the owning server's opaque
// part (OpaqueKeyPart).
type MusicKey struct {
	Kind   string // artist, album or track
	Scheme string // mb or local
	ID     string
}

// ParseMusicKey reads a music title key in its one canonical form,
// <kind>/mb/<mbid> or <kind>/local/<opaque>; false for anything else, an
// MBID in uppercase or a trailing slash included.
func ParseMusicKey(key string) (MusicKey, bool) {
	kind, rest, ok := strings.Cut(key, "/")
	if !ok || (kind != "artist" && kind != "album" && kind != "track") {
		return MusicKey{}, false
	}
	scheme, id, ok := strings.Cut(rest, "/")
	switch {
	case !ok:
		return MusicKey{}, false
	case scheme == "mb" && MBID(id), scheme == "local" && OpaqueKeyPart(id):
		return MusicKey{Kind: kind, Scheme: scheme, ID: id}, true
	}
	return MusicKey{}, false
}

// IsMusicKey says whether s is a canonical music key of that kind, of
// either scheme.
func IsMusicKey(s, kind string) bool {
	k, ok := ParseMusicKey(s)
	return ok && k.Kind == kind
}

// String is the key, as ParseMusicKey reads it.
func (k MusicKey) String() string { return k.Kind + "/" + k.Scheme + "/" + k.ID }

// Playable says whether play takes the title: a track (MSFP Media music 5).
func (k MusicKey) Playable() bool { return k.Kind == "track" }

// Local says whether the key is under the local scheme: a title that
// never matches another server's (MSFP Media music 2.2).
func (k MusicKey) Local() bool { return k.Scheme == "local" }

// TitleKey names a title of any content part (MSFP Media listing 4): its
// part (PartVideo or PartMusic), its kind, and whether play takes it.
type TitleKey struct {
	Part     string
	Kind     string
	Playable bool
}

// ParseKey reads a title key of either content part of version 0, in
// its canonical form only; false for any other text.
func ParseKey(key string) (TitleKey, bool) {
	if k, ok := ParseVideoKey(key); ok {
		return TitleKey{Part: PartVideo, Kind: k.Kind, Playable: k.Playable()}, true
	}
	if k, ok := ParseMusicKey(key); ok {
		return TitleKey{Part: PartMusic, Kind: k.Kind, Playable: k.Playable()}, true
	}
	return TitleKey{}, false
}

// The bounds of a music title's description (MSFP Media music 3), lengths
// in code points.
const (
	MaxMusicName    = 256 // a name
	MaxArtistNames  = 512
	MaxMusicArtists = 16
	MaxGenres       = 8
	MaxGenre        = 64
	MaxDiscNumber   = 999 // disc and number, from 1
	MaxMusicCopies  = 16
)

// MusicDescription are the bounds ReadDescription takes for a music
// title, of either scheme: a name of at most 256 characters, required,
// and no year (music titles carry their dates in date and originalDate;
// a year is ignored).
var MusicDescription = DescriptionBounds{NameRequired: true, MaxName: MaxMusicName}

// MaxSecondaryTypes is how many secondaryTypes an album keeps (MSFP Media music
// 3.2).
const MaxSecondaryTypes = 8

// musicTypeRe is the form of an album's primaryType and of each of its
// secondaryTypes (MSFP Media music 3): MusicBrainz's release-group types,
// lowercased, 1 to 32 characters of a–z 0–9 space . _ - /, with no space
// at either end.
var musicTypeRe = regexp.MustCompile(`^[a-z0-9._/-](?:[a-z0-9 ._/-]{0,30}[a-z0-9._/-])?$`)

// MusicType says whether s is of the form of a release-group type (MSFP Media music
// 3): kept as it is, known or not; the caller doesn't lowercase.
func MusicType(s string) bool { return musicTypeRe.MatchString(s) }

// CleanText cleans a peer's text as MSFP core section 9.1 cleans a
// server's name, cut to max code points: each run of white space one
// space, other control and format characters removed, both ends trimmed,
// cut, and trimmed again. Text, never markup: whoever shows it escapes it.
func CleanText(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.Is(unicode.White_Space, r) }), " ")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

// CleanGenres keeps an album's genres as MSFP Media music 3.2 says: each
// cleaned, kept when 1 to MaxGenre characters once cleaned (a longer one
// skipped, not cut) and not a repeat of a kept one compared without
// case; the first MaxGenres kept.
func CleanGenres(genres []string) []string {
	var out []string
	for _, g := range genres {
		if len(out) == MaxGenres {
			break
		}
		g = CleanText(g, len(g))
		repeat := slices.ContainsFunc(out, func(k string) bool { return strings.EqualFold(k, g) })
		if n := utf8.RuneCountInString(g); n >= 1 && n <= MaxGenre && !repeat {
			out = append(out, g)
		}
	}
	return out
}

// musicDateRe is a music date's form: YYYY, YYYY-MM or YYYY-MM-DD.
var musicDateRe = regexp.MustCompile(`^([0-9]{4})(?:-([0-9]{2})(?:-([0-9]{2}))?)?$`)

// MusicDate reads an album's date or originalDate (MSFP Media music 3.2):
// YYYY, YYYY-MM or YYYY-MM-DD, a year from 1000 to 9999, a month from 01
// to 12, a day that exists in that month. It gives the year; false for
// anything else, which counts as absent.
func MusicDate(s string) (year int, ok bool) {
	m := musicDateRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	year, _ = strconv.Atoi(m[1])
	if year < 1000 {
		return 0, false
	}
	if m[2] != "" {
		month, _ := strconv.Atoi(m[2])
		if month < 1 || month > 12 {
			return 0, false
		}
		if m[3] != "" {
			day, _ := strconv.Atoi(m[3])
			if day < 1 || day > time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
				return 0, false
			}
		}
	}
	return year, true
}

// Other identifiers of a music title (MSFP Media music 3.1), by scheme, and
// the kind that carries each.
var musicIDs = map[string]struct {
	re   *regexp.Regexp
	kind string
}{
	"mbreleasegroup": {mbidRe, "album"},
	"mbrecording":    {mbidRe, "track"},
}

// MusicID says whether value is an identifier of a known scheme for a
// title of kind, in its form (MSFP Media music 3.1).
func MusicID(kind, scheme, value string) bool {
	s, known := musicIDs[scheme]
	return known && s.kind == kind && s.re.MatchString(value)
}

// MusicTitle is a music title as sent in titles, and as a peer's is kept
// (MSFP Media music 3): its key, kind and library, and its description,
// cleaned. Text and numbers are "" or 0 when absent; Compilation and
// Image are nil when absent. An album's PrimaryType and SecondaryTypes
// are MusicBrainz's release-group types, lowercased, open. A track may
// carry its own dates and genres (without them, its album's are its
// own); an artist carries only a name, an image and ids. The JSON is the wire's: an
// owning server writes its own titles with it, and reads them back with
// ReadMusicTitle to send only what a receiver keeps.
type MusicTitle struct {
	Key            string            `json:"key"`
	Kind           string            `json:"-"` // its key's: a title carries no kind
	Library        string            `json:"library"`
	Name           string            `json:"name"`
	Artists        []string          `json:"artists,omitempty"`
	ArtistNames    string            `json:"artistNames,omitempty"`
	PrimaryType    string            `json:"primaryType,omitempty"`
	SecondaryTypes []string          `json:"secondaryTypes,omitempty"`
	Compilation    *bool             `json:"compilation,omitempty"`
	Date           string            `json:"date,omitempty"`
	OriginalDate   string            `json:"originalDate,omitempty"`
	Genres         []string          `json:"genres,omitempty"`
	Album          string            `json:"album,omitempty"`
	Disc           int               `json:"disc,omitempty"`
	Number         int               `json:"number,omitempty"`
	Copies         []MusicCopy       `json:"copies,omitempty"` // tracks only, at least one
	Image          *bool             `json:"image,omitempty"`  // local artists and albums only
	IDs            map[string]string `json:"ids,omitempty"`
}

// MusicCopy is a track's copy (MSFP Media music 3.3). The numbers that are
// optional are 0 when absent (none of their ranges holds 0). MediaType
// is the file's media type and Codec the registry's token, both open;
// Codecs is the RFC 6381 codecs string (required for aac and dts, as on
// video: RequiresCodecs); DurationMs is exact. A copy carries no name,
// no bitrate (Size × 8 / DurationMs) and no date.
type MusicCopy struct {
	ID         string      `json:"id"`
	DurationMs int64       `json:"durationMs"`
	MediaType  string      `json:"mediaType"`
	Codec      string      `json:"codec"`
	Codecs     string      `json:"codecs,omitempty"`
	Size       int64       `json:"size"`
	Etag       string      `json:"etag"`
	SampleRate int         `json:"sampleRate,omitempty"`
	BitDepth   int         `json:"bitDepth,omitempty"`
	Channels   int         `json:"channels,omitempty"`
	ReplayGain *ReplayGain `json:"replayGain,omitempty"`
	Lyrics     string      `json:"lyrics,omitempty"` // plain or synced: the lyrics call gives them
}

// ReplayGain is a copy's loudness values, to the ReplayGain 2.0
// reference (MSFP Media music 3.3): gains in dB, peaks linear; nil when
// absent. The owning server never applies them; the viewer's player
// does, lowering the gain to keep the peak under full scale.
type ReplayGain struct {
	TrackGain *float64 `json:"trackGain,omitempty"`
	TrackPeak *float64 `json:"trackPeak,omitempty"`
	AlbumGain *float64 `json:"albumGain,omitempty"`
	AlbumPeak *float64 `json:"albumPeak,omitempty"`
}

// The bounds of a track's copy (MSFP Media music 3.3).
const (
	MaxMusicDurationMs = 86_400_000
	// MaxCopySize (wire/copies.go) bounds a track copy's size too.
	MaxGain = 64 // dB, either way
	MaxPeak = 10
)

// R128Gain converts an EBU R128 gain (Opus's R128_TRACK_GAIN: 1/256 dB,
// relative to −23 LUFS) to ReplayGain 2.0's reference, which MSFP Media music
// 3.3 asks for: the value over 256, plus 5 dB (a helper; the
// implementers' notes give the conversion).
func R128Gain(q78 int) float64 { return float64(q78)/256 + 5 }

// ReadMusicTitle reads one entry of a titles answer as MSFP Media listing
// 3.3 and MSFP Media music 3.2 say: false, to skip it, when it isn't an
// object whose key and library are strings, when the key isn't a
// canonical music key of either scheme, when it has no name once cleaned, and for a track, when it is a
// track/mb whose album isn't an album/mb key, or has no copy left (a
// copies that isn't an array included). Every other member of the wrong
// type, out of range or for another kind counts as absent; a bad entry
// of artists, secondaryTypes, genres or ids is skipped alone. Whether library is one
// asked for is the caller's to check.
func ReadMusicTitle(raw json.RawMessage) (MusicTitle, bool) {
	var t struct {
		Key            string          `json:"key"`
		Library        string          `json:"library"`
		Artists        json.RawMessage `json:"artists"`
		ArtistNames    json.RawMessage `json:"artistNames"`
		PrimaryType    json.RawMessage `json:"primaryType"`
		SecondaryTypes json.RawMessage `json:"secondaryTypes"`
		Compilation    json.RawMessage `json:"compilation"`
		Date           json.RawMessage `json:"date"`
		OriginalDate   json.RawMessage `json:"originalDate"`
		Genres         json.RawMessage `json:"genres"`
		Album          json.RawMessage `json:"album"`
		Disc           json.RawMessage `json:"disc"`
		Number         json.RawMessage `json:"number"`
		Copies         json.RawMessage `json:"copies"`
		Image          json.RawMessage `json:"image"`
		IDs            json.RawMessage `json:"ids"`
	}
	if DecodeEntry(raw, &t) != nil {
		return MusicTitle{}, false
	}
	k, ok := ParseMusicKey(t.Key)
	if !ok {
		return MusicTitle{}, false
	}
	d, ok := ReadDescription(raw, MusicDescription)
	if !ok {
		return MusicTitle{}, false
	}
	out := MusicTitle{Key: t.Key, Kind: k.Kind, Library: t.Library, Name: d.Name}
	if k.Kind != "artist" {
		for _, s := range stringsOf(t.Artists) {
			if len(out.Artists) == MaxMusicArtists {
				break
			}
			if IsMusicKey(s, "artist") && !slices.Contains(out.Artists, s) {
				out.Artists = append(out.Artists, s)
			}
		}
		names, _ := musicString(t.ArtistNames)
		out.ArtistNames = CleanText(names, MaxArtistNames)
	}
	switch k.Kind {
	case "album":
		if pt, ok := musicString(t.PrimaryType); ok && MusicType(pt) {
			out.PrimaryType = pt
		}
		for _, st := range stringsOf(t.SecondaryTypes) {
			if len(out.SecondaryTypes) == MaxSecondaryTypes {
				break
			}
			if MusicType(st) && !slices.Contains(out.SecondaryTypes, st) {
				out.SecondaryTypes = append(out.SecondaryTypes, st)
			}
		}
		out.Compilation = musicBool(t.Compilation)
	}
	if k.Kind == "album" || k.Kind == "track" {
		dateYear := 0
		if s, _ := musicString(t.Date); s != "" {
			if y, ok := MusicDate(s); ok {
				out.Date, dateYear = s, y
			}
		}
		if s, _ := musicString(t.OriginalDate); s != "" {
			if y, ok := MusicDate(s); ok && (dateYear == 0 || y <= dateYear) {
				out.OriginalDate = s
			}
		}
		out.Genres = CleanGenres(stringsOf(t.Genres))
	}
	switch k.Kind {
	case "track":
		if s, _ := musicString(t.Album); IsMusicKey(s, "album") {
			out.Album = s
		}
		if k.Scheme == "mb" && !strings.HasPrefix(out.Album, "album/mb/") {
			return MusicTitle{}, false
		}
		disc, _ := musicInt(t.Disc, 1, MaxDiscNumber)
		number, _ := musicInt(t.Number, 1, MaxDiscNumber)
		out.Disc, out.Number = int(disc), int(number)
		if out.Copies, ok = ReadMusicCopies(t.Copies); !ok || len(out.Copies) == 0 {
			return MusicTitle{}, false
		}
	}
	if k.Local() && k.Kind != "track" {
		out.Image = musicBool(t.Image)
	}
	var ids map[string]json.RawMessage // not an object: none; none on local titles
	if json.Unmarshal(t.IDs, &ids) == nil && !k.Local() {
		for scheme, v := range ids {
			if value, ok := musicString(v); ok && MusicID(k.Kind, scheme, value) {
				if out.IDs == nil {
					out.IDs = map[string]string{}
				}
				out.IDs[scheme] = value
			}
		}
	}
	return out, true
}

// ReadMusicCopies reads a track's copies as MSFP Media music 3.3 says: false
// when copies isn't an array; else the copies kept, in the owner's order, at most
// MaxMusicCopies, the first. A copy is skipped alone when it isn't an
// object, misses a required member or breaks its form (id and etag of
// ID's form, size from 1 to MaxCopySize, durationMs from 1 to
// MaxMusicDurationMs, mediaType a media type and codec a token), or repeats a kept
// copy's id. Its optional members out of range or of the wrong type
// count as absent; video's members of a copy are ignored.
func ReadMusicCopies(raw json.RawMessage) ([]MusicCopy, bool) {
	var list []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &list) != nil {
		return nil, false
	}
	var out []MusicCopy
	for _, e := range list {
		if len(out) == MaxMusicCopies {
			break
		}
		c, ok := readMusicCopy(e)
		if ok && !slices.ContainsFunc(out, func(k MusicCopy) bool { return k.ID == c.ID }) {
			out = append(out, c)
		}
	}
	return out, true
}

// readMusicCopy reads one copy; false skips it.
func readMusicCopy(raw json.RawMessage) (MusicCopy, bool) {
	var o map[string]json.RawMessage
	if json.Unmarshal(raw, &o) != nil || o == nil {
		return MusicCopy{}, false
	}
	id, _ := musicString(o["id"])
	etag, _ := musicString(o["etag"])
	mediaType, _ := musicString(o["mediaType"])
	codec, _ := musicString(o["codec"])
	duration, okDuration := musicInt(o["durationMs"], 1, MaxMusicDurationMs)
	size, okSize := musicInt(o["size"], 1, MaxCopySize)
	if !ID(id) || !ID(etag) || !IsMediaType(mediaType) || !Token(codec) || !okDuration || !okSize {
		return MusicCopy{}, false
	}
	c := MusicCopy{ID: id, DurationMs: duration, MediaType: mediaType, Codec: codec, Size: size, Etag: etag}
	if codecs, ok := musicString(o["codecs"]); ok && copyCodecsRe.MatchString(codecs) {
		c.Codecs = codecs
	}
	rate, _ := musicInt(o["sampleRate"], 8_000, 768_000)
	depth, _ := musicInt(o["bitDepth"], 8, 32)
	channels, _ := musicInt(o["channels"], 1, 64)
	c.SampleRate, c.BitDepth, c.Channels = int(rate), int(depth), int(channels)
	var rg map[string]json.RawMessage
	if json.Unmarshal(o["replayGain"], &rg) == nil && rg != nil {
		g := ReplayGain{
			TrackGain: musicNumber(rg["trackGain"], -MaxGain, MaxGain, 2),
			TrackPeak: musicNumber(rg["trackPeak"], 0, MaxPeak, 6),
			AlbumGain: musicNumber(rg["albumGain"], -MaxGain, MaxGain, 2),
			AlbumPeak: musicNumber(rg["albumPeak"], 0, MaxPeak, 6),
		}
		if g != (ReplayGain{}) {
			c.ReplayGain = &g
		}
	}
	if l, _ := musicString(o["lyrics"]); l == "plain" || l == "synced" {
		c.Lyrics = l
	}
	return c, true
}

// musicString reads a JSON string; false for any other value, null and
// absent included.
func musicString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// musicBool reads a JSON boolean; nil for any other value.
func musicBool(raw json.RawMessage) *bool {
	switch string(raw) {
	case "true":
		b := true
		return &b
	case "false":
		b := false
		return &b
	}
	return nil
}

// musicInt reads an MSFP integer (MSFP Media base 2.2) from lo to hi; false
// for any other value.
func musicInt(raw json.RawMessage, lo, hi int64) (int64, bool) {
	if !isInteger(string(raw)) {
		return 0, false
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < lo || n > hi {
		return 0, false
	}
	return n, true
}

// musicNumber reads a JSON number from lo to hi, checked as sent, then
// rounded to places decimals, to the nearest (never −0); nil for any
// other value, a number too large to read included.
func musicNumber(raw json.RawMessage, lo, hi float64, places int) *float64 {
	if len(raw) == 0 || raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return nil
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || f < lo || f > hi {
		return nil
	}
	r, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', places, 64), 64)
	if r == 0 {
		r = 0 // −0 becomes 0
	}
	return &r
}

// integerIn reads an MSFP integer (MSFP Media base 2.2: no fraction, no
// exponent, no minus zero) within lo to hi; 0 otherwise, or -1 when lo
// is 0 and it isn't one.
func integerIn(raw json.RawMessage, lo, hi int64) int64 {
	none := int64(0)
	if lo == 0 {
		none = -1
	}
	s := string(raw)
	if !isInteger(s) || s == "-0" {
		return none
	}
	var n int64
	if json.Unmarshal(raw, &n) != nil || n < lo || n > hi {
		return none
	}
	return n
}

// AsCopy is a track's copy as a copy with no video, its one audio track
// a0: what Serve and the choices of MSFP Media playing 2.6 read.
func (m MusicCopy) AsCopy() Copy {
	return Copy{ID: m.ID, DurationMs: m.DurationMs, MediaType: m.MediaType, Size: m.Size, ETag: m.Etag,
		AudioTracks: []CopyAudio{{ID: "a0", Codec: m.Codec, Codecs: m.Codecs, Channels: m.Channels,
			SampleRate: m.SampleRate, BitDepth: m.BitDepth}}}
}
