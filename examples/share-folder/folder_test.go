package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// The folder's titles: films by TMDB ID, loose or in a folder with its
// extras; a series with its seasons, an episode by its TMDB episode ID
// (placed) and one by its numbers; a local film with its image; copies
// with their exact sizes and etags, subtitles and previews.
func TestFolderTitles(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Heat (1995) [tmdbid-949].mkv", "heat")
	write("Heat (1995) [tmdbid-949].fr.vtt", "WEBVTT\n")
	write("Heat (1995) [tmdbid-949].trickplay/layout.json",
		`{"tileWidth":160,"tileHeight":90,"columns":10,"rows":10,"intervalMs":10000,"tileCount":12}`)
	write("Alien (1979) [tmdbid-348]/Alien.mp4", "alien film")
	write("Alien (1979) [tmdbid-348]/sample.mp4", "short")
	write("Alien (1979) [tmdbid-348]/Trailers/Teaser.mp4", "teaser")
	write("Severance (2022) [tmdbid-95396]/Season 1/S01E01 [tmdbid-1949537].mkv", "e1")
	write("Severance (2022) [tmdbid-95396]/Season 1/S01E02.mkv", "e2")
	write("Holidays (2019).mp4", "home")
	write("Holidays (2019).jpg", "picture")
	write("Lain [tvdbid-78914].mkv", "a film by TheTVDB: none")

	f := &folder{dir: dir, opaque: func(s string) string { return "o" + string(rune('A'+len(s)%26)) }}
	page, err := f.Titles(context.Background(), nil, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]*msfp.VideoTitle{}
	var keys []string
	for _, tt := range page.Titles {
		v := tt.(*msfp.VideoTitle)
		byKey[v.Key] = v
		keys = append(keys, v.Key)
	}
	for _, k := range []string{"movie/tmdb/949", "movie/tmdb/348", "series/tmdb/95396", "series/tmdb/95396/s1",
		"series/tmdb/95396/e1949537", "series/tmdb/95396/s1/e2"} {
		if byKey[k] == nil {
			t.Errorf("%s missing from %v", k, keys)
		}
	}
	if len(keys) != 8 || !slices.IsSorted(keys) {
		t.Errorf("titles: %v", keys)
	}
	heat := byKey["movie/tmdb/949"]
	if c := heat.Copies[0]; c.Size != 4 || !wire.ID(c.ETag) || c.MediaType != "video/matroska" || len(c.SubtitleTracks) != 1 ||
		c.SubtitleTracks[0].Language != "fr" || c.PreviewLayout == nil || c.PreviewLayout.TileCount != 12 ||
		c.ID != "Heat (1995) [tmdbid-949].mkv" {
		t.Errorf("Heat's copy: %+v", c)
	}
	if c := byKey["movie/tmdb/348"].Copies[0]; c.ID != filepath.Join("Alien (1979) [tmdbid-348]", "Alien.mp4") {
		t.Errorf("Alien's copy, its largest file: %+v", c)
	}
	if e := byKey["series/tmdb/95396/e1949537"]; e.Place == nil || *e.Place != (msfp.Place{Season: 1, Episode: 1}) {
		t.Errorf("the episode by TMDB ID: %+v", e)
	}
	var extra, local *msfp.VideoTitle
	for k, v := range byKey {
		switch {
		case strings.HasPrefix(k, "movie/tmdb/348/x/"):
			extra = v
		case strings.HasPrefix(k, "movie/local/"):
			local = v
		}
	}
	if extra == nil || extra.Kind != "extra" || extra.ExtraType != "trailer" || extra.Name != "" {
		t.Errorf("Alien's trailer: %+v", extra)
	}
	if local == nil || local.Name != "Holidays" || local.Year != 2019 || !local.Image {
		t.Fatalf("the local film: %+v", local)
	}
	if img, err := f.Image(context.Background(), local.Key); err != nil || string(img) != "picture" {
		t.Errorf("its image: %q %v", img, err)
	}
	if _, err := f.Image(context.Background(), "movie/tmdb/949"); err == nil {
		t.Error("an image of a title not local")
	}
	// The etag follows the bytes: the same content, the same etag.
	again, _ := f.copy(heat.Copies[0].ID)
	if again.ETag != heat.Copies[0].ETag {
		t.Error("the etag changed with no change of bytes")
	}
}

// A file's media type from its first bytes (MSFP video section 3.5, the
// registry's brand table).
func TestSniff(t *testing.T) {
	ftyp := func(major string, compat ...string) []byte {
		b := []byte{0, 0, 0, byte(16 + 4*len(compat))}
		b = append(b, "ftyp"+major+"\x00\x00\x00\x00"...)
		for _, c := range compat {
			b = append(b, c...)
		}
		return b
	}
	ts := func(n, off int) []byte {
		b := make([]byte, 3*n+off)
		for k := range 3 {
			b[off+k*n] = 0x47
		}
		return b
	}
	for name, c := range map[string]struct {
		head []byte
		want string
	}{
		"isom":            {ftyp("isom"), "video/mp4"},
		"qt":              {ftyp("qt  "), "video/quicktime"},
		"M4A":             {ftyp("M4A "), "audio/mp4"},
		"3gp":             {ftyp("3gp5"), "video/3gpp"},
		"unknown, compat": {ftyp("zzzz", "mp42"), "video/mp4"},
		"unknown":         {ftyp("zzzz"), "video/mp4"},
		"matroska":        {append([]byte{0x1a, 0x45, 0xdf, 0xa3, 0x42, 0x82, 0x88}, "matroska"...), "video/matroska"},
		"webm":            {append([]byte{0x1a, 0x45, 0xdf, 0xa3, 0x42, 0x82, 0x84}, "webm"...), "video/webm"},
		"ts":              {ts(188, 0), "video/mp2t"},
		"m2ts":            {ts(192, 4), "video/vnd.dlna.mpeg-tts"},
		"nothing":         {[]byte("plain"), ""},
	} {
		if got := sniff(c.head); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	if audioOrVideo("video/matroska", false) != "audio/matroska" || audioOrVideo("video/quicktime", false) != "video/quicktime" ||
		audioOrVideo("video/mp4", true) != "video/mp4" {
		t.Error("audio or video")
	}
}

// ffprobe's facts in MSFP's vocabularies: H.273 numbers, codecs strings.
func TestProbeMapping(t *testing.T) {
	s := ffprobeStream{CodecName: "hevc", CodecTag: "hvc1", Profile: "Main 10", Level: 153, Width: 3840, Height: 2160,
		PixFmt: "yuv420p10le", ColorRange: "tv", ColorSpace: "bt2020nc", ColorTransfer: "smpte2084", ColorPrimaries: "bt2020",
		AvgFrameRate: "48000/2002", SampleAspectRatio: "1:1", FieldOrder: "progressive"}
	s.SideData = []ffprobeSide{{Type: "Display Matrix", Rotation: -90}}
	v := videoStream(s)
	c := v.Colour
	if v.Codecs != "hvc1.2.4.L153" || v.BitDepth != 10 || v.FPS != "24000/1001" || v.SampleAspect != "" || v.Interlaced ||
		*c.Primaries != 9 || *c.Transfer != 16 || *c.Matrix != 9 || *c.FullRange || v.Rotation == nil || *v.Rotation != 90 {
		t.Errorf("%+v %+v", v, c)
	}
	if _, err := wire.ReadCodecs(v.Codec, v.Codecs, v.BitDepth, v.DV); err != nil {
		t.Error(err)
	}
	h := videoStream(ffprobeStream{CodecName: "h264", Profile: "High", Level: 41, Width: 1920, Height: 1080, PixFmt: "yuv420p", Refs: 4})
	if h.Codecs != "avc1.640029" || h.RefFrames != 4 || h.BitDepth != 8 {
		t.Errorf("%+v", h)
	}
	if videoStream(ffprobeStream{CodecName: "hevc", Profile: "Rext", Level: 120, Width: 1, Height: 1}).Codecs != "" {
		t.Error("an HEVC range extension without its constraint bytes")
	}
	a := audioTrack(ffprobeStream{Index: 1, CodecName: "eac3", Profile: "Dolby Digital Plus + Dolby Atmos", Channels: 6})
	if a.ID != "a1" || a.Codecs != "ec-3" || a.Objects != "dolby-atmos" {
		t.Errorf("%+v", a)
	}
	for codec, want := range map[[2]string]string{{"aac", "LC"}: "mp4a.40.2", {"aac", "HE-AACv2"}: "mp4a.40.29",
		{"dts", "DTS-HD MA"}: "dtsh", {"dts", "DTS"}: "dtsc", {"dts", "DTS Express"}: "dtse", {"flac", ""}: "fLaC"} {
		if got := audioCodecs(codec[0], codec[1]); got != want {
			t.Errorf("%v: %q, want %q", codec, got, want)
		}
	}
}
