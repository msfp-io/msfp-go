package wire

import (
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text subtitles as WebVTT (MSFP video, section 6.3): a part is rebuilt,
// not filtered. Cue timings survive, the settings that place a cue (line,
// position, size, align), plain cue text, and the italic, bold and colour
// tags, balanced; identifiers, styles, regions, notes and every other tag
// go. The owning server rebuilds what it serves, the viewer's server again
// what it relays.

// The bounds of WebVTT (video section 6.3, media section 2.4). A whole
// track has bounds of its own: MaxWholeText (shared with ASS) and
// maxWholeCues.
const (
	MaxWebVTT    = 5 << 20  // bytes, a part as read
	MaxWholeText = 16 << 20 // bytes, a whole text track as read, WebVTT or ASS
	maxCues      = 50_000   // a part's cues, once rebuilt
	maxWholeCues = 200_000  // a whole track's
	maxCueLine   = 500      // bytes of a cue text line's text once cleaned, tags not counted
	maxCueTags   = 32       // tags a cue text line keeps
	maxCueDepth  = 8        // tags open at once in a cue
)

// cueTiming is a timing line, once white space at its ends is removed:
// two timestamps ([hours:]minutes:seconds.milliseconds) around "-->",
// with spaces, tabs or form feeds between, then the cue settings.
var cueTiming = regexp.MustCompile(`^((?:\d{1,3}:)?\d{2}:\d{2}\.\d{3})[ \t\f]+-->[ \t\f]+((?:\d{1,3}:)?\d{2}:\d{2}\.\d{3})([ \t\f].*)?$`)

// cueEscape escapes plain text for a cue: in WebVTT, "<" opens a tag and
// "&" a character reference.
var cueEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// RebuildWebVTT rebuilds a WebVTT part (a stretch) as video section 6.3
// says: false when it is over MaxWebVTT, doesn't start with WEBVTT, or
// holds more than 50,000 cues once rebuilt.
func RebuildWebVTT(in []byte) ([]byte, bool) { return rebuildWebVTT(in, MaxWebVTT, maxCues) }

// RebuildWholeWebVTT rebuilds a whole WebVTT track (a
// subtitles/<track>/whole answer) under the same rules, with a whole
// track's bounds: MaxWholeText bytes, 200,000 cues.
func RebuildWholeWebVTT(in []byte) ([]byte, bool) {
	return rebuildWebVTT(in, MaxWholeText, maxWholeCues)
}

func rebuildWebVTT(in []byte, most, mostCues int) ([]byte, bool) {
	if len(in) > most {
		return nil, false
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(in), "\r\n", "\n"), "\r", "\n")
	text = strings.TrimPrefix(text, "\uFEFF") // one byte order mark
	if !strings.HasPrefix(text, "WEBVTT") {
		return nil, false
	}
	var out strings.Builder
	out.WriteString("WEBVTT\n")
	cues := 0
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		// The first timing line starts the cue; what's before it (the
		// header, an identifier) goes. A block without one goes whole.
		timing := -1
		var m []string
		for i, l := range lines {
			if m = cueTiming.FindStringSubmatch(strings.TrimFunc(l, unicode.IsSpace)); m != nil {
				timing = i
				break
			}
		}
		if timing < 0 {
			continue
		}
		var body [][]cuePiece
		for _, l := range lines[timing+1:] {
			if c := cleanCueLine(l); c != nil {
				body = append(body, c)
			}
		}
		if len(body) == 0 {
			continue
		}
		if cues++; cues > mostCues {
			return nil, false
		}
		out.WriteString("\n" + m[1] + " --> " + m[2] + cueSettings(m[3]) + "\n")
		for _, l := range balanceCue(body) {
			out.WriteString(l + "\n")
		}
	}
	return []byte(out.String()), true
}

// Cue settings (video section 6.3): each kept in this order, when it is
// the first of its name and valid.
var (
	cueSettingNames = []string{"line", "position", "size", "align"}
	cuePercent      = `\d{1,3}(?:\.\d{1,3})?%`
	cueLineValue    = regexp.MustCompile(`^(` + cuePercent + `|-?\d+)(?:,(?:start|center|end))?$`)
	cuePosition     = regexp.MustCompile(`^(` + cuePercent + `)(?:,(?:line-left|center|line-right))?$`)
	cueSize         = regexp.MustCompile(`^(` + cuePercent + `)$`)
)

// cueSettings keeps a timing line's settings, each written after a space.
func cueSettings(s string) string {
	kept := map[string]string{}
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\f' }) {
		name, value, ok := strings.Cut(f, ":")
		if !ok || seen[name] {
			continue
		}
		// The first of its name, valid or not: an invalid one keeps a
		// later one of its name out too.
		seen[name] = true
		if cueSetting(name, value) {
			kept[name] = f
		}
	}
	var out strings.Builder
	for _, n := range cueSettingNames {
		if f, ok := kept[n]; ok {
			out.WriteString(" " + f)
		}
	}
	return out.String()
}

func cueSetting(name, value string) bool {
	switch name {
	case "line":
		m := cueLineValue.FindStringSubmatch(value)
		if m == nil {
			return false
		}
		if strings.HasSuffix(m[1], "%") {
			return cuePercentIn(m[1])
		}
		return mediaInt(m[1], -1000, 1000)
	case "position":
		m := cuePosition.FindStringSubmatch(value)
		return m != nil && cuePercentIn(m[1])
	case "size":
		m := cueSize.FindStringSubmatch(value)
		return m != nil && cuePercentIn(m[1])
	case "align":
		switch value {
		case "start", "center", "end", "left", "right":
			return true
		}
	}
	return false
}

// cuePercentIn says whether a percentage of cuePercent's form is 0 to 100.
func cuePercentIn(s string) bool {
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	return err == nil && v >= 0 && v <= 100
}

var mediaIntForm = regexp.MustCompile(`^-?\d+$`)

// mediaInt says whether s is an integer (an optional "-", then digits) from
// lo to hi.
func mediaInt(s string, lo, hi int64) bool {
	if !mediaIntForm.MatchString(s) {
		return false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	return err == nil && v >= lo && v <= hi
}

// cuePiece is a piece of a cleaned cue text line: a kept tag, or text
// (escaped once the line is cleaned).
type cuePiece struct {
	tag  bool
	text string
}

// cueColours are WebVTT's built-in text colour classes; bg_ before one
// is its background colour.
var cueColours = []string{"white", "lime", "cyan", "red", "yellow", "magenta", "blue", "black"}

// keptCueTag says whether a tag is kept (video section 6.3, step 3):
// <i>, <b>, their end tags, </c>, or <c.> with one colour class, or one
// text colour and one background colour.
func keptCueTag(t string) bool {
	switch t {
	case "<i>", "</i>", "<b>", "</b>", "</c>":
		return true
	}
	inner, ok := strings.CutPrefix(t, "<c.")
	if !ok || !strings.HasSuffix(inner, ">") {
		return false
	}
	classes := strings.Split(strings.TrimSuffix(inner, ">"), ".")
	if len(classes) > 2 {
		return false
	}
	backgrounds := 0
	for _, c := range classes {
		colour, bg := strings.CutPrefix(c, "bg_")
		if !slices.Contains(cueColours, colour) {
			return false
		}
		if bg {
			backgrounds++
		}
	}
	return len(classes) == 1 || backgrounds == 1
}

// cueTagAt says whether s[i] is a "<" that starts a tag: one followed by
// a letter (any script), "/", "!" or "?".
func cueTagAt(s string, i int) bool {
	if s[i] != '<' {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[i+1:])
	return unicode.IsLetter(r) || r == '/' || r == '!' || r == '?'
}

// untagCue turns what is shaped like a tag into one space; one without
// its ">" removes the rest of the text.
func untagCue(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if !cueTagAt(s, i) {
			b.WriteByte(s[i])
			continue
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			break
		}
		b.WriteByte(' ')
		i += j
	}
	return b.String()
}

// cleanCueLine cleans a cue text line, in video section 6.3's steps: its
// kept tags and its text, escaped, or nil when no text is left.
func cleanCueLine(s string) []cuePiece {
	s = dropInvisible(s)
	// Steps 2 and 3: tags (kept, or a space) and text.
	var pieces []cuePiece
	for i := 0; i < len(s); {
		if cueTagAt(s, i) {
			j := strings.IndexByte(s[i:], '>')
			if j < 0 {
				break // the rest of the line goes
			}
			if t := s[i : i+j+1]; keptCueTag(t) {
				pieces = append(pieces, cuePiece{tag: true, text: t})
			} else {
				pieces = append(pieces, cuePiece{text: " "})
			}
			i += j + 1
			continue
		}
		j := i + 1
		for j < len(s) && !cueTagAt(s, j) {
			j++
		}
		pieces = append(pieces, cuePiece{text: s[i:j]})
		i = j
	}
	// Steps 4 to 6: references decoded, spaces collapsed across tags, the
	// text cut to 500 bytes and the tags to 32.
	var out []cuePiece
	write := func(t string) {
		if n := len(out); n > 0 && !out[n-1].tag {
			out[n-1].text += t
		} else {
			out = append(out, cuePiece{text: t})
		}
	}
	size, tags := 0, 0
	// space: white space seen since the last character written; text: a
	// character written; spaced: the last thing written is a space.
	space, text, spaced := false, false, false
pieces:
	for _, p := range pieces {
		if p.tag {
			if tags == maxCueTags {
				continue
			}
			// A run of spaces before a tag is written before it.
			if space && text && !spaced {
				if size+1 > maxCueLine {
					break
				}
				write(" ")
				size++
				spaced = true
			}
			space = false
			tags++
			out = append(out, p)
			continue
		}
		for _, r := range untagCue(dropInvisible(html.UnescapeString(p.text))) {
			if unicode.IsControl(r) || unicode.IsSpace(r) {
				space = true
				continue
			}
			if space && text && !spaced {
				if size+1 > maxCueLine {
					break pieces
				}
				write(" ")
				size++
			}
			space = false
			n := utf8.RuneLen(r)
			if size+n > maxCueLine {
				break pieces
			}
			write(string(r))
			size += n
			text, spaced = true, false
		}
	}
	if !text {
		return nil
	}
	// Spaces at the end of the text go, tags after them or not.
	for i := len(out) - 1; i >= 0; i-- {
		if !out[i].tag {
			if out[i].text = strings.TrimRight(out[i].text, " "); out[i].text == "" {
				out = append(out[:i], out[i+1:]...)
			}
			break
		}
	}
	for i := range out {
		if !out[i].tag {
			out[i].text = cueEscape.Replace(out[i].text)
		}
	}
	return out
}

// balanceCue balances the tags of a cue's lines and writes each line: an
// end tag only closing the innermost open tag of its name, at most 8 open
// (one past them removed with its end tag), those still open closed at
// the cue's end, innermost first.
func balanceCue(lines [][]cuePiece) []string {
	type open struct {
		name    string
		removed bool // a start tag past the 8
	}
	var stack []open
	depth := 0
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		var b strings.Builder
		for _, p := range l {
			if !p.tag {
				b.WriteString(p.text)
				continue
			}
			name := strings.Trim(p.text, "</>")
			if strings.HasPrefix(name, "c.") {
				name = "c"
			}
			if !strings.HasPrefix(p.text, "</") {
				if depth == maxCueDepth {
					stack = append(stack, open{name, true})
					continue
				}
				stack = append(stack, open{name, false})
				depth++
				b.WriteString(p.text)
				continue
			}
			if n := len(stack); n > 0 && stack[n-1].name == name {
				if !stack[n-1].removed {
					depth--
					b.WriteString(p.text)
				}
				stack = stack[:n-1]
			}
		}
		out = append(out, b.String())
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if !stack[i].removed {
			out[len(out)-1] += "</" + stack[i].name + ">"
		}
	}
	return out
}

// directionControl says whether r is one of the direction controls
// (U+061C, U+200E, U+200F, U+202A to U+202E, U+2066 to U+2069).
func directionControl(r rune) bool {
	return r == 0x061C || r == 0x200E || r == 0x200F || r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069
}

// dropInvisible removes invalid UTF-8, U+FFFD and the direction controls.
func dropInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || directionControl(r) {
			return -1
		}
		return r
	}, s)
}

// StretchSeconds is how long a part of subtitles covers (media section
// 3.4): part n covers n × 120 to (n + 1) × 120 seconds, from 10 seconds
// early.
const StretchSeconds = 120

// stretchEarly is how early a stretch starts, in milliseconds.
const stretchEarly = 10_000

// inStretch says whether what shows from start to end (milliseconds)
// belongs in part n: it ends after the part's start, 10 seconds early,
// and starts before its end.
func inStretch(start, end int64, n int) bool {
	return end > int64(n)*StretchSeconds*1000-stretchEarly && start < int64(n+1)*StretchSeconds*1000
}

// WebVTTPart is part n of a whole WebVTT track, for an owning server
// that holds its subtitles as one file: the track rebuilt as
// RebuildWholeWebVTT does, keeping the cues that show during the part's
// stretch (media section 3.4). A part with no cue is "WEBVTT" and a line
// feed. False when the track isn't WebVTT within a whole track's bounds,
// or its part holds more cues than a part may.
func WebVTTPart(track []byte, n int) ([]byte, bool) {
	all, ok := RebuildWholeWebVTT(track)
	if !ok {
		return nil, false
	}
	var out strings.Builder
	out.WriteString("WEBVTT\n")
	cues := 0
	for _, cue := range strings.Split(string(all), "\n\n")[1:] {
		timing, _, _ := strings.Cut(cue, "\n")
		m := cueTiming.FindStringSubmatch(timing)
		if m == nil || !inStretch(cueMs(m[1]), cueMs(m[2]), n) {
			continue
		}
		if cues++; cues > maxCues {
			return nil, false
		}
		out.WriteString("\n" + strings.TrimSuffix(cue, "\n") + "\n")
	}
	return []byte(out.String()), true
}

// cueMs is a cue timestamp, [hours:]minutes:seconds.milliseconds, in
// milliseconds.
func cueMs(ts string) int64 {
	var ms int64
	clock, frac, _ := strings.Cut(ts, ".")
	for _, part := range strings.Split(clock, ":") {
		n, _ := strconv.ParseInt(part, 10, 64)
		ms = ms*60 + n
	}
	f, _ := strconv.ParseInt(frac, 10, 64)
	return ms*1000 + f
}
