package msfp

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

// noNullDecoder is the check marshal made before noNull: encoding/json's
// Decoder, token by token.
func noNullDecoder(b []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return true
		}
		if err != nil || tok == nil {
			return false
		}
	}
}

var nullSeeds = []string{
	`{}`, `null`, `{"a":null}`, `{"a":"null"}`, `{"n":1}`, `["\"null\"",null]`, `{"a":"\\"}`, `{"a":"\\\"n"}`,
	`[true,false,1e5,-2E-3,"xn"]`, `{"a":[{"b":{"c":null}}]}`, `"n"`, `{"nn":"n\\n"}`,
}

// noNull answers as the Decoder did, for every valid JSON body.
func FuzzNoNull(f *testing.F) {
	for _, s := range nullSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		// marshal only ever checks json.Marshal's output of Go values:
		// valid JSON, its numbers within a float64. (The Decoder refused
		// a larger number, parsing it as a float64: a refusal by the way,
		// never met.)
		var v any
		if json.Unmarshal(b, &v) != nil {
			return
		}
		if got, want := noNull(b), noNullDecoder(b); got != want {
			t.Fatalf("%q: noNull %v, the Decoder %v", b, got, want)
		}
	})
}
