package wire

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text subtitles as ASS (MSFP Media video 6.4): an ASS or SSA script
// served at a track's assUrl is rebuilt, not filtered: known sections,
// known lines and known override tags only, anything else dropped, sizes
// capped, and written back as an ASS (v4.00+) script in one form. The
// owning server rebuilds what it serves (converting SSA to ASS on the way),
// the viewer's server again what it relays.

// The bounds of ASS (MSFP Media video 6.4, MSFP Media fetching 2.4); a whole track
// is at most MaxWholeText bytes.
const (
	MaxASS            = 5 << 20 // bytes, a part as read
	maxASSStyles      = 1024
	maxASSEvents      = 50_000  // a part's events, once rebuilt
	maxWholeASSEvents = 200_000 // a whole track's
	maxASSEventText   = 65_536  // bytes of an event's text, once rebuilt
	maxASSBlocks      = 256     // override blocks an event
	maxASSTags        = 1024    // kept tags an event
	maxASSTransforms  = 16      // \t an event
	maxASSNumbers     = 10_000  // numbers a drawing
	maxASSName        = 64      // bytes of a style's name or font name
)

// RebuildASS rebuilds an ASS or SSA part (a stretch) as MSFP Media video 6.4
// says: false when it is over MaxASS, its first line isn't [Script Info],
// or it holds more than 50,000 events once rebuilt.
func RebuildASS(in []byte) ([]byte, bool) { return rebuildASS(in, MaxASS, maxASSEvents) }

// RebuildWholeASS rebuilds a whole ASS or SSA track (an ass/<track>/whole
// answer) under the same rules, with a whole track's bounds: MaxWholeText
// bytes, 200,000 events.
func RebuildWholeASS(in []byte) ([]byte, bool) {
	return rebuildASS(in, MaxWholeText, maxWholeASSEvents)
}

// ASSPart is part n of a whole ASS or SSA track, for an owning server that
// holds its subtitles as one file: the track rebuilt as RebuildWholeASS
// does, its header and the events that show during the part's stretch
// (MSFP Media fetching 3.4, MSFP Media video 6.4). A part with no event is valid.
// False when the track is refused, or its part holds more events than a
// part may.
func ASSPart(track []byte, n int) ([]byte, bool) {
	all, ok := RebuildWholeASS(track)
	if !ok {
		return nil, false
	}
	header, events, found := strings.Cut(string(all), "\nDialogue: ")
	if !found {
		return all, true
	}
	var out strings.Builder
	out.WriteString(header + "\n")
	kept := 0
	for _, e := range strings.Split(strings.TrimSuffix("Dialogue: "+events, "\n"), "\n") {
		// Rebuilt: layer, start, end first, in one form.
		f := strings.SplitN(strings.TrimPrefix(e, "Dialogue: "), ",", 4)
		if !inStretch(assMs(f[1]), assMs(f[2]), n) {
			continue
		}
		if kept++; kept > maxASSEvents {
			return nil, false
		}
		out.WriteString(e + "\n")
	}
	return []byte(out.String()), true
}

// assMs is an event's time, hours:minutes:seconds.centiseconds, in
// milliseconds.
func assMs(t string) int64 {
	clock, cs, _ := strings.Cut(t, ".")
	var ms int64
	for _, part := range strings.Split(clock, ":") {
		n, _ := strconv.ParseInt(part, 10, 64)
		ms = ms*60 + n
	}
	c, _ := strconv.ParseInt(cs, 10, 64)
	return ms*1000 + c*10
}

// assLine is a line of a known section: the section's header, or a name
// and its value.
type assLine struct {
	section     string // "info", "v4+", "v4" or "events"
	header      bool
	name, value string
}

func rebuildASS(in []byte, most, mostEvents int) ([]byte, bool) {
	if len(in) > most {
		return nil, false
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(in), "\r\n", "\n"), "\r", "\n")
	text = strings.TrimPrefix(text, "\uFEFF")
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimFunc(l, unicode.IsSpace)
		if l == "" || strings.HasPrefix(l, ";") || strings.HasPrefix(l, "!:") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 || lines[0] != "[Script Info]" {
		return nil, false
	}
	// The lines of the known sections; every other section's go whole.
	var known []assLine
	section := ""
	v4, v4plus := false, false
	for _, l := range lines {
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			switch {
			case assFold(l, "[Script Info]"):
				section = "info"
			case assFold(l, "[V4+ Styles]"):
				section, v4plus = "v4+", true
			case assFold(l, "[V4 Styles]"):
				section, v4 = "v4", true
			case assFold(l, "[Events]"):
				section = "events"
			default:
				section = ""
			}
			if section != "" {
				known = append(known, assLine{section: section, header: true})
			}
			continue
		}
		name, value, ok := strings.Cut(l, ":")
		if section == "" || !ok {
			continue
		}
		known = append(known, assLine{section: section, name: name, value: strings.TrimFunc(value, unicode.IsSpace)})
	}

	// Script information: the first line of each name, kept when valid.
	info := map[string]string{}
	seen := map[string]bool{}
	for _, l := range known {
		if l.section != "info" || l.header {
			continue
		}
		for _, n := range assInfo {
			if assFold(l.name, n.name) && !seen[n.name] {
				seen[n.name] = true
				if n.ok(l.value) {
					info[n.name] = l.value
				}
			}
		}
	}

	// Styles.
	var styles []string
	names := map[string]bool{}
	var format []string
	for _, l := range known {
		ssa := l.section == "v4"
		if !ssa && l.section != "v4+" {
			continue
		}
		switch {
		case l.header && ssa:
			format = assV4Style
		case l.header:
			format = assV4PlusStyle
		case l.name == "Format":
			format = assFormat(l.value)
		case l.name == "Style":
			s, name, ok := assStyle(l.value, format, ssa)
			if !ok || names[name] || len(styles) == maxASSStyles {
				continue
			}
			names[name] = true
			styles = append(styles, s)
		}
	}

	// Events: an SSA script's take SSA's default format.
	var events []string
	for _, l := range known {
		if l.section != "events" {
			continue
		}
		switch {
		case l.header && v4 && !v4plus:
			format = assSSAEvent
		case l.header:
			format = assEventFormat
		case l.name == "Format":
			format = assFormat(l.value)
		case l.name == "Dialogue":
			e, ok := assEvent(l.value, format, names)
			if !ok {
				continue
			}
			if len(events) == mostEvents {
				return nil, false
			}
			events = append(events, e)
		}
	}

	var out strings.Builder
	out.WriteString("[Script Info]\nScriptType: v4.00+\n")
	for _, n := range assInfo {
		if v, ok := info[n.name]; ok {
			out.WriteString(n.name + ": " + v + "\n")
		}
	}
	out.WriteString("\n[V4+ Styles]\nFormat: " + strings.Join(assV4PlusStyle, ", ") + "\n")
	if !names["Default"] {
		out.WriteString("Style: " + assDefaultStyle() + "\n")
	}
	for _, s := range styles {
		out.WriteString("Style: " + s + "\n")
	}
	out.WriteString("\n[Events]\nFormat: " + strings.Join(assEventFormat, ", ") + "\n")
	for _, e := range events {
		out.WriteString("Dialogue: " + e + "\n")
	}
	return []byte(out.String()), true
}

// assFold says whether a and b are equal without ASCII case (and only
// ASCII's: "K", the Kelvin sign, isn't "k").
func assFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if assLower(a[i]) != assLower(b[i]) {
			return false
		}
	}
	return true
}

func assLower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// assInfo is the script information kept, in the order it is written.
var assInfo = []struct {
	name string
	ok   func(string) bool
}{
	{"PlayResX", assResolution},
	{"PlayResY", assResolution},
	{"LayoutResX", assResolution},
	{"LayoutResY", assResolution},
	{"WrapStyle", assOneOf("0", "1", "2", "3")},
	{"ScaledBorderAndShadow", assOneOf("yes", "no")},
	{"Collisions", assOneOf("Normal", "Reverse")},
	{"Kerning", assOneOf("yes", "no")},
	{"YCbCr Matrix", assOneOf("None", "TV.601", "PC.601", "TV.709", "PC.709", "TV.FCC", "PC.FCC", "TV.240M", "PC.240M")},
}

func assResolution(s string) bool { return mediaInt(s, 1, 16384) }

func assOneOf(set ...string) func(string) bool {
	return func(s string) bool { return slices.Contains(set, s) }
}

// assNumberForm is ASS's number: an optional "-", 1 to 7 digits, then
// optionally "." and 1 to 3 digits.
var assNumberForm = regexp.MustCompile(`^-?\d{1,7}(?:\.\d{1,3})?$`)

func assNumber(s string, lo, hi float64) bool {
	if !assNumberForm.MatchString(s) {
		return false
	}
	v, err := strconv.ParseFloat(s, 64)
	return err == nil && v >= lo && v <= hi
}

func assNumIn(lo, hi float64) func(string) bool {
	return func(s string) bool { return assNumber(s, lo, hi) }
}

func assIntIn(lo, hi int64) func(string) bool {
	return func(s string) bool { return mediaInt(s, lo, hi) }
}

// assPlain makes text plain: invalid UTF-8, control characters and
// commas removed.
func assPlain(s string) string {
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || r == ',' {
			return -1
		}
		return r
	}, s)
}

var (
	assStyleHex  = regexp.MustCompile(`^&H([0-9A-Fa-f]{1,8})&?$`)
	assDecimal   = regexp.MustCompile(`^\d+$`)
	assTagColour = regexp.MustCompile(`^&H([0-9A-Fa-f]{1,6})&?$`)
	assTagAlpha  = regexp.MustCompile(`^&H([0-9A-Fa-f]{1,2})&?$`)
	assTime      = regexp.MustCompile(`^\d{1,3}:\d{2}:\d{2}\.\d{2}$`)
)

// assColour reads a style's colour, "&H" and 1 to 8 hexadecimal digits or
// a decimal integer, written "&H" and 8 uppercase digits.
func assColour(s string, _ bool) (string, bool) {
	var v uint64
	var err error
	if m := assStyleHex.FindStringSubmatch(s); m != nil {
		v, err = strconv.ParseUint(m[1], 16, 32)
	} else if assDecimal.MatchString(s) {
		v, err = strconv.ParseUint(s, 10, 32)
	} else {
		return "", false
	}
	return fmt.Sprintf("&H%08X", v), err == nil
}

// An assField is a field of a written style: its name, its default, and
// what it is written as (false when its value is bad).
type assField struct {
	name, def string
	read      func(v string, ssa bool) (string, bool)
}

func assValue(ok func(string) bool) func(string, bool) (string, bool) {
	return func(v string, _ bool) (string, bool) { return v, ok(v) }
}

func assName(v string, _ bool) (string, bool) {
	p := assPlain(v)
	return p, len(p) >= 1 && len(p) <= maxASSName
}

// assFlag is -1 or 0, any other integer -1.
func assFlag(v string, _ bool) (string, bool) {
	if !mediaIntForm.MatchString(v) {
		return "", false
	}
	if strings.Trim(strings.TrimPrefix(v, "-"), "0") == "" {
		return "0", true
	}
	return "-1", true
}

// ssaAlignment is how SSA's alignments become ASS's.
var ssaAlignment = map[string]string{"1": "1", "2": "2", "3": "3", "5": "7", "6": "8", "7": "9", "9": "4", "10": "5", "11": "6"}

// assAlignment reads an alignment: 1 to 9, or SSA's, converted.
func assAlignment(v string, ssa bool) (string, bool) {
	if ssa {
		a, ok := ssaAlignment[v]
		return a, ok
	}
	return v, len(v) == 1 && '1' <= v[0] && v[0] <= '9'
}

// assFields are a written style's fields, in ASS's order.
var assFields = []assField{
	{"Name", "", assName},
	{"Fontname", "Arial", assName},
	{"Fontsize", "20", assValue(assNumIn(0, 1000))},
	{"PrimaryColour", "&H00FFFFFF", assColour},
	{"SecondaryColour", "&H000000FF", assColour},
	{"OutlineColour", "&H00000000", assColour},
	{"BackColour", "&H00000000", assColour},
	{"Bold", "0", assValue(assIntIn(-1, 1000))},
	{"Italic", "0", assFlag},
	{"Underline", "0", assFlag},
	{"StrikeOut", "0", assFlag},
	{"ScaleX", "100", assValue(assNumIn(0, 1000))},
	{"ScaleY", "100", assValue(assNumIn(0, 1000))},
	{"Spacing", "0", assValue(assNumIn(-1000, 1000))},
	{"Angle", "0", assValue(assNumIn(-360, 360))},
	{"BorderStyle", "1", assValue(assOneOf("1", "3", "4"))},
	{"Outline", "2", assValue(assNumIn(0, 100))},
	{"Shadow", "2", assValue(assNumIn(0, 100))},
	{"Alignment", "2", assAlignment},
	{"MarginL", "10", assValue(assMargin)},
	{"MarginR", "10", assValue(assMargin)},
	{"MarginV", "10", assValue(assMargin)},
	{"Encoding", "1", assValue(assIntIn(0, 255))},
}

func assMargin(s string) bool { return mediaInt(s, 0, 16384) }

// The standard formats: of styles, ASS's and SSA's; of events, ASS's and
// SSA's.
var (
	assV4PlusStyle = []string{"Name", "Fontname", "Fontsize", "PrimaryColour", "SecondaryColour", "OutlineColour", "BackColour",
		"Bold", "Italic", "Underline", "StrikeOut", "ScaleX", "ScaleY", "Spacing", "Angle", "BorderStyle", "Outline", "Shadow",
		"Alignment", "MarginL", "MarginR", "MarginV", "Encoding"}
	assV4Style = []string{"Name", "Fontname", "Fontsize", "PrimaryColour", "SecondaryColour", "TertiaryColour", "BackColour",
		"Bold", "Italic", "BorderStyle", "Outline", "Shadow", "Alignment", "MarginL", "MarginR", "MarginV", "AlphaLevel", "Encoding"}
	assEventFormat = []string{"Layer", "Start", "End", "Style", "Name", "MarginL", "MarginR", "MarginV", "Effect", "Text"}
	assSSAEvent    = []string{"Marked", "Start", "End", "Style", "Name", "MarginL", "MarginR", "MarginV", "Effect", "Text"}
)

// assFormat reads a Format: line's field names.
func assFormat(v string) []string {
	f := strings.Split(v, ",")
	for i := range f {
		f[i] = strings.TrimFunc(f[i], unicode.IsSpace)
	}
	return f
}

// assStyle reads a Style: line under its format: the style as written,
// and its name; false when it is dropped.
func assStyle(value string, format []string, ssa bool) (string, string, bool) {
	fields := strings.Split(value, ",")
	if len(fields) != len(format) {
		return "", "", false
	}
	raw := map[string]string{}
	for i, name := range format {
		if ssa && name == "TertiaryColour" {
			name = "OutlineColour"
		}
		if _, ok := raw[name]; !ok {
			raw[name] = strings.TrimFunc(fields[i], unicode.IsSpace)
		}
	}
	out := make([]string, len(assFields))
	for i, f := range assFields {
		v, present := raw[f.name]
		w, ok := "", false
		if present {
			w, ok = f.read(v, ssa)
		}
		if !ok {
			if f.name == "Name" {
				return "", "", false
			}
			w = f.def
		}
		out[i] = w
	}
	return strings.Join(out, ","), out[0], true
}

// assDefaultStyle is the style Default, of every default.
func assDefaultStyle() string {
	out := make([]string, len(assFields))
	for i, f := range assFields {
		out[i] = f.def
	}
	out[0] = "Default"
	return strings.Join(out, ",")
}

// assEvent reads a Dialogue: line under its format: the event as
// written, or false when it is dropped.
func assEvent(value string, format []string, styles map[string]bool) (string, bool) {
	if len(format) == 0 {
		return "", false
	}
	parts := strings.SplitN(value, ",", len(format))
	if len(parts) != len(format) {
		return "", false
	}
	raw := map[string]string{}
	for i, name := range format[:len(format)-1] {
		if _, ok := raw[name]; !ok {
			raw[name] = strings.TrimFunc(parts[i], unicode.IsSpace)
		}
	}
	field := func(name, def string, ok func(string) bool) string {
		if v, present := raw[name]; present && ok(v) {
			return v
		}
		return def
	}
	start, end := raw["Start"], raw["End"]
	if !assTime.MatchString(start) || !assTime.MatchString(end) {
		return "", false
	}
	text := assText(parts[len(parts)-1], styles)
	if text == "" || len(text) > maxASSEventText {
		return "", false
	}
	return strings.Join([]string{
		field("Layer", "0", assIntIn(0, 1000)), start, end,
		field("Style", "Default", func(s string) bool { return styles[s] }), "",
		field("MarginL", "0", assMargin), field("MarginR", "0", assMargin), field("MarginV", "0", assMargin),
		"", text,
	}, ","), true
}

// assText rebuilds an event's text: text outside braces kept (a drawing's
// rebuilt), override blocks rebuilt, within the counts of an event.
func assText(s string, styles map[string]bool) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r == utf8.RuneError || directionControl(r) || unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	var out strings.Builder
	drawing, full := false, false
	blocks, tags, transforms := 0, 0, 0
	for {
		piece, rest, block := strings.Cut(s, "{")
		if drawing {
			piece = assDrawing(piece)
		}
		out.WriteString(piece)
		if !block {
			break
		}
		inner, after, closed := strings.Cut(rest, "}")
		if !closed {
			break // the rest of the text goes
		}
		s = after
		if full {
			continue
		}
		t := transforms
		kept, p := assBlock(inner, styles, &t)
		if len(kept) == 0 {
			continue
		}
		if blocks == maxASSBlocks || tags+len(kept) > maxASSTags {
			full = true // this block and every one after it go
			continue
		}
		blocks++
		tags += len(kept)
		transforms = t
		out.WriteString("{" + strings.Join(kept, "") + "}")
		if p >= 0 {
			drawing = p > 0
		}
	}
	return out.String()
}

// assCut cuts a block's tags (from its first "\") at each "\", but not
// inside a \t's parentheses: each tag without its "\".
func assCut(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		j := i + 1
		transform := strings.HasPrefix(s[j:], "t(")
		depth := 0
		for ; j < len(s); j++ {
			c := s[j]
			if c == '\\' && depth == 0 {
				break
			}
			if transform && c == '(' {
				depth++
			} else if transform && c == ')' && depth > 0 {
				depth--
			}
		}
		out = append(out, s[i+1:j])
		i = j
	}
	return out
}

// assBlock rebuilds what's inside an override block: its kept tags, and
// the last \p's value (-1 without one). What comes before the first "\"
// is a comment, dropped.
func assBlock(inner string, styles map[string]bool, transforms *int) ([]string, int) {
	k := strings.IndexByte(inner, '\\')
	if k < 0 {
		return nil, -1
	}
	var kept []string
	p := -1
	for _, raw := range assCut(inner[k:]) {
		t, pv, ok := assTag(raw, styles, false, transforms)
		if !ok {
			continue
		}
		if pv >= 0 {
			p = pv
		}
		kept = append(kept, t)
	}
	return kept, p
}

// assTagNames are the override tags kept; a tag is the longest of them
// its text starts with.
var assTagNames = []string{"b", "i", "u", "s", "bord", "xbord", "ybord", "be", "blur", "shad", "xshad", "yshad",
	"fn", "fs", "fscx", "fscy", "fsp", "frx", "fry", "frz", "fr", "fax", "fay", "fe",
	"c", "1c", "2c", "3c", "4c", "alpha", "1a", "2a", "3a", "4a", "an", "a", "k", "K", "kf", "ko", "q", "r",
	"pos", "org", "move", "fad", "fade", "t", "clip", "iclip", "p", "pbo"}

// assNotInTransform are the tags a \t never holds.
var assNotInTransform = []string{"t", "pos", "org", "move", "fad", "fade", "an", "a", "k", "K", "kf", "ko", "q", "r", "fn", "p", "pbo"}

// assParens reads "(a,b,…)": the arguments, white space around them
// removed.
func assParens(args string) ([]string, bool) {
	if !strings.HasPrefix(args, "(") || !strings.HasSuffix(args, ")") {
		return nil, false
	}
	parts := strings.Split(args[1:len(args)-1], ",")
	for i := range parts {
		parts[i] = strings.TrimFunc(parts[i], unicode.IsSpace)
	}
	return parts, true
}

func assAll(parts []string, ok func(string) bool) bool {
	for _, p := range parts {
		if !ok(p) {
			return false
		}
	}
	return true
}

var (
	assCoord  = assNumIn(-100_000, 100_000)
	assMillis = assIntIn(0, 3_600_000)
	assByte   = assIntIn(0, 255)
)

// assTag checks one override tag (without its "\"): the tag as written,
// the value of a \p (-1 for another tag), or false when it is dropped.
func assTag(raw string, styles map[string]bool, inTransform bool, transforms *int) (string, int, bool) {
	name := ""
	for _, n := range assTagNames {
		if strings.HasPrefix(raw, n) && len(n) > len(name) {
			name = n
		}
	}
	if name == "" || inTransform && slices.Contains(assNotInTransform, name) {
		return "", -1, false
	}
	args := raw[len(name):]
	as := func(ok bool) (string, int, bool) { return `\` + raw, -1, ok }
	written := func(w string, ok bool) (string, int, bool) { return w, -1, ok }
	switch name {
	case "b":
		return as(mediaInt(args, 0, 1000))
	case "i", "u", "s":
		return as(args == "0" || args == "1")
	case "bord", "xbord", "ybord", "be", "blur":
		return as(assNumber(args, 0, 100))
	case "shad", "xshad", "yshad", "fax", "fay":
		return as(assNumber(args, -100, 100))
	case "fn":
		f := assPlain(args)
		return written(`\fn`+f, len(f) <= maxASSName)
	case "fs", "fscx", "fscy":
		return as(assNumber(args, 0, 1000))
	case "fsp":
		return as(assNumber(args, -1000, 1000))
	case "frx", "fry", "frz", "fr":
		return as(assNumber(args, -3600, 3600))
	case "fe":
		return as(assByte(args))
	case "c", "1c", "2c", "3c", "4c":
		m := assTagColour.FindStringSubmatch(args)
		if m == nil {
			return "", -1, false
		}
		v, _ := strconv.ParseUint(m[1], 16, 32)
		return written(fmt.Sprintf(`\%s&H%06X&`, name, v), true)
	case "alpha", "1a", "2a", "3a", "4a":
		m := assTagAlpha.FindStringSubmatch(args)
		if m == nil {
			return "", -1, false
		}
		v, _ := strconv.ParseUint(m[1], 16, 32)
		return written(fmt.Sprintf(`\%s&H%02X&`, name, v), true)
	case "an":
		_, ok := assAlignment(args, false)
		return as(ok)
	case "a":
		a, ok := assAlignment(args, true)
		return written(`\an`+a, ok)
	case "k", "K", "kf", "ko":
		return as(mediaInt(args, 0, 100_000))
	case "q":
		return as(assOneOf("0", "1", "2", "3")(args))
	case "r":
		return as(args == "" || styles[args])
	case "p":
		if !mediaInt(args, 0, 16) {
			return "", -1, false
		}
		v, _ := strconv.Atoi(args)
		return `\p` + args, v, true
	case "pbo":
		return as(assCoord(args))
	case "pos", "org":
		a, ok := assParens(args)
		return written(`\`+name+"("+strings.Join(a, ",")+")", ok && len(a) == 2 && assAll(a, assCoord))
	case "move":
		a, ok := assParens(args)
		ok = ok && (len(a) == 4 || len(a) == 6) && assAll(a[:4], assCoord) && assAll(a[4:], assMillis)
		return written(`\move(`+strings.Join(a, ",")+")", ok)
	case "fad":
		a, ok := assParens(args)
		return written(`\fad(`+strings.Join(a, ",")+")", ok && len(a) == 2 && assAll(a, assMillis))
	case "fade":
		a, ok := assParens(args)
		ok = ok && len(a) == 7 && assAll(a[:3], assByte) && assAll(a[3:], assMillis)
		return written(`\fade(`+strings.Join(a, ",")+")", ok)
	case "clip", "iclip":
		a, ok := assParens(args)
		if !ok {
			return "", -1, false
		}
		switch len(a) {
		case 4: // a rectangle
			return written(`\`+name+"("+strings.Join(a, ",")+")", assAll(a, assCoord))
		case 2: // a scale, a drawing
			d := assDrawing(a[1])
			return written(`\`+name+"("+a[0]+","+d+")", mediaInt(a[0], 1, 16) && d != "")
		case 1: // a drawing
			d := assDrawing(a[0])
			return written(`\`+name+"("+d+")", d != "")
		}
		return "", -1, false
	case "t":
		return assTransform(args, styles, transforms)
	}
	return "", -1, false
}

// assTransform checks a \t's arguments: optionally two times, optionally
// an acceleration, then its tags, each checked.
func assTransform(args string, styles map[string]bool, transforms *int) (string, int, bool) {
	if *transforms == maxASSTransforms || !strings.HasPrefix(args, "(") || !strings.HasSuffix(args, ")") {
		return "", -1, false
	}
	body := args[1 : len(args)-1]
	k := strings.IndexByte(body, '\\')
	if k < 0 {
		return "", -1, false
	}
	nums := assFormat(body[:k]) // the numbers before the tags, each before a ","
	if nums[len(nums)-1] != "" {
		return "", -1, false
	}
	nums = nums[:len(nums)-1]
	switch len(nums) {
	case 0:
	case 1:
		if !assNumber(nums[0], 0, 100) {
			return "", -1, false
		}
	case 2:
		if !assAll(nums, assMillis) {
			return "", -1, false
		}
	case 3:
		if !assAll(nums[:2], assMillis) || !assNumber(nums[2], 0, 100) {
			return "", -1, false
		}
	default:
		return "", -1, false
	}
	var kept []string
	for _, raw := range assCut(strings.TrimFunc(body[k:], unicode.IsSpace)) {
		if t, _, ok := assTag(raw, styles, true, transforms); ok {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		return "", -1, false
	}
	*transforms++
	pre := strings.Join(nums, ",")
	if pre != "" {
		pre += ","
	}
	return `\t(` + pre + strings.Join(kept, "") + ")", -1, true
}

// assDrawing rebuilds a drawing: its commands and numbers (at most
// 10,000), single spaces between them, anything else dropped.
func assDrawing(s string) string {
	var out []string
	numbers := 0
	for _, f := range strings.FieldsFunc(s, unicode.IsSpace) {
		switch {
		case len(f) == 1 && strings.Contains("mnlbspc", f):
			out = append(out, f)
		case assCoord(f):
			if numbers++; numbers > maxASSNumbers {
				return strings.Join(out, " ")
			}
			out = append(out, f)
		}
	}
	return strings.Join(out, " ")
}
