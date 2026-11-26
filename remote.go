package msfp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// Playback, on the viewer's side (MSFP playback sections 2 to 8, video
// section 5, music section 5): this server's people play, download or
// scan a peer's title. The host decides who may, and what to ask (the
// copy, the delivery: it has every copy's facts and knows its device;
// the engine says nothing of the device), the engine calls play, checks
// and cleans every member
// of the answer (its etag against the catalog's), keeps the peer's URLs
// and ticket to itself, sends progress at its pace and stop, sends a
// download's late reports, restarts a file being read on the same etag,
// and stops after a restart the playbacks left on peers.

// Times of the viewer's side.
const (
	// tellEvery is how often progress is sent while the viewer watches
	// (MSFP playback section 3): never more often, but on a pause or a
	// resume, and every tellEvery while the host reports.
	tellEvery = 45 * time.Second
	// replaceWait is how long a play waits for the stop of the playback
	// it replaces (MSFP playback section 2.3).
	replaceWait = 5 * time.Second
	// stopTries is how often a stop is sent when there's no answer.
	stopTries = 3
)

// PlayRequest is what the host asks of a peer's title for one viewer
// session: one playback serves one viewer session, never two (MSFP
// playback section 2.7). The host chooses the copy and its tracks: it
// has every copy's facts in the catalog and knows its device.
type PlayRequest struct {
	// Title is the key, as the peer listed it.
	Title string
	// Purpose is PurposePlay ("" too), PurposeDownload or PurposeScan:
	// a download only of a peer whose limits allow downloads, of delivery
	// file or a sized transcode (MSFP playback section 7); a scan only of
	// a peer whose limits allow scans, always of delivery file (section 8),
	// for the host's media server's reads to list a file, never to watch
	// it.
	Purpose string
	// Delivery is DeliveryFile, DeliveryRemux (into MediaType: video/mp2t
	// or video/mp4, audio/… for a copy with no video), DeliveryTranscode
	// (to Transcode's targets: registry tokens or "copy" for a stream
	// served unchanged, the range sdr or keep; those left out take the
	// baseline's) or "": the peer then serves the baseline, H.264 High
	// 4.1 8-bit SDR and AAC-LC stereo in complete HLS (AAC alone for a
	// copy with no video), which every viewer's server must play (MSFP
	// playback section 2.7). The host chooses it from the copy's facts
	// and what its device plays: a copy whose mediaType, codec or threeD
	// it doesn't know, or whose codecs it can't read (wire.PlayableAsIs,
	// wire.ReadCodecs), is one it can't play as it is (core section 8.5).
	// A delivery refused is ErrDeliveryRefused: ask again with another
	// one, or none.
	Delivery  string
	MediaType string
	Transcode *TranscodeTargets
	// AudioRenditions asks, for hls, every audio track as a rendition, so
	// that switching tracks needs no new playback; the peer may make none
	// (RemotePlayback.AudioRenditions says).
	AudioRenditions bool
	// StartMs is where to start; MaxBitrateKbps the viewer's cap (0:
	// none).
	StartMs        int64
	MaxBitrateKbps int
	// VersionID is the copy to play (without it, the peer's first, its
	// preferred) and AudioTrack its audio track (without it, the file's
	// default), from the title's copies kept. Which subtitles a viewer
	// sees is the host's choice, among the copy's tracks and those the
	// answer serves apart; a subtitle is named only to burn it in
	// (Transcode.BurnSubtitle).
	VersionID, AudioTrack string
	// Queue are, for a track, the tracks to play after it, at most 5
	// (MSFP music section 5.2), each with its copy and its own delivery
	// when it names them: prepared under the same playback.
	Queue []QueueEntry
	// Replaces is a RemotePlayback of the same viewer to stop first: to
	// change the audio, the version or the quality, the old playback
	// stops and a new one starts; its stop is waited for up to 5 seconds,
	// so that it doesn't count against the pairing's streams.
	Replaces string
}

// QueueEntry is an entry of a music play's queue (MSFP music section
// 5.2): a track's key, the copy it plays ("": its first), and its own
// delivery with its own MediaType or Transcode ("": the request's).
type QueueEntry = wire.QueueAsk

// RemotePlayback is a playback on a peer, as the host shows it: its
// answer checked and cleaned, with what the catalog says of its copy. It
// holds no URL: the host serves the media at its own route, and asks the
// engine for each piece (Fetch), whose URL the engine builds from the
// peer's stream root and the answer's ticket.
type RemotePlayback struct {
	// ID is the engine's own for it, never the peer's.
	ID, Peer, Title string
	// Purpose and Delivery are the request's.
	Purpose, Delivery string
	// Method is direct, remux or transcode; Protocol hls or file, with
	// MimeType for a file: the copy's mediaType from the catalog (what a
	// file is relayed as).
	Method, Protocol, MimeType string
	// Etag is the copy's (the answer's), Size its exact size when the
	// catalog's copy has that etag (-1 when not known): what a host
	// presenting the copy as a file of its own gives its clients.
	// CopyChanged says the answer's etag isn't the catalog's: the answer
	// wins for this playback, and the title is read again.
	Etag        string
	Size        int64
	CopyChanged bool
	PositionMs  int64
	// DurationMs is the copy's, from the catalog (0 when not known).
	DurationMs int64
	// MaxBitrateKbps is the cap in effect, for a play (0: none).
	MaxBitrateKbps int
	// ScanBytes is a scan's cap: the bytes its ticket may read in all.
	ScanBytes int64
	// VersionID is the copy playing: its tracks are in the catalog.
	// AudioTrack is the audio playing; AudioRenditions: the playlist
	// carries every audio track as a rendition; false, another track
	// means a new playback.
	VersionID       string
	AudioTrack      string
	AudioRenditions bool
	// Subtitles, video only: the tracks of the copy served apart, each
	// fetched by its form (text: MediaSubtitles, WebVTT, and MediaASS when
	// ASS; picture: MediaPictures, PGS), in stretches of
	// wire.StretchSeconds or whole (MediaPath.Whole; text only). A track
	// of the copy not among them reaches a viewer only in the copy played
	// as it is, or burned in.
	Subtitles []wire.ServedSubtitle
	// Fonts is how many fonts the copy's ASS tracks use, fetched as
	// MediaFont 0 to Fonts-1.
	Fonts int
	// Sheets is how many seek-bar preview sheets the playback's ticket
	// opens (MediaPath Sheet): the sheet count of the copy's
	// previewLayout in the catalog, 0 for none or for a download.
	Sheets int
	// Queue are the queued tracks the peer prepared (music section 5.2),
	// each fetched with MediaPath.Entry its Place + 1.
	Queue []RemoteEntry
}

// RemoteEntry is a queued track of a playback on a peer: its place in
// the request's queue, its key, its copy, how it is served, its length
// from the catalog (0 when not known), and for a file the copy's
// mediaType.
type RemoteEntry struct {
	Place                      int
	Title, VersionID           string
	Method, Protocol, MimeType string
	DurationMs                 int64
}

// remoteEntry is a stream of a playback on a peer: its own (0) or a
// queue entry's (n+1), its protocol, and for a file the size and etag
// its answers are checked against (-1 and "": learnt from the first).
type remoteEntry struct {
	protocol  string
	size      int64
	etag      string
	mediaType string
}

// remote is a playback on a peer, live.
type remote struct {
	view      RemotePlayback
	peer      string
	peerName  string
	theirID   string
	base      string // the peer's stream base: its ticket in it, never shown
	audioOnly bool
	audio     map[string]bool // the playback's audio track IDs
	subtitles map[string]wire.SubtitleServing
	layout    *wire.PreviewLayout // the copy's, when its sheets are opened
	ctx       context.Context
	cancel    context.CancelFunc

	// Under remotes.mu. told is when the peer was last sent progress
	// (zero: never), fresh says the host reported since; track is the
	// queued track playing ("" for the title).
	entries    map[int]remoteEntry
	position   int64
	paused     bool
	told       time.Time
	toldPaused bool
	fresh      bool
	track      string
	playlists  map[string]bool // entry/name: names a master playlist named
	fontBytes  map[int]int     // the fonts relayed, by index: 64 MiB in all
}

// pastDownload is a download on a peer, kept for its late reports (MSFP
// playback section 7) after it stopped here.
type pastDownload struct {
	peer, theirID, title string
	queue                []string
	playedAt             time.Time
}

// remotes are this server's playbacks on peers.
type remotes struct {
	mu        sync.Mutex
	byID      map[string]*remote
	downloads map[string]pastDownload // by the engine's ID
	stopping  map[string]int          // stops on their way, by peer
	leftover  sync.Once
}

// playWire is play's request as sent.
type playWire struct {
	Title           string            `json:"title"`
	Purpose         string            `json:"purpose,omitempty"`
	Delivery        string            `json:"delivery,omitempty"`
	MediaType       string            `json:"mediaType,omitempty"`
	Transcode       *TranscodeTargets `json:"transcode,omitempty"`
	AudioRenditions bool              `json:"audioRenditions,omitempty"`
	StartPositionMs int64             `json:"startPositionMs,omitempty"`
	MaxBitrateKbps  int               `json:"maxBitrateKbps,omitempty"`
	AudioTrack      string            `json:"audioTrack,omitempty"`
	VersionID       string            `json:"versionId,omitempty"`
	Queue           []QueueEntry      `json:"queue,omitempty"`
}

// targetsAsSent are a transcode's targets as sent, in the steps MSFP
// gives them (playback section 2.7): the bitrate a whole 1,000, from
// 1,000; the height stepped down to one of wire.Heights (480 below 480);
// video's targets left out for a track.
func targetsAsSent(t *TranscodeTargets, video bool) *TranscodeTargets {
	if t == nil {
		return nil
	}
	out := *t
	if out.MaxBitrateKbps > 0 {
		out.MaxBitrateKbps = max(min(out.MaxBitrateKbps, wire.MaxKbps)/1000*1000, 1000)
	}
	if out.MaxHeight > 0 {
		h := wire.Heights[0]
		for _, s := range wire.Heights {
			if s <= out.MaxHeight {
				h = s
			}
		}
		out.MaxHeight = h
	}
	if !video {
		out.VideoCodec, out.MaxHeight, out.Range, out.BurnSubtitle = "", 0, "", ""
	}
	return &out
}

// Play starts a playback of a peer's title for one of this server's
// viewer sessions, or a download, or a scan (PlayRequest.Purpose). The
// host has decided the viewer may; the engine refuses before calling
// when the peer can't be used, or its last limits say no stream (no
// downloads, no scans). StreamLimitReached, ServerBusy (no answer, none
// signed, or a 503) and BackingOff (a 429, or calls unanswered lately)
// mean busy now; DeliveryRefused, ask with another delivery; NotPlayable
// and the others, not playable (MSFP playback section 2.5). Errors:
// ErrNotFound, ErrNotPaired, ErrUncallable, ErrSuspended, ErrKeyLost,
// ErrNotSupported, ErrRefusedDestination, ErrCertificateRefused,
// ErrStreamLimitReached, ErrServerBusy, ErrBackingOff,
// ErrDeliveryRefused, ErrNotPlayable, ErrInvalidRequest.
func (e *Engine) Play(ctx context.Context, peer string, req PlayRequest) (RemotePlayback, error) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return RemotePlayback{}, err
	}
	k, ok := wire.ParseKey(req.Title)
	if !ok || !k.Playable {
		return RemotePlayback{}, ErrNotPlayable.with(errors.New("not a key play takes"))
	}
	purpose := cmpOr(req.Purpose, wire.PurposePlay)
	if purpose == wire.PurposeScan && req.Delivery == "" {
		req.Delivery = wire.DeliveryFile
	}
	if !slices.Contains([]string{wire.PurposePlay, wire.PurposeDownload, wire.PurposeScan}, purpose) ||
		req.Delivery != "" && methodOf[req.Delivery] == "" || !wire.PurposeAllows(purpose, req.Delivery, req.Transcode) ||
		req.Transcode != nil && req.Delivery != wire.DeliveryTranscode || (req.MediaType != "") != (req.Delivery == wire.DeliveryRemux) ||
		req.MediaType != "" && !wire.IsMediaType(req.MediaType) || req.Transcode != nil && !targetsWellFormed(*req.Transcode) {
		return RemotePlayback{}, ErrInvalidRequest.with(errors.New("a purpose, delivery or transcode MSFP doesn't allow"))
	}
	if err := e.usablePeer(p, wire.PartPlayback); err != nil {
		return RemotePlayback{}, err
	}
	if !e.on(p, wire.PartMedia) || !e.on(p, k.Part) {
		return RemotePlayback{}, ErrNotSupported
	}
	audioOnly := k.Part == wire.PartMusic
	if l := p.TheirLimits; l != nil || purpose != wire.PurposePlay {
		var lim Limits
		if l != nil {
			lim = *l
		}
		most := lim.Streams
		switch {
		case purpose == wire.PurposeDownload:
			most = lim.Downloads
		case purpose == wire.PurposeScan:
			most = lim.ScansPerDay
		case audioOnly && lim.Audio:
			most = lim.AudioStreams
		}
		if most == 0 {
			return RemotePlayback{}, ErrStreamLimitReached
		}
	}
	if req.Replaces != "" {
		if err := e.Stop(ctx, req.Replaces, -1); err != nil && !errors.Is(err, ErrNotFound) {
			e.log.Info("msfp: the playback replaced couldn't be stopped", "error", err)
		}
	}
	e.waitStops(ctx, peer)
	in := playWire{Title: req.Title, Delivery: req.Delivery, MediaType: req.MediaType, AudioRenditions: req.AudioRenditions,
		Transcode:       targetsAsSent(req.Transcode, !audioOnly),
		StartPositionMs: min(max(req.StartMs, 0), wire.MaxPositionMs), MaxBitrateKbps: min(max(req.MaxBitrateKbps, 0), wire.MaxKbps)}
	if purpose != wire.PurposePlay {
		in.Purpose, in.StartPositionMs = purpose, 0
	}
	if _, ok := wire.TrackID(req.AudioTrack, 'a'); ok {
		in.AudioTrack = req.AudioTrack
	}
	if wire.ID(req.VersionID) {
		in.VersionID = req.VersionID
	}
	var asked []QueueEntry
	if audioOnly && len(req.Queue) > 0 {
		queue := make([]QueueEntry, len(req.Queue))
		for i, q := range req.Queue {
			queue[i] = q
			queue[i].Transcode = targetsAsSent(q.Transcode, false)
		}
		raw, _ := json.Marshal(queue)
		var err error
		if asked, err = wire.CheckQueue(req.Title, raw); err != nil {
			return RemotePlayback{}, ErrInvalidRequest.with(err)
		}
		in.Queue = queue
	}
	var raw json.RawMessage
	r, err := e.call(ctx, p, callPlay, in, &raw, false)
	switch {
	case errors.Is(err, ErrUnreachable):
		// No answer, or none signed: busy now (MSFP playback section 2.5).
		return RemotePlayback{}, ErrServerBusy.with(err)
	case err != nil:
		// What lasts says itself (ErrKeyLost, ErrSuspended, ErrUncallable,
		// ErrNotSupported, a destination or certificate refused…), as
		// does ErrBackingOff, busy now.
		return RemotePlayback{}, err
	case r.status == http.StatusConflict && r.problem == string(probNotPlayable):
		return RemotePlayback{}, ErrNotPlayable
	case r.status == http.StatusConflict && r.problem == string(probDelivery):
		return RemotePlayback{}, ErrDeliveryRefused
	case r.status == http.StatusConflict:
		return RemotePlayback{}, ErrStreamLimitReached
	case r.status == http.StatusServiceUnavailable:
		return RemotePlayback{}, ErrServerBusy
	case r.status == http.StatusTooManyRequests:
		return RemotePlayback{}, ErrBackingOff
	case r.status == http.StatusNotFound && r.problem == string(probNotSupported):
		return RemotePlayback{}, ErrNotSupported
	case r.status != http.StatusOK:
		if r.status == http.StatusNotFound {
			e.helloNow(p.ID)
		}
		return RemotePlayback{}, ErrNotPlayable.with(refusalError(r.status, r.problem))
	}
	rm, why := e.readPlay(ctx, p, raw, req, asked, purpose, audioOnly)
	if rm == nil {
		e.log.Warn("msfp: a peer's play answer broke MSFP's rules; it is stopped", "peer", p.ID, "why", why)
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &id) == nil && wire.PlayID(id.ID) {
			e.sendStop(p.ID, id.ID, -1, "")
		}
		return RemotePlayback{}, ErrNotPlayable.with(errors.New(why))
	}
	idBytes, err := e.random(16)
	if err != nil {
		e.sendStop(p.ID, rm.theirID, -1, "")
		return RemotePlayback{}, err
	}
	rm.view.ID = "r" + lowerBase32.EncodeToString(idBytes)
	rm.ctx, rm.cancel = context.WithCancel(context.Background())
	rm.position = rm.view.PositionMs
	// Kept until stopped, so that after a restart the peer is told it's
	// over (MSFP playback section 6.3).
	if err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		return tx.PutLeftover(Leftover{Peer: p.ID, PlaybackID: rm.theirID})
	}); err != nil {
		e.log.Warn("msfp: a playback on a peer couldn't be noted", "error", err)
	}
	var past []DownloadRecord
	var download *DownloadRecord
	rs := &e.remotes
	rs.mu.Lock()
	if rs.byID == nil {
		rs.byID = map[string]*remote{}
	}
	rs.byID[rm.view.ID] = rm
	if purpose == wire.PurposeDownload {
		if rs.downloads == nil {
			rs.downloads = map[string]pastDownload{}
		}
		now := e.now()
		for id, d := range rs.downloads {
			if now.Sub(d.playedAt) > wire.LateReports {
				delete(rs.downloads, id)
				past = append(past, DownloadRecord{ID: id, Viewer: true})
			}
		}
		var queue []string
		for _, q := range rm.view.Queue {
			queue = append(queue, q.Title)
		}
		rs.downloads[rm.view.ID] = pastDownload{peer: p.ID, theirID: rm.theirID, title: req.Title, queue: queue, playedAt: now}
		download = &DownloadRecord{ID: rm.view.ID, Viewer: true, Peer: p.ID, TheirID: rm.theirID, Title: req.Title,
			Queue: queue, PlayedAt: now}
	}
	rs.mu.Unlock()
	e.dropDownloads(ctx, past)
	if download != nil {
		e.keepDownload(ctx, *download)
	}
	if rm.view.CopyChanged {
		e.readSoon(p.ID)
	}
	return rm.view, nil
}

func cmpOr(s, or string) string {
	if s == "" {
		return or
	}
	return s
}

// readPlay checks and cleans a peer's play answer (MSFP playback section
// 2.3, video section 5.3, music section 5.2), its URLs built from the
// peer's stream root and the answer's ticket: nil, and why, when it
// breaks a "must".
func (e *Engine) readPlay(ctx context.Context, p *PeerRecord, raw json.RawMessage, req PlayRequest, asked []QueueEntry,
	purpose string, audioOnly bool) (*remote, string) {
	root := e.peerStreamRoot(p)
	part := wire.PartVideo
	if audioOnly {
		part = wire.PartMusic
	}
	ask := wire.Asked{Purpose: purpose, Delivery: req.Delivery, Part: part}
	a, err := wire.ReadPlayAnswer(raw, ask)
	if err != nil {
		return nil, "the playback's members break MSFP's rules"
	}
	// The copy as the catalog gave it: its etag, size, length, media type,
	// tracks and previews.
	c, known := e.catalogCopy(ctx, p.ID, req.Title, a.VersionID)
	if known {
		ask.CatalogEtag, ask.Subtitles = c.ETag, c.SubtitleTracks
		if a, err = wire.ReadPlayAnswer(raw, ask); err != nil {
			return nil, "the playback's members break MSFP's rules"
		}
	}
	rm := &remote{peer: p.ID, peerName: p.Name, theirID: a.ID, base: a.Base(root), audioOnly: audioOnly, audio: map[string]bool{},
		subtitles: map[string]wire.SubtitleServing{}, entries: map[int]remoteEntry{}}
	v := &rm.view
	v.Peer, v.Title, v.Purpose, v.Delivery = p.ID, req.Title, purpose, req.Delivery
	v.Method, v.Protocol, v.Etag, v.Size = a.Method, a.Protocol, a.Etag, -1
	v.PositionMs, v.ScanBytes = a.PositionMs, a.ScanBytes
	if a.MaxBitrateKbps != nil {
		v.MaxBitrateKbps = *a.MaxBitrateKbps
	}
	v.VersionID, v.AudioTrack, v.AudioRenditions = a.VersionID, a.AudioTrack, a.AudioRenditions
	v.CopyChanged = !known || a.ReadTitleAgain
	if known {
		v.DurationMs = c.DurationMs
		if v.Protocol == wire.ProtocolFile {
			v.MimeType = wire.RelayedFileType(c.MediaType)
		}
		if !v.CopyChanged {
			v.Size = c.Size
		}
		if !audioOnly {
			for _, t := range c.AudioTracks {
				rm.audio[t.ID] = true
			}
		}
	}
	rm.entries[0] = remoteEntry{protocol: v.Protocol, size: v.Size, etag: v.Etag, mediaType: c.MediaType}
	if !audioOnly {
		v.Subtitles, v.Fonts = a.Subtitles, a.FontCount
		for _, s := range a.Subtitles {
			rm.subtitles[s.ID] = s.Serving()
		}
		if l := c.PreviewLayout; known && !v.CopyChanged && l != nil && purpose == wire.PurposePlay {
			rm.layout, v.Sheets = l, l.SheetCount()
		}
	} else if len(asked) > 0 {
		var q struct {
			Queue json.RawMessage `json:"queue"`
		}
		_ = json.Unmarshal(raw, &q)
		for _, entry := range wire.ReadQueue(q.Queue, asked, req.Delivery) {
			re := RemoteEntry{Place: entry.At, Title: entry.Title, VersionID: entry.VersionID, Method: entry.Method,
				Protocol: entry.Protocol}
			ec, known := e.catalogCopy(ctx, p.ID, entry.Title, entry.VersionID)
			size := int64(-1)
			if known {
				re.DurationMs, size = ec.DurationMs, ec.Size
				if re.Protocol == wire.ProtocolFile {
					re.MimeType = wire.RelayedFileType(ec.MediaType)
				}
			}
			v.Queue = append(v.Queue, re)
			rm.entries[entry.At+1] = remoteEntry{protocol: entry.Protocol, size: size, etag: ec.ETag, mediaType: ec.MediaType}
		}
	}
	return rm, ""
}

// targetsWellFormed says whether a transcode's targets are of their
// forms (MSFP playback section 2.7): codecs and the range tokens.
func targetsWellFormed(t TranscodeTargets) bool {
	for _, s := range []string{t.VideoCodec, t.AudioCodec, t.Range} {
		if s != "" && !wire.Token(s) {
			return false
		}
	}
	return true
}

// catalogCopy is a peer's copy as its catalog gave it (a track's as a
// copy with no video); false when the title or the copy isn't kept.
func (e *Engine) catalogCopy(ctx context.Context, peer, key, id string) (wire.Copy, bool) {
	t, err := e.keptTitle(ctx, peer, key)
	if err != nil {
		return wire.Copy{}, false
	}
	switch t := t.(type) {
	case *VideoTitle:
		for _, c := range t.Copies {
			if c.ID == id {
				return c, true
			}
		}
	case *MusicTitle:
		for _, c := range t.Copies {
			if c.ID == id {
				return c.AsCopy(), true
			}
		}
	}
	return wire.Copy{}, false
}

// Restart starts a new playback of the copy a playback of delivery file
// was reading, once it ended (a long pause: MSFP playback section 2.7),
// so that the host goes on reading at the same byte: the same title,
// copy, purpose and delivery, only if the new answer's etag is the one
// it was reading; else the new playback is stopped and ErrCopyChanged
// says the title must be read again. The old one is stopped first if it
// is still live. Make one only for a read that arrives. Errors: Play's,
// ErrCopyChanged, ErrInvalidRequest (not a playback of a file).
func (e *Engine) Restart(ctx context.Context, old RemotePlayback) (RemotePlayback, error) {
	if old.Delivery != wire.DeliveryFile || old.Etag == "" || old.VersionID == "" {
		return RemotePlayback{}, ErrInvalidRequest.with(errors.New("only a playback of delivery file restarts"))
	}
	if e.liveRemote(old.ID) != nil {
		if err := e.Stop(ctx, old.ID, -1); err != nil && !errors.Is(err, ErrNotFound) {
			return RemotePlayback{}, err
		}
	}
	pb, err := e.Play(ctx, old.Peer, PlayRequest{Title: old.Title, Purpose: old.Purpose, Delivery: wire.DeliveryFile,
		VersionID: old.VersionID, AudioTrack: old.AudioTrack})
	if err != nil {
		return RemotePlayback{}, err
	}
	if pb.Etag != old.Etag || pb.VersionID != old.VersionID {
		if err := e.Stop(ctx, pb.ID, -1); err != nil {
			e.log.Info("msfp: a restart of another copy couldn't be stopped", "error", err)
		}
		e.readSoon(old.Peer)
		return RemotePlayback{}, ErrCopyChanged
	}
	if pb.Size < 0 {
		pb.Size = old.Size
		e.remotes.mu.Lock()
		if rm := e.remotes.byID[pb.ID]; rm != nil {
			ent := rm.entries[0]
			ent.size = old.Size
			rm.entries[0], rm.view.Size = ent, old.Size
		}
		e.remotes.mu.Unlock()
	}
	return pb, nil
}

// liveRemote is a playback on a peer by the engine's ID; nil for none.
func (e *Engine) liveRemote(id string) *remote {
	rs := &e.remotes
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.byID[id]
}

// Progress notes where a viewer is in a playback on a peer (MSFP
// playback section 3): the peer is told at the first report, at once on
// a pause or a resume, and otherwise every 45 seconds at most while the
// host reports, by Run's work of each second when no report comes at the
// time. It returns at once. A 410 from the peer ends the playback
// (RemotePlaybackEnded). A scan sends none.
//
// The peer ends a playback it hears nothing of for 2 minutes (playback
// section 6.3): while a viewer is on it, paused too, call Progress at
// least every minute, whatever the player reports. Calls cost nothing
// more: the peer still gets one report per 45 seconds at most.
func (e *Engine) Progress(id string, positionMs int64, paused bool) {
	rs := &e.remotes
	rs.mu.Lock()
	rm := rs.byID[id]
	if rm == nil || rm.view.Purpose == wire.PurposeScan {
		rs.mu.Unlock()
		return
	}
	now := e.now()
	rm.position, rm.paused, rm.fresh = min(max(positionMs, 0), wire.MaxPositionMs), paused, true
	due := rm.told.IsZero() || paused != rm.toldPaused || now.Sub(rm.told) >= tellEvery
	var in progressCall
	if due {
		in = rm.tell(now)
	}
	rs.mu.Unlock()
	if due {
		e.sendProgress(rm, in)
	}
}

// PlayingTrack says which track of a playback's queue plays now (its
// key; the playback's own title, or one of its Queue): progress and stop
// carry it, positions then in that track (MSFP music section 5.2).
// Errors: ErrNotFound, ErrInvalidRequest (a key of neither).
func (e *Engine) PlayingTrack(id, title string) error {
	rs := &e.remotes
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rm := rs.byID[id]
	if rm == nil {
		return ErrNotFound
	}
	if title != rm.view.Title && !slices.ContainsFunc(rm.view.Queue, func(q RemoteEntry) bool { return q.Title == title }) {
		return ErrInvalidRequest
	}
	if rm.track = title; title == rm.view.Title {
		rm.track = ""
	}
	return nil
}

// progressCall is progress's request.
type progressCall struct {
	ID         string `json:"id"`
	PositionMs int64  `json:"positionMs"`
	At         int64  `json:"at,omitempty"`
	Title      string `json:"title,omitempty"`
}

// tell notes that the peer is told now where the viewer is, and gives
// what it is sent. Under remotes.mu.
func (rm *remote) tell(now time.Time) progressCall {
	rm.told, rm.toldPaused, rm.fresh = now, rm.paused, false
	return progressCall{ID: rm.theirID, PositionMs: rm.position, Title: rm.track}
}

// tellWatched sends progress for the playbacks on peers the host
// reported since they were last sent it, 45 seconds ago or more: a host
// whose reports don't fall on the time still keeps its playbacks there
// told every 45 seconds.
func (e *Engine) tellWatched() {
	now := e.now()
	rs := &e.remotes
	rs.mu.Lock()
	type due struct {
		rm *remote
		in progressCall
	}
	var send []due
	for _, rm := range rs.byID {
		if rm.fresh && now.Sub(rm.told) >= tellEvery {
			send = append(send, due{rm, rm.tell(now)})
		}
	}
	rs.mu.Unlock()
	for _, d := range send {
		e.sendProgress(d.rm, d.in)
	}
}

// sendProgress sends progress in the background; a 410 ends the
// playback.
func (e *Engine) sendProgress(rm *remote, in progressCall) {
	e.background(func(ctx context.Context) {
		p, err := e.peerRecord(ctx, rm.peer)
		if err != nil {
			e.endRemote(rm, EndPairingEnded, true)
			return
		}
		var out struct{}
		r, err := e.call(ctx, p, callProgress, in, &out, true)
		switch {
		case err == nil && r.status == http.StatusGone:
			e.endRemote(rm, EndExpired, true)
			e.forgetLeftover(ctx, rm)
		case err != nil && !errors.Is(err, ErrBackingOff):
			e.log.Info("msfp: a peer couldn't be told a playback is watched", "peer", rm.peer, "error", err)
		}
	})
}

// OfflineReport is what a viewer did with a download away from this
// server (MSFP playback sections 3, 4 and 7): where it was, when, in
// which track of a queue (its key; "" for the title), and whether it
// stopped there.
type OfflineReport struct {
	PositionMs int64
	At         time.Time
	Title      string
	Stop       bool
}

// ReportOffline sends a download's late report, as progress or stop with
// at, when the viewer's device syncs: for 30 days after its play, even
// once it stopped here (MSFP playback section 7). Send them in the order
// of their At. Errors: ErrNotFound (no download of that ID within 30
// days), ErrInvalidRequest (an At out of its bounds, a Title not of the
// download), ErrPlaybackStopped (the peer's 410: too late), and the
// call's.
func (e *Engine) ReportOffline(ctx context.Context, id string, r OfflineReport) error {
	rs := &e.remotes
	rs.mu.Lock()
	d, ok := rs.downloads[id]
	rs.mu.Unlock()
	now := e.now()
	if !ok || now.Sub(d.playedAt) > wire.LateReports {
		return ErrNotFound
	}
	if r.At.Before(now.Add(-wire.AtBefore)) || r.At.After(now.Add(wire.AtAfter)) ||
		r.Title != "" && r.Title != d.title && !slices.Contains(d.queue, r.Title) {
		return ErrInvalidRequest
	}
	p, err := e.peerRecord(ctx, d.peer)
	if err != nil {
		return err
	}
	if err := e.usablePeer(p, wire.PartPlayback); err != nil {
		return err
	}
	in := progressCall{ID: d.theirID, PositionMs: min(max(r.PositionMs, 0), wire.MaxPositionMs), At: r.At.Unix()}
	if r.Title != d.title {
		in.Title = r.Title
	}
	call := callProgress
	if r.Stop {
		call = callStop
	}
	var out struct{}
	res, err := e.call(ctx, p, call, in, &out, false)
	switch {
	case err != nil:
		return err
	case res.status == http.StatusOK:
		return nil
	}
	return refusalError(res.status, res.problem)
}

// Stop ends a playback on a peer: the peer is sent stop (again when it
// doesn't answer, a few times), and the playback is forgotten.
// positionMs is where the viewer stopped, -1 when not known. Error:
// ErrNotFound.
func (e *Engine) Stop(ctx context.Context, id string, positionMs int64) error {
	rm := e.liveRemote(id)
	if rm == nil {
		return ErrNotFound
	}
	rs := &e.remotes
	rs.mu.Lock()
	if positionMs < 0 {
		positionMs = rm.position
	}
	track := rm.track
	rs.mu.Unlock()
	e.endRemote(rm, EndStopped, false)
	done := e.stopping(rm.peer)
	defer done()
	e.stopOnPeer(ctx, rm.peer, rm.theirID, positionMs, track)
	e.forgetLeftover(ctx, rm)
	return nil
}

// sendStop sends stop in the background.
func (e *Engine) sendStop(peer, theirID string, positionMs int64, track string) {
	done := e.stopping(peer)
	e.background(func(ctx context.Context) {
		defer done()
		e.stopOnPeer(ctx, peer, theirID, positionMs, track)
	})
}

// stopOnPeer sends stop, tried again a few times without an answer; not
// at all to a peer that can't be used now.
func (e *Engine) stopOnPeer(ctx context.Context, peer, theirID string, positionMs int64, track string) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil || e.usablePeer(p, wire.PartPlayback) != nil {
		return
	}
	in := struct {
		ID         string `json:"id"`
		PositionMs int64  `json:"positionMs,omitempty"`
		Title      string `json:"title,omitempty"`
	}{theirID, max(positionMs, 0), track}
	for try := range stopTries {
		var out struct{}
		r, err := e.call(ctx, p, callStop, in, &out, false)
		if err == nil || !errors.Is(err, ErrUnreachable) || ctx.Err() != nil {
			if err == nil && r.status != http.StatusOK {
				e.log.Info("msfp: a peer refused a stop", "peer", peer, "status", r.status)
			}
			return
		}
		if try < stopTries-1 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second << try):
			}
		}
	}
}

// stopping notes a stop on its way to peer, until done.
func (e *Engine) stopping(peer string) (done func()) {
	rs := &e.remotes
	rs.mu.Lock()
	if rs.stopping == nil {
		rs.stopping = map[string]int{}
	}
	rs.stopping[peer]++
	rs.mu.Unlock()
	return sync.OnceFunc(func() {
		rs.mu.Lock()
		if rs.stopping[peer]--; rs.stopping[peer] <= 0 {
			delete(rs.stopping, peer)
		}
		rs.mu.Unlock()
	})
}

// waitStops waits up to 5 seconds for the stops on their way to peer.
func (e *Engine) waitStops(ctx context.Context, peer string) {
	deadline := time.Now().Add(replaceWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		rs := &e.remotes
		rs.mu.Lock()
		n := rs.stopping[peer]
		rs.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// endRemote ends a playback on a peer here: its fetches are cut, and,
// when the peer or the pairing ended it (tell), the host is told.
func (e *Engine) endRemote(rm *remote, why EndReason, tell bool) {
	rs := &e.remotes
	rs.mu.Lock()
	if rs.byID[rm.view.ID] != rm {
		rs.mu.Unlock()
		return
	}
	delete(rs.byID, rm.view.ID)
	rs.mu.Unlock()
	rm.cancel()
	if tell {
		e.notify(RemotePlaybackEnded{Peer: rm.peer, Name: rm.peerName, Playback: rm.view.ID, Reason: why})
	}
}

// endRemotes ends this server's playbacks on a peer, as the pairing
// ended, or it was suspended, or can't be called; its downloads' late
// reports go nowhere then.
func (e *Engine) endRemotes(peer string, why EndReason) {
	rs := &e.remotes
	rs.mu.Lock()
	var end []*remote
	for _, rm := range rs.byID {
		if rm.peer == peer {
			end = append(end, rm)
		}
	}
	if why == EndPairingEnded {
		for id, d := range rs.downloads {
			if d.peer == peer {
				delete(rs.downloads, id)
			}
		}
	}
	rs.mu.Unlock()
	for _, rm := range end {
		e.endRemote(rm, why, true)
	}
}

func (e *Engine) forgetLeftover(ctx context.Context, rm *remote) {
	if err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		return tx.DeleteLeftover(Leftover{Peer: rm.peer, PlaybackID: rm.theirID})
	}); err != nil {
		e.log.Warn("msfp: a playback on a peer couldn't be forgotten", "error", err)
	}
}

// stopLeftovers tells peers the playbacks this server had on them before
// it started are over (MSFP playback section 6.3): once, at the first
// run.
func (e *Engine) stopLeftovers(ctx context.Context) {
	e.remotes.leftover.Do(func() {
		var left []Leftover
		if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
			var err error
			left, err = tx.Leftovers()
			return err
		}); err != nil {
			e.log.Warn("msfp: the playbacks left on peers couldn't be read", "error", err)
			return
		}
		live := map[Leftover]bool{}
		e.remotes.mu.Lock()
		for _, rm := range e.remotes.byID {
			live[Leftover{Peer: rm.peer, PlaybackID: rm.theirID}] = true
		}
		e.remotes.mu.Unlock()
		for _, l := range left {
			if live[l] {
				continue // started since this run began: not left over
			}
			e.stopOnPeer(ctx, l.Peer, l.PlaybackID, -1, "")
			if err := e.cfg.Store.Update(ctx, func(tx Tx) error { return tx.DeleteLeftover(l) }); err != nil {
				e.log.Warn("msfp: a playback left on a peer couldn't be forgotten", "error", err)
			}
		}
	})
}
