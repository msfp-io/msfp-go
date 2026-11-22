package msfp

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// The data objects of the host interfaces (engine.md section 7).

// Library is one of the host's libraries: its own ID and its name. A
// library carries no kind (MSFP catalog section 2): its titles' keys say
// what it holds. Offered is set in the answer of Offers.
type Library struct {
	ID      string
	Name    string
	Offered bool
}

// WantedLibrary is one of a peer's libraries: its public ID, its name,
// whether this server wants it, and whether it was gone from the peer's
// latest answer. What it holds is told by its titles' keys: a host types
// an empty library by its first titles.
type WantedLibrary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Wanted bool   `json:"wanted,omitempty"`
	Gone   bool   `json:"gone,omitempty"`
}

// Title is a title of a content part: a *VideoTitle or a *MusicTitle.
type Title interface {
	// Common gives the members every title has.
	Common() *TitleCommon
}

// TitleCommon are the members of every title: its key, its kind, and the
// libraries that hold it (the host's IDs from Catalog; for a peer's
// title, the one public ID it was listed in).
type TitleCommon struct {
	Key       string   `json:"key"`
	Kind      string   `json:"kind"`
	Libraries []string `json:"libraries"`
}

// VideoTitle is a film, a series, a season, an episode or an extra (MSFP
// video section 3): the host's own, as its Catalog gives them, or a
// peer's, as the engine kept it (video section 3.1). The engine cleans
// the host's before they leave as a receiver would keep them, so that
// nothing leaves that MSFP doesn't take.
type VideoTitle struct {
	TitleCommon
	// Copies are a film's, an episode's or an extra's copies, in the host's order of preference
	// (MSFP video sections 3.2 to 3.8): what a viewer's server needs to
	// list each as a file of its own before anyone plays it (its length,
	// size, etag, media type, streams and tracks, chapters, skip markers,
	// the layout of its seek-bar previews…). Required for those kinds:
	// one without a copy left once cleaned isn't sent; series and seasons
	// carry none.
	//
	// Of the host's own titles, each copy's ID is the host's handle of it,
	// the one Playback and Media are given: it never leaves; the engine
	// sends instead the version ID play gives for it, and plays a copy
	// only as it is described here. The host's order of its copies is its
	// preference, best first (video section 3.7): the first is the one a
	// play naming none plays. Language tags leave as wire.SentTag gives
	// them. Facts are in outside vocabularies or the MSFP registry (video
	// section 3.5): the file's media type (read from its first bytes
	// where it can), the registry's codec and subtitle tokens (ffprobe's
	// codec_name), RFC 6381 codecs strings (required for h264, hevc, av1,
	// vp9, aac and dts), H.273 colour code points; a host whose media
	// server names them otherwise maps them; the engine maps nothing, and
	// sends any well-formed value. A copy carries no name and its tracks
	// no title. Track IDs are a<n> and s<n>, n the stream's index in the
	// file. Its ETag changes whenever the copy's bytes change, and only
	// then: a change of a copy (its etag, its previewLayout) is a change
	// of the title (a changes reading sends it again). Of a peer's titles,
	// ID is the peer's version ID.
	Copies []wire.Copy `json:"copies,omitempty"`
	// Place is, for an episode keyed by its TMDB episode ID
	// (series/tmdb/<id>/e<episode id>), its place in TMDB's numbering:
	// TMDB's current season and episode numbers (MSFP video section 4.1).
	// The host fills it for each of its own such episodes; one it can't
	// place (nil, or numbers out of range) isn't sent. A new place is a
	// change of the title: a changes reading sends it again. A peer's is
	// always set for such episodes (the engine skips one without), nil
	// for every other title (place on them is ignored).
	Place *Place            `json:"place,omitempty"`
	With  []string          `json:"with,omitempty"`
	IDs   map[string]string `json:"ids,omitempty"`
	// ExtraType is an extra's (kind "extra", keyed under its film, series
	// or season: video section 3.9): open, a token (trailer, featurette,
	// behindthescenes, deletedscene, interview, scene, short, clip,
	// themesong, themevideo…); an extra without one isn't sent. An extra
	// carries no name: a viewer's server shows it by its type and a
	// number. Theme songs and videos are extras like the others: whether
	// the host offers them is its operator's choice; the engine carries
	// what the host gives.
	ExtraType string `json:"extraType,omitempty"`
	// Name is a local film's or series' name (required, at most 128:
	// catalog section 4.3, video section 3.10). It is text, never markup;
	// on other titles it isn't sent.
	Name string `json:"name,omitempty"`
	// Year and Image are a local film's or series' description: its year
	// (1800 to 9,999; 0 for none), and whether the owner shares one image
	// of it (the host's Images gives it; a peer's is fetched with
	// TitleImage). Ignored on other titles.
	Year  int  `json:"year,omitempty"`
	Image bool `json:"image,omitempty"`
}

// Place is an episode's place in TMDB's numbering (MSFP video section
// 4.1): Season from 0 (specials) to 9,999, Episode from 0 to 99,999, as
// TMDB numbers them in the series' own season list (not an episode
// group). A host whose files are numbered another way maps them to TMDB's.
type Place struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

// BestCopy is the title's first copy, the owning server's preferred
// (MSFP video section 3.7); false for a title without one (a series, a
// season). A viewer's server may show its picture as the title's; which
// copy it plays is its own choice, from every copy's facts.
func (t *VideoTitle) BestCopy() (wire.Copy, bool) {
	if len(t.Copies) == 0 {
		return wire.Copy{}, false
	}
	return t.Copies[0], true
}

// AudioLanguages are the languages of the title's audio tracks, over all
// its copies, each once, in the order of its copies and their tracks:
// what a viewer's server shows as the title's audio languages (MSFP
// video section 3.2: a title carries no summary of them).
func (t *VideoTitle) AudioLanguages() []string {
	var out []string
	for _, c := range t.Copies {
		for _, a := range c.AudioTracks {
			if a.Language != "" && !slices.Contains(out, a.Language) {
				out = append(out, a.Language)
			}
		}
	}
	return out
}

// SubtitleLanguages are the languages of the title's subtitle tracks,
// over all its copies, each once, in the order of its copies and their
// tracks.
func (t *VideoTitle) SubtitleLanguages() []string {
	var out []string
	for _, c := range t.Copies {
		for _, s := range c.SubtitleTracks {
			if s.Language != "" && !slices.Contains(out, s.Language) {
				out = append(out, s.Language)
			}
		}
	}
	return out
}

// MusicTitle is an artist, an album or a track, with its description
// (MSFP music section 3): its name; for albums and tracks, the artist
// keys credited and their names as written; for albums, the name to sort
// by, MusicBrainz's primary and secondary types of its release group
// (lowercased, open), whether it is a compilation; for albums and
// tracks, the dates of this edition and of the first, and genres; for
// tracks, the album key, the disc and number on it, and its copies; for
// local artists and albums, whether the owner shares an image; other
// identifiers by scheme, on mb titles only (mbreleasegroup on albums,
// mbrecording on tracks). There are no sort names. A track's artists,
// artistNames, dates and genres leave only when they differ from its
// album's (MSFP music section 3): of a peer's, an absent one is its
// album's. A peer's are cleaned as music section 3.2 says; the host's
// own are cleaned the same way before they leave.
type MusicTitle struct {
	TitleCommon
	Name           string   `json:"name"`
	Artists        []string `json:"artists,omitempty"`
	ArtistNames    string   `json:"artistNames,omitempty"`
	PrimaryType    string   `json:"primaryType,omitempty"`
	SecondaryTypes []string `json:"secondaryTypes,omitempty"`
	Compilation    *bool    `json:"compilation,omitempty"`
	Date           string   `json:"date,omitempty"`
	OriginalDate   string   `json:"originalDate,omitempty"`
	Genres         []string `json:"genres,omitempty"`
	Album          string   `json:"album,omitempty"`
	Disc           int      `json:"disc,omitempty"`
	Number         int      `json:"number,omitempty"`
	// Copies are a track's copies, in the owner's order of preference, at least one (music
	// section 3.3). Of the host's own, each ID is its handle, as for
	// VideoTitle.Copies: the engine sends the version ID made of it.
	Copies []wire.MusicCopy `json:"copies,omitempty"`
	// Image: a local artist or album whose image the owner shares (the
	// host's Images gives it); ignored on any other title.
	Image bool              `json:"image,omitempty"`
	IDs   map[string]string `json:"ids,omitempty"`
}

// Common gives the members every title has.
func (t *VideoTitle) Common() *TitleCommon { return &t.TitleCommon }

// Common gives the members every title has.
func (t *MusicTitle) Common() *TitleCommon { return &t.TitleCommon }

// titleJSON is how a stored title is written: one member naming its
// content part.
type titleJSON struct {
	Video *VideoTitle `json:"video,omitempty"`
	Music *MusicTitle `json:"music,omitempty"`
}

// MarshalTitle writes a title as JSON, for a store: {"video":{…}} or
// {"music":{…}}.
func MarshalTitle(t Title) ([]byte, error) {
	switch t := t.(type) {
	case *VideoTitle:
		return json.Marshal(titleJSON{Video: t})
	case *MusicTitle:
		return json.Marshal(titleJSON{Music: t})
	}
	return nil, errors.New("msfp: not a title")
}

// UnmarshalTitle reads what MarshalTitle wrote.
func UnmarshalTitle(b []byte) (Title, error) {
	var j titleJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	switch {
	case j.Video != nil:
		return j.Video, nil
	case j.Music != nil:
		return j.Music, nil
	}
	return nil, errors.New("msfp: not a title")
}

// TitlePage is a page of the host's titles: more says whether titles
// remain after the last.
type TitlePage struct {
	Titles []Title
	More   bool
}

// ChangePage is a page of what changed in the host's catalog: titles new
// or changed, keys gone, the page's last key, and whether more remain.
type ChangePage struct {
	Present []Title
	Gone    []string
	Last    string
	More    bool
}

// CopyInfo is what Playback.Describe tells of a copy, besides what its
// description in the catalog says (its length, parts, tracks, the
// layout of its previews and the rest: the engine takes those from the
// title's Copies).
type CopyInfo struct {
	// Fonts is how many fonts the copy's ASS tracks use (0 to 64), served
	// as MediaFont 0 to Fonts-1 (MSFP video section 6.6): each at most 10
	// MiB, 64 MiB in all; the host lists none past those bounds.
	Fonts int
	// NotApart lists the subtitle tracks the host can't serve apart (as
	// WebVTT for text, as PGS for pictures: MSFP video section 5.4): they
	// get no entry in the answer's subtitles. Every other track of a
	// format video section 5.4's table lists is served apart, in the
	// table's form: a host must serve them so (an external track it
	// can't serve apart it doesn't list in the catalog). A track of a
	// format the table doesn't list is served apart only when the host
	// says how, in Forms.
	NotApart []string
	// Forms are, by track ID, how the host serves apart a track of a
	// format video section 5.4's table doesn't list ("text", or
	// "picture"); ignored for a listed format.
	Forms map[string]string
}

// TranscodeTargets are the targets of a play with delivery "transcode"
// (MSFP playback section 2.7, video section 5.1): codecs as registry
// tokens, or "copy" for a stream served unchanged, and the range (sdr,
// keep); toward the host, those left out already hold the baseline's
// values (h264, aac, 2 channels, sdr; maxHeight 0, the copy's own
// height), and a copied stream, or a copy with no video, carries none
// of the targets that would change it.
type TranscodeTargets = wire.TranscodeTargets

// Served is what the engine has decided a play serves of the copy
// (wire.Serve): the method, and of the video and the audio "copy" for a
// stream left unchanged or the codec it becomes; for a transcode, its
// targets; for a remux, its segment format (mp2t or mp4).
type Served = wire.Served

// Encoders are the codecs a server's Playback converts to (Config.Encoders).
type Encoders = wire.Encoders

// The purposes of a playback (MSFP playback section 2): to watch it, to
// download it, to let a media server read the start of its file.
const (
	PurposePlay     = wire.PurposePlay
	PurposeDownload = wire.PurposeDownload
	PurposeScan     = wire.PurposeScan
)

// The deliveries a play may ask (MSFP playback section 2.7); "" is the
// baseline every viewer's server plays: H.264 and AAC in complete HLS
// (AAC alone for a track).
const (
	DeliveryFile      = wire.DeliveryFile
	DeliveryRemux     = wire.DeliveryRemux
	DeliveryTranscode = wire.DeliveryTranscode
)

// PeerRef names a peer for the host's live view: its ID and name, never
// a person.
type PeerRef struct {
	ID   string
	Name string
}

// StartRequest is what the engine asks Playback.Start to serve.
type StartRequest struct {
	// Copy is the handle of the copy chosen (its ID in the title's
	// Copies); Title the key played (an episode's, for a copy holding
	// several); AudioOnly, that it is a track (MSFP music section 5): no
	// video is served.
	Copy      string
	Title     string
	AudioOnly bool
	// Purpose is PurposePlay, PurposeDownload or PurposeScan (MSFP
	// playback sections 7 and 8): a scan is always of delivery file, and
	// converts nothing.
	Purpose string
	// Delivery is what the viewer's server asked: DeliveryFile (the copy
	// as it is: method direct, protocol file), DeliveryRemux (into
	// MediaType's segment format, as hls: video/mp2t or audio/mp2t for
	// MPEG-TS segments, video/mp4 or audio/mp4 for fragmented MP4),
	// DeliveryTranscode (to Transcode's targets, as hls), or "" for the
	// baseline: H.264 at most High 4.1, 8-bit 4:2:0, progressive, SDR in
	// BT.709, and AAC-LC of at most 2 channels and 48 kHz, in complete
	// HLS (AAC alone for a copy with no video), each stream already
	// within it left unchanged (Served says which), never the file. With
	// a delivery, the host serves exactly that or refuses with
	// ErrDeliveryRefused (its bandwidth, its policy, a delivery it can't
	// make of the copy); it never serves another (MSFP playback section
	// 2.2, step 7). The engine has already refused a delivery not of the
	// list, a remux to another media type, or to a segment format that
	// can't carry the copy's streams (Config.Encoders.Segments), a
	// transcode codec or range not in Config.Encoders, a copied stream no
	// segment format carries, burnSubtitle with video copy, file of a
	// copy of several parts, file or remux of a play whose overall rate
	// is above MaxBitrateKbps, and a BurnSubtitle the copy hasn't.
	//
	// The host's duties toward what it serves (MSFP playback section 2.7,
	// media section 3.1): in fragmented MP4 segments, HEVC written hvc1,
	// Dolby Vision dvh1 (dav1 on AV1), parameter sets out of band, in a
	// remux and a transcode alike; each #EXT-X-STREAM-INF of its master
	// playlist with BANDWIDTH, CODECS and, for video, RESOLUTION;
	// VIDEO-RANGE (PQ or HLG) when the picture served is PQ or HLG;
	// SUPPLEMENTAL-CODECS when Dolby Vision is kept; CHANNELS "16/JOC"
	// allowed on an E-AC-3 rendition whose objects is dolby-atmos. The
	// engine fills those it knows when the host leaves them out
	// (masterAttributes), and logs what stays missing.
	Delivery  string
	MediaType string
	Transcode *TranscodeTargets
	// Served is what the engine decided the play serves of the copy.
	Served Served
	// AudioRenditions asks, for hls, every audio track of the copy as an
	// #EXT-X-MEDIA rendition named by its ID (MSFP media section 3.1). It
	// is never refused: a host that makes none says so in
	// Started.AudioRenditions.
	AudioRenditions bool
	// Parts is the copy's files, as its description says (1 for one
	// file): a copy of several is started as "hls", never "file".
	Parts int
	Peer  PeerRef
	// AudioTrack is the audio track played: the one the request named,
	// else the file's default (the track it marks default, else its
	// first). Which subtitles a viewer sees is the viewer's server's
	// choice: they never make a playback transcode, but for
	// Transcode.BurnSubtitle.
	AudioTrack string
	// StartMs is where to start, in the copy; 0 for a download or a scan.
	StartMs int64
	// MaxBitrateKbps is the cap in effect for a play, 0 for none. A
	// download's speed is the engine's to pace (downloadKbps).
	MaxBitrateKbps int
	// Queue are the tracks a music play names after Title, that the host
	// prepares under the same playback (MSFP music section 5.2), each
	// with the copy its entry named (else its first) and its own
	// delivery, in the request's order; Place is its place in the
	// request's queue (0 to 4), which a queued track the engine left out
	// (not offered, no such copy, a delivery refused) keeps empty.
	Queue []QueuedCopy
	// Admit counts the pool's streams (and, for "transcode", its
	// transcodes), or the download, or the scan, in one step: the host
	// calls it once it knows the method, before starting any work (MSFP
	// playback section 6.2). Errors: ErrStreamLimitReached, ErrServerBusy.
	Admit func(method string) (Slot, error)
}

// QueuedCopy is a track of a play's queue: its place in the request's
// queue, its key, the handle of its copy, and what it is served as: its
// entry's own delivery, else the request's, with its media type or
// targets (as StartRequest's), and what the engine decided it serves.
type QueuedCopy struct {
	Place     int
	Title     string
	Copy      string
	Delivery  string
	MediaType string
	Transcode *TranscodeTargets
	Served    Served
}

// Slot is a stream counted by Admit.
type Slot interface {
	// Release frees what was counted, when the host's start fails.
	Release()
	// Transcoding moves a remux that turned out to re-encode the video
	// into the transcode count; false when none is left.
	Transcoding() bool
}

// Started is what Playback.Start answers.
type Started struct {
	// Handle is the host's for this playback (not the copy's): Progress,
	// Stop and Media.Serve are given it.
	Handle   string
	Method   string // "direct", "remux", "transcode", as admitted
	Protocol string // "hls" or "file"
	// PositionMs is where it starts, in the copy.
	PositionMs int64
	// AudioRenditions says the hls playlist carries every audio track as
	// a rendition, as StartRequest.AudioRenditions asked; false when the
	// host made none (play's answer says so: MSFP playback section 2.3).
	AudioRenditions bool
	// Queue are the queued tracks the host prepared, of those
	// StartRequest.Queue named (by Place); one it can't play is left
	// out. Their media are served under the same Handle, with
	// MediaPath.Entry their Place + 1.
	Queue []StartedEntry
}

// StartedEntry is a queued track the host prepared: its place in the
// request's queue, and how it is served.
type StartedEntry struct {
	Place    int
	Method   string
	Protocol string
}

// Report is a peer's progress or stop of a playback here, passed on to
// the host (MSFP playback sections 3, 4 and 7).
type Report struct {
	// Handle is the playback's (Started.Handle); Title the track playing
	// of a music queue, else the title played.
	Handle string
	Title  string
	// PositionMs, when HasPosition.
	PositionMs  int64
	HasPosition bool
	// At is when the viewer was there: the peer's at, or now.
	At time.Time
	// Stop says it is the stop's own report; Late, that it is a
	// download's late report, after its playback ended (the viewer
	// watched offline): the handle no longer plays.
	Stop, Late bool
}

// EndReason is why a playback ended (MSFP playback section 6.3).
type EndReason string

// The reasons a playback ends.
const (
	EndStopped      EndReason = "stopped"
	EndExpired      EndReason = "expired"
	EndTooLong      EndReason = "tooLong"
	EndPairingEnded EndReason = "pairingEnded"
	EndNotOffered   EndReason = "notOffered"
	EndOperator     EndReason = "operator"
)

// MediaPath is what a media request asks for: Master, Playlist, Segment,
// File, Subtitles, ASS, Pictures, Font or Sheet. Names are already
// checked against ^[A-Za-z0-9._-]{1,64}$, never "." or "..".
type MediaPath struct {
	Kind  MediaKind
	Name  string // Playlist, Segment
	Track string // Subtitles, ASS, Pictures
	Part  int    // Subtitles, ASS, Pictures: the stretch, unless Whole
	// Whole asks a whole text track (Subtitles, ASS), not a stretch.
	Whole bool
	Sheet int // Sheet
	Font  int // Font, 0 to 63
	// Entry is 0 for the playback's own stream, n+1 for the stream of
	// queue entry n (Master, Playlist, Segment, File).
	Entry int
	// OfCopy says the handle Media.Serve is given is a copy's, not a
	// playback's: a Sheet under a previews ticket (MSFP video section
	// 5.6), which no playback has.
	OfCopy bool
}

// MediaKind is the kind of a MediaPath.
type MediaKind string

// The kinds of MediaPath.
const (
	MediaMaster    MediaKind = "master"
	MediaPlaylist  MediaKind = "playlist"
	MediaSegment   MediaKind = "segment"
	MediaFile      MediaKind = "file"
	MediaSubtitles MediaKind = "subtitles"
	MediaASS       MediaKind = "ass"
	MediaPictures  MediaKind = "pictures"
	MediaFont      MediaKind = "font"
	MediaSheet     MediaKind = "sheet"
)

// Range is one byte range, parsed: First to Last included; Last -1 for
// "to the end"; Suffix for "the last Suffix bytes" (First and Last
// unused). For the file, the engine resolves it against the copy's size
// before Media.Serve: First to Last, both within the file.
type Range struct {
	First, Last, Suffix int64
}

// Limits are what a pairing may use of a server (MSFP playback section
// 5): streams at once (0 to 1,000), transcodes among them, a bitrate cap
// (0: none); downloads at once (0 to 1,000; 0: none), downloads started
// in any 24 hours (0 to 10,000; 0: no cap besides Downloads), each
// download's speed cap in kbit/s (0 to 1,000,000; 0: none), scans
// started in any 24 hours (0 to 1,000,000; 0: no scan access), the
// bytes one scan may read (ScanBytes: the pool's own scan cap, 4 MiB to
// 1 GiB, 0 for the floor, 4 MiB; wire.RecommendedScanBytes, 32 MiB, is
// the implementers' notes' suggestion), which no limits object carries:
// each scan's answer states it (MSFP playback section 8). With
// Audio, audio playbacks (tracks) are counted apart, within AudioStreams
// and AudioTranscodes (music section 5.1); without, they count in
// Streams and Transcodes.
type Limits struct {
	Streams         int  `json:"streams"`
	Transcodes      int  `json:"transcodes"`
	BitrateKbps     int  `json:"bitrateKbps,omitempty"`
	Downloads       int  `json:"downloads,omitempty"`
	DownloadsPerDay int  `json:"downloadsPerDay,omitempty"`
	DownloadKbps    int  `json:"downloadKbps,omitempty"`
	ScansPerDay     int  `json:"scansPerDay,omitempty"`
	ScanBytes       int  `json:"scanBytes,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	AudioStreams    int  `json:"audioStreams,omitempty"`
	AudioTranscodes int  `json:"audioTranscodes,omitempty"`
}

// Check returns ErrInvalidLimits when l is out of range.
func (l Limits) Check() error {
	if l.Streams < 0 || l.Streams > wire.MaxStreams || l.Transcodes < 0 || l.Transcodes > l.Streams ||
		l.BitrateKbps < 0 || l.BitrateKbps > wire.MaxLimitKbps ||
		l.Downloads < 0 || l.Downloads > wire.MaxDownloads || l.DownloadsPerDay < 0 || l.DownloadsPerDay > wire.MaxDownloadsPerDay ||
		l.DownloadKbps < 0 || l.DownloadKbps > wire.MaxLimitKbps || l.ScansPerDay < 0 || l.ScansPerDay > wire.MaxScansPerDay ||
		l.ScanBytes != 0 && (l.ScanBytes < wire.MinScanBytes || l.ScanBytes > wire.MaxScanBytes) ||
		l.AudioStreams < 0 || l.AudioStreams > wire.MaxStreams || l.AudioTranscodes < 0 || l.AudioTranscodes > l.AudioStreams {
		return ErrInvalidLimits
	}
	return nil
}

// ownChangeJSON is how a stored OwnChange is written.
type ownChangeJSON struct {
	Seq   int64           `json:"seq"`
	Key   string          `json:"key"`
	Title json.RawMessage `json:"title,omitempty"`
	At    time.Time       `json:"at"`
}

// MarshalJSON writes the change, its title as MarshalTitle does.
func (c OwnChange) MarshalJSON() ([]byte, error) {
	j := ownChangeJSON{Seq: c.Seq, Key: c.Key, At: c.At}
	if c.Title != nil {
		b, err := MarshalTitle(c.Title)
		if err != nil {
			return nil, err
		}
		j.Title = b
	}
	return json.Marshal(j)
}

// UnmarshalJSON reads what MarshalJSON wrote.
func (c *OwnChange) UnmarshalJSON(b []byte) error {
	var j ownChangeJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*c = OwnChange{Seq: j.Seq, Key: j.Key, At: j.At}
	if len(j.Title) > 0 {
		t, err := UnmarshalTitle(j.Title)
		if err != nil {
			return err
		}
		c.Title = t
	}
	return nil
}
