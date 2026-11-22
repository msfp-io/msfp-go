package msfp

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// Playback, on the owning side (MSFP playback sections 2 to 8, video
// section 5, music section 5): play, progress and stop from peers, the
// choice of the copy and the tracks among the title's copies as the
// catalog sends them, the delivery and the purpose (a play, a download,
// a scan), the pool's limits counted in one step with the host's start
// (Admit), the playbacks' states, tickets and endings, and a download's
// late reports. Playbacks live in memory: after a restart, a peer's
// progress gets 410 and it starts again; downloads alone are kept in the
// store for their late reports (downloads.go).

// Times of a playback (MSFP playback section 6.3).
const (
	// expiryEvery is how often expired playbacks are looked for.
	expiryEvery = 30 * time.Second
	// endedKept is how long why a playback ended is remembered; a
	// download's ending is kept for its late reports (wire.LateReports).
	endedKept = time.Hour
	// hostTime bounds the host's Stop and Progress calls made in the
	// background.
	hostTime = 30 * time.Second
)

// plays are the peers' playbacks here, live, and those ended lately.
type plays struct {
	mu       sync.Mutex
	byID     map[string]*playback
	byTicket map[[32]byte]*playback
	counts   map[string]*counts // by pool, admitted slots included
	ended    map[string]endedPlay
	expired  time.Time // when expiry last ran
}

// counts are what a pool uses of this server now: its playbacks of
// purpose play (video and audio apart when its limits say so), its
// downloads, and when the downloads and scans of the last 24 hours
// started.
type counts struct {
	streams, transcodes           int
	audioStreams, audioTranscodes int
	downloads                     int
	downloadDay, scanDay          []time.Time
}

// endedPlay is a playback ended lately: whose, why, when; for a
// download, what its late reports need (MSFP playback section 7).
type endedPlay struct {
	peer     string
	reason   EndReason
	at       time.Time
	purpose  string
	title    string
	queue    []string
	handle   string
	playedAt time.Time
	lastAt   time.Time
}

// subtitleServed is how a subtitle track of a playback is served apart:
// as WebVTT (text), as ASS too (ass), or as PGS (pictures).
type subtitleServed struct{ text, ass, pictures bool }

// entryServed is a stream of a playback: its own (entry 0) or a queue
// entry's: how it is served, and for a file its exact size, etag and
// media type (the copy's, from the catalog).
type entryServed struct {
	protocol  string
	size      int64
	etag      string
	mediaType string
}

// playback is a peer's live playback here. Its fields are set once it
// is registered, but used, active, open, lastAt, scanSent, scanReads
// and playlists, which plays.mu guards.
type playback struct {
	id, peer, peerName string
	title              string
	queue              []string // the request's queue, by place
	libraries          []string // the host's libraries holding the title, offered when it started
	handle, method     string
	purpose            string
	audioOnly          bool
	ticket             [32]byte
	started            time.Time
	sheets, fonts      int
	// sheetWidth and sheetHeight bound a sheet: its layout's full sheet.
	sheetWidth, sheetHeight int
	audio                   map[string]bool // the copy's audio track IDs
	subtitles               map[string]subtitleServed
	entries                 map[int]entryServed // 0: its own stream; n+1: queue entry n
	slot                    *countSlot
	pace                    *pacer // a download's, at downloadKbps; nil for none
	ctx                     context.Context
	cancel                  context.CancelFunc
	used                    time.Time
	active                  bool
	open                    int
	lastAt                  time.Time
	scanSent                int64
	scanCap                 int64 // a scan's: the bytes its ticket serves in all (its pool's scanBytes)
	scanReads               int
	fontBytes               map[int]int       // the fonts served, by index: 64 MiB in all
	playlists               map[string][]byte // served, by entry/name: served the same again
	playlistBytes           int
	names                   map[string]bool // entry/name: the names its playlists gave
	facts                   servedFacts     // its own stream's, for its master playlist
}

// countSlot is what a play counted in a pool (Admit): a stream, and when
// transcode one of its transcodes; or a download; or a scan.
type countSlot struct {
	e         *Engine
	pool      string
	purpose   string
	audio     bool // counted among audio streams (Limits.Audio)
	limits    Limits
	transcode bool
	released  bool
}

func (c *counts) of(audio bool) (streams, transcodes *int) {
	if audio {
		return &c.audioStreams, &c.audioTranscodes
	}
	return &c.streams, &c.transcodes
}

func (c *counts) empty() bool {
	return c.streams == 0 && c.transcodes == 0 && c.audioStreams == 0 && c.audioTranscodes == 0 && c.downloads == 0 &&
		len(c.downloadDay) == 0 && len(c.scanDay) == 0
}

// lastDay keeps of times those of the 24 hours before now.
func lastDay(times []time.Time, now time.Time) []time.Time {
	return slices.DeleteFunc(times, func(t time.Time) bool { return !now.Before(t.Add(24 * time.Hour)) })
}

func (s *countSlot) caps() (streams, transcodes int) {
	if s.audio {
		return s.limits.AudioStreams, s.limits.AudioTranscodes
	}
	return s.limits.Streams, s.limits.Transcodes
}

// Release frees what was counted; once, however often it is called. A
// download or a scan stays in the day's count it took.
func (s *countSlot) Release() {
	ps := &s.e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if s.released {
		return
	}
	s.released = true
	c := ps.counts[s.pool]
	if c == nil {
		return
	}
	switch s.purpose {
	case wire.PurposeDownload:
		c.downloads--
	case wire.PurposeScan:
	default:
		streams, transcodes := c.of(s.audio)
		*streams--
		if s.transcode {
			*transcodes--
		}
	}
	now := s.e.now()
	c.downloadDay, c.scanDay = lastDay(c.downloadDay, now), lastDay(c.scanDay, now)
	if c.empty() {
		delete(ps.counts, s.pool)
	}
}

// Transcoding counts a remux that turned out to re-encode the video as a
// transcode from then on (MSFP playback section 6); false when the
// pairing has none left: the host then stops it. A download's or a
// scan's takes none of the pool's (the server's budget is the host's).
func (s *countSlot) Transcoding() bool {
	ps := &s.e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if s.transcode {
		return true
	}
	if s.released {
		return false
	}
	if s.purpose != wire.PurposePlay {
		return true
	}
	_, most := s.caps()
	_, transcodes := ps.countsOf(s.pool).of(s.audio)
	if *transcodes >= most {
		return false
	}
	*transcodes++
	s.transcode = true
	return true
}

func (ps *plays) countsOf(pool string) *counts {
	if ps.counts == nil {
		ps.counts = map[string]*counts{}
	}
	c := ps.counts[pool]
	if c == nil {
		c = &counts{}
		ps.counts[pool] = c
	}
	return c
}

// admitIn counts a playback of purpose in a pool, in one step no other
// play comes between (MSFP playback section 6.2): for a play, a stream
// (and for "transcode" a transcode); for a download, one of its
// downloads and of the day's; for a scan, one of the day's.
func (e *Engine) admitIn(pool string, l Limits, purpose string, audio bool, method string) (*countSlot, error) {
	switch method {
	case "direct", "remux", "transcode":
	default:
		return nil, ErrInternal.with(errors.New("admit of a method MSFP doesn't have"))
	}
	s := &countSlot{e: e, pool: pool, purpose: purpose, audio: audio && l.Audio, limits: l}
	ps := &e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	c := ps.countsOf(pool)
	now := e.now()
	c.downloadDay, c.scanDay = lastDay(c.downloadDay, now), lastDay(c.scanDay, now)
	switch purpose {
	case wire.PurposeDownload:
		if c.downloads >= l.Downloads || l.DownloadsPerDay > 0 && len(c.downloadDay) >= l.DownloadsPerDay {
			return nil, ErrStreamLimitReached
		}
		c.downloads++
		c.downloadDay = append(c.downloadDay, now)
		return s, nil
	case wire.PurposeScan:
		if len(c.scanDay) >= l.ScansPerDay {
			return nil, ErrStreamLimitReached
		}
		c.scanDay = append(c.scanDay, now)
		return s, nil
	}
	most, mostTranscodes := s.caps()
	streams, transcodes := c.of(s.audio)
	switch {
	case *streams >= most:
		return nil, ErrStreamLimitReached
	case method == "transcode" && *transcodes >= mostTranscodes:
		return nil, ErrServerBusy
	}
	*streams++
	if method == "transcode" {
		*transcodes++
		s.transcode = true
	}
	return s, nil
}

// roomIn says whether a pool has room for a playback of purpose now
// (room), and whether its limits allow any at all (open).
func (e *Engine) roomIn(pool string, l Limits, purpose string, audio bool) (room, open bool) {
	ps := &e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var c counts
	if have := ps.counts[pool]; have != nil {
		c = *have
	}
	now := e.now()
	switch purpose {
	case wire.PurposeDownload:
		today := len(lastDay(slices.Clone(c.downloadDay), now))
		return c.downloads < l.Downloads && (l.DownloadsPerDay == 0 || today < l.DownloadsPerDay), l.Downloads > 0
	case wire.PurposeScan:
		return len(lastDay(slices.Clone(c.scanDay), now)) < l.ScansPerDay, l.ScansPerDay > 0
	}
	s := &countSlot{audio: audio && l.Audio, limits: l}
	most, _ := s.caps()
	used, _ := c.of(s.audio)
	return *used < most, most > 0
}

// versionID is a copy's version ID toward a peer: a keyed MAC of its
// handle under the IDs secret, which tells the peer nothing of it (MSFP
// catalog section 2.1), stable as long as the handle is. Its form is
// Overfin's from before the engine ("ver_", then the first 16 bytes of
// HMAC-SHA-256 over "version", the peer's ID and the handle, a line feed
// between each, base64url), so that the version IDs its peers kept stay
// the same once it hosts the engine.
func (e *Engine) versionID(peer, handle string) string {
	m := hmac.New(sha256.New, e.ourKeys().ids)
	m.Write([]byte("version\n" + peer + "\n" + handle))
	return "ver_" + wire.Encode64(m.Sum(nil)[:16])
}

// offeredCopy is a copy of a title as the catalog sends it to a peer
// (cleaned, in the host's order of preference, its ID the version ID),
// with the host's handle of it; copy is it whole (a track's as a copy
// with no video, its one audio track a0).
type offeredCopy struct {
	handle string
	copy   wire.Copy
	lyrics string
}

func (c offeredCopy) id() string { return c.copy.ID }

// offeredCopies are the copies of exactly that title the peer may play
// here, as the catalog sends them to it (MSFP playback section 2.2, step
// 3; video section 3.7: in the host's order of preference), and the host's libraries holding
// it among those offered; none when the title isn't sent to the peer.
func (e *Engine) offeredCopies(ctx context.Context, p *PeerRecord, key string) ([]offeredCopy, []string, error) {
	if e.cfg.Catalog == nil {
		return nil, nil, nil
	}
	libs := offered(p)
	slices.Sort(libs)
	t, err := e.hostTitle(ctx, key, libs)
	if err != nil || t == nil {
		return nil, nil, err
	}
	s, err := e.senderFor(ctx, p, libs)
	if err != nil {
		return nil, nil, err
	}
	raw, ok, err := s.title(t)
	if err != nil || !ok {
		return nil, nil, err
	}
	handles := map[string]string{}
	var out []offeredCopy
	switch t := t.(type) {
	case *VideoTitle:
		for _, c := range t.Copies {
			handles[e.versionID(p.ID, c.ID)] = c.ID
		}
		v, ok := wire.ReadVideoTitle(raw)
		if !ok {
			return nil, nil, nil
		}
		for _, c := range v.Copies {
			out = append(out, offeredCopy{handle: handles[c.ID], copy: c})
		}
	case *MusicTitle:
		for _, c := range t.Copies {
			handles[e.versionID(p.ID, c.ID)] = c.ID
		}
		m, ok := wire.ReadMusicTitle(raw)
		if !ok {
			return nil, nil, nil
		}
		for _, c := range m.Copies {
			out = append(out, offeredCopy{handle: handles[c.ID], copy: c.AsCopy(), lyrics: c.Lyrics})
		}
	}
	out = slices.DeleteFunc(out, func(c offeredCopy) bool { return c.handle == "" })
	var held []string
	for _, l := range t.Common().Libraries {
		if slices.Contains(libs, l) && !slices.Contains(held, l) {
			held = append(held, l)
		}
	}
	return out, held, nil
}

// senderFor is a sender of titles to the peer, for these libraries.
func (e *Engine) senderFor(ctx context.Context, p *PeerRecord, libs []string) (*sender, error) {
	var ids map[string]string
	if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		ids, err = tx.LibraryIDs()
		return err
	}); err != nil {
		return nil, err
	}
	pub := map[string]string{}
	for _, l := range libs {
		pub[l] = ids[l]
	}
	return &sender{e: e, ctx: ctx, p: p, libs: libs, local: localOffered(p), pub: pub, parents: map[string]string{}}, nil
}

// servedAs is how a subtitle track of format is served apart (MSFP
// video section 5.3): the form video section 5.4's table gives a listed
// format, else the one the host says (form), else none; none either
// when the host says it can't.
func servedAs(format, form string, notApart bool) subtitleServed {
	if notApart {
		return subtitleServed{}
	}
	if s, listed := wire.ServedForms[format]; listed {
		return subtitleServed{text: s.Form == wire.FormText, ass: s.ASS, pictures: s.Form == wire.FormPicture}
	}
	return subtitleServed{text: form == wire.FormText, pictures: form == wire.FormPicture}
}

// playAnswer is play's answer (MSFP playback section 2.3, video section
// 5.3, music section 5.2): no URL, the viewer's server builds them from
// the ticket.
type playAnswer struct {
	ID              string                `json:"id"`
	Ticket          string                `json:"ticket"`
	Protocol        string                `json:"protocol"`
	Method          string                `json:"method"`
	Etag            string                `json:"etag"`
	PositionMs      int64                 `json:"positionMs"`
	VersionID       string                `json:"versionId"`
	AudioTrack      string                `json:"audioTrack,omitempty"`
	AudioRenditions *bool                 `json:"audioRenditions,omitempty"`
	MaxBitrateKbps  int                   `json:"maxBitrateKbps,omitempty"`
	ScanBytes       int64                 `json:"scanBytes,omitempty"`
	Subtitles       []wire.ServedSubtitle `json:"subtitles,omitempty"`
	FontCount       int                   `json:"fontCount,omitempty"`
	Queue           []wire.QueueEntry     `json:"queue,omitempty"`
}

// refusalOf is a wire refusal of play, progress or stop as answered.
func refusalOf(err error) response {
	var pr *wire.PlayRefusal
	if errors.As(err, &pr) {
		switch pr.Problem {
		case wire.ProblemStreamLimit:
			return refuse(probStreamLimit)
		case wire.ProblemDeliveryRefused:
			return refuse(probDelivery)
		case wire.ProblemNotPlayable:
			return refuse(probNotPlayable)
		case wire.ProblemServerBusy:
			return refuse(probServerBusy)
		case wire.ProblemStopped:
			return refuse(probStopped)
		case wire.ProblemNotFound:
			return refuse(probNotFound)
		}
	}
	return refuse(probInvalid)
}

// play starts a paired peer's playback of a title here (MSFP playback
// section 2.2), in its steps.
func (e *Engine) play(req *request) response {
	p := req.peer
	if p == nil || p.State != Paired {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartPlayback) {
		return refuse(probNotSupported)
	}
	// 1. The request, as MSFP reads it: a key of a content part on.
	in, err := wire.ReadPlay(req.body, func(t string) bool { k, ok := e.keyOn(p, t); return ok && k.Playable })
	if err != nil {
		return refusalOf(err)
	}
	k, _ := wire.ParseKey(in.Title)
	video, audioOnly := k.Part == wire.PartVideo, k.Part == wire.PartMusic
	var queue []wire.QueueAsk
	if audioOnly {
		var q struct {
			Queue json.RawMessage `json:"queue"`
		}
		_ = json.Unmarshal(req.body, &q)
		if queue, err = wire.CheckQueue(in.Title, q.Queue); err != nil {
			return refusalOf(err)
		}
	}
	if !video && in.Transcode != nil {
		// Members of another content part are ignored (playback section
		// 2.4).
		in.Transcode.BurnSubtitle = ""
	}
	// 2. No room at all for this purpose in any of the link's pools.
	if _, open := e.poolRoom(p, in.Purpose, audioOnly); !open {
		return refuse(probStreamLimit)
	}
	// 3. The title's copies, as the catalog sends them to it.
	if e.cfg.Catalog == nil || e.cfg.Playback == nil {
		return refuse(probNotFound)
	}
	ctx := req.r.Context()
	copies, libs, err := e.offeredCopies(ctx, p, in.Title)
	if err != nil {
		e.log.Warn("msfp: the host's title couldn't be read for a play", "error", err)
		return refuse(probInternal)
	}
	if len(copies) == 0 {
		return refuse(probNotFound)
	}
	// 4. The copy (the one versionId names, else the first, the host's
	// preferred), then malformed track IDs refused; the audio (the one
	// audioTrack names, else the file's default).
	whole := make([]wire.Copy, len(copies))
	for i, c := range copies {
		whole[i] = c.copy
	}
	chosen := copies[wire.PickCopy(whole, in.VersionID)]
	if !in.TrackIDsOK() {
		return refuse(probInvalid)
	}
	audio := wire.DefaultAudio(chosen.copy.AudioTracks, in.AudioTrack)
	// 5. The purpose's limits all in use: refused before any work (the
	// count that matters is Admit's).
	if room, _ := e.poolRoom(p, in.Purpose, audioOnly); !room {
		return refuse(probStreamLimit)
	}
	// 6 and 7. The bitrate cap (0: none), and the deliveries MSFP itself
	// refuses, or what is served: the baseline's streams left unchanged
	// when within it, the targets left out the baseline's.
	kbps := in.BitrateCap(e.cfg.MaxBitrateKbps, generous(p).BitrateKbps)
	served, err := wire.Serve(in, chosen.copy, audio, kbps, e.cfg.Encoders)
	if err != nil {
		return refusalOf(err)
	}
	if in.Delivery == wire.DeliveryTranscode {
		in.Transcode = served.Transcode
	}
	info, err := e.cfg.Playback.Describe(ctx, chosen.handle)
	if err != nil {
		return e.hostRefusal(err, "describe")
	}
	start := in.StartPositionMs
	if in.Purpose != wire.PurposePlay {
		start = 0
	} else if start == 0 && video && chosen.copy.StartMs != nil {
		start = *chosen.copy.StartMs
	}
	// The queue: each entry's copy (the one it names, else its first) and
	// its own delivery, else the request's; one not offered, of no such
	// copy, or that can't be served so keeps its place empty (MSFP music
	// section 5.2). A codec not made refuses the request.
	var queued []QueuedCopy
	queueCopies := map[int]offeredCopy{}
	queueTranscodes := false
	for n, ask := range queue {
		er, err := ask.Request(in)
		if err != nil {
			return refusalOf(err)
		}
		if er.Transcode != nil {
			if err := e.cfg.Encoders.CheckCodecs(*er.Transcode, false); err != nil {
				return refusalOf(err)
			}
		}
		qc, _, err := e.offeredCopies(ctx, p, ask.Title)
		if err != nil {
			return refuse(probInternal)
		}
		// The copy the entry names, else its first (MSFP music section
		// 5.2, as play's versionId: playback section 2.6). A track with
		// no copy offered is left out.
		if len(qc) == 0 {
			continue
		}
		i := max(slices.IndexFunc(qc, func(c offeredCopy) bool { return c.id() == ask.VersionID }), 0)
		c := qc[i]
		s, err := wire.Serve(er, c.copy, wire.DefaultAudio(c.copy.AudioTracks, ""), kbps, e.cfg.Encoders)
		if err != nil {
			continue
		}
		queueTranscodes = queueTranscodes || s.Transcodes
		queued = append(queued, QueuedCopy{Place: n, Title: ask.Title, Copy: c.handle, Delivery: er.Delivery,
			MediaType: er.MediaType, Transcode: s.Transcode, Served: s})
		queueCopies[n] = c
	}
	// counted is the method admitted: a transcode whose video and audio
	// are both copied takes no transcode; a queue with a track converted
	// takes one (MSFP playback section 2.6, music section 5.2).
	counted := func(method string) string {
		switch {
		case method == "transcode" && in.Delivery == wire.DeliveryTranscode && !served.Transcodes && !queueTranscodes:
			return "remux"
		case method != "transcode" && queueTranscodes:
			return "transcode"
		}
		return method
	}
	// 8. The host starts it, admitted by the pool's limits first.
	var slot *countSlot
	sr := StartRequest{Copy: chosen.handle, Title: in.Title, AudioOnly: audioOnly, Purpose: in.Purpose, Delivery: in.Delivery,
		MediaType: in.MediaType, Transcode: in.Transcode, Served: served, AudioRenditions: in.AudioRenditions,
		Parts: chosen.copy.FileCount(), Peer: PeerRef{ID: p.ID, Name: p.Name}, AudioTrack: audio, StartMs: start,
		MaxBitrateKbps: kbps, Queue: queued,
		Admit: func(method string) (Slot, error) {
			if slot != nil {
				return nil, ErrInternal.with(errors.New("admit called twice"))
			}
			s, err := e.admitPools(p, in.Purpose, audioOnly, counted(method))
			if err != nil {
				return nil, err
			}
			slot = s
			return s, nil
		}}
	started, err := e.cfg.Playback.Start(ctx, sr)
	if err != nil {
		if slot != nil {
			slot.Release()
		}
		return e.hostRefusal(err, "start")
	}
	if why := startedBreaks(in, chosen, served, started, queued); why != "" {
		e.log.Error("msfp: the host started a playback MSFP doesn't allow; it is stopped", "why", why,
			"decided", served.Method, "method", started.Method, "protocol", started.Protocol)
		if slot != nil {
			slot.Release()
		}
		e.hostStop(started.Handle, EndStopped)
		return refuse(probInternal)
	}
	if slot == nil {
		// A host that didn't ask: admitted now, its playback stopped when
		// the limits refuse it.
		if slot, err = e.admitPools(p, in.Purpose, audioOnly, counted(started.Method)); err != nil {
			e.hostStop(started.Handle, EndStopped)
			return e.hostRefusal(err, "admit")
		}
	}
	pb, ticket := e.register(p, in, queue, libs, chosen, info, audio, served, started, slot, queueCopies, audioOnly)
	if pb == nil {
		return refuse(probInternal)
	}
	// The pairing ended, or the library is no longer offered, while it
	// started: it ends at once, saying which.
	switch q, err := e.peerRecord(ctx, p.ID); {
	case errors.Is(err, ErrNotFound) || err == nil && (q.State != Paired || !q.Suspended.IsZero()):
		e.endPlayback(pb, EndPairingEnded)
		return refuse(probNotFound)
	case err != nil:
		e.endPlayback(pb, EndStopped)
		return refuse(probInternal)
	case !stillOffered(q, pb.libraries):
		e.endPlayback(pb, EndNotOffered)
		return refuse(probNotFound)
	}
	// 9. The answer: the ticket, and what the catalog can't know (MSFP
	// playback section 2.3); the viewer's server builds every URL.
	out := playAnswer{ID: pb.id, Ticket: ticket, Protocol: started.Protocol, Method: started.Method, Etag: chosen.copy.ETag,
		PositionMs: started.PositionMs, VersionID: chosen.id(), AudioTrack: audio}
	if in.AudioRenditions && started.Protocol == wire.ProtocolHLS {
		// Asked for, never refused: the answer says whether the host made
		// them (MSFP playback section 2.3), false when it made none.
		made := started.AudioRenditions
		out.AudioRenditions = &made
	}
	if in.Purpose == wire.PurposePlay {
		out.MaxBitrateKbps = kbps
	}
	if in.Purpose == wire.PurposeScan {
		out.ScanBytes = pb.scanCap
	}
	for _, t := range chosen.copy.SubtitleTracks {
		if how, ok := pb.subtitles[t.ID]; ok && (how.text || how.pictures) {
			s := wire.ServedSubtitle{ID: t.ID, Form: wire.FormText, ASS: how.ass}
			if how.pictures {
				s.Form, s.ASS = wire.FormPicture, false
			}
			out.Subtitles = append(out.Subtitles, s)
		}
	}
	out.FontCount = pb.fonts
	for _, q := range started.Queue {
		out.Queue = append(out.Queue, wire.QueueEntry{Title: queue[q.Place].Title, VersionID: queueCopies[q.Place].id(),
			Method: q.Method, Protocol: q.Protocol, At: q.Place})
	}
	slices.SortFunc(out.Queue, func(a, b wire.QueueEntry) int { return a.At - b.At })
	e.log.Info("msfp: a peer's playback started", "peer", p.ID, "playback", pb.id, "method", started.Method, "purpose", in.Purpose)
	e.notify(PlaybackStarted{Peer: p.ID, Name: p.Name, Playback: pb.id, Title: pb.title, Method: pb.method, Purpose: pb.purpose})
	return answer(out)
}

// stillOffered says whether one of the libraries a playback's title was
// in is still offered to the peer.
func stillOffered(p *PeerRecord, libs []string) bool { return offeredIn(libs, offered(p)) }

// offeredIn says whether one of libs is among those offered.
func offeredIn(libs, offered []string) bool {
	return len(libs) == 0 || slices.ContainsFunc(libs, func(l string) bool { return slices.Contains(offered, l) })
}

// methodOf is the method each delivery is served with.
var methodOf = map[string]string{wire.DeliveryFile: "direct", wire.DeliveryRemux: "remux", wire.DeliveryTranscode: "transcode"}

// startedBreaks says what the host's start breaks of MSFP, "" for
// nothing: a method or protocol MSFP doesn't have, another method than
// a transcode the engine decided (Served: without delivery, a copy
// outside the baseline is transcoded; one within it may be served
// direct, remux or transcode: section 2.3), another delivery than the one
// asked (file is direct as file; remux, transcode and the baseline
// without delivery are hls),
// file of a copy of several parts, file played otherwise than direct; a
// queue entry of a place not asked, twice, or served against the same
// rules for its own delivery.
func startedBreaks(in wire.PlayRequest, c offeredCopy, served wire.Served, s Started, asked []QueuedCopy) string {
	check := func(delivery, decided, method, protocol string, parts int) string {
		switch {
		case method != "direct" && method != "remux" && method != "transcode" || protocol != "hls" && protocol != "file":
			return "a method or protocol MSFP doesn't have"
		case delivery != "" && method != methodOf[delivery]:
			return "another method than the delivery asked"
		case decided == wire.MethodTranscode && method != decided:
			return "another method than the transcode decided"
		case delivery == wire.DeliveryFile && protocol != "file" || delivery != wire.DeliveryFile && protocol == "file":
			return "another protocol than the delivery asked"
		case protocol == "file" && method != "direct":
			return "a file that isn't played as it is"
		case protocol == "file" && parts > 1:
			return "a copy of several parts as file"
		}
		return ""
	}
	if why := check(in.Delivery, served.Method, s.Method, s.Protocol, c.copy.FileCount()); why != "" {
		return why
	}
	seen := map[int]bool{}
	for _, q := range s.Queue {
		i := slices.IndexFunc(asked, func(a QueuedCopy) bool { return a.Place == q.Place })
		if i < 0 || seen[q.Place] {
			return "a queue entry of no place asked"
		}
		seen[q.Place] = true
		if why := check(asked[i].Delivery, asked[i].Served.Method, q.Method, q.Protocol, 1); why != "" {
			return "a queue entry: " + why
		}
	}
	return ""
}

// register makes a started playback live: its ID and its ticket, 128
// bits and 256 bits of randomness, the ticket kept by its hash only
// (MSFP media section 1). It gives the ticket's text, for the answer
// only.
func (e *Engine) register(p *PeerRecord, in wire.PlayRequest, queue []wire.QueueAsk, libs []string, c offeredCopy, info CopyInfo,
	audio string, served wire.Served, s Started, slot *countSlot, queueCopies map[int]offeredCopy, audioOnly bool) (*playback, string) {
	fail := func() (*playback, string) {
		slot.Release()
		e.hostStop(s.Handle, EndStopped)
		return nil, ""
	}
	idBytes, err := e.random(16)
	if err != nil {
		return fail()
	}
	ticketBytes, err := e.random(32)
	if err != nil {
		return fail()
	}
	ticket := wire.Encode64(ticketBytes)
	now := e.now()
	var titles []string
	for _, q := range queue {
		titles = append(titles, q.Title)
	}
	pb := &playback{id: "play_" + lowerBase32.EncodeToString(idBytes), peer: p.ID, peerName: p.Name, title: in.Title, queue: titles,
		libraries: libs, handle: s.Handle, method: s.Method, purpose: in.Purpose, audioOnly: audioOnly,
		ticket: sha256.Sum256([]byte(ticket)), started: now, used: now, slot: slot, audio: map[string]bool{},
		subtitles: map[string]subtitleServed{}, entries: map[int]entryServed{0: {protocol: s.Protocol, size: c.copy.Size,
			etag: c.copy.ETag, mediaType: c.copy.MediaType}}}
	pb.facts = servedFacts{copy: c.copy, audio: audio, served: served}
	if in.Purpose == wire.PurposeScan {
		pb.scanCap = wire.ScanCap(int64(slot.limits.ScanBytes))
	}
	if in.Purpose == wire.PurposeDownload && slot.limits.DownloadKbps > 0 {
		pb.pace = newPacer(slot.limits.DownloadKbps, e.cfg.Clock)
	}
	if !audioOnly {
		for _, a := range c.copy.AudioTracks {
			pb.audio[a.ID] = true
		}
	}
	if !audioOnly && in.Purpose != wire.PurposeScan {
		// A playback's ticket opens its copy's sheets (video section 5.5);
		// a download's opens none.
		if t := c.copy.PreviewLayout; t != nil && in.Purpose == wire.PurposePlay {
			pb.sheets, pb.sheetWidth, pb.sheetHeight = t.SheetCount(), t.Columns*t.TileWidth, t.Rows*t.TileHeight
		}
		hasASS := false
		for _, t := range c.copy.SubtitleTracks {
			how := servedAs(t.Format, info.Forms[t.ID], slices.Contains(info.NotApart, t.ID))
			if how.text || how.pictures {
				pb.subtitles[t.ID] = how
			}
			hasASS = hasASS || how.ass
		}
		if hasASS && info.Fonts > 0 && info.Fonts <= wire.MaxFonts {
			pb.fonts = info.Fonts
		}
	}
	for _, q := range s.Queue {
		qc := queueCopies[q.Place]
		pb.entries[q.Place+1] = entryServed{protocol: q.Protocol, size: qc.copy.Size, etag: qc.copy.ETag, mediaType: qc.copy.MediaType}
	}
	pb.ctx, pb.cancel = context.WithCancel(context.Background())
	ps := &e.plays
	ps.mu.Lock()
	if ps.byID == nil {
		ps.byID, ps.byTicket = map[string]*playback{}, map[[32]byte]*playback{}
	}
	ps.byID[pb.id], ps.byTicket[pb.ticket] = pb, pb
	ps.mu.Unlock()
	if pb.purpose == wire.PurposeDownload {
		e.keepDownload(context.Background(), DownloadRecord{ID: pb.id, Peer: pb.peer, Title: pb.title, Queue: pb.queue,
			Handle: pb.handle, PlayedAt: pb.started})
	}
	return pb, ticket
}

// streamBase is the stream root of the address the request was signed
// for: the former one during a move, for a peer still on it (MSFP media
// section 1, core section 13.1, step 5).
func (e *Engine) streamBase(req *request) string {
	a := e.address()
	addr := a.address
	if req.v.Former && a.former != "" {
		addr = a.former
	}
	return addr + e.cfg.Prefix + "/api/v1/stream/"
}

// hostRefusal is the host's error as the peer is told it (engine.md
// section 6).
func (e *Engine) hostRefusal(err error, what string) response {
	switch {
	case errors.Is(err, ErrNotPlayable):
		return refuse(probNotPlayable)
	case errors.Is(err, ErrDeliveryRefused):
		return refuse(probDelivery)
	case errors.Is(err, ErrStreamLimitReached):
		return refuse(probStreamLimit)
	case errors.Is(err, ErrServerBusy):
		return refuse(probServerBusy)
	case errors.Is(err, ErrNotFound):
		return refuse(probNotFound)
	}
	e.log.Warn("msfp: the host's playback failed", "step", what, "error", err)
	return refuse(probInternal)
}

// hostStop tells the host a playback ended, in the background.
func (e *Engine) hostStop(handle string, why EndReason) {
	if e.cfg.Playback == nil {
		return
	}
	e.background(func(ctx context.Context) {
		defer func() {
			if v := recover(); v != nil {
				e.log.Error("msfp: the host's Stop crashed", "panic", v)
			}
		}()
		ctx, cancel := context.WithTimeout(ctx, hostTime)
		defer cancel()
		if err := e.cfg.Playback.Stop(ctx, handle, why); err != nil {
			e.log.Warn("msfp: the host's Stop failed", "error", err)
		}
	})
}

// endPlayback ends a live playback, once: its ticket stops working at
// once, its open responses are cut, its stream is freed, the host is
// told (MSFP playback section 6.3).
func (e *Engine) endPlayback(pb *playback, why EndReason) {
	ps := &e.plays
	ps.mu.Lock()
	if ps.byID[pb.id] != pb {
		ps.mu.Unlock()
		return
	}
	delete(ps.byID, pb.id)
	delete(ps.byTicket, pb.ticket)
	if ps.ended == nil {
		ps.ended = map[string]endedPlay{}
	}
	ps.ended[pb.id] = endedPlay{peer: pb.peer, reason: why, at: e.now(), purpose: pb.purpose, title: pb.title,
		queue: pb.queue, handle: pb.handle, playedAt: pb.started, lastAt: pb.lastAt}
	ps.mu.Unlock()
	pb.cancel()
	pb.slot.Release()
	e.hostStop(pb.handle, why)
	e.log.Info("msfp: a peer's playback ended", "peer", pb.peer, "playback", pb.id, "why", why)
	e.notify(PlaybackEnded{Peer: pb.peer, Name: pb.peerName, Playback: pb.id, Title: pb.title, Reason: why})
}

// endPlaybacks ends every live playback for which which says so.
func (e *Engine) endPlaybacks(which func(*playback) bool, why EndReason) {
	ps := &e.plays
	ps.mu.Lock()
	var end []*playback
	for _, pb := range ps.byID {
		if which(pb) {
			end = append(end, pb)
		}
	}
	ps.mu.Unlock()
	for _, pb := range end {
		e.endPlayback(pb, why)
	}
}

// expirePlaybacks ends the playbacks with neither progress nor a media
// request for 2 minutes (an open response counts as use; never a
// download), those past their life (a download's 24 hours, a scan's 10
// minutes) and those longer than LongestPlayback (a download's 24 hours
// stand instead); it forgets endings past their time.
func (e *Engine) expirePlaybacks(force bool) {
	now := e.now()
	ps := &e.plays
	ps.mu.Lock()
	if !force && now.Sub(ps.expired) < expiryEvery {
		ps.mu.Unlock()
		return
	}
	ps.expired = now
	var expired, tooLong []*playback
	for _, pb := range ps.byID {
		if pb.open > 0 {
			pb.used = now
		}
		life := e.cfg.LongestPlayback
		if l := wire.Life(pb.purpose); l > 0 && (pb.purpose == wire.PurposeDownload || l < life) {
			life = l
		}
		switch {
		case now.Sub(pb.started) >= life:
			tooLong = append(tooLong, pb)
		case wire.Idles(pb.purpose) && now.Sub(pb.used) >= wire.IdleEnd:
			expired = append(expired, pb)
		}
	}
	var past []DownloadRecord
	for id, ended := range ps.ended {
		if ended.purpose == wire.PurposeDownload {
			if now.Sub(ended.playedAt) > wire.LateReports {
				delete(ps.ended, id)
				past = append(past, DownloadRecord{ID: id})
			}
		} else if now.Sub(ended.at) >= endedKept {
			delete(ps.ended, id)
		}
	}
	ps.mu.Unlock()
	e.dropDownloads(context.Background(), past)
	for _, pb := range tooLong {
		e.endPlayback(pb, EndTooLong)
	}
	for _, pb := range expired {
		e.endPlayback(pb, EndExpired)
	}
}

// used notes a playback's use: a progress, a media request.
func (e *Engine) used(pb *playback) {
	ps := &e.plays
	ps.mu.Lock()
	pb.used, pb.active = e.now(), true
	ps.mu.Unlock()
}

// playState is the playback a report names as wire's rules see it, and the
// live playback when it is live: a playback of another pairing, or one
// never given, has no state.
func (e *Engine) playState(peer, id string) (wire.PlaybackState, *playback) {
	ps := &e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if pb := ps.byID[id]; pb != nil && pb.peer == peer {
		st := wire.StateStarted
		if pb.active {
			st = wire.StateActive
		}
		return wire.PlaybackState{State: st, Purpose: pb.purpose, Title: pb.title, Queue: pb.queue,
			PlayedAt: pb.started, LastAt: pb.lastAt}, pb
	}
	if ended, ok := ps.ended[id]; ok && ended.peer == peer {
		return wire.PlaybackState{State: wire.StateEnded, Purpose: ended.purpose, Title: ended.title, Queue: ended.queue,
			PlayedAt: ended.playedAt, LastAt: ended.lastAt}, nil
	}
	return wire.PlaybackState{}, nil
}

// report answers a peer's progress or stop (MSFP playback sections 3, 4
// and 7): read as MSFP reads it, applied to the playback's state, kept
// reports passed on to the host; a stop ends a live playback.
func (e *Engine) report(req *request, call string) response {
	p := req.peer
	if p == nil || p.State != Paired {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartPlayback) {
		return refuse(probNotSupported)
	}
	now := e.now()
	r, err := wire.ReadReport(call, req.body, now)
	if err != nil {
		return refusalOf(err)
	}
	state, pb := e.playState(p.ID, r.ID)
	out, err := state.Apply(call, r, now)
	if err != nil {
		return refusalOf(err)
	}
	if pb != nil && call == wire.CallProgress {
		e.used(pb)
	}
	if (out.Effect == wire.EffectRecorded || out.Effect == wire.EffectRecordedAndEnded) && e.cfg.Playback != nil {
		handle := ""
		var kept *DownloadRecord // a download's, its latest at kept across restarts
		ps := &e.plays
		ps.mu.Lock()
		if pb != nil {
			pb.lastAt, handle = out.At, pb.handle
			if pb.purpose == wire.PurposeDownload {
				kept = &DownloadRecord{ID: pb.id, Peer: pb.peer, Title: pb.title, Queue: pb.queue, Handle: pb.handle,
					PlayedAt: pb.started, LastAt: pb.lastAt.UTC()}
			}
		} else if ended, ok := ps.ended[r.ID]; ok {
			ended.lastAt, handle = out.At, ended.handle
			ps.ended[r.ID] = ended
			if ended.purpose == wire.PurposeDownload {
				kept = &DownloadRecord{ID: r.ID, Peer: ended.peer, Title: ended.title, Queue: ended.queue, Handle: ended.handle,
					PlayedAt: ended.playedAt, LastAt: ended.lastAt.UTC()}
			}
		}
		ps.mu.Unlock()
		if kept != nil {
			e.keepDownload(req.r.Context(), *kept)
		}
		hr := Report{Handle: handle, Title: out.Title, PositionMs: out.PositionMs, HasPosition: out.HasPosition,
			At: out.At, Stop: call == wire.CallStop, Late: pb == nil}
		if err := e.cfg.Playback.Progress(req.r.Context(), hr); err != nil {
			e.log.Info("msfp: the host's Progress failed", "error", err)
		}
	}
	if pb != nil && call == wire.CallStop {
		e.endPlayback(pb, EndStopped)
	}
	return answer(struct{}{})
}

// progress is a peer telling that a playback is still watched (MSFP
// playback section 3).
func (e *Engine) progress(req *request) response { return e.report(req, wire.CallProgress) }

// stop is a peer telling that a playback is over (MSFP playback section
// 4): 200 whether or not it was live; another pairing's changes nothing.
func (e *Engine) stop(req *request) response { return e.report(req, wire.CallStop) }

// StopPlayback ends a peer's playback here from the host's side, for
// why (MSFP playback section 6.3): EndOperator when the operator stops
// it, EndExpired or EndTooLong when the host's own idle or time rules
// end it, EndNotOffered when its copy is no longer shared; "" is
// EndOperator. Errors: ErrNotFound, ErrInvalidReason for another reason (the
// viewer's stop, the pairing's end are the engine's own).
func (e *Engine) StopPlayback(id string, why EndReason) error {
	switch why {
	case "":
		why = EndOperator
	case EndOperator, EndExpired, EndTooLong, EndNotOffered:
	default:
		return ErrInvalidReason
	}
	ps := &e.plays
	ps.mu.Lock()
	pb := ps.byID[id]
	ps.mu.Unlock()
	if pb == nil {
		return ErrNotFound
	}
	e.endPlayback(pb, why)
	return nil
}

// PlaybackView is a peer's playback here, for the host's live view: the
// peer (never a person), the title played, how, for what purpose, since
// when, and whether it was used since it started ("started" or
// "active"). Handle is the host's own (Started.Handle), for a host that
// ends a playback from its side (StopPlayback with ID and its reason).
type PlaybackView struct {
	ID, Peer, PeerName, Title, Method, Purpose, Handle string
	Since                                              time.Time
	State                                              string
}

// Playbacks lists the peers' playbacks here, live, oldest first.
func (e *Engine) Playbacks() []PlaybackView {
	ps := &e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]PlaybackView, 0, len(ps.byID))
	for _, pb := range ps.byID {
		v := PlaybackView{ID: pb.id, Peer: pb.peer, PeerName: pb.peerName, Title: pb.title, Method: pb.method, Purpose: pb.purpose,
			Handle: pb.handle, Since: pb.started, State: "started"}
		if pb.active {
			v.State = "active"
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b PlaybackView) int { return cmp.Or(a.Since.Compare(b.Since), strings.Compare(a.ID, b.ID)) })
	return out
}

// playAllowed says whether a peer may still use a playback here: paired,
// not suspended, its title's library still offered (MSFP media section
// 4). Read from storage at each media request, so an ending noticed only
// then still stops the ticket.
func (e *Engine) playAllowed(ctx context.Context, pb *playback) bool {
	p, err := e.peerRecord(ctx, pb.peer)
	switch {
	case errors.Is(err, ErrNotFound):
		e.endPlayback(pb, EndPairingEnded)
		return false
	case err != nil:
		return false
	case p.State != Paired || !p.Suspended.IsZero():
		e.endPlayback(pb, EndPairingEnded)
		return false
	case !stillOffered(p, pb.libraries):
		e.endPlayback(pb, EndNotOffered)
		return false
	}
	return true
}

// pacer paces a download's bytes at its speed cap (MSFP playback section
// 7, media section 4.1): over any 10 seconds, never more than the cap
// allows, all its fetches together; it slows the body, never refuses.
// It lets a second's worth through at once and refills at nine tenths of
// the cap, so that a second's burst and nine seconds' refill stay within
// ten seconds' worth.
type pacer struct {
	mu     sync.Mutex
	clock  Clock
	rate   float64 // bytes per second
	tokens float64
	last   time.Time
}

func newPacer(kbps int, clock Clock) *pacer {
	rate := float64(kbps) * 1000 / 8
	return &pacer{clock: clock, rate: rate, tokens: rate, last: clock.Now()}
}

// take waits until n bytes may go, or ctx is done: on the engine's
// clock (Config.Clock), and with its timers when it has some (Timers).
func (p *pacer) take(ctx context.Context, n int) error {
	p.mu.Lock()
	now := p.clock.Now()
	if now.After(p.last) {
		p.tokens = min(p.rate, p.tokens+now.Sub(p.last).Seconds()*p.rate*0.9)
		p.last = now
	}
	p.tokens -= float64(n)
	debt := -p.tokens
	p.mu.Unlock()
	if debt <= 0 {
		return nil
	}
	return sleep(ctx, p.clock, time.Duration(debt/(p.rate*0.9)*float64(time.Second)))
}

// sleep waits d on clock, or until ctx is done: with the clock's timer
// when it is a Timers, the system's otherwise.
func sleep(ctx context.Context, clock Clock, d time.Duration) error {
	var c <-chan time.Time
	if t, ok := clock.(Timers); ok {
		c = t.After(d)
	} else {
		t := time.NewTimer(d)
		defer t.Stop()
		c = t.C
	}
	select {
	case <-c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
