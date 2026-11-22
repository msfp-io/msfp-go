package wire

import (
	"encoding/binary"
	"fmt"
)

// Fonts (MSFP video, section 6.6): the fonts a copy's ASS tracks use,
// served apart, at most 8 a copy. Both sides check a font's
// structure before serving or relaying it; a viewer's server may refuse
// fonts entirely and never fetch them.

// The bounds of fonts (video section 6.6, media sections 1 and 2.4).
const (
	MaxFont       = 10 << 20 // bytes, a font
	maxFontTables = 64
)

// The media types a font is served as (media section 3.4).
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

// CheckFont checks a font as video section 6.6 says, in its steps: at
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
