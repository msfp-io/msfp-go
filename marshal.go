package msfp

import (
	"encoding/json"
	"errors"
)

// marshal writes a body the engine sends: JSON, its members in the
// order of v's fields (the order of the specification's tables, as the
// vectors write them), and never a null (MSFP Media base 2.2): a field
// that could be null is omitempty, or always set, and a null that slips
// through is an error, not a body.
func marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if !noNull(b) {
		return nil, errors.New("msfp: a null in a body")
	}
	return b, nil
}

// noNull says whether b, valid JSON (json.Marshal's), holds no null.
// Outside strings, the only token that starts with n is null; inside,
// a backslash escapes the next byte. One pass over the bytes, where
// encoding/json's Decoder took several times as long, token by token.
func noNull(b []byte) bool {
	in := false
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case in && c == '\\':
			i++
		case in && c == '"':
			in = false
		case in:
		case c == '"':
			in = true
		case c == 'n':
			return false
		}
	}
	return true
}
