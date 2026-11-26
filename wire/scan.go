package wire

import (
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"
)

// scanner reads a JSON value into the tree strictTree gives, in one pass
// over the bytes: objects as map[string]any (a member name twice
// refused, compared once its escapes are decoded), lists as []any,
// numbers as json.Number (as JSON writes them), strings, booleans, and
// null only with nulls. It is what readValue reads with encoding/json's
// Decoder, token by token, several times faster. The body's characters
// and escapes are checked before (utf8.Valid, escapesOK).
type scanner struct {
	b     []byte
	i     int
	nulls bool
	depth int // how deep a value may nest: maxDepth when 0
}

func (s *scanner) ws() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

// value reads the value at s.i, depth deep.
func (s *scanner) value(depth int) (any, bool) {
	limit := s.depth
	if limit == 0 {
		limit = maxDepth
	}
	if depth > limit || s.i >= len(s.b) {
		return nil, false
	}
	switch c := s.b[s.i]; {
	case c == '{':
		return s.object(depth)
	case c == '[':
		return s.list(depth)
	case c == '"':
		return s.str()
	case c == 't':
		return true, s.literal("true")
	case c == 'f':
		return false, s.literal("false")
	case c == 'n':
		return nil, s.nulls && s.literal("null")
	case c == '-' || c >= '0' && c <= '9':
		return s.number()
	}
	return nil, false
}

func (s *scanner) literal(word string) bool {
	if len(s.b)-s.i < len(word) || string(s.b[s.i:s.i+len(word)]) != word {
		return false
	}
	s.i += len(word)
	return true
}

func (s *scanner) object(depth int) (any, bool) {
	s.i++ // {
	obj := map[string]any{}
	s.ws()
	if s.i < len(s.b) && s.b[s.i] == '}' {
		s.i++
		return obj, true
	}
	for {
		if s.i >= len(s.b) || s.b[s.i] != '"' {
			return nil, false
		}
		name, ok := s.str()
		if !ok {
			return nil, false
		}
		key := name.(string)
		if _, dup := obj[key]; dup {
			return nil, false // the same member twice
		}
		s.ws()
		if s.i >= len(s.b) || s.b[s.i] != ':' {
			return nil, false
		}
		s.i++
		s.ws()
		v, ok := s.value(depth + 1)
		if !ok {
			return nil, false
		}
		obj[key] = v
		s.ws()
		if s.i >= len(s.b) {
			return nil, false
		}
		switch s.b[s.i] {
		case ',':
			s.i++
			s.ws()
		case '}':
			s.i++
			return obj, true
		default:
			return nil, false
		}
	}
}

func (s *scanner) list(depth int) (any, bool) {
	s.i++ // [
	list := []any{}
	s.ws()
	if s.i < len(s.b) && s.b[s.i] == ']' {
		s.i++
		return list, true
	}
	for {
		v, ok := s.value(depth + 1)
		if !ok {
			return nil, false
		}
		list = append(list, v)
		s.ws()
		if s.i >= len(s.b) {
			return nil, false
		}
		switch s.b[s.i] {
		case ',':
			s.i++
			s.ws()
		case ']':
			s.i++
			return list, true
		default:
			return nil, false
		}
	}
}

// number reads a number as JSON writes it: -? (0 | [1-9][0-9]*)
// (.[0-9]+)? ([eE][+-]?[0-9]+)?, kept as written.
func (s *scanner) number() (any, bool) {
	start := s.i
	if s.b[s.i] == '-' {
		s.i++
	}
	switch {
	case s.i < len(s.b) && s.b[s.i] == '0':
		s.i++
	case s.i < len(s.b) && s.b[s.i] >= '1' && s.b[s.i] <= '9':
		s.digits()
	default:
		return nil, false
	}
	if s.i < len(s.b) && s.b[s.i] == '.' {
		s.i++
		if !s.digits() {
			return nil, false
		}
	}
	if s.i < len(s.b) && (s.b[s.i] == 'e' || s.b[s.i] == 'E') {
		s.i++
		if s.i < len(s.b) && (s.b[s.i] == '+' || s.b[s.i] == '-') {
			s.i++
		}
		if !s.digits() {
			return nil, false
		}
	}
	return json.Number(s.b[start:s.i]), true
}

// digits reads one digit or more; false when there's none.
func (s *scanner) digits() bool {
	start := s.i
	for s.i < len(s.b) && s.b[s.i] >= '0' && s.b[s.i] <= '9' {
		s.i++
	}
	return s.i > start
}

// str reads a string, its escapes decoded. A control character unescaped
// is refused, as JSON refuses it.
func (s *scanner) str() (any, bool) {
	s.i++ // "
	start := s.i
	// Most strings hold no escape: taken as they are.
	for s.i < len(s.b) {
		c := s.b[s.i]
		switch {
		case c == '"':
			v := string(s.b[start:s.i])
			s.i++
			return v, true
		case c == '\\':
			return s.escaped(start)
		case c < 0x20:
			return nil, false
		}
		s.i++
	}
	return nil, false
}

// escaped goes on reading a string from its first escape, at s.i.
func (s *scanner) escaped(start int) (any, bool) {
	out := make([]byte, 0, s.i-start+16)
	out = append(out, s.b[start:s.i]...)
	for s.i < len(s.b) {
		c := s.b[s.i]
		switch {
		case c == '"':
			s.i++
			return string(out), true
		case c < 0x20:
			return nil, false
		case c != '\\':
			out = append(out, c)
			s.i++
			continue
		}
		if s.i+1 >= len(s.b) {
			return nil, false
		}
		switch e := s.b[s.i+1]; e {
		case '"', '\\', '/':
			out = append(out, e)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r, ok := hex4(s.b[s.i+2:])
			if !ok {
				return nil, false
			}
			s.i += 6
			if utf16.IsSurrogate(r) {
				// A pair (escapesOK refused a surrogate alone).
				lo, ok := rune(0), false
				if s.i+6 <= len(s.b) && s.b[s.i] == '\\' && s.b[s.i+1] == 'u' {
					lo, ok = hex4(s.b[s.i+2:])
				}
				if r = utf16.DecodeRune(r, lo); !ok || r == utf8.RuneError {
					return nil, false
				}
				s.i += 6
			}
			out = utf8.AppendRune(out, r)
			continue
		default:
			return nil, false
		}
		s.i += 2
	}
	return nil, false
}
