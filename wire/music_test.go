package wire

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// musicCase is a case of vectors/v0/music.json.
type musicCase struct {
	Name, Kind, Text, Request, Output string
	Valid                             bool
}

// The cases of vectors/v0/music.json: keys, what a caller keeps of each
// title (local ones included), the queue of a play, and the lyrics call.
func TestMusicVectors(t *testing.T) {
	var v struct {
		Libraries           []string
		StreamRoot          string
		Keys                []musicCase
		Titles, LocalTitles []musicCase
		Queue, Lyrics       []musicCase
	}
	readVectors(t, "music.json", &v)
	if len(v.Keys) == 0 || len(v.Titles) == 0 || len(v.LocalTitles) == 0 || len(v.Queue) == 0 || len(v.Lyrics) == 0 {
		t.Fatal("no music cases")
	}
	for _, c := range v.Keys {
		k, ok := ParseMusicKey(c.Text)
		if ok != c.Valid || ok && (k.Kind != c.Kind || k.String() != c.Text) {
			t.Errorf("key %s: %+v %v, want %v %s", c.Name, k, ok, c.Valid, c.Kind)
		}
		if tk, ok := ParseKey(c.Text); ok && c.Valid && (tk.Part != PartMusic || tk.Playable != (c.Kind == "track")) {
			t.Errorf("key %s: ParseKey %+v", c.Name, tk)
		}
	}
	for _, c := range append(v.Titles, v.LocalTitles...) {
		got, ok := ReadMusicTitle(json.RawMessage(c.Text))
		if ok && !slices.Contains(v.Libraries, got.Library) {
			ok = false // the caller's check: a library it asked for
		}
		if ok != c.Valid {
			t.Errorf("title %s: kept %v, want %v", c.Name, ok, c.Valid)
		} else if ok {
			sameJSON(t, "title "+c.Name, got, c.Output)
		}
	}
	for _, c := range v.Queue {
		var req struct {
			Title    string          `json:"title"`
			Delivery string          `json:"delivery"`
			Queue    json.RawMessage `json:"queue"`
		}
		request := c.Text
		if c.Kind == "answer" {
			request = c.Request
		}
		var asked []string
		err := DecodeJSON([]byte(request), &req)
		if err == nil {
			asked, err = CheckQueue(req.Title, req.Queue)
		}
		if c.Kind == "request" {
			if (err == nil) != c.Valid {
				t.Errorf("queue %s: accepted %v, want %v", c.Name, err == nil, c.Valid)
			}
			continue
		}
		if err != nil {
			t.Fatalf("queue %s: the request refused: %v", c.Name, err)
		}
		var answer struct {
			Stream struct {
				URL string `json:"url"`
			} `json:"stream"`
			Queue json.RawMessage `json:"queue"`
		}
		if err := DecodeAnswer([]byte(c.Text), &answer); err != nil {
			t.Fatalf("queue %s: %v", c.Name, err)
		}
		sameJSON(t, "queue "+c.Name, ReadQueue(answer.Queue, asked, req.Delivery, v.StreamRoot, answer.Stream.URL), c.Output)
	}
	for _, c := range v.Lyrics {
		if c.Kind == "request" {
			if r, ok := ReadLyricsRequest([]byte(c.Text)); ok != c.Valid || ok && (r.Title == "" || r.VersionID == "") {
				t.Errorf("lyrics %s: read %v %+v, want %v", c.Name, ok, r, c.Valid)
			}
			continue
		}
		sets, err := ReadLyrics([]byte(c.Text))
		if (err == nil) != c.Valid {
			t.Errorf("lyrics %s: kept %v, want %v", c.Name, err == nil, c.Valid)
		} else if err == nil {
			sameJSON(t, "lyrics "+c.Name, LyricsAnswer{StructuredLyrics: sets}, c.Output)
		}
	}
}

// sameJSON checks that got, written as JSON, is the same value as want.
func sameJSON(t *testing.T, name string, got any, want string) {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var have, expected any
	if err := json.Unmarshal(raw, &have); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !reflect.DeepEqual(have, expected) {
		t.Errorf("%s:\n got %s\nwant %s", name, raw, want)
	}
}

func TestWebVTTPart(t *testing.T) {
	track := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nfirst\n\n00:01:55.000 --> 00:02:05.000\nacross\n\n" +
		"00:02:30.000 --> 00:02:31.000\n<b>second</b>\n\n00:05:00.000 --> 00:05:01.000\nthird\n"
	for _, c := range []struct {
		n    int
		want []string
	}{{0, []string{"first", "across"}}, {1, []string{"across", "second"}}, {2, []string{"third"}}, {3, nil}} {
		out, ok := WebVTTPart([]byte(track), c.n)
		if !ok {
			t.Fatalf("part %d refused", c.n)
		}
		again, ok := RebuildWebVTT(out)
		if !ok || string(again) != string(out) {
			t.Errorf("part %d isn't in its rebuilt form: %q", c.n, out)
		}
		for _, w := range c.want {
			if !strings.Contains(string(out), "\n"+w+"\n") {
				t.Errorf("part %d: no %q in %q", c.n, w, out)
			}
		}
		if got := strings.Count(string(out), "-->"); got != len(c.want) {
			t.Errorf("part %d: %d cues in %q", c.n, got, out)
		}
	}
	if _, ok := WebVTTPart([]byte("not vtt"), 0); ok {
		t.Error("a track that isn't WebVTT gave a part")
	}
}

func TestMusicDates(t *testing.T) {
	for s, want := range map[string]int{"1970": 1970, "1970-01": 1970, "2024-02-29": 2024, "0999": 0,
		"2023-02-29": 0, "1970-13": 0, "1970-1-1": 0, "1970-00": 0, "19700": 0, "": 0} {
		if y, ok := MusicDate(s); y != want || ok != (want > 0) {
			t.Errorf("MusicDate(%q) = %d %v, want %d", s, y, ok, want)
		}
	}
	if g := R128Gain(-1280); g != 0 {
		t.Errorf("R128 gain of -5 dB: %v, want 0", g)
	}
}

// An owning server's own title, written with MusicTitle's JSON, reads
// back as it was.
func TestMusicTitleRoundTrip(t *testing.T) {
	yes, gain := true, -4.12
	sent := MusicTitle{Key: "track/local/T1", Kind: "track", Library: "m1", Name: "x",
		Artists: []string{"artist/local/A1"}, Album: "album/mb/0b000000-0000-4000-8000-000000000002", Disc: 1, Number: 2,
		Copies: []MusicCopy{{ID: "c1", DurationMs: 1000, Container: "flac", Codec: "flac", Size: 10, Etag: "e1",
			BitrateKbps: 936, ReplayGain: &ReplayGain{TrackGain: &gain}, Lyrics: "plain"}},
		IDs: map[string]string{"isrc": "USSM17000123"}}
	raw, err := Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ReadMusicTitle(raw)
	if !ok || !reflect.DeepEqual(got, sent) {
		t.Errorf("read back %v %+v", ok, got)
	}
	album := MusicTitle{Key: "album/local/A2", Kind: "album", Library: "m1", Name: "y", Compilation: &yes, Image: &yes}
	raw, _ = Marshal(album)
	if got, ok := ReadMusicTitle(raw); !ok || !reflect.DeepEqual(got, album) {
		t.Errorf("album read back %v %+v", ok, got)
	}
}

func TestQueueURLs(t *testing.T) {
	root := "https://a.example.org/api/v1/stream/"
	if u := QueueURL(root, "Tk1", 3, "hls"); u != root+"Tk1/queue/3/master.m3u8" {
		t.Error(u)
	}
	if u := QueueURL(root, "Tk1", 0, "file"); u != root+"Tk1/queue/0/file" {
		t.Error(u)
	}
	if q, err := CheckQueue("track/local/T1", nil); q != nil || err != nil {
		t.Errorf("no queue: %v %v", q, err)
	}
	if _, err := CheckQueue("album/local/A1", nil); err == nil {
		t.Error("an album's play took a queue check")
	}
}
