package wire

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// hello is the shape of a hello message (section 9.2), for decoding
// the strict vectors into a struct as well as into a map.
type hello struct {
	State        string   `json:"state"`
	Certificates []string `json:"certificates,omitempty"`
}

// The json and integers cases of the test draft's strict vectors
// (testdata/test-draft-strict.json), through DecodeJSON: valid ones decode,
// invalid ones are refused.
func TestStrictJSONVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/test-draft-strict.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		JSON []struct {
			Name, Text, Hex string
			Valid           bool
		}
		Integers []struct {
			Text  string
			Valid bool
		}
	}
	if err := json.Unmarshal(b, &v); err != nil || len(v.JSON) == 0 || len(v.Integers) == 0 {
		t.Fatal("no cases", err)
	}
	for _, c := range v.JSON {
		body := []byte(c.Text)
		if c.Hex != "" {
			body, _ = hex.DecodeString(c.Hex)
		}
		var msg hello
		if err := DecodeJSON(body, &msg); (err == nil) != c.Valid {
			t.Errorf("json %s: %v", c.Name, err)
		}
		var any map[string]any
		if err := DecodeJSON(body, &any); (err == nil) != c.Valid {
			t.Errorf("json %s, into a map: %v", c.Name, err)
		}
	}
	for _, c := range v.Integers {
		var n struct {
			N int64 `json:"n"`
		}
		if err := DecodeJSON([]byte(`{"n":`+c.Text+`}`), &n); (err == nil) != c.Valid {
			t.Errorf("integer %s: %v", c.Text, err)
		}
	}
}

// The cases of MSFP Media base 2.1 and 2.2 (msfp-media's base.json):
// json through DecodeJSON, as a request whose members are all unknown,
// and DecodeEntry, which reads every member, null as left out;
// integers in an integer field; base64url through Decode64. The engine
// reads no hex nor decimal text of MSFP Media's, so hex and decimals
// have no reader here.
func TestMediaBaseVectors(t *testing.T) {
	var v struct {
		JSON []struct {
			Name, Text, Hex string
			Valid           bool
		} `json:"json"`
		Integers []struct {
			Name, Text string
			Valid      bool
		} `json:"integers"`
		Base64url []struct {
			Name, Text string
			Bytes      int
			Valid      bool
		} `json:"base64url"`
	}
	readVectors(t, "media/v0/base.json", &v)
	if len(v.JSON) == 0 || len(v.Integers) == 0 || len(v.Base64url) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.JSON {
		body := []byte(c.Text)
		if c.Hex != "" {
			body, _ = hex.DecodeString(c.Hex)
		}
		var msg hello
		if err := DecodeJSON(body, &msg); (err == nil) != c.Valid {
			t.Errorf("json %s: %v", c.Name, err)
		}
		var all map[string]any
		if err := DecodeEntry(body, &all); (err == nil) != c.Valid {
			t.Errorf("json %s, as an entry: %v", c.Name, err)
		}
	}
	for _, c := range v.Integers {
		var n struct {
			N int64 `json:"n"`
		}
		if err := DecodeJSON([]byte(`{"n":`+c.Text+`}`), &n); (err == nil) != c.Valid {
			t.Errorf("integer %s: %v", c.Name, err)
		}
	}
	for _, c := range v.Base64url {
		if _, ok := Decode64(c.Text, c.Bytes); ok != c.Valid {
			t.Errorf("base64url %s: %v", c.Name, ok)
		}
	}
}

// A number that isn't an integer is refused only where an integer is
// read: in an integer field, a list or a map of them, or behind a
// pointer; an unknown member holding one is ignored like any other
// (MSFP Media base 2.2).
func TestNumbersOutsideIntegers(t *testing.T) {
	var into struct {
		N    int             `json:"n"`
		P    *int64          `json:"p"`
		List []uint          `json:"list"`
		Map  map[string]int  `json:"map"`
		Raw  json.RawMessage `json:"raw"`
	}
	for _, body := range []string{`{"n":1.5}`, `{"n":1e3}`, `{"n":-0}`, `{"n":9007199254740992}`, `{"p":2.0}`,
		`{"list":[1,2.5]}`, `{"map":{"a":1E1}}`} {
		if err := DecodeJSON([]byte(body), &into); err == nil {
			t.Errorf("%s accepted", body)
		}
	}
	for _, body := range []string{`{"n":1,"zz":2.5}`, `{"n":1,"zz":{"x":[1e300,-0,0.1]}}`, `{"raw":[1.5]}`, `{"N":1.5}`} {
		if err := DecodeJSON([]byte(body), &into); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
}

// null is never a valid value (MSFP Media base 2.2): refused anywhere in a
// request's known members, at the top of an answer; inside an answer,
// read as left out. In a request's unknown members, ignored like any
// other value. Marshal never writes one.
func TestNull(t *testing.T) {
	var known struct {
		Name  string            `json:"name"`
		Inner struct{ A int }   `json:"inner"`
		Map   map[string]string `json:"map"`
	}
	for _, body := range []string{`{"name":"x","later":null}`, `{"name":"x","later":{"a":[null]}}`, `{"inner":{"A":1,"B":null}}`} {
		if err := DecodeJSON([]byte(body), &known); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
	for _, body := range []string{`{"name":null}`, `{"inner":null}`, `{"inner":{"A":null}}`, `{"map":{"k":null}}`} {
		if DecodeJSON([]byte(body), &known) == nil {
			t.Errorf("%s accepted", body)
		}
	}
	for _, body := range []string{`{"state":null}`, `{"list":[1,null]}`, `{"a":{"b":null}}`} {
		var m map[string]any
		if DecodeJSON([]byte(body), &m) == nil {
			t.Errorf("%s accepted", body)
		}
		inside := body != `{"state":null}`
		if err := DecodeAnswer([]byte(body), &m); (err == nil) != inside {
			t.Errorf("%s, as an answer: %v", body, err)
		}
		if err := DecodeEntry([]byte(body), &m); err != nil {
			t.Errorf("%s, as an entry: %v", body, err)
		}
	}
	var v struct {
		List  []string          `json:"list"`
		Inner *struct{ A int }  `json:"inner"`
		Map   map[string]string `json:"map"`
		Name  string            `json:"name"`
	}
	b, err := Marshal(v)
	if err != nil || string(b) != `{"name":""}` {
		t.Errorf("Marshal: %s, %v", b, err)
	}
	if _, err := Marshal([]any{1, nil}); err == nil {
		t.Error("null in a list written")
	}
}
