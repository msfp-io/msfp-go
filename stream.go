package msfp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// Media, on the owning side (MSFP media sections 1 to 4, video section
// 6): the stream routes of peers' tickets. Every request is checked
// before the host serves a byte: the ticket, live and of a pairing still
// paired, and of a kind that opens that URL; GET only (HEAD and the
// rest 405); no query; no browser's headers; a Range header of one of
// the three forms, on the file and segments only; the fetches at once.
// The file is served by byte ranges against the copy's exact size and
// etag (a scan's within its cap, a download's paced); the host's
// playlists are checked against MSFP's subset, its subtitles and fonts
// rebuilt or checked, before they leave.

// Bounds of the fetches at once on a peer's tickets (MSFP media section
// 2.4): per ticket and per peer, each with a queue as long, waited in
// for 10 seconds at most.
const (
	fetchesPerPlayback = 8
	fetchesPerPeer     = 64
	fetchWait          = 10 * time.Second
)

// paceChunk is how many bytes of a download go at a time.
const paceChunk = 16 << 10

// streamRoute is where StreamHandler is mounted, after Config.Prefix.
const streamRoute = "/api/v1/stream/"

// StreamHandler serves the media of peers' playbacks here, under
// <Prefix>/api/v1/stream/: mount it there, on the raw path, with no
// authentication in front and no compression of its answers. A ticket
// the engine doesn't know (one the host gave its own people, or one
// ended) goes to fallback, which may be nil: the host's not-found answer
// then (Config.NotFound). The fallback hears GET only on the media URL
// forms (ParseMediaPath, and <ticket>/image): HEAD and every other
// method get 405 with Allow: GET from the engine, whatever the ticket
// (MSFP media section 2.2). A host that answers its own clients' HEAD
// (a copy presented as a file) does so on a route of its own.
func (e *Engine) StreamHandler(fallback http.Handler) http.Handler {
	if fallback == nil {
		fallback = e.cfg.NotFound
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.serveStream(w, r, fallback)
	})
}

// partRe is a subtitle part's, a sheet's, a font's or a queue entry's
// number, without leading zeros.
var partRe = regexp.MustCompile(`^(0|[1-9][0-9]{0,3})$`)

// ParseMediaPath reads what a URL asks of a playback, after its ticket
// (or, at the host's own viewer route, after its own ID for the
// playback): "master.m3u8", "file", "subtitles/<track>/<part>" or
// ".../whole", "ass/<track>/<part>" or ".../whole",
// "pictures/<track>/<part>", "fonts/<n>", "trickplay/<n>",
// "queue/<n>/file", "queue/<n>/master.m3u8" or "queue/<n>/<name>", or a
// bare name, a playlist when it ends in .m3u8 and a segment otherwise.
// Each part is checked as MSFP media section 1 says, compared as sent:
// nothing decoded; false for anything else. A host's viewer route serves
// playlists unchanged this way: their bare names resolve under it.
func ParseMediaPath(rest string) (MediaPath, bool) {
	parts := strings.Split(rest, "/")
	for _, s := range parts {
		if !wire.FileName(s) {
			return MediaPath{}, false
		}
	}
	number := func(s string, most int) (int, bool) {
		n, err := strconv.Atoi(s)
		return n, err == nil && partRe.MatchString(s) && n <= most
	}
	entry := 0
	if len(parts) == 3 && parts[0] == "queue" {
		n, ok := number(parts[1], wire.MaxQueue-1)
		if !ok {
			return MediaPath{}, false
		}
		entry, parts = n+1, parts[2:]
	}
	switch {
	case len(parts) == 1 && parts[0] == "master.m3u8":
		return MediaPath{Kind: MediaMaster, Entry: entry}, true
	case len(parts) == 1 && parts[0] == "file":
		return MediaPath{Kind: MediaFile, Entry: entry}, true
	case len(parts) == 1 && strings.HasSuffix(parts[0], ".m3u8"):
		return MediaPath{Kind: MediaPlaylist, Name: parts[0], Entry: entry}, true
	case len(parts) == 1:
		return MediaPath{Kind: MediaSegment, Name: parts[0], Entry: entry}, true
	case entry > 0:
		return MediaPath{}, false
	case len(parts) == 2 && parts[0] == "trickplay":
		n, ok := number(parts[1], wire.MaxSheets-1)
		return MediaPath{Kind: MediaSheet, Sheet: n}, ok
	case len(parts) == 2 && parts[0] == "fonts":
		n, ok := number(parts[1], wire.MaxFonts-1)
		return MediaPath{Kind: MediaFont, Font: n}, ok
	case len(parts) == 3 && (parts[0] == "subtitles" || parts[0] == "ass" || parts[0] == "pictures"):
		kind := map[string]MediaKind{"subtitles": MediaSubtitles, "ass": MediaASS, "pictures": MediaPictures}[parts[0]]
		if parts[2] == "whole" && kind != MediaPictures {
			return MediaPath{Kind: kind, Track: parts[1], Whole: true}, true
		}
		n, ok := number(parts[2], 9999)
		return MediaPath{Kind: kind, Track: parts[1], Part: n}, ok
	}
	return MediaPath{}, false
}

// mediaPath reads what a request asks of a peer's playback here: false
// for anything its ticket doesn't open (media section 4: a scan's opens
// the file only; a download's the stream, its subtitles and fonts) or
// the playback doesn't serve (another protocol's names, a queue entry
// it hasn't, a track it doesn't serve apart, a sheet or font past the
// count).
func mediaPath(rest string, pb *playback) (MediaPath, bool) {
	p, ok := ParseMediaPath(rest)
	if !ok {
		return MediaPath{}, false
	}
	switch p.Kind {
	case MediaFile, MediaMaster, MediaPlaylist, MediaSegment:
		ent, ok := pb.entries[p.Entry]
		if !ok || pb.purpose == wire.PurposeScan && (p.Kind != MediaFile || p.Entry != 0) ||
			p.Entry > 0 && pb.purpose != wire.PurposePlay {
			return MediaPath{}, false
		}
		if p.Kind == MediaPlaylist || p.Kind == MediaSegment {
			// Exactly the names its playlists gave (media section 1).
			return p, ent.protocol == "hls" && pb.names[strconv.Itoa(p.Entry)+"/"+p.Name]
		}
		return p, ent.protocol == "file" == (p.Kind == MediaFile)
	case MediaSheet:
		return p, p.Sheet < pb.sheets
	case MediaFont:
		return p, p.Font < pb.fonts
	}
	how := pb.subtitles[p.Track]
	switch p.Kind {
	case MediaSubtitles:
		return p, how.text
	case MediaASS:
		return p, how.ass
	}
	return p, how.pictures
}

// ParseRange reads a Range header of MSFP media section 2.1: one range of
// bytes in one of three forms, each number 1 to 15 digits; never several
// ranges, "bytes=-" or "bytes=-0", nor a first byte past the last.
func ParseRange(h string) (Range, bool) {
	r, ok := wire.ParseRange(h)
	if !ok {
		return Range{}, false
	}
	return Range{First: r.First, Last: r.Last, Suffix: r.Suffix}, true
}

// mediaMethod answers a media request of another method than GET with
// 405 and Allow: GET, HEAD included, never 404 (MSFP media section
// 2.2): false when it did.
func mediaMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	w.WriteHeader(http.StatusMethodNotAllowed)
	return false
}

// plainMediaRequest says whether a media request is as a viewer's
// server sends it: no query, none of a browser's headers (MSFP media
// section 2): a browser must never use a peer's ticket.
func plainMediaRequest(r *http.Request) bool {
	return !strings.Contains(r.RequestURI, "?") && r.URL.RawQuery == "" && r.Header.Get("Origin") == "" &&
		r.Header.Get("Sec-Fetch-Site") == "" && r.Header.Get("Sec-Fetch-Mode") == ""
}

// serveStream serves a request on the stream route.
func (e *Engine) serveStream(w http.ResponseWriter, r *http.Request, fallback http.Handler) {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), e.cfg.Prefix+streamRoute)
	ticket, name, _ := strings.Cut(rest, "/")
	if !ok || !wire.ID(ticket) {
		fallback.ServeHTTP(w, r)
		return
	}
	// HEAD and the rest get 405 on every media URL, whatever its ticket:
	// live, ended, or never given (MSFP media section 2.2).
	if _, media := ParseMediaPath(name); (media || name == "image") && !mediaMethod(w, r) {
		return
	}
	if e.serveImage(w, r, ticket, name) {
		return
	}
	ps := &e.plays
	ps.mu.Lock()
	pb := ps.byTicket[sha256.Sum256([]byte(ticket))]
	ps.mu.Unlock()
	if pb == nil {
		// A ticket of the host's own people, or one that ended: the
		// host's to answer (it answers 404 to one it doesn't know).
		fallback.ServeHTTP(w, r)
		return
	}
	if !mediaMethod(w, r) {
		return
	}
	notFound := func() { e.cfg.NotFound.ServeHTTP(w, r) }
	if !plainMediaRequest(r) {
		notFound()
		return
	}
	path, ok := mediaPath(name, pb)
	if !ok || e.cfg.Media == nil || !e.playAllowed(r.Context(), pb) {
		notFound()
		return
	}
	var rng *Range
	if h := r.Header.Values("Range"); len(h) > 0 && path.Kind == MediaSegment {
		parsed, ok := ParseRange(h[0])
		if !ok || len(h) > 1 {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		rng = &parsed
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(pb.ctx, cancel)
	defer stop()
	freeOne, ok := e.fetchSlots.take(ctx, pb.id)
	if !ok {
		e.busy(w, ctx)
		return
	}
	defer freeOne()
	freeAll, ok := e.peerFetchSlots.take(ctx, pb.peer)
	if !ok {
		e.busy(w, ctx)
		return
	}
	defer freeAll()
	e.used(pb)
	ps.mu.Lock()
	pb.open++
	ps.mu.Unlock()
	defer func() {
		ps.mu.Lock()
		pb.open--
		ps.mu.Unlock()
		e.used(pb)
	}()
	r = r.WithContext(ctx)
	switch path.Kind {
	case MediaMaster, MediaPlaylist:
		e.servePlaylist(w, r, pb, path)
		return
	case MediaFile:
		e.serveFile(w, r, pb, path)
		return
	case MediaSubtitles, MediaASS, MediaPictures, MediaFont:
		e.serveSmall(w, r, pb, path)
		return
	case MediaSheet:
		e.serveSheet(w, r, pb.handle, path, pb.sheetWidth, pb.sheetHeight)
		return
	}
	sink := &httpSink{w: w, ctx: ctx, pace: pb.pace}
	err := e.cfg.Media.Serve(ctx, pb.handle, path, rng, sink)
	if sink.started || ctx.Err() != nil {
		return
	}
	e.mediaError(w, r, err)
}

// serveFile serves the file of a playback, or of a queue entry, by byte
// ranges (MSFP media sections 2.1, 3.2, 4.1 and 4.2): its exact size and
// etag, 206 for a range it can satisfy, 416 with bytes */<size> for one
// it can't or a malformed one; under a scan's ticket, a range only, at
// most 2 at once, cut at the cap; a download's paced.
func (e *Engine) serveFile(w http.ResponseWriter, r *http.Request, pb *playback, path MediaPath) {
	ent := pb.entries[path.Entry]
	size := ent.size
	unsatisfied := func() {
		w.Header().Set("Content-Range", wire.UnsatisfiedRange(size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}
	hs := r.Header.Values("Range")
	if len(hs) > 1 {
		unsatisfied()
		return
	}
	h := ""
	if len(hs) == 1 {
		h = hs[0]
	}
	start, end := int64(0), size-1
	var unsent int64 // of a scan's range held, what wasn't sent
	switch {
	case pb.purpose == wire.PurposeScan:
		ps := &e.plays
		ps.mu.Lock()
		if pb.scanReads >= wire.ScanReadsAtOnce {
			ps.mu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		s, en, ok := wire.ScanSpan(pb.scanSent, h, size, pb.scanCap)
		if !ok {
			ps.mu.Unlock()
			unsatisfied()
			return
		}
		// The range is held against the cap while it is served, so that
		// two fetches at once never pass it together; what wasn't sent
		// (a fetch cut, a host's failure) is given back after: the cap
		// counts the bytes sent (MSFP media section 4.2).
		pb.scanSent += en - s + 1
		pb.scanReads++
		ps.mu.Unlock()
		start, end = s, en
		defer func() {
			ps.mu.Lock()
			pb.scanReads--
			pb.scanSent -= unsent
			ps.mu.Unlock()
		}()
	case h != "":
		br, ok := wire.ParseRange(h)
		if !ok {
			unsatisfied()
			return
		}
		s, en, ok := br.Span(size)
		if !ok {
			unsatisfied()
			return
		}
		start, end = s, en
	}
	sink := &fileSink{w: w, ctx: r.Context(), etag: ent.etag, mediaType: ent.mediaType, left: end - start + 1, pace: pb.pace,
		status: http.StatusOK}
	var rng *Range
	if h != "" {
		rng = &Range{First: start, Last: end}
		sink.status, sink.contentRange = http.StatusPartialContent, wire.ContentRange(start, end, size)
	}
	err := e.cfg.Media.Serve(r.Context(), pb.handle, path, rng, sink)
	unsent = sink.left
	switch {
	case sink.started || r.Context().Err() != nil:
	case err == nil && sink.left == 0:
		sink.begin()
	default:
		e.mediaError(w, r, err)
	}
}

// fileSink is the ResponseSink of the file: the engine's status and
// headers (the copy's media type from the catalog), at most left bytes, paced
// for a download.
type fileSink struct {
	w            http.ResponseWriter
	ctx          context.Context
	status       int
	mediaType    string // the copy's, from the catalog
	contentRange string
	etag         string
	left         int64
	pace         *pacer
	started      bool
}

func (s *fileSink) Start(int, string, int64, string) {
	if !s.started {
		s.begin()
	}
}

// begin writes the status and headers, once.
func (s *fileSink) begin() {
	if s.started {
		return
	}
	s.started = true
	h := s.w.Header()
	h.Set("Content-Type", wire.RelayedFileType(s.mediaType))
	h.Set("Content-Length", strconv.FormatInt(s.left, 10))
	h.Set("Accept-Ranges", "bytes")
	h.Set("ETag", `"`+s.etag+`"`)
	if s.contentRange != "" {
		h.Set("Content-Range", s.contentRange)
	}
	s.w.WriteHeader(s.status)
}

// errPastRange is a host writing past the bytes asked: what follows
// isn't sent.
var errPastRange = errors.New("msfp: the host wrote past the range")

func (s *fileSink) Write(b []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	s.begin()
	written := 0
	for len(b) > 0 {
		if s.left == 0 {
			return written, errPastRange
		}
		n := int(min(int64(len(b)), s.left, paceChunk))
		if s.pace != nil {
			if err := s.pace.take(s.ctx, n); err != nil {
				return written, err
			}
		}
		m, err := s.w.Write(b[:n])
		written += m
		s.left -= int64(m)
		if err != nil {
			return written, err
		}
		b = b[n:]
	}
	return written, nil
}

// busy answers 503 to a fetch over the bounds, unless it was cut.
func (e *Engine) busy(w http.ResponseWriter, ctx context.Context) {
	if ctx.Err() == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

// mediaError answers a media request the host failed: its NotFound,
// RangeNotSatisfiable and ServerBusy as 404, 416 and 503; anything else
// 500, logged.
func (e *Engine) mediaError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		e.log.Warn("msfp: the host's media answered nothing")
		w.WriteHeader(http.StatusInternalServerError)
	case errors.Is(err, ErrNotFound):
		e.cfg.NotFound.ServeHTTP(w, r)
	case errors.Is(err, ErrRangeNotSatisfiable):
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	case errors.Is(err, ErrServerBusy):
		w.WriteHeader(http.StatusServiceUnavailable)
	default:
		e.log.Warn("msfp: the host's media failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// servePlaylist serves a playlist the host made, once checked against
// MSFP's subset (MSFP media section 3.1): complete, and in a master
// playlist audio renditions named by the copy's audio track IDs only.
// One the host made outside it is refused, with a warning, rather than
// refused by every peer.
//
// The first served of each name is kept and served again on every fetch
// under the ticket: the same playlist on every fetch (section 3.1),
// whatever the host makes the next time. At most playlistsKept are kept,
// of playlistBytesKept bytes together; past that, each is checked only.
func (e *Engine) servePlaylist(w http.ResponseWriter, r *http.Request, pb *playback, path MediaPath) {
	key := strconv.Itoa(path.Entry) + "/" + path.Name
	ps := &e.plays
	ps.mu.Lock()
	kept, ok := pb.playlists[key]
	ps.mu.Unlock()
	if ok {
		writePlaylist(w, kept)
		return
	}
	var buf boundedSink
	buf.max = wire.MaxPlaylist
	err := e.cfg.Media.Serve(r.Context(), pb.handle, path, nil, &buf)
	if !buf.started {
		e.mediaError(w, r, err)
		return
	}
	if err != nil || buf.over || buf.status != http.StatusOK {
		e.log.Warn("msfp: the host's playlist couldn't be served", "error", err, "status", buf.status)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	subset := wire.MediaSubset
	if path.Kind == MediaMaster {
		subset = wire.MasterSubset
	}
	kept, err = wire.FilterPlaylist(buf.body.Bytes(), subset, func(id string) bool { return pb.audio[id] })
	if err == nil && !bytes.Equal(kept, normalLines(buf.body.Bytes())) {
		err = errors.New("tags outside MSFP's subset")
	}
	if err != nil {
		e.log.Warn("msfp: the host made a playlist outside what MSFP allows; it isn't sent", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if path.Kind == MediaMaster && path.Entry == 0 {
		var missing []string
		if kept, missing = masterAttributes(kept, pb.facts); len(missing) > 0 {
			e.log.Warn("msfp: the host's master playlist lacks attributes MSFP asks for", "missing", strings.Join(missing, ","))
		}
	}
	ps.mu.Lock()
	for _, n := range playlistNames(kept) {
		if pb.names == nil {
			pb.names = map[string]bool{}
		}
		pb.names[strconv.Itoa(path.Entry)+"/"+n] = true
	}
	if first, ok := pb.playlists[key]; ok {
		kept = first // another fetch kept it meanwhile
	} else if len(pb.playlists) < playlistsKept && pb.playlistBytes+len(kept) <= playlistBytesKept {
		if pb.playlists == nil {
			pb.playlists = map[string][]byte{}
		}
		pb.playlists[key] = kept
		pb.playlistBytes += len(kept)
	}
	ps.mu.Unlock()
	writePlaylist(w, kept)
}

// uriValues are the URI= values of a playlist's tags.
var uriValues = regexp.MustCompile(`URI="([^"]*)"`)

// playlistNames are the names a checked playlist gives: its URI lines
// and the URI= values of its tags (renditions, EXT-X-MAP).
func playlistNames(kept []byte) []string {
	names := wire.URILines(kept)
	for _, m := range uriValues.FindAllSubmatch(kept, -1) {
		names = append(names, string(m[1]))
	}
	return names
}

// Bounds of the playlists a playback keeps, to serve them the same.
const (
	playlistsKept     = 16
	playlistBytesKept = 4 << 20
)

// writePlaylist answers with a checked playlist.
func writePlaylist(w http.ResponseWriter, kept []byte) {
	h := w.Header()
	h.Set("Content-Type", wire.HLSType)
	h.Set("Content-Length", strconv.Itoa(len(kept)))
	w.WriteHeader(http.StatusOK)
	w.Write(kept)
}

// smallRules are, for what is read whole, its bound as read, how it is
// rebuilt or checked, and its type.
func smallRules(p MediaPath) (most int, rebuild func([]byte) ([]byte, string, bool)) {
	text := func(f func([]byte) ([]byte, bool), ct string) func([]byte) ([]byte, string, bool) {
		return func(b []byte) ([]byte, string, bool) { out, ok := f(b); return out, ct, ok }
	}
	switch {
	case p.Kind == MediaSubtitles && p.Whole:
		return wire.MaxWholeText, text(wire.RebuildWholeWebVTT, "text/vtt; charset=utf-8")
	case p.Kind == MediaSubtitles:
		return wire.MaxWebVTT, text(wire.RebuildWebVTT, "text/vtt; charset=utf-8")
	case p.Kind == MediaASS && p.Whole:
		return wire.MaxWholeText, text(wire.RebuildWholeASS, "text/x-ass; charset=utf-8")
	case p.Kind == MediaASS:
		return wire.MaxASS, text(wire.RebuildASS, "text/x-ass; charset=utf-8")
	case p.Kind == MediaPictures:
		return wire.MaxPGS, func(b []byte) ([]byte, string, bool) {
			return b, "application/octet-stream", wire.CheckPGS(b) == nil
		}
	}
	return wire.MaxFont, func(b []byte) ([]byte, string, bool) {
		ct, err := wire.CheckFont(b)
		return b, ct, err == nil
	}
}

// serveSmall serves what is read whole: a stretch or a whole text
// track, rebuilt as WebVTT or ASS; a stretch of a picture track, checked
// as PGS; a font, checked (MSFP video sections 6.3 to 6.6). What the
// host made outside MSFP's rules isn't sent.
func (e *Engine) serveSmall(w http.ResponseWriter, r *http.Request, pb *playback, path MediaPath) {
	most, rebuild := smallRules(path)
	buf := boundedSink{max: most}
	err := e.cfg.Media.Serve(r.Context(), pb.handle, path, nil, &buf)
	if !buf.started {
		e.mediaError(w, r, err)
		return
	}
	if err != nil || buf.over || buf.status != http.StatusOK {
		e.log.Warn("msfp: the host's subtitles or font couldn't be served", "error", err, "status", buf.status)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	body, ct, ok := rebuild(buf.body.Bytes())
	if ok && path.Kind == MediaFont {
		ps := &e.plays
		ps.mu.Lock()
		ok = fontsWithin(&pb.fontBytes, path.Font, len(body))
		ps.mu.Unlock()
	}
	if !ok {
		e.log.Warn("msfp: the host's subtitles or font are outside what MSFP allows; they aren't sent", "kind", path.Kind)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// fontsWithin notes font n of a copy, of size bytes, among those a
// playback served or relayed (*sizes, by index), and says whether they
// stay within 64 MiB in all (MSFP video section 6.6): a font past that
// isn't served, and isn't noted.
func fontsWithin(sizes *map[int]int, n, size int) bool {
	total := size
	for i, s := range *sizes {
		if i != n {
			total += s
		}
	}
	if total > wire.MaxFontsBytes {
		return false
	}
	if *sizes == nil {
		*sizes = map[int]int{}
	}
	(*sizes)[n] = size
	return true
}

// serveSheet serves a preview sheet the host made, checked (a JPEG
// within the layout, MSFP video section 6.2) and re-encoded with the
// SheetEncoder, so that only its pixels leave: whatever else the host's
// file holds (metadata, trailing bytes) stays here. One outside the
// layout isn't sent.
func (e *Engine) serveSheet(w http.ResponseWriter, r *http.Request, handle string, path MediaPath, width, height int) {
	ctx := r.Context()
	select {
	case e.sheetGate <- struct{}{}:
		defer func() { <-e.sheetGate }()
	case <-ctx.Done():
		return
	}
	buf := boundedSink{max: wire.MaxSheetBytes}
	err := e.cfg.Media.Serve(ctx, handle, path, nil, &buf)
	if !buf.started {
		e.mediaError(w, r, err)
		return
	}
	if err != nil || buf.over || buf.status != http.StatusOK {
		e.log.Warn("msfp: the host's preview sheet couldn't be served", "error", err, "status", buf.status)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var sheet []byte
	if sheetFits(buf.body.Bytes(), width, height) {
		sheet, err = e.cfg.SheetEncoder.Reencode(buf.body.Bytes(), width, height)
	}
	if sheet == nil || err != nil {
		e.log.Warn("msfp: the host's preview sheet isn't a JPEG within its layout; it isn't sent", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Content-Length", strconv.Itoa(len(sheet)))
	w.WriteHeader(http.StatusOK)
	w.Write(sheet)
}

// normalLines is a playlist with its empty lines dropped, each line
// ending in a line feed, as FilterPlaylist writes what it keeps.
func normalLines(b []byte) []byte {
	var out bytes.Buffer
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSuffix(line, "\r"); line != "" {
			out.WriteString(line + "\n")
		}
	}
	return out.Bytes()
}

// NewHTTPSink is a ResponseSink on an http.ResponseWriter, for a host's
// viewer route around Fetch: the status and only the headers MSFP needs
// (Content-Type, Content-Length, Content-Range, Accept-Ranges, ETag), no
// content coding. Writes fail once ctx is done.
func NewHTTPSink(ctx context.Context, w http.ResponseWriter) ResponseSink {
	return &httpSink{w: w, ctx: ctx}
}

// ETagSink is what the ResponseSink of NewHTTPSink implements besides:
// Fetch gives the file's ETag through it before Start, so that a host
// presenting a copy as a file of its own may give it to its clients.
type ETagSink interface {
	SetETag(etag string)
}

// httpSink is a ResponseSink on an http.ResponseWriter: the status and
// only the headers MSFP needs, no content coding; a download's body
// paced.
type httpSink struct {
	w       http.ResponseWriter
	ctx     context.Context
	pace    *pacer
	etag    string
	started bool
}

func (s *httpSink) SetETag(etag string) { s.etag = etag }

func (s *httpSink) Start(status int, contentType string, length int64, contentRange string) {
	if s.started {
		return
	}
	s.started = true
	h := s.w.Header()
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	if length >= 0 {
		h.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	if contentRange != "" {
		h.Set("Content-Range", contentRange)
	}
	if status == http.StatusOK || status == http.StatusPartialContent {
		h.Set("Accept-Ranges", "bytes")
		if s.etag != "" {
			h.Set("ETag", `"`+s.etag+`"`)
		}
	}
	s.w.WriteHeader(status)
}

func (s *httpSink) Write(b []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		// The playback ended, or the peer left: nothing more goes.
		return 0, err
	}
	if !s.started {
		s.Start(http.StatusOK, "", -1, "")
	}
	if s.pace == nil {
		return s.w.Write(b)
	}
	written := 0
	for len(b) > 0 {
		n := min(len(b), paceChunk)
		if err := s.pace.take(s.ctx, n); err != nil {
			return written, err
		}
		m, err := s.w.Write(b[:n])
		written += m
		if err != nil {
			return written, err
		}
		b = b[n:]
	}
	return written, nil
}

// boundedSink is a ResponseSink that keeps an answer in memory, up to
// max bytes.
type boundedSink struct {
	max          int
	started      bool
	over         bool
	status       int
	contentType  string
	length       int64
	contentRange string
	body         bytes.Buffer
}

func (s *boundedSink) Start(status int, contentType string, length int64, contentRange string) {
	if !s.started {
		s.started, s.status, s.contentType, s.length, s.contentRange = true, status, contentType, length, contentRange
	}
}

func (s *boundedSink) Write(b []byte) (int, error) {
	if !s.started {
		s.Start(http.StatusOK, "", -1, "")
	}
	if s.body.Len()+len(b) > s.max {
		s.over = true
		return 0, errors.New("msfp: an answer over its bound")
	}
	return s.body.Write(b)
}
