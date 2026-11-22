package wire

import (
	"errors"
	"time"
)

// The limits a libraries answer sends (MSFP playback section 5, music
// section 5.1): what a caller keeps of them, and how an owning server
// counts a play against them (playback sections 2.2, 6, 7 and 8).

// Limits is the limits object: playbacks of purpose play at once, of
// them transcoded, a bitrate cap (0: none); downloads at once, a day,
// their speed cap (0: none); scans a day and the bytes one scan may read
// (ScanBytes, 0: none sent, the floor); with music, audio playbacks
// apart.
type Limits struct {
	Streams         int `json:"streams"`
	Transcodes      int `json:"transcodes"`
	BitrateKbps     int `json:"bitrateKbps"`
	Downloads       int `json:"downloads"`
	DownloadsPerDay int `json:"downloadsPerDay"`
	DownloadKbps    int `json:"downloadKbps"`
	ScansPerDay     int `json:"scansPerDay"`
	ScanBytes       int `json:"scanBytes,omitempty"`
	AudioStreams    int `json:"audioStreams,omitempty"`
	AudioTranscodes int `json:"audioTranscodes,omitempty"`
}

// The tops of the limits' members (playback section 5).
const (
	MaxStreams         = 1000
	MaxLimitKbps       = 1_000_000
	MaxDownloads       = 1000
	MaxDownloadsPerDay = 10_000
	MaxScansPerDay     = 1_000_000
	// MinScanBytes and MaxScanBytes bound scanBytes: 4 MiB, the floor a
	// caller counts on when it is absent or out of range, to 1 GiB.
	// RecommendedScanBytes is the recommended default for owners.
	MinScanBytes         = 4 << 20
	MaxScanBytes         = 1 << 30
	RecommendedScanBytes = 32 << 20
)

// ErrLimits is a limits object out of its ranges.
var ErrLimits = errors.New("msfp: limits out of range")

// limitTops are the members a caller reads, with their tops.
var limitTops = []struct {
	name    string
	lo, top int64
	of      func(*Limits) *int
}{
	{"streams", 0, MaxStreams, func(l *Limits) *int { return &l.Streams }},
	{"transcodes", 0, MaxStreams, func(l *Limits) *int { return &l.Transcodes }},
	{"bitrateKbps", 0, MaxLimitKbps, func(l *Limits) *int { return &l.BitrateKbps }},
	{"downloads", 0, MaxDownloads, func(l *Limits) *int { return &l.Downloads }},
	{"downloadsPerDay", 0, MaxDownloadsPerDay, func(l *Limits) *int { return &l.DownloadsPerDay }},
	{"downloadKbps", 0, MaxLimitKbps, func(l *Limits) *int { return &l.DownloadKbps }},
	{"scansPerDay", 0, MaxScansPerDay, func(l *Limits) *int { return &l.ScansPerDay }},
	{"scanBytes", MinScanBytes, MaxScanBytes, func(l *Limits) *int { return &l.ScanBytes }},
	{"audioStreams", 0, MaxStreams, func(l *Limits) *int { return &l.AudioStreams }},
	{"audioTranscodes", 0, MaxStreams, func(l *Limits) *int { return &l.AudioTranscodes }},
}

// Check says whether an owning server may send l: every member within
// its range (ScanBytes 0, not sent, or MinScanBytes to MaxScanBytes),
// transcodes at most streams, audioTranscodes at most audioStreams.
func (l Limits) Check() error {
	for _, m := range limitTops {
		v := int64(*m.of(&l))
		if m.name == "scanBytes" && v == 0 {
			continue
		}
		if v < m.lo || v > m.top {
			return ErrLimits
		}
	}
	if l.Transcodes > l.Streams || l.AudioTranscodes > l.AudioStreams {
		return ErrLimits
	}
	return nil
}

// KeptLimits is what a caller keeps of a peer's limits (playback section
// 5). Downloads, DownloadsPerDay, DownloadKbps and ScansPerDay are 0 when
// absent or out of range: no downloads, no scan access; ScanBytes is
// MinScanBytes then. Streams,
// Transcodes, BitrateKbps, AudioStreams and AudioTranscodes say whether
// the peer sent them, in range.
type KeptLimits struct {
	Limits
	HasStreams, HasTranscodes, HasBitrate, HasAudioStreams, HasAudioTranscodes bool
}

// OffersDownloads says whether the caller shows Download for the peer's
// titles: downloads above 0 (playback section 7).
func (k KeptLimits) OffersDownloads() bool { return k.Downloads > 0 }

// OffersScans says whether the caller asks the peer for scans:
// scansPerDay above 0 (playback section 8).
func (k KeptLimits) OffersScans() bool { return k.ScansPerDay > 0 }

// ScanCap is the bytes one scan ticket serves in all (playback section
// 8, media section 4.2): ScanBytes, or MinScanBytes when it is 0 (not
// sent) or out of range. Both sides count on it: the owning server cuts
// its scans there, the viewer's server lets its media server read no
// more in one scan.
func (l Limits) ScanCap() int64 {
	if l.ScanBytes < MinScanBytes || l.ScanBytes > MaxScanBytes {
		return MinScanBytes
	}
	return int64(l.ScanBytes)
}

// ReadLimits reads the limits object of a libraries answer as a caller
// keeps it (playback section 5): false, ignored whole, for one that isn't
// an object or one of whose members isn't an integer (null included); a
// member out of its range is ignored alone; transcodes above streams is
// ignored only when streams is kept, audioTranscodes likewise; unknown
// members are ignored.
func ReadLimits(raw []byte) (KeptLimits, bool) {
	var k KeptLimits
	tree, err := strictTree(raw, true)
	if err != nil {
		return k, false
	}
	o, ok := tree.(map[string]any)
	if !ok {
		return k, false
	}
	has := map[string]bool{}
	for _, m := range limitTops {
		v, ok := o[m.name]
		if !ok {
			continue
		}
		n, ok := integerOf(v)
		if !ok {
			return KeptLimits{}, false
		}
		if n >= m.lo && n <= m.top {
			*m.of(&k.Limits) = int(n)
			has[m.name] = true
		}
	}
	if has["transcodes"] && has["streams"] && k.Transcodes > k.Streams {
		k.Transcodes, has["transcodes"] = 0, false
	}
	if has["audioTranscodes"] && has["audioStreams"] && k.AudioTranscodes > k.AudioStreams {
		k.AudioTranscodes, has["audioTranscodes"] = 0, false
	}
	if !has["scanBytes"] {
		k.ScanBytes = MinScanBytes
	}
	k.HasStreams, k.HasTranscodes, k.HasBitrate = has["streams"], has["transcodes"], has["bitrateKbps"]
	k.HasAudioStreams, k.HasAudioTranscodes = has["audioStreams"], has["audioTranscodes"]
	return k, true
}

// LimitUse is what a pairing (or the pool a play counts against) uses
// now: its playbacks of purpose play, of them transcoded, its downloads,
// the downloads and scans started in the last 24 hours, and how many
// transcodes the server's budget has free.
type LimitUse struct {
	Streams, Transcodes, Downloads int
	DownloadsToday, ScansToday     int
	ServerTranscodesFree           int
}

func streamLimit(why string) error { return &PlayRefusal{409, ProblemStreamLimit, why} }

// Open is step 2 of playback section 2.2: a 409 stream-limit-reached when
// the limit of the purpose is 0 (streams for play, downloads for
// download, scansPerDay for scan).
func (l Limits) Open(purpose string) error {
	switch {
	case (purpose == PurposePlay || purpose == "") && l.Streams == 0:
		return streamLimit("streams is 0")
	case purpose == PurposeDownload && l.Downloads == 0:
		return streamLimit("downloads is 0")
	case purpose == PurposeScan && l.ScansPerDay == 0:
		return streamLimit("scansPerDay is 0")
	}
	return nil
}

// Room is step 5 of playback section 2.2: a 409 stream-limit-reached when
// the limits of the purpose are used: for play, the streams all in use;
// for download, the downloads all in use or downloadsPerDay reached (0:
// no daily cap); for scan, scansPerDay reached. A download or a scan
// never counts against streams.
func (l Limits) Room(purpose string, u LimitUse) error {
	switch purpose {
	case PurposeDownload:
		if u.Downloads >= l.Downloads {
			return streamLimit("the downloads are all in use")
		}
		if l.DownloadsPerDay > 0 && u.DownloadsToday >= l.DownloadsPerDay {
			return streamLimit("downloadsPerDay reached")
		}
	case PurposeScan:
		if u.ScansToday >= l.ScansPerDay {
			return streamLimit("scansPerDay reached")
		}
	default:
		if u.Streams >= l.Streams {
			return streamLimit("the streams are all in use")
		}
	}
	return nil
}

// TranscodeRoom is the transcode a playback that converts needs (playback
// sections 2.6 and 7): for a play, one of the pairing's transcodes and
// one of the server's budget; for a download, one of the server's budget
// only. A 503 server-busy without.
func (l Limits) TranscodeRoom(purpose string, u LimitUse) error {
	if (purpose == PurposePlay || purpose == "") && u.Transcodes >= l.Transcodes {
		return &PlayRefusal{503, ProblemServerBusy, "the pairing's transcodes are all in use, or 0"}
	}
	if u.ServerTranscodesFree <= 0 {
		return &PlayRefusal{503, ProblemServerBusy, "the server's transcode budget is used"}
	}
	return nil
}

// The lives of playbacks (playback sections 6.3, 7 and 8).
const (
	// IdleEnd ends a play or a scan without a progress or a media
	// request for that long; never a download.
	IdleEnd = 2 * time.Minute
	// DownloadLife and ScanLife end a download and a scan after their
	// play was answered.
	DownloadLife = 24 * time.Hour
	ScanLife     = 10 * time.Minute
	// LateReports: a download's progress and stop with at are recorded
	// for that long after its play, even once it has ended.
	LateReports = 30 * 24 * time.Hour
	// ScanReadsAtOnce is how many reads of one scan are served at once.
	ScanReadsAtOnce = 2
	// DownloadPace is the span over which a download's speed is
	// measured against downloadKbps.
	DownloadPace = 10 * time.Second
)

// Idles says whether a playback of purpose ends after IdleEnd unused: a
// play's and a scan's, never a download's.
func Idles(purpose string) bool { return purpose != PurposeDownload }

// Life is how long a playback of purpose lasts at most from its answer,
// as MSFP sets it: DownloadLife, ScanLife, or 0 for a play (the owning
// server's own longest playback, if it has one).
func Life(purpose string) time.Duration {
	switch purpose {
	case PurposeDownload:
		return DownloadLife
	case PurposeScan:
		return ScanLife
	}
	return 0
}
