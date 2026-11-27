package wire

import (
	"encoding/binary"
	"fmt"
	"strconv"
)

// Fonts (MSFP Media video 6.6): the fonts a copy's ASS tracks use,
// served apart, at most 64 a copy, 64 MiB in all. Both sides check a font's
// structure before serving or relaying it; a viewer's server may refuse
// fonts entirely and never fetch them.

// The bounds of fonts (MSFP Media video 6.6, MSFP Media fetching 1 and 2.4).
const (
	MaxFont       = 10 << 20 // bytes, a font
	MaxFonts      = 64       // fonts a copy, its URLs fonts/0 to fonts/63
	MaxFontsBytes = 64 << 20 // bytes, a copy's fonts in all
	maxFontTables = 64
)

// CheckFontSet checks the sizes of one copy's fonts, in the order of
// fonts, against MSFP Media video 6.6's bounds: at most MaxFonts, each 1 to
// MaxFont bytes, MaxFontsBytes in all. The error names the first font
// that breaks a bound: one the owning server doesn't list, and a
// viewer's server refuses (those it relays in a playback adding up).
func CheckFontSet(sizes []int64) error {
	var total int64
	for i, n := range sizes {
		switch {
		case i == MaxFonts:
			return fmt.Errorf("msfp: font %d: more than %d fonts", i, MaxFonts)
		case n < 1:
			return fmt.Errorf("msfp: font %d: a size that isn't an integer from 1", i)
		case n > MaxFont:
			return fmt.Errorf("msfp: font %d: over 10 MiB", i)
		}
		if total += n; total > MaxFontsBytes {
			return fmt.Errorf("msfp: font %d: the copy's fonts over 64 MiB in all", i)
		}
	}
	return nil
}

// ReadFontURLs reads the fonts member of play's answer as a viewer's
// server keeps it (MSFP Media video 5.3): an array of strings, at most
// MaxFonts, entry n exactly <base>fonts/<n>, base being the stream's base
// (StreamBase: <stream root><ticket>/), every font under that one
// ticket. An error drops the member.
func ReadFontURLs(v any, base string) ([]string, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("msfp: fonts isn't an array")
	}
	if len(list) > MaxFonts {
		return nil, fmt.Errorf("msfp: more than %d fonts", MaxFonts)
	}
	out := []string{}
	for n, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("msfp: fonts[%d] isn't a string", n)
		}
		if s != base+"fonts/"+strconv.Itoa(n) {
			return nil, fmt.Errorf("msfp: fonts[%d] isn't <stream root><ticket>/fonts/%d", n, n)
		}
		out = append(out, s)
	}
	return out, nil
}

// The media types a font is served as (MSFP Media fetching 3.4).
const (
	FontTTF = "font/ttf"
	FontOTF = "font/otf"
)

// fontTables are the tables every font has; it has glyf and loca too, or
// CFF or CFF2.
var fontTables = []string{"cmap", "head", "hhea", "hmtx", "maxp", "name", "post"}

func fontStep(step int, format string, a ...any) error {
	return fmt.Errorf("msfp: a font refused, step %d: %s", step, fmt.Sprintf(format, a...))
}

// CheckFont checks a font as MSFP Media video 6.6 says, in its steps: at
// most MaxFont bytes; TrueType (00 01 00 00, or Apple's "true") or
// OpenType with CFF ("OTTO"), never a collection, WOFF or another kind;
// 1 to 64 tables, the directory and every table within the file, each
// tag of printable ASCII and once; the required tables. It gives the
// type the font is served as: FontTTF or FontOTF.
func CheckFont(b []byte) (string, error) {
	if len(b) > MaxFont {
		return "", fontStep(1, "over 10 MiB")
	}
	if len(b) < 4 {
		return "", fontStep(2, "under 4 bytes")
	}
	var typ string
	switch string(b[:4]) {
	case "\x00\x01\x00\x00", "true":
		typ = FontTTF
	case "OTTO":
		typ = FontOTF
	default:
		return "", fontStep(2, "neither TrueType nor OpenType")
	}
	if len(b) < 6 {
		return "", fontStep(3, "no table count")
	}
	n := int(binary.BigEndian.Uint16(b[4:]))
	if n < 1 || n > maxFontTables {
		return "", fontStep(3, "%d tables", n)
	}
	if 12+16*n > len(b) {
		return "", fontStep(3, "the table directory past the end")
	}
	tags := map[string]bool{}
	for i := range n {
		rec := b[12+16*i:]
		for _, c := range rec[:4] {
			if c < 0x20 || c > 0x7e {
				return "", fontStep(4, "a tag byte outside 0x20 to 0x7E")
			}
		}
		tag := string(rec[:4])
		if tags[tag] {
			return "", fontStep(4, "the tag %q twice", tag)
		}
		tags[tag] = true
		// In 64 bits: an offset and a length can't wrap.
		off, length := uint64(binary.BigEndian.Uint32(rec[8:])), uint64(binary.BigEndian.Uint32(rec[12:]))
		if off+length > uint64(len(b)) {
			return "", fontStep(4, "the table %q past the end", tag)
		}
	}
	for _, t := range fontTables {
		if !tags[t] {
			return "", fontStep(5, "no %s table", t)
		}
	}
	if !(tags["glyf"] && tags["loca"]) && !tags["CFF "] && !tags["CFF2"] {
		return "", fontStep(5, "no outlines: neither glyf and loca, nor CFF or CFF2")
	}
	return typ, nil
}
