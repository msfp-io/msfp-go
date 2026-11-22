package wire

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// What FilterPlaylist keeps names files of the stream only, has no
// character players end lines at inside a line, holds only the subset's
// tags, and is kept whole when checked again.
func FuzzFilterPlaylist(f *testing.F) {
	for _, s := range []string{"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n",
		"#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-TARGETDURATION:6\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6,\nseg0.m4s\r\n#EXT-X-ENDLIST\n",
		"#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"a1\",URI=\"a1.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n",
		"#EXTM3U\n#EXT-X-KEY:URI=\"k\"\n", "#EXTM3U\n#EXTINF:6,\rhttps://e.example/s\n", "#EXTM3U\n#X:uri=\"a\"URI=\"b\"\n"} {
		f.Add([]byte(s), false)
		f.Add([]byte(s), true)
	}
	f.Fuzz(func(t *testing.T, body []byte, master bool) {
		subset := MediaSubset
		if master {
			subset = MasterSubset
		}
		out, err := FilterPlaylist(body, subset, fuzzAudio)
		if err != nil {
			return
		}
		if !bytes.HasPrefix(out, []byte("#EXTM3U\n")) {
			t.Fatalf("kept %q", out)
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
			if strings.ContainsFunc(line, lineBreaking) || line == "" {
				t.Fatalf("a line %q kept", line)
			}
			if strings.HasPrefix(line, "#") {
				tag, _, _ := strings.Cut(line, ":")
				found := false
				for _, s := range subset {
					found = found || s == tag
				}
				if !found {
					t.Fatalf("a tag outside the subset kept: %q", line)
				}
				continue
			}
			if !FileName(line) {
				t.Fatalf("an address kept: %q", line)
			}
		}
		// Checked again, it is kept whole, unless the subset was another
		// kind's, which leaves neither a master nor a media playlist.
		if !bytes.Contains(out, []byte("\n#EXTINF")) && !bytes.Contains(out, []byte("\n#EXT-X-STREAM-INF")) {
			return
		}
		again, err := FilterPlaylist(out, subset, fuzzAudio)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("checked again: %q, %v", again, err)
		}
	})
}

// fuzzAudio: a playback of one audio track, a1.
func fuzzAudio(id string) bool { return id == "a1" }

// A rebuilt WebVTT part is rebuilt again as it is, or, when the rules
// read it differently a second time, once more as that second rebuild
// (video section 6.3: a "<" before a tag the balancing removes may
// meet a letter, and read as a tag once decoded).
func FuzzRebuildWebVTT(f *testing.F) {
	f.Add([]byte("WEBVTT\n\n00:01.000 --> 00:02.000\n<b>Hi</b> &lt;i&gt;\n"))
	f.Add([]byte("WEBVTT\r\n\r\n1\r\n00:00:01.000 --> 00:00:02.000 align:start\r\na < b &amp; c\r\n"))
	f.Add([]byte("WEBVTT\n00:00.000 --> 00:00.000\n<</b>A"))
	f.Fuzz(func(t *testing.T, in []byte) {
		stable(t, in, RebuildWebVTT)
	})
}

// stable checks that rebuilding settles: a second rebuild gives what a
// third does.
func stable(t *testing.T, in []byte, rebuild func([]byte) ([]byte, bool)) {
	out, ok := rebuild(in)
	if !ok {
		return
	}
	again, ok := rebuild(out)
	if !ok {
		t.Fatalf("%q rebuilt as %q, then refused", in, out)
	}
	if third, ok := rebuild(again); !ok || string(third) != string(again) {
		t.Fatalf("%q rebuilt as %q, then %q, then %q", in, out, again, third)
	}
}

// A rebuilt ASS part is ASS of one form, UTF-8, its lines of the known
// sections only, and rebuilding it settles (video section 6.4: white
// space a removed control character uncovers, at a style name's start
// or an event's end, goes at the second rebuild).
func FuzzRebuildASS(f *testing.F) {
	f.Add([]byte("[Script Info]\nPlayResX: 1920\n[V4+ Styles]\nStyle: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,2,2,10,10,10,1\n" +
		"[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,{\\b1\\t(0,500,\\fs30)\\pos(1,2)}Hi{\\p1}m 0 0 l 1 1{\\p0}\n"))
	f.Add([]byte("[Script Info]\n[Events]\nDialogue:,0:00:00.00,0:00:00.00,,,,,,,    \xe7"))
	f.Add([]byte("[Script Info]\r\n[V4 Styles]\r\nStyle: S,Arial,20,65535,255,0,0,0,0,1,2,2,10,10,10,10,0,1\r\n[Events]\r\nDialogue: Marked=0,0:00:01.00,0:00:02.00,S,,0,0,0,,{\\a5\\clip(1,m 0 0 l 4 4)}x\r\n[Fonts]\r\nfontname: a.ttf\r\n"))
	f.Fuzz(func(t *testing.T, in []byte) {
		out, ok := RebuildASS(in)
		if !ok {
			return
		}
		s := string(out)
		if !strings.HasPrefix(s, "[Script Info]\nScriptType: v4.00+\n") || !utf8.ValidString(s) || strings.ContainsAny(s, "\r\x00") {
			t.Fatalf("not ASS of one form: %q", s)
		}
		for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
			if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "Format: ") || strings.HasPrefix(line, "Style: ") || strings.HasPrefix(line, "Dialogue: ") {
				continue
			}
			if name, _, ok := strings.Cut(line, ": "); !ok || !slices.ContainsFunc(assInfo, func(n struct {
				name string
				ok   func(string) bool
			}) bool {
				return n.name == name
			}) && name != "ScriptType" {
				t.Fatalf("a line kept: %q", line)
			}
		}
		if strings.Count(s, "\nStyle: ") <= maxASSStyles {
			stable(t, in, RebuildASS)
		}
	})
}

// A font is accepted only with its signature and tables within the
// file; never a panic.
func FuzzCheckFont(f *testing.F) {
	font := []byte("\x00\x01\x00\x00\x00\x09\x00\x00\x00\x00\x00\x00")
	for _, tag := range []string{"cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp", "name", "post"} {
		font = append(font, tag...)
		font = append(font, 0, 0, 0, 0, 0, 0, 0, 0x9c, 0, 0, 0, 4)
	}
	font = append(font, 0, 0, 0, 0)
	f.Add(font)
	f.Add([]byte("OTTO\x00\x01"))
	f.Add([]byte("ttcf"))
	f.Fuzz(func(t *testing.T, b []byte) {
		typ, err := CheckFont(b)
		if err != nil {
			return
		}
		switch {
		case typ == FontTTF && (string(b[:4]) == "\x00\x01\x00\x00" || string(b[:4]) == "true"):
		case typ == FontOTF && string(b[:4]) == "OTTO":
		default:
			t.Fatalf("%q for %x", typ, b[:4])
		}
		n := int(b[4])<<8 | int(b[5])
		if n < 1 || n > maxFontTables || 12+16*n > len(b) {
			t.Fatalf("%d tables in %d bytes", n, len(b))
		}
	})
}
