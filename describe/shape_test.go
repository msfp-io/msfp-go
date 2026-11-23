package describe

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go/wire"
)

var update = flag.Bool("update", false, "rewrite testdata/*.copy.json from the fixtures")

// Each fixture's copy, against its golden copy (testdata/<name>.copy.json;
// -update rewrites them), and read back by the wire's reader unchanged:
// every value Shape sends is one a receiver keeps.
func TestShapeFixtures(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.json")
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, ".copy.json") {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		n++
		t.Run(name, func(t *testing.T) {
			c := Shape(loadFixture(t, name), []External{{Language: "fre", Format: "subrip", Forced: true}})
			c.ID, c.ETag = "v1", "e1"
			got, err := wire.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			var pretty bytes.Buffer
			json.Indent(&pretty, got, "", " ")
			pretty.WriteByte('\n')
			golden := filepath.Join("testdata", name+".copy.json")
			if *update {
				if err := os.WriteFile(golden, pretty.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(pretty.Bytes(), want) {
				t.Errorf("got\n%s\nwant\n%s", pretty.Bytes(), want)
			}
			back, err := wire.ReadCopy(got)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, c) {
				t.Errorf("read back differs:\n%+v\n%+v", back, c)
			}
		})
	}
	if n < 15 {
		t.Errorf("%d fixtures", n)
	}
}

// The facts each fixture is there for.
func TestShapeFacts(t *testing.T) {
	shape := func(name string) wire.Copy { return Shape(loadFixture(t, name), nil) }

	c := shape("h264-aac-mp4")
	if c.MediaType != "video/mp4" || c.DurationMs != 2002 || c.Video.Codecs != "avc1.64000C" || c.Video.RefFrames != 4 ||
		c.Video.Rotation == nil || *c.Video.Rotation != 270 || c.Video.FPS != "24000/1001" {
		t.Errorf("h264: %+v", c.Video)
	}
	if len(c.AudioTracks) != 1 || c.AudioTracks[0].ID != "a1" || c.AudioTracks[0].Codecs != "mp4a.40.2" {
		t.Errorf("aac: %+v", c.AudioTracks)
	}
	if len(c.Chapters) != 2 || c.Chapters[0].Name != "Opening" || c.Chapters[1].Name != "" || c.Chapters[1].StartMs != 1000 {
		t.Errorf("chapters (a site's name dropped): %+v", c.Chapters)
	}

	if c := shape("h264-high10-mp4"); c.Video.Codecs != "avc3.6E000C" || c.Video.BitDepth != 10 {
		t.Errorf("high 10, avc3: %+v", c.Video)
	}
	if c := shape("h264-int-ts"); c.MediaType != "video/mp2t" || !c.Video.Interlaced || c.Video.Codecs != "avc1.640015" ||
		c.Video.RefFrames != 4 || c.Video.FPS != "25/1" {
		t.Errorf("interlaced TS, Annex B record: %+v", c.Video)
	}

	c = shape("hevc-hdr10-mkv")
	v := c.Video
	if v.Codecs != "hvc1.2.4.L60.90" || *v.Colour.Primaries != 9 || *v.Colour.Transfer != 16 || *v.Colour.Matrix != 9 ||
		*v.Colour.FullRange || !v.HighDynamicRange() || v.HDR != nil {
		t.Errorf("hdr10: %+v %+v", v, v.Colour)
	}
	a := c.AudioTracks
	if len(a) != 2 || a[0].Language != "fr" || !a[0].Default || a[0].Codecs != "ec-3" || a[0].Channels != 6 ||
		a[1].Language != "en" || a[1].Role != "commentary" {
		t.Errorf("audio: %+v", a)
	}
	s := c.SubtitleTracks
	if len(s) != 2 || s[0].ID != "s3" || !s[0].Forced || s[0].Format != "subrip" || s[1].ID != "s4" || !s[1].HearingImpaired || s[1].Format != "ass" {
		t.Errorf("subtitles: %+v", s)
	}

	if c := shape("hevc-hlg-atc-ts"); *c.Video.Colour.Transfer != wire.TransferHLG || c.Video.Codecs != "hvc1.2.4.L60.90" {
		t.Errorf("HLG through the SEI, Annex B record: %+v", c.Video)
	}
	if c := shape("hevc-rext-mp4"); c.Video.Codecs != "hev1.4.10.L60.9D.8" {
		t.Errorf("rext: %q", c.Video.Codecs)
	}
	if c := shape("hevc-pq-mp4"); c.Video.Codecs != "hev1.2.4.L60.90" || c.Video.FPS != "24/1" {
		t.Errorf("hev1: %+v", c.Video)
	}
	if c := shape("av1-mp4"); c.Video.Codecs != "av01.0.00M.10" {
		t.Errorf("av1: %q", c.Video.Codecs)
	}
	if c := shape("av1-444-mkv"); c.Video.Codecs != "av01.1.00M.08.0.000.01.01.01.0" {
		t.Errorf("av1 4:4:4: %q", c.Video.Codecs)
	}
	if c := shape("vp9-444-mp4"); c.Video.Codecs != "vp09.01.11.08.03.01.01.01.00" {
		t.Errorf("vp9 4:4:4: %q", c.Video.Codecs)
	}
	if c := shape("vp9-opus-webm"); c.MediaType != "video/webm" || c.Video.Codecs != "vp09.00.11.08" || c.AudioTracks[0].Codec != "opus" {
		t.Errorf("webm: %+v", c)
	}

	c = shape("dts-truehd-3d-mkv")
	if c.ThreeD != "sbs" || c.AudioTracks[0].Codecs != "dtsc" || c.AudioTracks[1].Codecs != "mlpa" || c.AudioTracks[1].BitDepth != 24 {
		t.Errorf("3d, dts, truehd: %+v", c)
	}
	if c := shape("mpeg2-m2ts"); c.MediaType != "video/vnd.dlna.mpeg-tts" || c.Video.Codec != "mpeg2video" || c.Video.Codecs != "" {
		t.Errorf("m2ts: %+v", c)
	}
	if c := shape("mpeg4-avi"); c.MediaType != "video/avi" || c.AudioTracks[0].Codec != "pcm_s16le" || c.AudioTracks[0].BitDepth != 16 {
		t.Errorf("avi: %+v", c)
	}
	if c := shape("tone-flac"); c.MediaType != "audio/flac" || c.Video != nil || c.AudioTracks[0].Codecs != "fLaC" {
		t.Errorf("flac with cover art: %+v", c)
	}
	if c := shape("tone-wav"); c.MediaType != "audio/wave" || c.AudioTracks[0].BitDepth != 24 {
		t.Errorf("wav: %+v", c)
	}
	if c := shape("adts-aac"); c.MediaType != "audio/aac" || c.AudioTracks[0].ID != "a0" {
		t.Errorf("adts: %+v", c)
	}

	// Attachments take a stream index: external subtitles count above it.
	c = Shape(loadFixture(t, "with-font-mkv"), []External{{Language: "eng", Format: "webvtt"}})
	if len(c.SubtitleTracks) != 2 || c.SubtitleTracks[1].ID != "s3" || !c.SubtitleTracks[1].External || c.SubtitleTracks[1].Language != "en" {
		t.Errorf("external: %+v", c.SubtitleTracks)
	}

	c = shape("handmade-dv-atmos")
	v = c.Video
	if v.Codecs != "hvc1.2.4.L153" || v.DV == nil || v.DV.Profile != 8 || v.DV.Compatibility != 1 || *v.DV.EL ||
		!reflect.DeepEqual(v.HDR, []string{"hdr10plus"}) || c.ThreeD != "" {
		t.Errorf("dv: %+v %+v", v, v.DV)
	}
	a = c.AudioTracks
	if len(a) != 4 || a[0].Objects != "dolby-atmos" || !a[0].Original || a[1].Objects != "dolby-atmos" ||
		a[2].Objects != "dts-x" || a[2].Codecs != "dtsh" || a[2].Role != "dub" || a[3].Codecs != "mp4a.40.29" || a[3].Role != "description" {
		t.Errorf("atmos, dts:x, roles: %+v", a)
	}
	s = c.SubtitleTracks
	if len(s) != 2 || s[0].ID != "s5" || !s[0].Forced || s[0].Format != "hdmv_pgs_subtitle" || !s[1].HearingImpaired {
		t.Errorf("pgs forced by its title, captions: %+v", s)
	}
	want := []wire.CopyChapter{{StartMs: 0, Name: "Chapter 1"}, {StartMs: 60000}, {StartMs: 120000}, {StartMs: 180000},
		{StartMs: 240000, Name: "The Desert"}}
	if !reflect.DeepEqual(c.Chapters, want) {
		t.Errorf("chapters: %+v", c.Chapters)
	}
}

// A display matrix: its turn clockwise; mirrors and odd angles left out.
func TestRotation(t *testing.T) {
	m := func(a, b, c, d int) string {
		return "\n00000000: " + itoa(a) + " " + itoa(b) + " 0\n00000001: " + itoa(c) + " " + itoa(d) + " 0\n00000002: 0 0 1073741824\n"
	}
	for _, c := range []struct {
		d    SideData
		want int // -1 for none
	}{
		{SideData{Rotation: 90, DisplayMatrix: m(0, -65536, 65536, 0)}, 270},
		{SideData{Rotation: -90, DisplayMatrix: m(0, 65536, -65536, 0)}, 90},
		{SideData{Rotation: 180, DisplayMatrix: m(-65536, 0, 0, -65536)}, 180},
		{SideData{Rotation: 0, DisplayMatrix: m(65536, 0, 0, 65536)}, -1},
		{SideData{Rotation: 0, DisplayMatrix: m(-65536, 0, 0, 65536)}, -1}, // mirrored
		{SideData{Rotation: 45}, -1},
		{SideData{Rotation: 90.5}, -1},
	} {
		got := rotation(c.d)
		if c.want < 0 && got != nil || c.want >= 0 && (got == nil || *got != c.want) {
			t.Errorf("%+v: %v", c.d, got)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestTitleFlags(t *testing.T) {
	for title, want := range map[string]fromTitle{
		"Director's Commentary": {audioRole: "commentary", subtitleRole: "commentary"},
		"English AD":            {audioRole: "description"},
		"Audio Description":     {audioRole: "description"},
		"English (SDH)":         {hearingImpaired: true},
		"CC":                    {hearingImpaired: true},
		"Forced":                {forced: true},
		"Français (forcés)":     {forced: true},
		"Foreign parts only":    {forced: true},
		"Easy Reader":           {subtitleRole: "easyreader"},
		"English":               {},
		"RELEASEGROUP":          {},
		"Ad Astra":              {},
		"Special Forces":        {},
	} {
		if got := titleFlags(title); got != want {
			t.Errorf("%q: %+v, want %+v", title, got, want)
		}
	}
}

func TestLooksLikeLocation(t *testing.T) {
	for s, want := range map[string]bool{"Chapter 3": false, "The Desert": false, "00:12:00.000": false, "Opening": false,
		"movie.mkv": true, "www.example.com": true, "visit example.org": true, "http://a": true, "C:\\x": true, "a/b": true,
		"me@mail.example": true, "Dr. Strangelove": false} {
		if looksLikeLocation(s) != want {
			t.Errorf("%q: %v", s, !want)
		}
	}
}

func TestFPS(t *testing.T) {
	for _, c := range []struct{ r, avg, want string }{
		{"24000/1001", "24000/1001", "24000/1001"},
		{"24000/1001", "10000000/417083", "24000/1001"}, // the average, near the declared rate
		{"24000/1001", "2997/125", "24000/1001"},
		{"50/1", "25/1", "25/1"}, // a field rate declared: the average
		{"0/0", "30/1", "30/1"},
		{"90000/1", "0/0", "90000/1"},
		{"2000000/1", "0/0", ""},
	} {
		if got := fpsOf(Stream{RFrameRate: c.r, AvgFrameRate: c.avg}); got != c.want {
			t.Errorf("%s %s: %q", c.r, c.avg, got)
		}
	}
}

func TestMediaTypes(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"video/matroska", ""}: "audio/matroska", {"video/mp4", ""}: "audio/mp4", {"audio/mp4", "v"}: "video/mp4",
		{"video/quicktime", ""}: "video/quicktime", {"video/avi", ""}: "video/avi", {"audio/mpeg", "v"}: "audio/mpeg",
		{"video/mp2t", ""}: "audio/mp2t", {"video/vnd.dlna.mpeg-tts", ""}: "video/vnd.dlna.mpeg-tts",
	} {
		if got := AudioOrVideo(in[0], in[1] != ""); got != want {
			t.Errorf("%v: %q", in, got)
		}
	}
	for in, want := range map[string]string{"video/x-msvideo": "video/avi", "Audio/X-WAV": "audio/wave",
		"video/x-matroska; codecs=x": "video/matroska", "video/mp4": "video/mp4"} {
		if got := CanonicalType(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	for in, want := range map[string]string{"matroska,webm": "video/matroska", "mov,mp4,m4a,3gp,3g2,mj2": "video/mp4",
		"mpegts": "video/mp2t", "mkv": "video/matroska", "m2ts": "video/vnd.dlna.mpeg-tts", "avi": "video/avi",
		"webm": "video/webm", "hls": "", "mkv,webm": "video/matroska", "concat": ""} {
		if got := ContainerType(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
