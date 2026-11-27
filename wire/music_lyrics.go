package wire

import (
	"encoding/json"
	"errors"
)

// The lyrics call (MSFP Media music 6): a signed call, with no ticket,
// for a copy whose lyrics is plain or synced, answered with lyrics shaped
// as OpenSubsonic's structuredLyrics, times in milliseconds.

// The bounds of a lyrics answer (MSFP Media music 6).
const (
	MaxLyricsBytes = 1 << 20 // an answer of exactly this size is read
	MaxLyricsSets  = 8
	MaxLyricsLines = 2000 // per set
	MaxLyricsLine  = 512  // a line's value, in code points
)

// LyricsKinds are OpenSubsonic's kinds of a lyrics set (MSFP Media music
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
// a tag (LanguageTag), "" when unknown; Kind a token (LyricsKinds are
// examples). A set is synced when its lines carry start (Synced); the
// owning server has applied any offset before sending, and a viewer's
// server takes the artist and the title from the track's own members.
type Lyrics struct {
	Lang string       `json:"lang,omitempty"`
	Kind string       `json:"kind"`
	Line []LyricsLine `json:"line"`
}

// Synced says whether a set is synced: its lines carry start.
func (l Lyrics) Synced() bool { return len(l.Line) > 0 && l.Line[0].Start != nil }

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
var ErrLyrics = errors.New("federation: lyrics outside what MSFP Media music 6 allows")

// ReadLyrics is what the viewer's server keeps of a lyrics answer (MSFP Media music
// 6): per set, lang a valid tag, else absent; kind a token, kept
// known or not, else main. Its lines left are the objects whose value is
// a string: a set is synced when every one carries start, plain when
// none does, and skipped when they mix. Per line, value cleaned and cut
// to MaxLyricsLine but kept when empty; for a synced set, start an
// integer from 0 to MaxMusicDurationMs, not before the line kept before
// it, else the line is skipped. The first MaxLyricsLines lines and
// MaxLyricsSets sets are kept, counting kept ones only; a set with no
// line left is skipped. Members of earlier drafts (synced, offset,
// displayArtist, displayTitle) are ignored.
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
	s := Lyrics{Kind: LyricsKinds[0]}
	if lang, _ := musicString(o["lang"]); LanguageTag(lang) {
		s.Lang = lang
	}
	if kind, _ := musicString(o["kind"]); Token(kind) {
		s.Kind = kind
	}
	var raws []json.RawMessage
	if len(o["line"]) > 0 && o["line"][0] == '[' {
		_ = json.Unmarshal(o["line"], &raws)
	}
	type line struct {
		o     map[string]json.RawMessage
		value string
	}
	var lines []line
	starts := 0
	for _, raw := range raws {
		var lo map[string]json.RawMessage
		if json.Unmarshal(raw, &lo) != nil || lo == nil {
			continue
		}
		value, ok := musicString(lo["value"])
		if !ok {
			continue
		}
		lines = append(lines, line{lo, value})
		if _, has := lo["start"]; has {
			starts++
		}
	}
	if starts != 0 && starts != len(lines) {
		return Lyrics{}, false // a set mixing synced and plain lines
	}
	synced := starts > 0
	var last int64
	for _, ln := range lines {
		if len(s.Line) == MaxLyricsLines {
			break
		}
		l := LyricsLine{Value: CleanText(ln.value, MaxLyricsLine)}
		if synced {
			start, ok := musicInt(ln.o["start"], 0, MaxMusicDurationMs)
			if !ok || start < last {
				continue
			}
			last, l.Start = start, &start
		}
		s.Line = append(s.Line, l)
	}
	return s, len(s.Line) > 0
}
