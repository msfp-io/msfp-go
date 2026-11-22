package msfp

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// The data objects of the host interfaces (engine.md section 7).

// Library is one of the host's libraries: its own ID, its name and its
// kind ("movies", "tvshows", "music", "mixed"…, MSFP catalog section 2).
// Offered is set in the answer of Offers.
type Library struct {
	ID      string
	Name    string
	Kind    string
	Offered bool
}

// WantedLibrary is one of a peer's libraries: its public ID, its name
// and kind, whether this server wants it, and whether it was gone from
// the peer's latest answer.
type WantedLibrary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
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
	// Copies are a film's, an episode's or an extra's copies, best first
	// (MSFP video sections 3.2 to 3.8): what a viewer's server needs to
	// list each as a file of its own before anyone plays it (its length,
	// size, etag, container, streams and tracks, chapters, skip
	// markers…). Required for those kinds: one without a copy left once
	// cleaned isn't sent; series and seasons carry none.
	//
	// Of the host's own titles, each copy's ID is the host's handle of it,
	// the one Playback and Media are given: it never leaves; the engine
	// sends instead the version ID play gives for it, and plays a copy
	// only as it is described here. The engine
	// sends the copies in the order of video section 3.7 (wire.SortCopies,
	// keeping the host's order between equal ones), language tags as
	// wire.SentTag gives them, names and track titles only when they are
	// labels (never a file name or a release group: video section 3.6).
	// Its ETag changes whenever the copy's bytes change, and only then: a
	// change of a copy is a change of the title (a changes reading sends it
	// again). Of a peer's titles, ID is the peer's version ID.
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
	// or season: video section 3.9): one of wire.ExtraTypes, "clip" for
	// another. Theme songs and theme videos are never extras.
	ExtraType string `json:"extraType,omitempty"`
	// Name is an extra's own short name (optional, at most 64
	// characters), or a local film's or series' name (required, at most
	// 128: catalog section 4.3, video section 3.10). It is the owner's
	// words, never a file name, text never markup; on other titles it
	// isn't sent.
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

// BestCopy is the title's best copy: the first of its copies, which are
// best first (MSFP video section 3.7); false for a title without one (a
// series, a season). A viewer's server shows its picture as the title's.
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
// by, the release type, whether it is a compilation, the dates of this
// edition and of the first, and genres; for tracks, the album key, the
// disc and number on it, and its copies; for local artists and albums,
// whether the owner shares an image; other identifiers by scheme
// (mbreleasegroup, mbrecording, isrc, acoustid). A peer's are cleaned as
// music section 3.2 says; the host's own are cleaned the same way before
// they leave.
type MusicTitle struct {
	TitleCommon
	Name         string   `json:"name"`
	SortName     string   `json:"sortName,omitempty"`
	Artists      []string `json:"artists,omitempty"`
	ArtistNames  string   `json:"artistNames,omitempty"`
	ReleaseType  string   `json:"releaseType,omitempty"`
	Compilation  *bool    `json:"compilation,omitempty"`
	Date         string   `json:"date,omitempty"`
	OriginalDate string   `json:"originalDate,omitempty"`
	Genres       []string `json:"genres,omitempty"`
	Album        string   `json:"album,omitempty"`
	Disc         int      `json:"disc,omitempty"`
	Number       int      `json:"number,omitempty"`
	// Copies are a track's copies, best first, at least one (music
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
// description in the catalog says (its length, parts, tracks and the
// rest: the engine takes those from the title's Copies).
type CopyInfo struct {
	// Trickplay is the layout of the copy's previews, for a copy whose
	// catalog description says it has some (Previews); nil for none.
	Trickplay *Trickplay
	// Fonts is how many fonts the copy's ASS tracks use (0 to 8), served
	// as MediaFont 0 to Fonts-1 (MSFP video section 6.6).
	Fonts int
	// NotApart lists the subtitle tracks the host can't serve apart (as
	// WebVTT for text, as PGS for pictures: MSFP video section 5.4): they
	// get no URL; every other track of a format MSFP serves apart does.
	NotApart []string
}

// Trickplay is a copy's seek-bar previews (MSFP video section 5.5): the
// tiles' size, the grid of a sheet, the time between tiles, the tiles in
// all, and (in a RemotePlayback) the sheets listed.
type Trickplay struct {
	TileWidth, TileHeight int
	Columns, Rows         int
	IntervalMs            int
	TileCount             int
	Sheets                int
}

// Version is one of a title's copies as a peer sees it: an opaque ID and
// a name (MSFP playback section 2.3).
type Version struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// PlaybackInfo is how a playback is served, for the viewer's player
// information: why it is converted (none when played as it is), the
// container, the bitrate, the video and the audio.
type PlaybackInfo struct {
	Reasons     []string    `json:"reasons,omitempty"`
	Container   string      `json:"container,omitempty"`
	BitrateKbps int         `json:"bitrateKbps,omitempty"`
	Video       *StreamInfo `json:"video,omitempty"`
	Audio       *StreamInfo `json:"audio,omitempty"`
}

// StreamInfo describes the video or the audio of a playback.
type StreamInfo struct {
	Codec       string `json:"codec,omitempty"`
	Profile     string `json:"profile,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	BitDepth    int    `json:"bitDepth,omitempty"`
	Range       string `json:"range,omitempty"`
	BitrateKbps int    `json:"bitrateKbps,omitempty"`
	Channels    int    `json:"channels,omitempty"`
	Language    string `json:"language,omitempty"`
}

// Capabilities are what a viewer's device plays (MSFP playback section
// 2.1, video section 5.1). Toward a peer, only the values MSFP lists are
// sent, coarsened (wire.Capabilities.Coarsen); toward the host, only
// those values, exactly as MSFP writes them. An empty list, or 0, says
// nothing.
type Capabilities = wire.Capabilities

// TranscodeTargets are the targets of a play with delivery "transcode"
// (MSFP playback section 2.7, video section 5.1): a member left out is
// the owning server's choice.
type TranscodeTargets = wire.TranscodeTargets

// The purposes of a playback (MSFP playback section 2): to watch it, to
// download it, to let a media server read the start of its file.
const (
	PurposePlay     = wire.PurposePlay
	PurposeDownload = wire.PurposeDownload
	PurposeScan     = wire.PurposeScan
)

// The deliveries a play may ask (MSFP playback section 2.7); "" lets
// the owning server decide.
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
	// Container, as hls), DeliveryTranscode (to Transcode's targets, as
	// hls), or "" for the host's choice from the capabilities. With a
	// delivery, the host serves exactly that or refuses with
	// ErrDeliveryRefused (its bandwidth, its policy, a delivery it can't
	// make of the copy); it never serves another (MSFP playback section
	// 2.2, step 7). The engine has already refused file of a copy of
	// several parts, file or remux of a play above MaxBitrateKbps, and a
	// BurnSubtitle the copy hasn't.
	Delivery  string
	Container string
	Transcode *TranscodeTargets
	// AudioRenditions asks, for hls, every audio track of the copy as an
	// #EXT-X-MEDIA rendition named by its ID (MSFP media section 3.1).
	AudioRenditions bool
	// Parts is the copy's files, as its description says (1 for one
	// file): a copy of several is started as "hls", never "file".
	Parts int
	Peer  PeerRef
	// AudioTrack and SubtitleTrack are the engine's choice ("": none
	// shown); subtitles never make a playback transcode, but for
	// Transcode.BurnSubtitle.
	AudioTrack    string
	SubtitleTrack string
	// StartMs is where to start, in the copy; 0 for a download or a scan.
	StartMs int64
	// Capabilities are what the viewer's device plays, as the peer said,
	// only values MSFP lists. Without a delivery, nothing said about
	// containers or video codecs: no direct play (H.264 and AAC in HLS;
	// AAC in HLS for a track); about audio codecs: AAC only; about hdr:
	// standard range.
	Capabilities Capabilities
	// MaxBitrateKbps is the cap in effect for a play: 120,000 when none
	// was set. A download's speed is the engine's to pace (downloadKbps).
	MaxBitrateKbps int
	// Queue are the tracks a music play names after Title, that the host
	// prepares under the same playback (MSFP music section 5.2), each
	// with its first copy, in the request's order; Place is its place in
	// the request's queue (0 to 4), which a queued track the engine left
	// out (not offered, no copy) keeps empty.
	Queue []QueuedCopy
	// Admit counts the pool's streams (and, for "transcode", its
	// transcodes), or the download, or the scan, in one step: the host
	// calls it once it knows the method, before starting any work (MSFP
	// playback section 6.2). Errors: ErrStreamLimitReached, ErrServerBusy.
	Admit func(method string) (Slot, error)
}

// QueuedCopy is a track of a play's queue: its place in the request's
// queue, its key, and the handle of its first copy.
type QueuedCopy struct {
	Place int
	Title string
	Copy  string
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
	// MimeType is the file's type, for "file" (a video/ or audio/ type).
	MimeType string
	// PositionMs is where it starts, in the copy.
	PositionMs int64
	Info       *PlaybackInfo
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
	MimeType string
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
	Paused      bool
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
	Font  int // Font, 0 to 7
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
// 5): streams at once (0 to 50), transcodes among them, a bitrate cap
// (0: none); downloads at once (0 to 50; 0: none), downloads started in
// any 24 hours (0 to 10,000; 0: no cap besides Downloads), each
// download's speed cap in kbit/s (0 to 1,000,000; 0: none), scans
// started in any 24 hours (0 to 1,000,000; 0: no scan access). With
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
