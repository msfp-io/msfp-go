package wire

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Media (MSFP media, section 3): what a server checks of the parts of a
// stream it fetches from another before relaying them: HLS playlists
// here; the file's ranges and answers, and the scan cap, in
// media_file.go; WebVTT, ASS, PGS and fonts in webvtt.go, ass.go, pgs.go
// and font.go.

// MaxPlaylist bounds a playlist, as read: 2 MiB (MSFP media, section 2.4).
const MaxPlaylist = 2 << 20

var fileNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// FileName says whether s may name a file of a stream (MSFP media): the
// characters of fileNameRe, never "." or "..", which a URL resolves as a
// folder, not a file.
func FileName(s string) bool {
	return s != "." && s != ".." && fileNameRe.MatchString(s)
}

// QueueStreamBase is the folder of queue entry n's stream (media
// sections 1 and 3), under a playback's base (<stream root><ticket>/):
// its file and master.m3u8 are there, and the bare names its playlists
// give resolve there.
func QueueStreamBase(base string, n int) string {
	return base + "queue/" + strconv.Itoa(n) + "/"
}

// The bounds of a playlist (MSFP media, section 3.1).
const (
	MaxPlaylistURIs   = 50_000 // URI lines and URI= values together
	MaxRenditions     = 32     // #EXT-X-MEDIA lines of a master playlist
	MaxTargetDuration = 60     // seconds
)

// Tags of the HLS subset (MSFP media, section 3.1), by playlist kind:
// what a relayed playlist keeps; other tags and comments are dropped.
var (
	MasterSubset = []string{"#EXTM3U", "#EXT-X-VERSION", "#EXT-X-INDEPENDENT-SEGMENTS", "#EXT-X-STREAM-INF", "#EXT-X-MEDIA"}
	MediaSubset  = []string{"#EXTM3U", "#EXT-X-VERSION", "#EXT-X-TARGETDURATION", "#EXT-X-MEDIA-SEQUENCE",
		"#EXT-X-PLAYLIST-TYPE", "#EXT-X-INDEPENDENT-SEGMENTS", "#EXTINF", "#EXT-X-DISCONTINUITY", "#EXT-X-ENDLIST", "#EXT-X-MAP"}
)

func playlistStep(step int, why string) error {
	return fmt.Errorf("msfp: a playlist outside what MSFP allows, step %d: %s", step, why)
}

// CheckPlaylist checks a paired server's playlist as MSFP media
// section 3.1 says, in its order (FilterPlaylist).
func CheckPlaylist(body []byte, audio func(id string) bool) error {
	_, err := FilterPlaylist(body, nil, audio)
	return err
}

// FilterPlaylist is a paired server's playlist once checked as media
// section 3.1 says, in its order, keeping only the tags of subset,
// comments dropped, each line ending in a line feed; with a nil subset,
// every line is kept. The checks: at most 2 MiB (1); lines split at line
// feeds, one carriage return at a line's end removed, and no character
// players end a line at inside one (2); a first line of exactly #EXTM3U
// (3); every URI line a file name of the stream (4), and every URI=, in
// any case and anywhere in a tag line, a quoted one (5); no EXT-X-DEFINE
// (6), EXT-X-KEY or EXT-X-SESSION-KEY (7); at most MaxPlaylistURIs URIs
// (8); a media playlist (one with #EXTINF) complete, of type VOD, its
// durations within its target duration, and a playlist either a media
// or a master one (9); a master's #EXT-X-MEDIA audio renditions, at
// most MaxRenditions, each NAME an audio track ID that audio says the
// playback has (10). A nil audio has none.
func FilterPlaylist(body []byte, subset []string, audio func(id string) bool) ([]byte, error) {
	if len(body) > MaxPlaylist {
		return nil, playlistStep(1, "over 2 MiB")
	}
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
		// Players end lines at a carriage return, or at U+2028, too:
		// such a character inside a line would hide what follows it
		// from these checks, as another line players read.
		if strings.ContainsFunc(lines[i], lineBreaking) {
			return nil, playlistStep(2, "a control character inside a line")
		}
	}
	if lines[0] != "#EXTM3U" {
		return nil, playlistStep(3, "not a playlist")
	}
	uris, lastURI := 0, -1
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !FileName(line) {
			return nil, playlistStep(4, "a URI line that isn't a file of the stream")
		}
		uris++
		lastURI = i
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			n, ok := quotedURIs(line)
			if !ok {
				return nil, playlistStep(5, "a URI attribute that isn't a quoted file of the stream")
			}
			uris += n
		}
	}
	if slices.ContainsFunc(lines, func(l string) bool { return hlsTag(l, "#EXT-X-DEFINE") }) {
		return nil, playlistStep(6, "variables, which MSFP leaves out")
	}
	if slices.ContainsFunc(lines, func(l string) bool {
		return strings.HasPrefix(l, "#EXT-X-KEY:") || strings.HasPrefix(l, "#EXT-X-SESSION-KEY:")
	}) {
		return nil, playlistStep(7, "keys, which MSFP leaves out")
	}
	if uris > MaxPlaylistURIs {
		return nil, playlistStep(8, "too many addresses")
	}
	media := slices.ContainsFunc(lines, func(l string) bool { return hlsTag(l, "#EXTINF") })
	master := slices.ContainsFunc(lines, func(l string) bool { return hlsTag(l, "#EXT-X-STREAM-INF") })
	if !media && !master {
		return nil, playlistStep(9, "neither a master nor a media playlist")
	}
	if media {
		if err := checkMediaPlaylist(lines, lastURI); err != nil {
			return nil, playlistStep(9, err.Error())
		}
	}
	if master {
		if err := checkRenditions(lines, audio); err != nil {
			return nil, playlistStep(10, err.Error())
		}
	}
	var out strings.Builder
	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") && subset != nil {
			tag, _, _ := strings.Cut(line, ":")
			if !slices.Contains(subset, tag) {
				continue
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return []byte(out.String()), nil
}

// hlsTag says whether line is the tag name, alone or with a value.
func hlsTag(line, name string) bool {
	return line == name || strings.HasPrefix(line, name+":")
}

var (
	extinfForm = regexp.MustCompile(`^#EXTINF:(\d+(?:\.\d+)?)(?:,.*)?$`)
	targetForm = regexp.MustCompile(`^#EXT-X-TARGETDURATION:(\d+)$`)
)

// checkMediaPlaylist is step 9 for a media playlist: complete from the
// first fetch (one #EXT-X-PLAYLIST-TYPE:VOD, one #EXT-X-ENDLIST after
// the last URI line), a target duration of 1 to 60 seconds, every
// #EXTINF above 0 and, rounded, at most the first target duration.
func checkMediaPlaylist(lines []string, lastURI int) error {
	vod, ends, endAt, target := 0, 0, -1, int64(-1)
	for i, l := range lines {
		switch {
		case l == "#EXT-X-PLAYLIST-TYPE:VOD":
			vod++
		case hlsTag(l, "#EXT-X-PLAYLIST-TYPE"):
			return errors.New("a playlist that grows, of a type other than VOD")
		case l == "#EXT-X-ENDLIST":
			ends++
			endAt = i
		case hlsTag(l, "#EXT-X-TARGETDURATION"):
			m := targetForm.FindStringSubmatch(l)
			if m == nil || !mediaInt(m[1], 1, MaxTargetDuration) {
				return errors.New("a target duration that isn't 1 to 60 seconds")
			}
			if target < 0 {
				target, _ = strconv.ParseInt(m[1], 10, 64)
			}
		}
	}
	switch {
	case vod != 1:
		return errors.New("not exactly one #EXT-X-PLAYLIST-TYPE:VOD")
	case ends != 1:
		return errors.New("a playlist that grows, without exactly one #EXT-X-ENDLIST")
	case endAt < lastURI:
		return errors.New("#EXT-X-ENDLIST before the last URI line")
	case target < 0:
		return errors.New("no #EXT-X-TARGETDURATION")
	}
	for _, l := range lines {
		if !hlsTag(l, "#EXTINF") {
			continue
		}
		m := extinfForm.FindStringSubmatch(l)
		if m == nil {
			return errors.New("a segment duration that isn't a decimal")
		}
		d, err := strconv.ParseFloat(m[1], 64)
		if err != nil || d <= 0 {
			return errors.New("a segment duration not above 0")
		}
		if math.Round(d) > float64(target) {
			return errors.New("a segment longer than the target duration")
		}
	}
	return nil
}

// hlsAttribute is one attribute of an attribute list (RFC 8216 section
// 4.2), and the comma after it.
var hlsAttribute = regexp.MustCompile(`^([A-Z0-9-]+)=("[^"\r\n]*"|[^",]*)(?:,|$)`)

// hlsAttributes reads an attribute list: each name once, quoted values
// with their quotes.
func hlsAttributes(s string) (map[string]string, bool) {
	out := map[string]string{}
	for s != "" {
		m := hlsAttribute.FindStringSubmatch(s)
		if m == nil {
			return nil, false
		}
		if _, twice := out[m[1]]; twice {
			return nil, false
		}
		out[m[1]] = m[2]
		s = s[len(m[0]):]
	}
	return out, true
}

// checkRenditions is step 10 for a master playlist: at most 32
// #EXT-X-MEDIA lines, each TYPE=AUDIO and a quoted NAME that is one of
// the playback's audio track IDs.
func checkRenditions(lines []string, audio func(id string) bool) error {
	n := 0
	for _, l := range lines {
		if !hlsTag(l, "#EXT-X-MEDIA") {
			continue
		}
		if n++; n > MaxRenditions {
			return errors.New("over 32 renditions")
		}
		a, ok := hlsAttributes(strings.TrimPrefix(strings.TrimPrefix(l, "#EXT-X-MEDIA"), ":"))
		switch {
		case !ok:
			return errors.New("a rendition's malformed attribute list")
		case a["TYPE"] != "AUDIO":
			return errors.New("a rendition that isn't audio")
		}
		name, quoted := strings.CutPrefix(a["NAME"], `"`)
		name, closed := strings.CutSuffix(name, `"`)
		if !quoted || !closed || audio == nil || !audio(name) {
			return errors.New("a rendition whose NAME isn't an audio track of the playback")
		}
	}
	return nil
}

// URILines are a checked playlist's URI lines.
func URILines(playlist []byte) []string {
	var out []string
	for _, line := range strings.Split(string(playlist), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// lineBreaking says whether a character may end a line for some player,
// or hide what follows it: control characters but tabs, and Unicode's
// line breaks.
func lineBreaking(r rune) bool {
	return r < 0x20 && r != '\t' || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029
}

// quotedURIs counts a tag line's URI= values, false when one isn't a
// quoted file name of the stream: every URI=, in any case and anywhere
// in the line (inside another attribute's quoted string too), followed by
// a quote, a file name and a quote. HLS quoted strings have no escapes.
func quotedURIs(line string) (int, bool) {
	// Upper case, ASCII letters only, byte for byte: offsets stay the
	// line's (strings.ToUpper may change a line's length).
	upper := []byte(line)
	for i, c := range upper {
		if 'a' <= c && c <= 'z' {
			upper[i] = c - 'a' + 'A'
		}
	}
	n := 0
	for from := 0; ; {
		i := bytes.Index(upper[from:], []byte("URI="))
		if i < 0 {
			return n, true
		}
		rest := line[from+i+len("URI="):]
		if !strings.HasPrefix(rest, `"`) {
			return n, false
		}
		name, _, closed := strings.Cut(rest[1:], `"`)
		if !closed || !FileName(name) {
			return n, false
		}
		n++
		from += i + len("URI=")
	}
}
