package describe

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// What ffprobe's JSON says, as far as a copy uses it: the format, the
// streams, the chapters, and each stream's record (its extradata, from
// the run with -show_data).

// Format is ffprobe's format section.
type Format struct {
	Name     string `json:"format_name"`
	Streams  int    `json:"nb_streams"`
	Duration string `json:"duration"`
	Size     string `json:"size"`
	Tags     Tags   `json:"tags"`
}

// Tags are a stream's, a chapter's or the format's tags. ffprobe prints
// Matroska's in upper case and MP4's in lower case: Get reads both.
type Tags map[string]string

// Get is the tag named key, in any case.
func (t Tags) Get(key string) string {
	if v, ok := t[key]; ok {
		return v
	}
	for k, v := range t {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// Stream is ffprobe's stream section: the fields a copy uses.
type Stream struct {
	Index             int         `json:"index"`
	CodecType         string      `json:"codec_type"`
	CodecName         string      `json:"codec_name"`
	CodecTag          string      `json:"codec_tag_string"`
	Profile           string      `json:"profile"`
	Level             int         `json:"level"`
	Width             int         `json:"width"`
	Height            int         `json:"height"`
	PixFmt            string      `json:"pix_fmt"`
	BitsPerRawSample  string      `json:"bits_per_raw_sample"`
	BitsPerSample     int         `json:"bits_per_sample"`
	ColorRange        string      `json:"color_range"`
	ColorSpace        string      `json:"color_space"`
	ColorTransfer     string      `json:"color_transfer"`
	ColorPrimaries    string      `json:"color_primaries"`
	FieldOrder        string      `json:"field_order"`
	Refs              int         `json:"refs"`
	SampleAspectRatio string      `json:"sample_aspect_ratio"`
	AvgFrameRate      string      `json:"avg_frame_rate"`
	RFrameRate        string      `json:"r_frame_rate"`
	Channels          int         `json:"channels"`
	SampleRate        string      `json:"sample_rate"`
	Disposition       Disposition `json:"disposition"`
	Tags              Tags        `json:"tags"`
	SideData          []SideData  `json:"side_data_list"`
	// Record is the stream's extradata, from the run with -show_data;
	// nil when that run didn't cover the stream or it has none.
	Record []byte `json:"-"`
}

// Disposition is a stream's flags, by ffprobe's names.
type Disposition struct {
	Default         int `json:"default"`
	Dub             int `json:"dub"`
	Original        int `json:"original"`
	Comment         int `json:"comment"`
	Forced          int `json:"forced"`
	HearingImpaired int `json:"hearing_impaired"`
	VisualImpaired  int `json:"visual_impaired"`
	AttachedPic     int `json:"attached_pic"`
	Captions        int `json:"captions"`
	Descriptions    int `json:"descriptions"`
	StillImage      int `json:"still_image"`
}

// SideData is a stream's or a frame's side data: a display matrix, a
// Dolby Vision configuration record, a stereo 3D layout, HDR10+.
type SideData struct {
	Type          string  `json:"side_data_type"`
	Rotation      float64 `json:"rotation"`
	DisplayMatrix string  `json:"displaymatrix"`
	DVProfile     int     `json:"dv_profile"`
	DVLevel       int     `json:"dv_level"`
	ELPresent     int     `json:"el_present_flag"`
	Compatibility int     `json:"dv_bl_signal_compatibility_id"`
	StereoType    string  `json:"type"`
	Inverted      int     `json:"inverted"`
}

// Chapter is ffprobe's chapter section.
type Chapter struct {
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Tags      Tags   `json:"tags"`
}

// Frame is a frame of the frame read: its colour transfer (the
// alternative transfer characteristics SEI's when the stream has one:
// ffmpeg's decoders apply it) and its side data (HDR10+).
type Frame struct {
	ColorTransfer string     `json:"color_transfer"`
	SideData      []SideData `json:"side_data_list"`
}

// output is a whole ffprobe output.
type output struct {
	Format   *Format   `json:"format"`
	Streams  []Stream  `json:"streams"`
	Chapters []Chapter `json:"chapters"`
	Frames   []Frame   `json:"frames"`
}

// dataStream is a stream of the run with -show_data.
type dataStream struct {
	Index         int    `json:"index"`
	Extradata     string `json:"extradata"`
	ExtradataSize int    `json:"extradata_size"`
}

// strictJSON decodes one JSON value from b into v, nothing after it.
func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(v); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("data after the JSON value")
	}
	return nil
}

// hexDump reads ffprobe's dump of a stream's data ("00000000: 0164 000c
// ...  .d.."), each line's 16 bytes in the 40 columns after its offset;
// false when the dump isn't of that form or its length isn't size.
func hexDump(s string, size int) ([]byte, bool) {
	out := make([]byte, 0, min(len(s)/3, size))
	for line := range strings.SplitSeq(s, "\n") {
		if line == "" {
			continue
		}
		if len(line) < 11 || line[8:10] != ": " {
			return nil, false
		}
		cols := line[10:min(len(line), 50)]
		b, err := hex.DecodeString(strings.ReplaceAll(strings.TrimRight(cols, " "), " ", ""))
		if err != nil || len(b) > 16 {
			return nil, false
		}
		out = append(out, b...)
		if len(out) > size {
			return nil, false
		}
	}
	return out, len(out) == size
}

// seconds is ffprobe's time in seconds ("12.345000") in milliseconds,
// rounded; false when it isn't one.
func seconds(s string) (int64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f > 1e10 || f != f {
		return 0, false
	}
	return int64(f*1000 + 0.5), true
}
