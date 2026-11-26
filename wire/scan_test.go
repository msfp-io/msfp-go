package wire

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"unicode/utf8"
)

// The scanner against the reader it replaced (encoding/json's Decoder,
// token by token): the same tree, or both refusing, for every body.

// strictTreeDecoder is strictTree as it was before the scanner.
func strictTreeDecoder(body []byte, nulls bool) (any, error) {
	if bytes.HasPrefix(body, []byte("\xef\xbb\xbf")) || !utf8.Valid(body) || !escapesOK(body) {
		return nil, ErrJSON
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	v, err := readValue(dec, 0, nulls)
	if err != nil {
		return nil, ErrJSON
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrJSON
	}
	return v, nil
}

func readValue(dec *json.Decoder, depth int, nulls bool) (any, error) {
	if depth > maxDepth {
		return nil, ErrJSON
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				name, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := name.(string)
				if _, dup := obj[key]; !ok || dup {
					return nil, ErrJSON // the same member twice
				}
				if obj[key], err = readValue(dec, depth+1, nulls); err != nil {
					return nil, err
				}
			}
			_, err := dec.Token() // }
			return obj, err
		case '[':
			list := []any{}
			for dec.More() {
				v, err := readValue(dec, depth+1, nulls)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token() // ]
			return list, err
		}
		return nil, ErrJSON
	case json.Number:
		// Any I-JSON number: whether it is an integer is the field's
		// question (keepExact), since a version list ignores entries
		// that aren't (section 10.2) and unknown members are ignored
		// whatever they hold.
		return t, nil
	case nil:
		if !nulls {
			return nil, ErrJSON // null is never a valid value
		}
		return nil, nil
	default: // string, bool
		return t, nil
	}
}

// marshalDecoder is Marshal as it was before the scanner.
func marshalDecoder(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	tree, err = dropNulls(tree)
	if err != nil {
		return nil, err
	}
	return json.Marshal(tree)
}

var scanSeeds = []string{
	`{}`, `[]`, `{"a":1}`, ` {"a" : [1, 2.5, -0, 1e3, -1.5E-7, true, false, null, "x"]} `,
	`{"a":{"b":{"c":[{"d":"\\u00e9\\n\\"\\/"}]}}}`, `{"a":1,"a":2}`, `{"\\u0061":1,"a":2}`,
	`{"a":01}`, `{"a":1.}`, `{"a":.5}`, `{"a":-}`, `{"a":1e}`, `{"a":+1}`, `{"a":[1,]}`, `{"a":1,}`,
	`{,}`, `{"a"}`, `{"a":}`, `{"a" 1}`, `[1 2]`, `{"a":1}x`, `{"a":1} {}`, `"x"`, `12`, `null`, ``, ` `,
	`{"a":"\x01"}`, "{\"a\":\"\t\"}", `{"a":"\\ud83d\\ude00"}`, `{"a":"\\ud83d"}`, `{"a":"\\q"}`,
	`{"a":"\\u12"}`, `{"a":tru}`, `{"a":nul}`, `{"a":[[[[[[[[[[[[[[[[[[[[1]]]]]]]]]]]]]]]]]]]}`,
	`{"a":"é","b":"\\u00E9"}`, `[1e999]`, `[-0.0e-0]`, `[0e+1]`, `{"":0}`, `{"a":[{}]}`, "\xef\xbb\xbf{}",
}

func sameStrict(t *testing.T, body []byte) {
	// Marshal writes what it wrote before, for any tree a body gives.
	if tree, err := strictTreeDecoder(body, true); err == nil {
		want, wantErr := marshalDecoder(tree)
		tree2, _ := strictTreeDecoder(body, true)
		got, gotErr := Marshal(tree2)
		if (wantErr == nil) != (gotErr == nil) || !bytes.Equal(want, got) {
			t.Fatalf("Marshal of %q: before %s %v, now %s %v", body, want, wantErr, got, gotErr)
		}
	}
	for _, nulls := range []bool{false, true} {
		want, wantErr := strictTreeDecoder(body, nulls)
		got, gotErr := strictTree(body, nulls)
		if (wantErr == nil) != (gotErr == nil) || !reflect.DeepEqual(want, got) {
			t.Fatalf("%q (nulls %v): decoder %#v %v, scanner %#v %v", body, nulls, want, wantErr, got, gotErr)
		}
	}
}

func TestScannerAsDecoder(t *testing.T) {
	for _, s := range scanSeeds {
		sameStrict(t, []byte(s))
	}
	deep := bytes.Repeat([]byte("["), maxDepth+1)
	sameStrict(t, append(deep, bytes.Repeat([]byte("]"), maxDepth+1)...))
	deep = bytes.Repeat([]byte("["), maxDepth+2)
	sameStrict(t, append(deep, bytes.Repeat([]byte("]"), maxDepth+2)...))
}

func FuzzScannerAsDecoder(f *testing.F) {
	for _, s := range scanSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) { sameStrict(t, body) })
}
