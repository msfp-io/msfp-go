package wire

// The capability list (MSFP Media playing 2.8): what a device plays,
// sent instead of delivery by a player to its own source, which then
// picks the delivery. MSFP refuses it (MSFP playback section 2): a
// server never describes a device to a peer, and the engine never sends
// one. These are the list's types and the reading of its forms, for a
// host that serves its own players with MSFP Media; the matching of a
// copy against the list (playing 2.8, "Matching" and "What the source
// picks") is the host's.

// The bounds of a capability list (MSFP Media playing 2.8).
const (
	MaxCapFiles     = 64
	MaxCapSegments  = 4
	MaxCapEntries   = 32 // video and audio entries, and subtitles
	MaxCapCodecs    = 16 // codecs strings of an entry
	MaxCapTransfers = 16
	MaxCapDV        = 16
	MaxCapTokens    = 8 // hdr, threeD, objects
)

// Capabilities is a capability list: the containers played as a file
// and the HLS segment formats beyond the baseline's (media types), the
// video and audio codecs decoded with their limits, and the subtitle
// formats drawn from inside a file (registry tokens). Values are hints:
// one of the right form a source doesn't know is kept, and never
// matches a copy.
type Capabilities struct {
	Files     []string          `json:"files,omitempty"`
	Segments  []string          `json:"segments,omitempty"`
	Video     []VideoCapability `json:"video,omitempty"`
	Audio     []AudioCapability `json:"audio,omitempty"`
	Subtitles []string          `json:"subtitles,omitempty"`
}

// VideoCapability is a video entry: one codec and how far the device
// plays it. Zero numbers are absent: no bound, but MaxBitDepth, 8 when
// absent; no Transfers is standard range only (1, 6, 14, 15).
type VideoCapability struct {
	Codec        string   `json:"codec"`
	Codecs       []string `json:"codecs,omitempty"`
	MaxWidth     int      `json:"maxWidth,omitempty"`
	MaxHeight    int      `json:"maxHeight,omitempty"`
	MaxFPS       string   `json:"maxFps,omitempty"`
	MaxBitDepth  int      `json:"maxBitDepth,omitempty"`
	Transfers    []int    `json:"transfers,omitempty"`
	HDR          []string `json:"hdr,omitempty"`
	DV           []int    `json:"dv,omitempty"`
	Interlaced   bool     `json:"interlaced,omitempty"`
	MaxRefFrames int      `json:"maxRefFrames,omitempty"`
	ThreeD       []string `json:"threeD,omitempty"`
}

// AudioCapability is an audio entry: one codec and how far the device
// plays it. Zero numbers are absent: no bound, but MaxChannels, 2 when
// absent.
type AudioCapability struct {
	Codec         string   `json:"codec"`
	Codecs        []string `json:"codecs,omitempty"`
	MaxChannels   int      `json:"maxChannels,omitempty"`
	Objects       []string `json:"objects,omitempty"`
	MaxSampleRate int      `json:"maxSampleRate,omitempty"`
	MaxBitDepth   int      `json:"maxBitDepth,omitempty"`
}

// readCapabilities reads a capability list as MSFP Media playing 2.8
// says ("Reading the list"), refusing (400) what isn't an object, a
// member of the wrong JSON type, a list past its bound, an entry without
// codec, a number out of its range, a value not of its form (a token, a
// media type, a codecs string, an fps fraction), and an entry of a codec
// whose streams require codecs (RequiresCodecs) without them. Unknown
// members are ignored.
func readCapabilities(v any) (*Capabilities, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, badPlay("capabilities isn't an object")
	}
	o := members(m)
	var c Capabilities
	var err error
	if c.Files, err = o.list("files", MaxCapFiles, IsMediaType); err != nil {
		return nil, err
	}
	if c.Segments, err = o.list("segments", MaxCapSegments, IsMediaType); err != nil {
		return nil, err
	}
	if c.Subtitles, err = o.list("subtitles", MaxCapEntries, Token); err != nil {
		return nil, err
	}
	video, err := o.objects("video", MaxCapEntries)
	if err != nil {
		return nil, err
	}
	for _, e := range video {
		v := VideoCapability{}
		if v.Codec, v.Codecs, err = e.codecEntry(); err != nil {
			return nil, err
		}
		for _, n := range []struct {
			name   string
			lo, hi int64
			dst    *int
		}{{"maxWidth", 1, 100_000, &v.MaxWidth}, {"maxHeight", 1, 100_000, &v.MaxHeight},
			{"maxBitDepth", 1, 16, &v.MaxBitDepth}, {"maxRefFrames", 1, 16, &v.MaxRefFrames}} {
			if *n.dst, err = e.bounded(n.name, n.lo, n.hi); err != nil {
				return nil, err
			}
		}
		if _, there := e["maxRefFrames"]; there && v.Codec != "h264" {
			return nil, badPlay("maxRefFrames on an entry that isn't h264")
		}
		if raw, there := e["maxFps"]; there {
			if v.MaxFPS = ratio(raw, "/", MaxFPSTerm, false); v.MaxFPS == "" {
				return nil, badPlay("maxFps isn't a fraction")
			}
		}
		if v.Transfers, err = e.integers("transfers", MaxCapTransfers, 0, 255); err != nil {
			return nil, err
		}
		if v.DV, err = e.integers("dv", MaxCapDV, 0, 127); err != nil {
			return nil, err
		}
		if v.HDR, err = e.list("hdr", MaxCapTokens, Token); err != nil {
			return nil, err
		}
		if v.ThreeD, err = e.list("threeD", MaxCapTokens, Token); err != nil {
			return nil, err
		}
		if v.Interlaced, _, err = e.boolean("interlaced"); err != nil {
			return nil, err
		}
		c.Video = append(c.Video, v)
	}
	audio, err := o.objects("audio", MaxCapEntries)
	if err != nil {
		return nil, err
	}
	for _, e := range audio {
		a := AudioCapability{}
		if a.Codec, a.Codecs, err = e.codecEntry(); err != nil {
			return nil, err
		}
		for _, n := range []struct {
			name   string
			lo, hi int64
			dst    *int
		}{{"maxChannels", 1, 64, &a.MaxChannels}, {"maxSampleRate", 8_000, 768_000, &a.MaxSampleRate},
			{"maxBitDepth", 8, 32, &a.MaxBitDepth}} {
			if *n.dst, err = e.bounded(n.name, n.lo, n.hi); err != nil {
				return nil, err
			}
		}
		if a.Objects, err = e.list("objects", MaxCapTokens, Token); err != nil {
			return nil, err
		}
		c.Audio = append(c.Audio, a)
	}
	return &c, nil
}

// codecEntry reads an entry's codec, required, and its codecs strings,
// required for the codecs whose streams require them.
func (o members) codecEntry() (string, []string, error) {
	codec, have, err := o.text("codec")
	if err != nil {
		return "", nil, err
	}
	if !have || !Token(codec) {
		return "", nil, badPlay("an entry without a codec token")
	}
	codecs, err := o.list("codecs", MaxCapCodecs, CodecsString)
	if err != nil {
		return "", nil, err
	}
	if RequiresCodecs(codec) && len(codecs) == 0 {
		return "", nil, badPlay("an entry of " + codec + " without codecs")
	}
	return codec, codecs, nil
}

// list reads an array of strings of at most most entries, each of its
// form.
func (o members) list(name string, most int, form func(string) bool) ([]string, error) {
	v, there := o[name]
	if !there {
		return nil, nil
	}
	a, ok := v.([]any)
	if !ok || len(a) > most {
		return nil, badPlay(name + " isn't an array within its bound")
	}
	out := make([]string, 0, len(a))
	for _, e := range a {
		s, ok := e.(string)
		if !ok || !form(s) {
			return nil, badPlay(name + " holds a value not of its form")
		}
		out = append(out, s)
	}
	return out, nil
}

// integers reads an array of at most most integers from lo to hi.
func (o members) integers(name string, most int, lo, hi int64) ([]int, error) {
	v, there := o[name]
	if !there {
		return nil, nil
	}
	a, ok := v.([]any)
	if !ok || len(a) > most {
		return nil, badPlay(name + " isn't an array within its bound")
	}
	out := make([]int, 0, len(a))
	for _, e := range a {
		n, ok := integerOf(e)
		if !ok || n < lo || n > hi {
			return nil, badPlay(name + " holds a number out of its range")
		}
		out = append(out, int(n))
	}
	return out, nil
}

// objects reads an array of at most most objects.
func (o members) objects(name string, most int) ([]members, error) {
	v, there := o[name]
	if !there {
		return nil, nil
	}
	a, ok := v.([]any)
	if !ok || len(a) > most {
		return nil, badPlay(name + " isn't an array within its bound")
	}
	out := make([]members, 0, len(a))
	for _, e := range a {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, badPlay(name + " holds an entry that isn't an object")
		}
		out = append(out, m)
	}
	return out, nil
}

// bounded reads an integer member from lo to hi; 0 when absent.
func (o members) bounded(name string, lo, hi int64) (int, error) {
	v, there := o[name]
	if !there {
		return 0, nil
	}
	n, ok := integerOf(v)
	if !ok || n < lo || n > hi {
		return 0, badPlay(name + " is out of its range")
	}
	return int(n), nil
}
