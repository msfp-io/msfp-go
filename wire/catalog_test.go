package wire

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The catalog's cases of vectors/v0/strict.json: titlePages,
// opaqueKeyParts and localImages.

func TestCatalogPages(t *testing.T) {
	var v struct {
		TitlePages []struct {
			Name    string  `json:"name"`
			Changes bool    `json:"changes"`
			Titles  int     `json:"titles"`
			Gone    int     `json:"gone"`
			Bytes   int     `json:"bytes"`
			Remain  bool    `json:"remain"`
			After   string  `json:"after"`
			LastKey string  `json:"lastKey"`
			Next    *string `json:"next"`
			Valid   bool    `json:"valid"`
			Reason  string  `json:"reason"`
			Caller  string  `json:"caller"`
		} `json:"titlePages"`
	}
	readVectors(t, "strict.json", &v)
	if len(v.TitlePages) == 0 {
		t.Fatal("no titlePages")
	}
	const coreAnswer = 4 << 20 // the core's bound of an answer (core section 3)
	for _, c := range v.TitlePages {
		p := Page{Changes: c.Changes, Titles: c.Titles, Gone: c.Gone, Bytes: c.Bytes, Remain: c.Remain,
			After: c.After, LastKey: c.LastKey, HasNext: c.Next != nil}
		if c.Next != nil {
			p.Next = *c.Next
		}
		err := CheckPage(p)
		if (err == nil) != c.Valid || err != nil && err.Error() != c.Reason {
			t.Errorf("%s: CheckPage = %v; want valid %v (%s)", c.Name, err, c.Valid, c.Reason)
		}
		caller := "stop"
		switch {
		case c.Bytes > coreAnswer:
			caller = "fail"
		case AskOn(c.After, p.Next):
			caller = "ask on"
		}
		if caller != c.Caller {
			t.Errorf("%s: the caller would %s; want %s", c.Name, caller, c.Caller)
		}
	}
}

func TestCatalogPageBudget(t *testing.T) {
	b := NewPageBudget(PageReserve)
	for b.Add(10) {
	}
	if b.Keys() != MaxPageTitles {
		t.Errorf("%d small titles fit; want %d", b.Keys(), MaxPageTitles)
	}

	// Ended by size: entries of 1 MiB fit twice beside the reserve.
	b = NewPageBudget(PageReserve)
	n := 0
	for b.Add(1 << 20) {
		n++
	}
	if n != 2 {
		t.Errorf("%d titles of 1 MiB fit; want 2", n)
	}
	// Exactly 3 MiB in all, commas included, fits; one more byte doesn't.
	b = NewPageBudget(MaxPageBytes - 21)
	if !b.Add(10) || !b.Add(10) || b.Add(1) {
		t.Error("the 3 MiB bound isn't inclusive")
	}
	// An entry that alone passes the bound: false on an empty page.
	b = NewPageBudget(PageReserve)
	if b.Add(MaxPageBytes) || b.Keys() != 0 {
		t.Error("a title of 3 MiB alone was counted")
	}
}

func TestCatalogOpaqueKeyParts(t *testing.T) {
	var v struct {
		OpaqueKeyParts []struct {
			Text   string `json:"text"`
			Valid  bool   `json:"valid"`
			Reason string `json:"reason"`
		} `json:"opaqueKeyParts"`
	}
	readVectors(t, "strict.json", &v)
	if len(v.OpaqueKeyParts) == 0 {
		t.Fatal("no opaqueKeyParts")
	}
	for _, c := range v.OpaqueKeyParts {
		if OpaqueKeyPart(c.Text) != c.Valid {
			t.Errorf("OpaqueKeyPart(%q) = %v (%s)", c.Text, !c.Valid, c.Reason)
		}
	}
}

func TestCatalogLocalImages(t *testing.T) {
	type offered struct {
		Key   string `json:"key"`
		Image bool   `json:"image"`
	}
	var v struct {
		LocalImages []struct {
			Name       string    `json:"name"`
			Side       string    `json:"side"`
			Parts      []string  `json:"parts"`
			Offered    []offered `json:"offered"`
			Body       string    `json:"body"`
			StreamRoot string    `json:"streamRoot"`
			Hex        string    `json:"hex"`
			Size       int       `json:"size"`
			Status     int       `json:"status"`
			Type       string    `json:"type"`
			Valid      bool      `json:"valid"`
			Ticket     string    `json:"ticket"`
			URL        string    `json:"url"`
			Format     string    `json:"format"`
			Reason     string    `json:"reason"`
		} `json:"localImages"`
	}
	readVectors(t, "strict.json", &v)
	sides := map[string]int{}
	for _, c := range v.LocalImages {
		sides[c.Side]++
		switch c.Side {
		case "request":
			on := func(part string) bool { return slices.Contains(c.Parts, part) }
			image := func(key string) bool {
				i := slices.IndexFunc(c.Offered, func(o offered) bool { return o.Key == key })
				return i >= 0 && c.Offered[i].Image
			}
			title, status, problem := CheckImageRequest(on, []byte(c.Body), image)
			if status != c.Status || problem != c.Type {
				t.Errorf("%s: %d %q; want %d %q", c.Name, status, problem, c.Status, c.Type)
			}
			if status == 200 && !strings.Contains(c.Body, `"`+title+`"`) {
				t.Errorf("%s: title %q", c.Name, title)
			}
		case "answer":
			ticket, ok := ReadImageAnswer([]byte(c.Body))
			if ok != c.Valid || ticket != c.Ticket {
				t.Errorf("%s: %q, %v; want %q, %v (%s)", c.Name, ticket, ok, c.Ticket, c.Valid, c.Reason)
			}
			if ok && ImageURL(c.StreamRoot, ticket) != c.URL {
				t.Errorf("%s: ImageURL", c.Name)
			}
		case "image":
			format, err := checkImage(unhex(t, c.Hex), c.Size)
			if (err == nil) != c.Valid || format != c.Format || err != nil && err.Error() != c.Reason {
				t.Errorf("%s: %q, %v; want %q, %v (%s)", c.Name, format, err, c.Format, c.Valid, c.Reason)
			}
		default:
			t.Errorf("%s: unknown side %q", c.Name, c.Side)
		}
	}
	if sides["request"] == 0 || sides["answer"] == 0 || sides["image"] == 0 {
		t.Errorf("sides %v", sides)
	}
	// CheckImage on a whole image: the head is all of it.
	if f, err := CheckImage(unhex(t, "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489")); f != ImagePNG || err != nil {
		t.Errorf("CheckImage = %q, %v", f, err)
	}
}

func TestCatalogDescription(t *testing.T) {
	cases := []struct {
		json string
		b    DescriptionBounds
		want Description
		ok   bool
	}{
		{`{"name":" Home\u0000  movie ","year":1999,"image":true}`, LocalDescription, Description{Name: "Home movie", Year: 1999, Image: true}, true},
		{`{"name":"x","year":1799,"image":"yes"}`, LocalDescription, Description{Name: "x"}, true},
		{`{"name":"x","year":2000.5}`, LocalDescription, Description{Name: "x"}, true},
		{`{"name":"x","year":10000,"image":false}`, LocalDescription, Description{Name: "x"}, true},
		{`{"name":"` + strings.Repeat("é", 130) + `"}`, LocalDescription, Description{Name: strings.Repeat("é", 128)}, true},
		{`{"name":"  "}`, LocalDescription, Description{}, false},
		{`{"name":5}`, LocalDescription, Description{}, false},
		{`{"name":null}`, LocalDescription, Description{}, false},
		{`{}`, LocalDescription, Description{}, false},
		{`[]`, LocalDescription, Description{}, false},
		{`{"year":1000}`, DescriptionBounds{MaxName: 256, MinYear: 1000, MaxYear: 9999}, Description{Year: 1000}, true},
	}
	for _, c := range cases {
		got, ok := ReadDescription(json.RawMessage(c.json), c.b)
		if ok != c.ok || got != c.want {
			t.Errorf("ReadDescription(%s) = %+v, %v; want %+v, %v", c.json, got, ok, c.want, c.ok)
		}
	}
}

func TestLanguageValidTag(t *testing.T) {
	for _, s := range []string{"fr", "fr-CA", "yue", "zh-Hant-TW", "es-419", "tlh"} {
		if !LanguageTag(s) {
			t.Errorf("LanguageTag(%q) = false", s)
		}
	}
	for _, s := range []string{"fre", "fr-ca", "FR", "e", "und", "fr_CA", "", "fr--CA", "zh-hant-tw", "toolongtagvalue"} {
		if LanguageTag(s) {
			t.Errorf("LanguageTag(%q) = true", s)
		}
	}
	// The rule is SentTag's: every tag it sends is valid, and no other.
	var v struct {
		LanguageCodes []struct {
			Text   string `json:"text"`
			Output string `json:"output"`
			Valid  bool   `json:"valid"`
		} `json:"languageCodes"`
	}
	readVectors(t, "strict.json", &v)
	for _, c := range v.LanguageCodes {
		if c.Valid && !LanguageTag(c.Output) {
			t.Errorf("LanguageTag(%q) = false, the tag sent for %q", c.Output, c.Text)
		}
		if LanguageTag(c.Text) != (c.Valid && c.Text == c.Output) {
			t.Errorf("LanguageTag(%q) = %v", c.Text, LanguageTag(c.Text))
		}
	}
}

// openTokens and openMediaTypes of strict.json (core section 8.5).
func TestOpenTokens(t *testing.T) {
	type valued struct {
		Text  string `json:"text"`
		Valid bool   `json:"valid"`
	}
	var v struct {
		OpenTokens     []valued `json:"openTokens"`
		OpenMediaTypes []valued `json:"openMediaTypes"`
	}
	readVectors(t, "strict.json", &v)
	if len(v.OpenTokens) == 0 || len(v.OpenMediaTypes) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.OpenTokens {
		if Token(c.Text) != c.Valid {
			t.Errorf("%q: %v", c.Text, !c.Valid)
		}
	}
	for _, c := range v.OpenMediaTypes {
		if IsMediaType(c.Text) != c.Valid {
			t.Errorf("media type %q: %v", c.Text, !c.Valid)
		}
	}
	for _, c := range []struct {
		a, b string
		same bool
	}{{"video/mp4", "audio/mp4", true}, {"audio/matroska", "video/matroska", true}, {"audio/mpeg", "video/mpeg", false},
		{"video/mp4", "video/mp4", true}, {"video/mp4", "video/quicktime", false}} {
		if SameContainer(c.a, c.b) != c.same {
			t.Errorf("SameContainer(%s, %s)", c.a, c.b)
		}
	}
}
