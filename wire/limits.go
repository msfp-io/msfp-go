package wire

import (
	"errors"
	"time"
)

// The limits a libraries answer sends (MSFP playback section 5, MSFP Media music
// 5.1): what a caller keeps of them, and how an owning server
// counts a play against them (MSFP Media playing 2.2 and 6, MSFP playback sections 6 and 8).

// Limits is the limits object: playbacks of purpose play at once, of
// them transcoded, a bitrate cap (0: none); downloads at once, a day,
// their speed cap (0: none); scans a day; with music, audio playbacks
// apart. The bytes one scan may read are the scan answer's (ScanCap),
// not a limit.
type Limits struct {
	Streams         int `json:"streams"`
	Transcodes      int `json:"transcodes"`
	BitrateKbps     int `json:"bitrateKbps"`
	Downloads       int `json:"downloads"`
	DownloadsPerDay int `json:"downloadsPerDay"`
	DownloadKbps    int `json:"downloadKbps"`
	ScansPerDay     int `json:"scansPerDay"`
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
	// MinScanBytes and MaxScanBytes bound a scan answer's scanBytes
	// (playback section 8): 4 MiB, the floor a caller counts on when it
	// is absent or out of range, to 1 GiB. RecommendedScanBytes is the
	// implementers' notes' suggestion for owners.
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
	{"audioStreams", 0, MaxStreams, func(l *Limits) *int { return &l.AudioStreams }},
	{"audioTranscodes", 0, MaxStreams, func(l *Limits) *int { return &l.AudioTranscodes }},
}

// Check says whether an owning server may send l: every member within
// its range, transcodes at most streams, audioTranscodes at most audioStreams.
func (l Limits) Check() error {
	for _, m := range limitTops {
		v := int64(*m.of(&l))
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
// absent or out of range: no downloads, no scan access. Streams,
// Transcodes, BitrateKbps, AudioStreams and AudioTranscodes say whether
// the peer sent them, in range.
type KeptLimits struct {
	Limits
	HasStreams, HasTranscodes, HasBitrate, HasAudioStreams, HasAudioTranscodes bool
}

// OffersDownloads says whether the caller shows Download for the peer's
// titles: downloads above 0 (MSFP Media playing 6).
func (k KeptLimits) OffersDownloads() bool { return k.Downloads > 0 }

// OffersScans says whether the caller asks the peer for scans:
// scansPerDay above 0 (playback section 8).
func (k KeptLimits) OffersScans() bool { return k.ScansPerDay > 0 }

// ScanCap is the bytes one scan ticket serves in all (playback section
// 8, media section 4.2) for the scanBytes a scan's answer states:
// itself, or MinScanBytes when it is 0 (none) or out of range. Both
// sides count on it: the owning server cuts its scans there, the
// viewer's server lets its media server read no more in one scan.
func ScanCap(scanBytes int64) int64 {
	if scanBytes < MinScanBytes || scanBytes > MaxScanBytes {
		return MinScanBytes
	}
	return scanBytes
}

// ReadLimits reads the limits object of a libraries answer as a caller
// keeps it (playback section 5): false, ignored whole, for one that isn't
// an object or one of whose members isn't an integer (null included); a
// member out of its range is ignored alone; transcodes above streams is
// ignored only when streams is kept, audioTranscodes likewise; unknown
// members are ignored, scanBytes of earlier drafts among them.
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

// Without is the use with a live playback of purpose counted as ended:
// the one a play replaces (MSFP Media playing 2.2, step 8; MSFP playback
// section 6). A play frees its stream, and when it holds one
// (transcode) its transcode, the pairing's and the server's; a download
// frees a download, never the day's count; a scan frees nothing.
func (u LimitUse) Without(purpose string, transcode bool) LimitUse {
	switch purpose {
	case PurposePlay, "":
		u.Streams = max(u.Streams-1, 0)
		if transcode {
			u.Transcodes = max(u.Transcodes-1, 0)
			u.ServerTranscodesFree++
		}
	case PurposeDownload:
		u.Downloads = max(u.Downloads-1, 0)
		if transcode {
			u.ServerTranscodesFree++
		}
	}
	return u
}

func streamLimit(why string) error { return &PlayRefusal{409, ProblemStreamLimit, why} }

// Open is step 2 of MSFP Media playing 2.2: a 409 stream-limit-reached when
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

// Room is step 5 of MSFP Media playing 2.2: a 409 stream-limit-reached when
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

// TranscodeRoom is the transcode a playback that converts needs (MSFP Media playing
// 2.6 and 6): for a play, one of the pairing's transcodes and
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

// The interval of progress (MSFP Media playing 3): the source's, given
// by play's answer in progressSeconds, 45 to 120 seconds, 60 when absent.
const (
	MinProgressSeconds     = 45
	MaxProgressSeconds     = 120
	DefaultProgressSeconds = 60
)

// ProgressInterval is the interval of a playback whose answer gave
// progressSeconds: that many seconds, DefaultProgressSeconds when it is
// out of 45 to 120 (or 0, absent).
func ProgressInterval(progressSeconds int) time.Duration {
	if progressSeconds < MinProgressSeconds || progressSeconds > MaxProgressSeconds {
		progressSeconds = DefaultProgressSeconds
	}
	return time.Duration(progressSeconds) * time.Second
}

// IdleLimit is the idle limit of a playback whose answer gave
// progressSeconds (MSFP Media playing 3 and 5): twice its interval. A
// play or a scan with neither a progress nor a media request for that
// long ends; never a download.
func IdleLimit(progressSeconds int) time.Duration { return 2 * ProgressInterval(progressSeconds) }

// The lives of playbacks (MSFP Media playing 5 and 6, MSFP playback
// section 8).
const (
	// IdleEnd is the idle limit at the default interval, 2 minutes
	// (IdleLimit(DefaultProgressSeconds)).
	IdleEnd = 2 * DefaultProgressSeconds * time.Second
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

// Idles says whether a playback of purpose ends after its idle limit
// (IdleLimit) unused: a play's and a scan's, never a download's.
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
