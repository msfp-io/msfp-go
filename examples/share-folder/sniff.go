package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// mediaTypeOf is a file's media type (MSFP video section 3.5), from its
// first bytes where it can (an ISO BMFF brand, a Matroska DocType, a
// transport stream's packets), else from its extension; audio/<sub> for
// a file with no picture, of the pairs the registry lists.
func mediaTypeOf(path string, picture bool) string {
	head := make([]byte, 4096)
	if f, err := os.Open(path); err == nil {
		n, _ := f.Read(head)
		head = head[:n]
		f.Close()
	} else {
		head = nil
	}
	t := sniff(head)
	if t == "" {
		t = map[string]string{".mp4": "video/mp4", ".m4v": "video/mp4", ".mkv": "video/matroska", ".webm": "video/webm",
			".mov": "video/quicktime", ".ts": "video/mp2t", ".m2ts": "video/vnd.dlna.mpeg-tts"}[strings.ToLower(filepath.Ext(path))]
	}
	if t == "" {
		t = "video/mp4"
	}
	return audioOrVideo(t, picture)
}

// audioOrVideo turns video/<sub> into audio/<sub> for a file with no
// picture, for the pairs the registry lists (section 4.1).
func audioOrVideo(t string, picture bool) string {
	if sub, ok := strings.CutPrefix(t, "video/"); ok && !picture {
		switch sub {
		case "mp4", "matroska", "webm", "ogg", "3gpp", "3gpp2", "mp2t":
			return "audio/" + sub
		}
	}
	return t
}

// brandTypes are the registry's brand table (section 4.5): the media
// type an ISO BMFF brand chooses.
var brandTypes = map[string]string{"qt  ": "video/quicktime", "mp41": "video/mp4", "mp42": "video/mp4",
	"avc1": "video/mp4", "av01": "video/mp4", "M4V ": "video/mp4", "MSNV": "video/mp4", "dby1": "video/mp4",
	"M4A ": "audio/mp4", "M4B ": "audio/mp4", "M4P ": "audio/mp4", "iamf": "audio/mp4",
	"3g2a": "video/3gpp2", "3g2b": "video/3gpp2", "mjp2": "video/mj2", "mj2s": "video/mj2"}

func brandType(b string) string {
	switch {
	case brandTypes[b] != "":
		return brandTypes[b]
	case b == "isom" || len(b) == 4 && strings.HasPrefix(b, "iso"):
		return "video/mp4"
	case strings.HasPrefix(b, "dash") || strings.HasPrefix(b, "cmf") || b == "msdh" || b == "msix":
		return "video/mp4"
	case strings.HasPrefix(b, "3g"):
		return "video/3gpp"
	}
	return ""
}

// sniff is the media type a file's first bytes say; "" when they say
// none.
func sniff(b []byte) string {
	switch {
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		if t := brandType(string(b[8:12])); t != "" {
			return t
		}
		size := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
		for i := 16; i+4 <= min(size, len(b)); i += 4 {
			if t := brandType(string(b[i : i+4])); t != "" {
				return t
			}
		}
		return "video/mp4" // an unknown brand
	case bytes.HasPrefix(b, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		// The DocType element (0x4282), its size, its string.
		if i := bytes.Index(b, []byte{0x42, 0x82}); i >= 0 && i+3 < len(b) {
			n := int(b[i+2] &^ 0x80)
			if b[i+2]&0x80 != 0 && i+3+n <= len(b) && string(b[i+3:i+3+n]) == "webm" {
				return "video/webm"
			}
		}
		return "video/matroska" // less precise for an unknown DocType, never wrong
	case packets(b, 0, 188):
		return "video/mp2t"
	case packets(b, 4, 192):
		return "video/vnd.dlna.mpeg-tts"
	}
	return ""
}

// packets says whether b holds transport stream packets of size n, their
// sync byte at offset from each packet's start: three in a row.
func packets(b []byte, offset, n int) bool {
	for k := range 3 {
		if i := offset + k*n; i >= len(b) || b[i] != 0x47 {
			return false
		}
	}
	return true
}
