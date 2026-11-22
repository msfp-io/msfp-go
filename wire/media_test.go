package wire

import (
	"strconv"
	"strings"
	"testing"
)

// The playback of the hls vectors: audio tracks a1 to a40.
func vectorAudio(id string) bool {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "a"))
	return strings.HasPrefix(id, "a") && err == nil && n >= 1 && n <= 40 && "a"+strconv.Itoa(n) == id
}

// A paired server's playlist passes only when it names files of the same
// stream, by name: no path, no other server.
func TestMediaPlaylist(t *testing.T) {
	const media = "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-TARGETDURATION:6\n"
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n", true},
		{media + "#EXT-X-MAP:URI=\"init0.mp4\"\n#EXTINF:6,\ns0.mp4\r\n#EXT-X-ENDLIST\n", true},
		{"#EXTM3U\nhttps://elsewhere.example.org/s0.ts\n", false},
		{"#EXTM3U\n../../api/v1/me\n", false},
		{"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://k.example.org/key\"\n", false},
		// Unquoted, which players may read too.
		{"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=https://k.example.org/key\n", false},
		{"#EXTM3U\n#EXT-X-MAP:uri=https://k.example.org/i.mp4\n", false},
		{"#EXTM3U\n#EXT-X-MAP:URI=init0.mp4\n", false},
		{"<html>\n", false},
		// "." and "..", which a URL resolves as folders (MSFP media).
		{"#EXTM3U\n..\n", false},
		{"#EXTM3U\n.\n", false},
		{"#EXTM3U\n#EXT-X-MAP:URI=\"..\"\n", false},
		{"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n...\n", true},
		// Complete from the first fetch: a playlist that grows is refused.
		{media + "#EXTINF:6,\ns0.mp4\n", false},
		// Neither a master nor a media playlist.
		{"#EXTM3U\n#EXT-X-VERSION:7\n", false},
	} {
		if err := CheckPlaylist([]byte(tc.body), vectorAudio); (err == nil) != tc.ok {
			t.Errorf("%q: %v", tc.body, err)
		}
	}
}

// Audio renditions name tracks the playback has: with no tracks (a nil
// audio), none passes.
func TestMediaPlaylistRenditions(t *testing.T) {
	master := "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"a2\",DEFAULT=YES,AUTOSELECT=YES,URI=\"a2.m3u8\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"audio\"\nv0.m3u8\n"
	if got, err := FilterPlaylist([]byte(master), MasterSubset, vectorAudio); err != nil || string(got) != master {
		t.Errorf("%v\n%s", err, got)
	}
	if err := CheckPlaylist([]byte(master), nil); err == nil {
		t.Error("a rendition of a playback without audio tracks")
	}
}

func TestMediaQueueStreamBase(t *testing.T) {
	if got := QueueStreamBase("https://a.example.org/api/v1/stream/t/", 4); got != "https://a.example.org/api/v1/stream/t/queue/4/" {
		t.Error(got)
	}
}

// A relayed playlist keeps the subset's tags only: comments and other
// tags are dropped, URIs checked in them all the same.
func TestMediaFilterPlaylistKeepsTheSubset(t *testing.T) {
	got, err := FilterPlaylist([]byte("#EXTM3U\r\n# a comment\n#EXT-X-FOO:BAR=1\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-TARGETDURATION:6\n\n#EXTINF:6,\nseg0.ts\n#EXT-X-ENDLIST"), MediaSubset, nil)
	if want := "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nseg0.ts\n#EXT-X-ENDLIST\n"; err != nil || string(got) != want {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := FilterPlaylist([]byte("#EXTM3U\n#EXT-X-FOO:URI=\"a/b\"\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n"), MediaSubset, nil); err == nil {
		t.Fatal("a dropped tag's URI still counts")
	}
	if _, err := FilterPlaylist([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n"+strings.Repeat("s.ts\n", MaxPlaylistURIs)), nil, nil); err == nil {
		t.Fatal("too many URIs")
	}
	// Byte offsets stay right past letters whose upper case is longer.
	if _, err := FilterPlaylist([]byte("#EXTM3U\n#EXTINF:6,ßßßß uri=\"a/b\"\nseg0.ts\n"), nil, nil); err == nil {
		t.Fatal("a URI after non-ASCII text")
	}
}

// What Overfin as the owning server sent of Jellyfin 12.2's HLS, as a
// paired server's relay read it in a browser check (2026-11-16): within
// the subset, kept whole.
func TestMediaFilterPlaylistOfJellyfin(t *testing.T) {
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=5198950,AVERAGE-BANDWIDTH=5198950,VIDEO-RANGE=SDR,CODECS=\"avc1.64001F,mp4a.40.2\",RESOLUTION=1280x720,FRAME-RATE=24\nv0.m3u8\n"
	media := "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:11\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-MAP:URI=\"init.mp4\"\n" +
		"#EXTINF:10.417000,\nseg0.mp4\n#EXTINF:10.416000,\nseg1.mp4\n#EXT-X-ENDLIST\n"
	for body, subset := range map[string][]string{master: MasterSubset, media: MediaSubset} {
		if got, err := FilterPlaylist([]byte(body), subset, nil); err != nil || string(got) != body {
			t.Errorf("%v\n%s", err, got)
		}
	}
}

// A character players read as a line's end, inside a line, would hide
// an address from the check.
func TestMediaFilterPlaylistHiddenLines(t *testing.T) {
	for _, tc := range []struct {
		body   string
		subset []string
	}{
		{"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\rhttps://elsewhere.example/v.m3u8\n", MasterSubset},
		{"#EXTM3U\n#EXTINF:6, https://elsewhere.example/s.ts\n", MediaSubset},
		{"#EXTM3U\n#EXTINF:6,\rhttps://elsewhere.example/s.ts\n", MediaSubset},
		{"#EXTM3U\n#EXTINF:6,\x0bs.ts\n", MediaSubset},
		{"#EXTM3U\n#EXTINF:6,\u0085s.ts\n", nil},
	} {
		if _, err := FilterPlaylist([]byte(tc.body), tc.subset, nil); err == nil {
			t.Errorf("%q passed", tc.body)
		}
	}
	for _, ok := range []string{"#EXTM3U\r\n#EXT-X-STREAM-INF:BANDWIDTH=1,\tRESOLUTION=1x1\r\nv0.m3u8\r\n"} {
		if _, err := FilterPlaylist([]byte(ok), MasterSubset, nil); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

// MSFP's media test vectors (https://github.com/msfp-io/msfp-spec,
// vectors/v0/media.json), copied in testdata/v0/media.json.
type mediaCase struct {
	Name   string `json:"name"`
	Text   string `json:"text"`
	Hex    string `json:"hex"`
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
	Output string `json:"output"`
	Type   string `json:"type"`
	// ranges, fileAnswers and scans.
	Size          int64  `json:"size"`
	Start         int64  `json:"start"`
	End           int64  `json:"end"`
	Range         string `json:"range"`
	ETag          string `json:"etag"`
	Status        int    `json:"status"`
	ETagHeader    string `json:"etagHeader"`
	ContentRange  string `json:"contentRange"`
	ContentLength string `json:"contentLength"`
	Sent          int64  `json:"sent"`
}

type mediaVectors struct {
	WebVTT      []mediaCase `json:"webvtt"`
	ASS         []mediaCase `json:"ass"`
	PGS         []mediaCase `json:"pgs"`
	Fonts       []mediaCase `json:"fonts"`
	HLS         []mediaCase `json:"hls"`
	Ranges      []mediaCase `json:"ranges"`
	FileAnswers []mediaCase `json:"fileAnswers"`
	Scans       []mediaCase `json:"scans"`
}

func readMediaVectors(t *testing.T) mediaVectors {
	t.Helper()
	var v mediaVectors
	readVectors(t, "media.json", &v)
	return v
}

// input is a case's bytes: its text, or its hex when it isn't UTF-8.
func (c mediaCase) input(t *testing.T) []byte {
	if c.Hex != "" {
		return unhex(t, c.Hex)
	}
	return []byte(c.Text)
}

// sameStep says whether err is refused at the step the case's reason
// names ("step 4: …"), when it names one.
func sameStep(err error, reason string) bool {
	want, _, _ := strings.Cut(reason, ":")
	if !strings.HasPrefix(want, "step ") {
		return true
	}
	return err != nil && strings.Contains(err.Error(), want+":")
}

func rebuiltVectors(t *testing.T, cases []mediaCase, rebuild func([]byte) ([]byte, bool)) {
	t.Helper()
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		out, ok := rebuild(c.input(t))
		if ok != c.Valid || string(out) != c.Output {
			t.Errorf("%s: %v\n%q\nwant %v\n%q", c.Name, ok, out, c.Valid, c.Output)
			continue
		}
		// Rebuilding is stable: a rebuilt part rebuilds to itself (but a
		// script of 1,024 styles without Default, written with 1,025).
		if ok && strings.Count(c.Output, "\nStyle: ") <= maxASSStyles {
			if again, ok := rebuild(out); !ok || string(again) != string(out) {
				t.Errorf("%s: rebuilt again as %q", c.Name, again)
			}
		}
	}
}

func TestMediaWebVTTVectors(t *testing.T) {
	v := readMediaVectors(t)
	rebuiltVectors(t, v.WebVTT, RebuildWebVTT)
	// A whole track takes the same rules.
	rebuiltVectors(t, v.WebVTT, RebuildWholeWebVTT)
}

func TestMediaASSVectors(t *testing.T) {
	v := readMediaVectors(t)
	rebuiltVectors(t, v.ASS, RebuildASS)
	rebuiltVectors(t, v.ASS, RebuildWholeASS)
}

func TestMediaPGSVectors(t *testing.T) {
	v := readMediaVectors(t)
	if len(v.PGS) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.PGS {
		if err := CheckPGS(c.input(t)); (err == nil) != c.Valid {
			t.Errorf("%s: %v, want valid %v", c.Name, err, c.Valid)
		}
	}
}

func TestMediaFontVectors(t *testing.T) {
	v := readMediaVectors(t)
	if len(v.Fonts) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.Fonts {
		typ, err := CheckFont(c.input(t))
		if (err == nil) != c.Valid || typ != c.Type || !sameStep(err, c.Reason) {
			t.Errorf("%s: %q %v, want valid %v %q (%s)", c.Name, typ, err, c.Valid, c.Type, c.Reason)
		}
	}
}

func TestMediaHLSVectors(t *testing.T) {
	v := readMediaVectors(t)
	if len(v.HLS) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.HLS {
		if err := CheckPlaylist([]byte(c.Text), vectorAudio); (err == nil) != c.Valid || !sameStep(err, c.Reason) {
			t.Errorf("%s: %v, want valid %v (%s)", c.Name, err, c.Valid, c.Reason)
		}
	}
}

func TestMediaRangeVectors(t *testing.T) {
	v := readMediaVectors(t)
	if len(v.Ranges) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.Ranges {
		r, ok := ParseRange(c.Text)
		start, end := int64(0), int64(0)
		if ok {
			start, end, ok = r.Span(c.Size)
		}
		if ok != c.Valid || ok && (start != c.Start || end != c.End) {
			t.Errorf("%s: %v %d-%d, want %v %d-%d", c.Name, ok, start, end, c.Valid, c.Start, c.End)
		}
	}
}

func TestMediaFileAnswerVectors(t *testing.T) {
	v := readMediaVectors(t)
	if len(v.FileAnswers) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.FileAnswers {
		err := CheckFileAnswer(c.Range, c.Size, c.ETag, MediaAnswer{
			Status: c.Status, ETag: c.ETagHeader, ContentRange: c.ContentRange, ContentLength: c.ContentLength})
		if (err == nil) != c.Valid {
			t.Errorf("%s: %v, want valid %v (%s)", c.Name, err, c.Valid, c.Reason)
		}
	}
}

func TestMediaScanVectors(t *testing.T) {
	v := readMediaVectors(t)
	if len(v.Scans) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.Scans {
		status, cr := 416, UnsatisfiedRange(c.Size)
		if start, end, ok := ScanSpan(c.Sent, c.Range, c.Size); ok {
			status, cr = 206, ContentRange(start, end, c.Size)
			if c.Sent+end-start+1 > ScanCap {
				t.Errorf("%s: past the cap", c.Name)
			}
		}
		if status != c.Status || cr != c.ContentRange {
			t.Errorf("%s: %d %q, want %d %q", c.Name, status, cr, c.Status, c.ContentRange)
		}
	}
}

// A segment's 206 may give "*" as its total, and a 200 to a Range is the
// whole segment.
func TestMediaSegmentAnswer(t *testing.T) {
	for _, c := range []struct {
		rng string
		a   MediaAnswer
		ok  bool
	}{
		{"", MediaAnswer{Status: 200}, true},
		{"bytes=0-99", MediaAnswer{Status: 200}, true},
		{"bytes=0-99", MediaAnswer{Status: 206, ContentRange: "bytes 0-99/*", ContentLength: "100"}, true},
		{"bytes=0-99", MediaAnswer{Status: 206, ContentRange: "bytes 0-99/1000"}, true},
		{"bytes=-100", MediaAnswer{Status: 206, ContentRange: "bytes 900-999/*"}, false},
		{"bytes=-100", MediaAnswer{Status: 206, ContentRange: "bytes 900-999/1000"}, true},
		{"bytes=0-99", MediaAnswer{Status: 206, ContentRange: "bytes 0-100/*"}, false},
		{"", MediaAnswer{Status: 206, ContentRange: "bytes 0-99/*"}, false},
		{"bytes=0-99", MediaAnswer{Status: 416}, false},
	} {
		if err := CheckSegmentAnswer(c.rng, c.a); (err == nil) != c.ok {
			t.Errorf("%q %+v: %v", c.rng, c.a, err)
		}
	}
}

// A part's events: those that show during its stretch, from 10 seconds
// early.
func TestMediaASSPart(t *testing.T) {
	track := "[Script Info]\n[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,first\n" +
		"Dialogue: 0,0:01:55.00,0:02:05.00,Default,,0,0,0,,across\nDialogue: 0,0:05:00.00,0:05:01.00,Default,,0,0,0,,third\n"
	for n, want := range [][]string{{"first", "across"}, {"across"}, {"third"}, nil} {
		out, ok := ASSPart([]byte(track), n)
		if !ok {
			t.Fatalf("part %d refused", n)
		}
		if again, ok := RebuildASS(out); !ok || string(again) != string(out) {
			t.Errorf("part %d isn't in its rebuilt form: %q", n, out)
		}
		if got := strings.Count(string(out), "\nDialogue: "); got != len(want) {
			t.Errorf("part %d: %d events in %q", n, got, out)
		}
		for _, w := range want {
			if !strings.Contains(string(out), ","+w+"\n") {
				t.Errorf("part %d: no %q in %q", n, w, out)
			}
		}
	}
	if _, ok := ASSPart([]byte("WEBVTT"), 0); ok {
		t.Error("a track that isn't ASS gave a part")
	}
}

// A whole track has bounds of its own: over a part's, within a whole
// track's.
func TestMediaWholeTrackBounds(t *testing.T) {
	var vtt strings.Builder
	vtt.WriteString("WEBVTT\n")
	for i := range maxCues + 1 {
		vtt.WriteString("\n00:00:01.000 --> 00:00:02.000\n" + strconv.Itoa(i) + "\n")
	}
	if _, ok := RebuildWebVTT([]byte(vtt.String())); ok {
		t.Error("a part of 50,001 cues")
	}
	if _, ok := RebuildWholeWebVTT([]byte(vtt.String())); !ok {
		t.Error("a whole track of 50,001 cues refused")
	}
	big := []byte("WEBVTT\n" + strings.Repeat(" ", MaxWebVTT))
	if _, ok := RebuildWebVTT(big); ok {
		t.Error("a part over 5 MiB")
	}
	if _, ok := RebuildWholeWebVTT(big); !ok {
		t.Error("a whole track of 5 MiB refused")
	}
	if _, ok := RebuildWholeASS([]byte("[Script Info]\n" + strings.Repeat(" ", MaxWholeText))); ok {
		t.Error("a whole track over 16 MiB")
	}
}
