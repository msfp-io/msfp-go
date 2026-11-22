package wire

// Open values (MSFP core section 8.5). Many fields take a value from a
// list that grows: a codec, a container, a library's kind. MSFP keeps no
// such list: a field it calls open takes a token, and a receiver keeps a
// token it doesn't know as it is. Where values come from an outside
// vocabulary, the part names it: ffmpeg's names for containers, codecs
// and subtitle formats (video section 3.5), the DASH role scheme for
// audio roles, MusicBrainz's release-group types for albums. A sender
// whose media server names these otherwise maps its names to those; this
// package maps nothing.

// MaxToken is a token's greatest length, in bytes.
const MaxToken = 32

// Token says whether s is a token of core section 8.5: 1 to 32 bytes of
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

// LibraryKind is a library's kind as a caller keeps it (catalog section
// 2.2): a token, known or not (movies, tvshows, music, mixed…); "",
// absent, for anything else (an empty string, another case, a space).
// The kinds are open: a server sends the kind its media server gives the
// library, and leaves it out when it has none.
func LibraryKind(v any) string { return tokenOf(v) }
