package msfp

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go/wire"
)

// The names an owning server serves under a ticket (MSFP Media's
// fetching.json names, fetching 1 and 4.2, and MSFP's media.json names,
// the scan ticket of MSFP media 4.2): exactly those its answer opened,
// 404 past the counts.
func TestServedNames(t *testing.T) {
	type nameCase struct {
		Name   string
		Text   string
		Valid  bool
		Ticket struct {
			Kind, Protocol string
			Subtitles      []wire.ServedSubtitle
			FontCount      int
			SheetCount     int
			Queue          []string
			Names          []string
		}
	}
	var v struct{ Names []nameCase }
	for _, name := range []string{"media/v0/fetching.json", "msfp/v0/media.json"} {
		raw, err := os.ReadFile("wire/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var f struct{ Names []nameCase }
		if err := json.Unmarshal(raw, &f); err != nil || len(f.Names) == 0 {
			t.Fatal(name, ": no cases", err)
		}
		v.Names = append(v.Names, f.Names...)
	}
	for _, c := range v.Names {
		tk := c.Ticket
		var ok bool
		switch tk.Kind {
		case "image":
			ok = c.Text == "image"
		case "previews":
			p, parsed := ParseMediaPath(c.Text)
			ok = parsed && p.Kind == MediaSheet && p.Sheet < tk.SheetCount
		default:
			purpose := map[string]string{"playback": wire.PurposePlay, "download": wire.PurposeDownload, "scan": wire.PurposeScan}[tk.Kind]
			pb := &playback{purpose: purpose, fonts: tk.FontCount, subtitles: map[string]wire.SubtitleServing{},
				entries: map[int]entryServed{0: {protocol: tk.Protocol}}, names: map[string]bool{}}
			if purpose == wire.PurposePlay {
				pb.sheets = tk.SheetCount
			}
			if purpose != wire.PurposeScan {
				for _, s := range tk.Subtitles {
					pb.subtitles[s.ID] = s.Serving()
				}
			}
			for n, protocol := range tk.Queue {
				if protocol != "" {
					pb.entries[n+1] = entryServed{protocol: protocol}
				}
			}
			for e := range len(tk.Queue) + 1 {
				for _, n := range tk.Names {
					pb.names[strconv.Itoa(e)+"/"+n] = true
				}
			}
			_, ok = mediaPath(c.Text, pb)
		}
		if ok != c.Valid {
			t.Errorf("%s (%s %s): served %v", c.Name, tk.Kind, c.Text, ok)
		}
	}
}

// The master playlist's attributes the copy's facts tell, filled when
// the host leaves them out (MSFP Media fetching 3.1).
func TestMasterAttributes(t *testing.T) {
	pq, one := 16, 1
	c := wire.Copy{DurationMs: 1000, Size: 1000,
		Video: &wire.VideoStream{Codec: "hevc", Codecs: "hvc1.2.4.L153.B0", Width: 3840, Height: 2160, BitDepth: 10,
			Colour: &wire.VideoColour{Primaries: &one, Transfer: &pq}, DV: &wire.VideoDV{Profile: 8, Level: 6, Compatibility: 1}},
		AudioTracks: []wire.CopyAudio{{ID: "a1", Codec: "eac3", Codecs: "ec-3", Objects: "dolby-atmos"}}}
	f := servedFacts{copy: c, audio: "a1", served: wire.Served{Method: wire.MethodRemux, Video: wire.CodecCopy, Audio: wire.CodecCopy, Segments: wire.SegmentsMP4}}
	in := "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"a1\",URI=\"a1.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=9000\nv0.m3u8\n"
	out, missing := masterAttributes([]byte(in), f)
	want := "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"a1\",URI=\"a1.m3u8\",CHANNELS=\"16/JOC\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=9000,CODECS=\"hvc1.2.4.L153.B0,ec-3\",RESOLUTION=3840x2160,VIDEO-RANGE=PQ,SUPPLEMENTAL-CODECS=\"dvh1.08.06/db1p\"\nv0.m3u8\n"
	if string(out) != want || len(missing) != 0 {
		t.Errorf("%q %v", out, missing)
	}
	// Converted to the baseline: nothing the facts tell, CODECS missing.
	f.served = wire.Served{Method: wire.MethodTranscode, Video: "h264", Audio: "aac"}
	if out, missing := masterAttributes([]byte(in), f); strings.Contains(string(out), "PQ") || len(missing) != 2 {
		t.Errorf("%q %v", out, missing)
	}
}
