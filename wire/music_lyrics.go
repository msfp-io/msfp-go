package wire

import (
	"encoding/json"
	"errors"
)

// The lyrics call (MSFP music section 6): a signed call, with no ticket,
// for a copy whose lyrics is plain or synced, answered with lyrics shaped
// as OpenSubsonic's structuredLyrics, times in milliseconds.

// The bounds of a lyrics answer (music section 6).
const (
	MaxLyricsBytes  = 1 << 20 // an answer of exactly this size is read
	MaxLyricsSets   = 8
	MaxLyricsLines  = 2000 // per set
	MaxLyricsLine   = 512  // a line's value, in code points
	MaxLyricsOffset = 600_000
	MaxDisplayTitle = 256 // displayArtist takes MaxArtistNames
)

// LyricsKinds are OpenSubsonic's kinds of a lyrics set (music section
// 6): examples, since kind is open. A kind that isn't a token is kept as
// main; a viewer's server never shows a set of a kind it doesn't know as
// the main lyrics.
var LyricsKinds = []string{"main", "translation", "pronunciation"}

// LyricsRequest is a lyrics call's request: a track key, as the owning
// server sent it in titles, and the copy's id.
type LyricsRequest struct {
	Title     string `json:"title"`
	VersionID string `json:"versionId"`
}

// ReadLyricsRequest reads a lyrics request; false means 400
// invalid-request: a body that isn't I-JSON or an object, a title that
// isn't a track key, a versionId that isn't of ID's form. Whether the
// owning server offers the caller that track and copy, with lyrics, is
// its own check (404 not-found otherwise).
func ReadLyricsRequest(body []byte) (LyricsRequest, bool) {
	var r LyricsRequest
	if DecodeJSON(body, &r) != nil || !IsMusicKey(r.Title, "track") || !ID(r.VersionID) {
		return LyricsRequest{}, false
	}
	return r, true
}

// LyricsAnswer is a lyrics call's answer: at most MaxLyricsSets sets.
type LyricsAnswer struct {
	StructuredLyrics []Lyrics `json:"structuredLyrics"`
}

// Lyrics is a set of lyrics, one per language, kind and timing. Lang is
// a tag (LanguageTag), "" when unknown; Kind a token (LyricsKinds are examples); Offset
// the milliseconds to add to every line's start.
type Lyrics struct {
	Lang          string       `json:"lang,omitempty"`
	Kind          string       `json:"kind"`
	Synced        bool         `json:"synced"`
	Offset        int64        `json:"offset"`
	DisplayArtist string       `json:"displayArtist,omitempty"`
	DisplayTitle  string       `json:"displayTitle,omitempty"`
	Line          []LyricsLine `json:"line"`
}

// LyricsLine is a line: plain text, never markup, possibly empty (a
// pause), and for synced lyrics when it starts, in milliseconds from the
// track's start (nil for plain lyrics).
type LyricsLine struct {
	Start *int64 `json:"start,omitempty"`
	Value string `json:"value"`
}

// ErrLyrics is a failed lyrics fetch: an answer past MaxLyricsBytes, not
// I-JSON or not an object, whose structuredLyrics isn't an array, or with
// no set left.
var ErrLyrics = errors.New("federation: lyrics outside what music section 6 allows")

// ReadLyrics is what the viewer's server keeps of a lyrics answer (music
// section 6): per set, synced a boolean, else the set is skipped; lang a
// valid tag, else absent; kind a token, kept known or not, else main; offset an
// integer within ±MaxLyricsOffset, else 0; displayArtist and
// displayTitle cleaned (CleanText), empty counting as absent. Per line,
// value a string, cleaned and cut to MaxLyricsLine but kept when empty;
// for synced lyrics, start an integer from 0 to MaxMusicDurationMs, not
// before the line kept before it, else the line is skipped; for plain
// lyrics, start ignored. The first MaxLyricsLines lines and
// MaxLyricsSets sets are kept, counting kept ones only; a set with no
// line left is skipped.
func ReadLyrics(body []byte) ([]Lyrics, error) {
	if len(body) > MaxLyricsBytes {
		return nil, ErrLyrics
	}
	var a struct {
		StructuredLyrics json.RawMessage `json:"structuredLyrics"`
	}
	var sets []json.RawMessage
	if DecodeAnswer(body, &a) != nil || len(a.StructuredLyrics) == 0 || a.StructuredLyrics[0] != '[' ||
		json.Unmarshal(a.StructuredLyrics, &sets) != nil {
		return nil, ErrLyrics
	}
	var out []Lyrics
	for _, raw := range sets {
		if len(out) == MaxLyricsSets {
			break
		}
		if s, ok := readLyrics(raw); ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, ErrLyrics
	}
	return out, nil
}

// readLyrics reads one set; false skips it.
func readLyrics(raw json.RawMessage) (Lyrics, bool) {
	var o map[string]json.RawMessage
	if json.Unmarshal(raw, &o) != nil || o == nil {
		return Lyrics{}, false
	}
	synced := musicBool(o["synced"])
	if synced == nil {
		return Lyrics{}, false
	}
	s := Lyrics{Kind: LyricsKinds[0], Synced: *synced}
	if lang, _ := musicString(o["lang"]); LanguageTag(lang) {
		s.Lang = lang
	}
	if kind, _ := musicString(o["kind"]); Token(kind) {
		s.Kind = kind
	}
	s.Offset, _ = musicInt(o["offset"], -MaxLyricsOffset, MaxLyricsOffset)
	artist, _ := musicString(o["displayArtist"])
	title, _ := musicString(o["displayTitle"])
	s.DisplayArtist, s.DisplayTitle = CleanText(artist, MaxArtistNames), CleanText(title, MaxDisplayTitle)
	var lines []json.RawMessage
	if len(o["line"]) > 0 && o["line"][0] == '[' {
		_ = json.Unmarshal(o["line"], &lines)
	}
	var last int64
	for _, raw := range lines {
		if len(s.Line) == MaxLyricsLines {
			break
		}
		var lo map[string]json.RawMessage
		if json.Unmarshal(raw, &lo) != nil || lo == nil {
			continue
		}
		value, ok := musicString(lo["value"])
		if !ok {
			continue
		}
		l := LyricsLine{Value: CleanText(value, MaxLyricsLine)}
		if s.Synced {
			start, ok := musicInt(lo["start"], 0, MaxMusicDurationMs)
			if !ok || start < last {
				continue
			}
			last, l.Start = start, &start
		}
		s.Line = append(s.Line, l)
	}
	return s, len(s.Line) > 0
}
