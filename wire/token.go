package wire

import "strings"

// Open values (MSFP Media base 3). Many fields take a value from a
// list that grows: a codec, a subtitle format, an edition. MSFP keeps no
// closed list for them: a field it calls open takes a token (or, for a
// container, a media type), and a receiver keeps a value it doesn't know
// as it is. The lists are MSFP's registry (spec/registry.md) or an
// outside vocabulary the part names: IANA's media types, RFC 6381's
// codecs strings, ITU-T H.273's colour code points, the DASH role
// scheme, MusicBrainz's release-group types. A sender whose media server
// names these otherwise maps its names to those; this package maps
// nothing.

// MaxToken is a token's greatest length, in bytes.
const MaxToken = 32

// Token says whether s is a token of MSFP Media base 3: 1 to 32 bytes of
// a–z 0–9 . _ -.
func Token(s string) bool {
	if len(s) == 0 || len(s) > MaxToken {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// tokenOf is v when it is a string that is a token, else "": how a
// receiver keeps an optional open member (absent when it isn't a token).
func tokenOf(v any) string {
	if s, ok := v.(string); ok && Token(s) {
		return s
	}
	return ""
}

// MaxMediaType is a media type's greatest length, in bytes (RFC 6838's
// bound).
const MaxMediaType = 127

// IsMediaType says whether s is a media type of MSFP Media base 3: an
// IANA-form media type, lowercase, exactly one /, no parameters, each
// half starting with a letter or a digit, at most 127 bytes of a–z 0–9
// . + - _ /. Fields that take one (a copy's mediaType, a remux's) follow
// the rules of tokens.
func IsMediaType(s string) bool {
	if len(s) == 0 || len(s) > MaxMediaType {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '+' || c == '-' || c == '_' || c == '/') {
			return false
		}
	}
	top, sub, ok := strings.Cut(s, "/")
	if !ok || strings.Contains(sub, "/") {
		return false
	}
	alnum := func(h string) bool { return h != "" && (h[0] >= 'a' && h[0] <= 'z' || h[0] >= '0' && h[0] <= '9') }
	return alnum(top) && alnum(sub)
}

// mediaTypeOf is v when it is a string that is a media type, else "".
func mediaTypeOf(v any) string {
	if s, ok := v.(string); ok && IsMediaType(s) {
		return s
	}
	return ""
}

// PairedSubtypes are the subtypes for which a receiver treats audio/<sub>
// and video/<sub> as the same container (the registry's section 4.1).
var PairedSubtypes = []string{"mp4", "matroska", "webm", "ogg", "3gpp", "3gpp2", "mp2t"}

// SameContainer says whether two media types name the same container:
// equal, or audio/<sub> and video/<sub> of a pair the registry lists.
func SameContainer(a, b string) bool {
	if a == b {
		return true
	}
	ta, sa, _ := strings.Cut(a, "/")
	tb, sb, _ := strings.Cut(b, "/")
	if sa != sb || !(ta == "audio" && tb == "video" || ta == "video" && tb == "audio") {
		return false
	}
	for _, p := range PairedSubtypes {
		if p == sa {
			return true
		}
	}
	return false
}
