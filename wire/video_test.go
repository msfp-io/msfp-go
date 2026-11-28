package wire

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Video title keys in their one canonical form (MSFP Media video 4):
// any other form of the same title is refused.
func TestParseVideoKey(t *testing.T) {
	valid := map[string]VideoKey{
		"movie/tmdb/949":              {Kind: "movie", Scheme: "tmdb", ID: 949},
		"movie/tmdb/9999999999":       {Kind: "movie", Scheme: "tmdb", ID: 9_999_999_999},
		"series/tmdb/1399":            {Kind: "series", Scheme: "tmdb", ID: 1399},
		"series/tmdb/1399/s0":         {Kind: "season", Scheme: "tmdb", ID: 1399},
		"series/tmdb/1399/s9999":      {Kind: "season", Scheme: "tmdb", ID: 1399, Season: 9999},
		"series/tmdb/1399/e63056":     {Kind: "episode", Scheme: "tmdb", ID: 1399, EpisodeID: 63056},
		"series/tmdb/1399/e1":         {Kind: "episode", Scheme: "tmdb", ID: 1399, EpisodeID: 1},
		"series/tmdb/1399/s1/e2":      {Kind: "episode", Scheme: "tmdb", ID: 1399, Season: 1, Episode: 2},
		"series/tmdb/1399/s1/e0":      {Kind: "episode", Scheme: "tmdb", ID: 1399, Season: 1},
		"series/tmdb/1399/s0/e99999":  {Kind: "episode", Scheme: "tmdb", ID: 1399, Episode: 99999},
		"series/tmdb/1/e9999999999":   {Kind: "episode", Scheme: "tmdb", ID: 1, EpisodeID: 9_999_999_999},
		"series/tvdb/81189/a27":       {Kind: "episode", Scheme: "tvdb", ID: 81189, Episode: 27, Absolute: true},
		"series/tvdb/81189/a0":        {Kind: "episode", Scheme: "tvdb", ID: 81189, Absolute: true},
		"series/tvdb/81189/s1/e2":     {Kind: "episode", Scheme: "tvdb", ID: 81189, Season: 1, Episode: 2},
		"movie/local/q3Zp0Lw8":        {Kind: "movie", Scheme: "local", Local: "q3Zp0Lw8"},
		"series/local/Hk2v9TnA/s1/e3": {Kind: "episode", Scheme: "local", Local: "Hk2v9TnA", Season: 1, Episode: 3},
		"movie/tmdb/348/x/Tr7aQ1":     {Kind: "extra", Scheme: "tmdb", ID: 348, Extra: "Tr7aQ1", ParentKind: "movie"},
		"series/tmdb/1399/s0/x/a":     {Kind: "extra", Scheme: "tmdb", ID: 1399, Extra: "a", ParentKind: "season"},
		"series/local/x/x/x":          {Kind: "extra", Scheme: "local", Local: "x", Extra: "x", ParentKind: "series"},
	}
	for key, want := range valid {
		got, ok := ParseVideoKey(key)
		if !ok || got != want || got.String() != key {
			t.Errorf("%s: %+v %v, want %+v", key, got, ok, want)
		}
	}
	for _, key := range []string{
		"", "movie/tmdb/", "movie/tmdb/0", "movie/tmdb/0949", "movie/tmdb/10000000000", "movie/tmdb/-1",
		"movie/tmdb/949/s1", "movie/imdb/tt0133093", "movie/tvdb/81189", "Movie/tmdb/949", "movie/TMDB/949",
		"season/tmdb/1399", "episode/tmdb/1399", "series/tmdb/1399/", "series/tmdb/1399/s01", "series/tmdb/1399/s10000",
		"series/tmdb/1399/e0", "series/tmdb/1399/e063056", "series/tmdb/1399/e12345678901", "series/tmdb/1399/a1",
		"series/tmdb/1399/s1/e01", "series/tmdb/1399/s1/e100000", "series/tmdb/1399/s1/e", "series/tmdb/1399/s/e1",
		"series/tmdb/1399/e63056/e1", "series/tmdb/1399/s1/e2/x", "series/tmdb/+1399", "series/tmdb/1399/s+1",
		"series/tmdb/1399/s1/s2", "movie/tmdb/949 ", "item/0123456789abcdef0123456789abcdef",
		"series/tmdb/1399/e63056/x/a", "series/tmdb/1399/s1/e1/x/a", "movie/tmdb/1/x/a/x/b", "movie/tmdb/1/x/",
		"movie/tmdb/1/x/a.b", "movie/tmdb/1/y/a", "series/local/a/e1", "series/tvdb/1/e1",
	} {
		if k, ok := ParseVideoKey(key); ok {
			t.Errorf("%q accepted as %+v", key, k)
		}
	}
	k, _ := ParseVideoKey("series/tvdb/81189/s1/x/Tr1")
	if p, ok := k.Parent(); !ok || p.String() != "series/tvdb/81189/s1" || k.Series() != "series/tvdb/81189" || !k.Playable() {
		t.Errorf("extra's parent %+v %v, series %s", p, ok, k.Series())
	}
}

func FuzzParseVideoKey(f *testing.F) {
	for _, k := range []string{"movie/tmdb/949", "series/tmdb/1399/s1/e0", "series/tmdb/1399/e63056", "series/tmdb/1/s01",
		"series/tvdb/1/a0", "movie/local/x/x/y", "series/tmdb/1/s1/x/Tr"} {
		f.Add(k)
	}
	f.Fuzz(func(t *testing.T, key string) {
		k, ok := ParseVideoKey(key)
		if ok && k.String() != key {
			t.Fatalf("%q read as %q", key, k.String())
		}
	})
}

// What a receiver keeps of a title (MSFP Media listing 3.3, MSFP Media video
// 3.1): a bad member skipped, or counted as absent, not the whole title.
func TestReadVideoTitle(t *testing.T) {
	const copies = `"copies":[{"id":"v1","durationMs":1000,"mediaType":"video/matroska","size":1,"etag":"t","audioTracks":[]}]`
	read := func(s string) (VideoTitle, bool) { return ReadVideoTitle(json.RawMessage(s)) }
	got, ok := read(`{"key":"series/tmdb/1399/e63056","kind":"episode","library":"L","place":{"season":1,"episode":1,"x":2},` + copies + `,
		"with":["series/tmdb/1399/e63057","series/tmdb/1399/e63056","series/tmdb/2/e1","series/tmdb/1399/s1/e3",7,"series/tmdb/1399/s1","series/tvdb/1399/s1/e3"],
		"ids":{"imdb":"tt1480055","tvdb":"0123","wikidata":"Q1"},"audio":["en"]}`)
	if !ok {
		t.Fatal("refused")
	}
	if want := []string{"series/tmdb/1399/e63057", "series/tmdb/1399/s1/e3"}; !slices.Equal(got.With, want) {
		t.Errorf("with %v", got.With)
	}
	if len(got.IDs) != 1 || got.IDs["imdb"] != "tt1480055" {
		t.Errorf("ids %v", got.IDs)
	}
	if got.Place == nil || *got.Place != (Place{Season: 1, Episode: 1}) || len(got.Copies) != 1 {
		t.Errorf("%+v", got)
	}
	for _, place := range []string{``, `,"place":null`, `,"place":[1,1]`, `,"place":{"season":1}`,
		`,"place":{"season":1.0,"episode":1}`, `,"place":{"season":"1","episode":1}`, `,"place":{"season":10000,"episode":1}`,
		`,"place":{"season":1,"episode":-1}`} {
		if _, ok := read(`{"key":"series/tmdb/1399/e63056","kind":"episode","library":"L",` + copies + place + `}`); ok {
			t.Errorf("place %q kept", place)
		}
	}
	// No copies, copies of the wrong type, none left: skipped; series and
	// seasons carry none.
	for _, s := range []string{`{"key":"movie/tmdb/949","kind":"movie","library":"L"}`,
		`{"key":"movie/tmdb/949","kind":"movie","library":"L","copies":{}}`,
		`{"key":"movie/tmdb/949","kind":"movie","library":"L","copies":[1,{"id":"v1"}]}`} {
		if _, ok := read(s); ok {
			t.Errorf("%s kept", s)
		}
	}
	if got, ok := read(`{"key":"series/tmdb/1399/s1","kind":"season","library":"L",` + copies + `,"ids":{"imdb":"tt0944947"}}`); !ok || got.Copies != nil || got.IDs != nil {
		t.Errorf("season %+v %v", got, ok)
	}
	// Repeated copy IDs: the first kept; at most 16; an unknown media type
	// kept as it is.
	var list []string
	for i := range 20 {
		list = append(list, `{"id":"v`+strconv.Itoa(i%17)+`","durationMs":1,"mediaType":"x/y","size":1,"etag":"t","audioTracks":[]}`)
	}
	got, _ = read(`{"key":"movie/tmdb/1","kind":"movie","library":"L","copies":[` + strings.Join(list, ",") + `]}`)
	if len(got.Copies) != MaxCopies || got.Copies[0].MediaType != "x/y" {
		t.Errorf("copies %+v", got.Copies)
	}
}

func TestCleanWithCap(t *testing.T) {
	var with []string
	for i := range 20 {
		with = append(with, VideoKey{Kind: "episode", Scheme: "tmdb", ID: 1, EpisodeID: int64(i + 2)}.String())
	}
	if n := len(CleanWith(VideoKey{Kind: "episode", Scheme: "tmdb", ID: 1, EpisodeID: 1}, with)); n != MaxWith {
		t.Errorf("%d kept in with", n)
	}
}

func TestOtherID(t *testing.T) {
	for _, c := range []struct {
		scheme, value string
		ok            bool
	}{
		{"imdb", "tt0133093", true}, {"imdb", "tt1234567890", true}, {"imdb", "tt012345", false},
		{"imdb", "tt12345678901", false}, {"imdb", "nm0000206", false}, {"imdb", "TT0133093", false},
		{"tvdb", "81189", true}, {"tvdb", "9999999999", true}, {"tvdb", "081189", false}, {"tvdb", "0", false},
		{"tvdb", "12345678901", false}, {"tmdb", "949", true}, {"tmdb", "0949", false}, {"mb", "949", false},
	} {
		if OtherID(c.scheme, c.value) != c.ok {
			t.Errorf("%s %s: %v", c.scheme, c.value, !c.ok)
		}
	}
	if !VideoOtherID("series", "tvdb", "tmdb", "1399") || VideoOtherID("series", "tmdb", "tmdb", "1399") ||
		VideoOtherID("movie", "local", "tmdb", "949") || VideoOtherID("movie", "tmdb", "imdb", "tt0133093") ||
		!VideoOtherID("episode", "tmdb", "tvdb", "81189") || VideoOtherID("series", "tvdb", "tvdb", "81189") {
		t.Error("VideoOtherID")
	}
}

// A copy written with Marshal is read back unchanged, and audioTracks
// stays even when empty.
func TestCopiesRoundTrip(t *testing.T) {
	start, end, zero, pq, yes := int64(0), int64(900), 0, 16, true
	c := Copy{ID: "v1", DurationMs: 1000, StartMs: &start, EndMs: &end, MediaType: "video/matroska", Size: 3, ETag: "t", Parts: 2,
		Video: &VideoStream{Codec: "h264", Width: 1920, Height: 1080, FPS: "24000/1001", HDR: []string{"hdr10plus"}, RefFrames: 4,
			Codecs: "avc1.640028", SampleAspect: "32:27", Rotation: &zero, Colour: &VideoColour{Transfer: &pq, FullRange: &yes},
			DV: &VideoDV{Profile: 8, Level: 6, Compatibility: 1, EL: &yes}},
		AudioTracks:    []CopyAudio{{ID: "a0", Codec: "eac3", Codecs: "ec-3", Objects: "dolby-atmos", Default: true}},
		SubtitleTracks: []CopySubtitle{{ID: "s3", Role: "commentary", Format: "subrip"}},
		Chapters:       []CopyChapter{{StartMs: 0, Name: "One"}}, Segments: []CopySegment{{Kind: "intro", StartMs: 0, EndMs: 10}},
		PreviewLayout: &PreviewLayout{TileWidth: 320, TileHeight: 180, Columns: 10, Rows: 10, IntervalMs: 10000, TileCount: 99},
	}
	b, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadCopy(b)
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Errorf("%+v %v\n%s", got, err, b)
	}
	b, _ = Marshal(Copy{ID: "v1", MediaType: "video/mp4", Size: 1, ETag: "t", AudioTracks: []CopyAudio{}})
	if _, err := ReadCopy(b); err != nil {
		t.Errorf("%s: %v", b, err)
	}
}

// The cases of MSFP Media's vectors/v0/video.json.
func TestVideoVectors(t *testing.T) {
	type valued struct {
		Name, Text, Output, Reason, Kind, Side string
		Valid                                  bool
	}
	var v struct {
		Libraries                   []string
		StreamRoot                  string
		Titles, Extras, LocalTitles []valued
		Copies                      []valued
		LocalKeys, TvdbKeys         []valued
		Previews                    []valued
		AnswerCopy                  string
		Answers, Codecs, FontSets   []valued
	}
	readVectors(t, "media/v0/video.json", &v)
	if len(v.Titles) == 0 || len(v.Copies) == 0 || len(v.Extras) == 0 || len(v.LocalTitles) == 0 ||
		len(v.LocalKeys) == 0 || len(v.TvdbKeys) == 0 || len(v.Previews) == 0 || len(v.Answers) == 0 ||
		len(v.Codecs) == 0 || len(v.FontSets) == 0 {
		t.Fatal("a list of cases is missing")
	}
	check := func(list string, c valued, got any, ok bool) {
		t.Helper()
		if ok != c.Valid {
			t.Errorf("%s %s: kept %v, want %v (%s)", list, c.Name, ok, c.Valid, c.Reason)
			return
		}
		if !ok || c.Output == "" {
			return
		}
		b, _ := json.Marshal(got)
		var have, want any
		json.Unmarshal(b, &have)
		have = videoNoFalse(have)
		if err := json.Unmarshal([]byte(c.Output), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(have, videoNoFalse(want)) {
			t.Errorf("%s %s:\n got %s\nwant %s", list, c.Name, b, c.Output)
		}
	}
	for list, cases := range map[string][]valued{"titles": v.Titles, "extras": v.Extras, "localTitles": v.LocalTitles} {
		for _, c := range cases {
			got, ok := ReadVideoTitle(json.RawMessage(c.Text))
			if ok && !slices.Contains(v.Libraries, got.Library) {
				ok = false // the caller's check: a library it asked for
			}
			check(list, c, videoTitleOut(got), ok)
		}
	}
	for _, c := range v.Copies {
		got, err := ReadCopy(json.RawMessage(c.Text))
		check("copies", c, got, err == nil)
	}
	for list, cases := range map[string][]valued{"localKeys": v.LocalKeys, "tvdbKeys": v.TvdbKeys} {
		for _, c := range cases {
			k, ok := ParseVideoKey(c.Text)
			check(list, c, nil, ok)
			if ok && (k.Kind != c.Kind || k.String() != c.Text) {
				t.Errorf("%s %s: kind %s, %s", list, c.Name, k.Kind, k)
			}
		}
	}
	for _, c := range v.Codecs {
		var s struct {
			Codec    string   `json:"codec"`
			Codecs   string   `json:"codecs"`
			BitDepth int      `json:"bitDepth"`
			DV       *VideoDV `json:"dv"`
		}
		if err := json.Unmarshal([]byte(c.Text), &s); err != nil {
			t.Fatal(err)
		}
		f, err := ReadCodecs(s.Codec, s.Codecs, s.BitDepth, s.DV)
		if !Token(s.Codec) {
			err = errors.New("codec missing or not a token")
		}
		check("codecs", c, f, err == nil)
	}
	answerCopy, err := ReadCopy(json.RawMessage(v.AnswerCopy))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Answers {
		tree, err := strictTree([]byte(c.Text), true)
		o, _ := tree.(map[string]any)
		ticket, _ := o["ticket"].(string)
		ok := err == nil && o != nil && ID(ticket)
		a := PlayAnswer{Ticket: ticket, Protocol: ProtocolHLS}
		if ok {
			a.Subtitles = readServedSubtitles(o["subtitles"], answerCopy.SubtitleTracks)
			if n, ok := integerOf(o["fontCount"]); ok && n >= 0 && n <= MaxFonts {
				a.FontCount = int(n)
			}
		}
		urls := a.URLs(v.StreamRoot, answerCopy.PreviewLayout.SheetCount())[1:] // the stream's isn't among them
		out := map[string]any{"fontCount": a.FontCount, "urls": urls}
		if a.Subtitles != nil {
			out["subtitles"] = a.Subtitles
		}
		check("answers", c, out, ok)
	}
	for _, c := range v.FontSets {
		var sizes []int64
		err := json.Unmarshal([]byte(c.Text), &sizes)
		if err == nil {
			err = CheckFontSet(sizes)
		}
		var total int64
		for _, n := range sizes {
			total += n
		}
		check("fontSets", c, map[string]any{"fonts": len(sizes), "bytes": total}, err == nil)
	}
	for _, c := range v.Previews {
		if c.Side == "request" {
			_, err := ReadPreviewsRequest([]byte(c.Text))
			check("previews", c, nil, err == nil)
			continue
		}
		p, err := ReadPreviewsAnswer([]byte(c.Text))
		check("previews", c, p, err == nil)
	}
}

// videoTitleOut writes a kept title as the vectors do.
func videoTitleOut(t VideoTitle) map[string]any {
	out := map[string]any{"key": t.Key, "library": t.Library}
	set := func(name string, v any, present bool) {
		if present {
			out[name] = v
		}
	}
	set("extraType", t.ExtraType, t.ExtraType != "")
	if d := t.Description; d != nil {
		set("name", d.Name, true)
		set("year", d.Year, d.Year != 0)
		set("image", true, d.Image)
	}
	set("place", t.Place, t.Place != nil)
	set("with", t.With, t.With != nil)
	set("ids", t.IDs, t.IDs != nil)
	set("copies", t.Copies, t.Copies != nil)
	return out
}

// videoNoFalse drops the members that are false: a flag false is kept as
// absent.
func videoNoFalse(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			if e == false {
				delete(t, k)
				continue
			}
			t[k] = videoNoFalse(e)
		}
	case []any:
		for i, e := range t {
			t[i] = videoNoFalse(e)
		}
	}
	return v
}
