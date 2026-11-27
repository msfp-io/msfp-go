package msfp

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/msfp-io/msfp-go/wire"
)

// The master playlist's attributes (MSFP Media fetching 3.1, MSFP Media playing
// 2.7): each #EXT-X-STREAM-INF carries BANDWIDTH, CODECS and,
// for video, RESOLUTION; VIDEO-RANGE (PQ or HLG) when the picture served
// is PQ or HLG; SUPPLEMENTAL-CODECS when Dolby Vision is kept; an E-AC-3
// rendition whose objects is dolby-atmos may say CHANNELS "16/JOC". They
// are the host's to write, its transcoder knowing what it makes. The
// engine fills, for a playback's own stream, those the copy's facts tell
// when what is served of them is the copy's own (a stream copied, the
// range kept), and logs those that stay missing.

// servedFacts are what the engine knows of a playback's own stream: the
// copy, its audio track played, and what is served of it.
type servedFacts struct {
	copy   wire.Copy
	audio  string
	served wire.Served
}

var attrNameRe = map[string]*regexp.Regexp{}

func hasAttr(list, name string) bool {
	re, ok := attrNameRe[name]
	if !ok {
		re = regexp.MustCompile(`(^|,)` + regexp.QuoteMeta(name) + `=`)
		attrNameRe[name] = re
	}
	return re.MatchString(list)
}

func init() {
	for _, n := range []string{"BANDWIDTH", "CODECS", "RESOLUTION", "VIDEO-RANGE", "SUPPLEMENTAL-CODECS", "CHANNELS", "TYPE=AUDIO", "NAME"} {
		hasAttr("", n)
	}
}

// masterAttributes fills the attributes the facts tell into a master
// playlist, and names those still missing.
func masterAttributes(playlist []byte, f servedFacts) ([]byte, []string) {
	v := f.copy.Video
	videoCopied := v != nil && f.served.Video == wire.CodecCopy
	audioCopied := f.served.Audio == wire.CodecCopy
	var track *wire.CopyAudio
	for i := range f.copy.AudioTracks {
		if f.copy.AudioTracks[i].ID == f.audio {
			track = &f.copy.AudioTracks[i]
		}
	}
	// What the facts tell (wire's rules, the same for any server).
	var codecs []string
	videoCodecs, supplemental := "", ""
	if videoCopied && v.Codecs != "" {
		videoCodecs, supplemental = wire.CopiedVideoCodecs(*v, f.served.Segments == wire.SegmentsMP4)
		codecs = append(codecs, videoCodecs)
	}
	audioCodecs := ""
	if audioCopied && track != nil {
		audioCodecs = track.Codecs
		if audioCodecs == "" {
			audioCodecs = wire.FixedAudioCodecs(track.Codec)
		}
	}
	if audioCodecs != "" {
		codecs = append(codecs, audioCodecs)
	}
	codecsKnown := (v == nil || videoCodecs != "") && audioCodecs != ""
	rangeKept := v != nil && (videoCopied || f.served.Transcode != nil && f.served.Transcode.Range == wire.RangeKeep)
	videoRange := ""
	if rangeKept {
		if r := wire.KeptVideoRange(*v, videoCopied); r != "SDR" {
			videoRange = r
		}
	}
	bandwidth := int64(0)
	if (v == nil || videoCopied) && audioCopied && len(f.copy.AudioTracks) <= 1 {
		bandwidth = f.copy.BitsPerSecond()
	}
	lines := strings.Split(string(playlist), "\n")
	missing := map[string]bool{}
	for i, line := range lines {
		head, list, ok := strings.Cut(line, ":")
		switch {
		case !ok:
		case head == "#EXT-X-STREAM-INF":
			add := func(name, value string) {
				if !hasAttr(list, name) && value != "" {
					list += "," + name + "=" + value
				}
				if !hasAttr(list, name) {
					missing[name] = true
				}
			}
			if codecsKnown {
				add("CODECS", strconv.Quote(strings.Join(codecs, ",")))
			} else {
				add("CODECS", "")
			}
			if bandwidth > 0 {
				add("BANDWIDTH", strconv.FormatInt(bandwidth, 10))
			} else {
				add("BANDWIDTH", "")
			}
			if v != nil {
				res := ""
				if videoCopied {
					res = fmt.Sprintf("%dx%d", v.Width, v.Height)
				}
				add("RESOLUTION", res)
			}
			if videoRange != "" && !hasAttr(list, "VIDEO-RANGE") {
				list += ",VIDEO-RANGE=" + videoRange
			}
			if supplemental != "" && !hasAttr(list, "SUPPLEMENTAL-CODECS") {
				list += ",SUPPLEMENTAL-CODECS=" + strconv.Quote(supplemental)
			}
			lines[i] = head + ":" + list
		case head == "#EXT-X-MEDIA" && strings.Contains(list, "TYPE=AUDIO") && !hasAttr(list, "CHANNELS"):
			// A rendition of an E-AC-3 track with Atmos, left unchanged.
			if id := attrValue(list, "NAME"); id != "" && audioCopied {
				for _, a := range f.copy.AudioTracks {
					if a.ID == id && a.Codec == "eac3" && a.Objects == "dolby-atmos" {
						lines[i] = line + `,CHANNELS="16/JOC"`
					}
				}
			}
		}
	}
	var names []string
	for _, n := range []string{"BANDWIDTH", "CODECS", "RESOLUTION"} {
		if missing[n] {
			names = append(names, n)
		}
	}
	return []byte(strings.Join(lines, "\n")), names
}

// attrValue is an attribute's quoted value in a list, unquoted; "" when
// it has none.
func attrValue(list, name string) string {
	i := strings.Index(","+list, ","+name+`="`)
	if i < 0 {
		return ""
	}
	rest := list[i+len(name)+2:]
	v, _, _ := strings.Cut(rest, `"`)
	return v
}
