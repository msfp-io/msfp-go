package msfp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// Media, on the viewer's side (MSFP media sections 2 and 3, video section
// 6): the relay. The host's viewer route maps its own URL to a MediaPath
// and calls Fetch; the engine fetches from the peer's stream root with
// the playback's ticket, which never leaves the engine, and checks
// everything before a byte reaches the viewer.

// Bounds of the relay (MSFP media section 2.4).
const (
	// smallReadTime bounds the reading whole of a playlist, a subtitle
	// part, a PGS part, a font or a sheet.
	smallReadTime = 2 * time.Minute
	// smallReads and sheetReads bound how many of them are held in memory
	// at once across the server.
	smallReads = 4
	sheetReads = 4
)

// Fetch serves a piece of a playback on a peer, for the viewer playing
// it: the same MediaPath the owning side serves (MediaPath.Entry for a
// queue entry's stream); rng is the viewer's Range header as received
// ("" for none), sent on for the file and segments only, once
// well-formed. Content-Type is set by the engine; an out that implements
// ETagSink is given the file's ETag first. The file's answers are
// checked against the copy's etag and exact size (a different one: the
// copy changed, ErrNotFound, and Restart won't join it). A fetch is cut
// when its playback ends. Errors (nothing written then): ErrNotFound (any
// failed fetch: the peer's body and headers never pass with a failure),
// ErrRangeNotSatisfiable (a malformed Range: nothing is fetched; or the
// peer's 416, a scan's past its cap among them), ErrServerBusy (the
// bounds of fetches at once, or the peer's 503).
func (e *Engine) Fetch(ctx context.Context, id string, path MediaPath, rng string, out ResponseSink) error {
	rm := e.liveRemote(id)
	if rm == nil {
		return ErrNotFound
	}
	if path.Kind == MediaSegment && e.isPlaylist(rm, path.Entry, path.Name) {
		path.Kind = MediaPlaylist
	}
	name, ok := e.relayName(rm, path)
	if !ok {
		return ErrNotFound
	}
	media := path.Kind == MediaFile || path.Kind == MediaSegment
	var want Range
	if !media {
		rng = ""
	} else if rng != "" {
		if want, ok = ParseRange(rng); !ok {
			return ErrRangeNotSatisfiable
		}
	}
	p, err := e.peerRecord(ctx, rm.peer)
	if err != nil || e.usablePeer(p, wire.PartMedia) != nil {
		// Never to a peer that can't be called, or is suspended (MSFP core
		// sections 7.5 and 12.6), whatever ended its playbacks.
		if err == nil || errors.Is(err, ErrNotFound) {
			e.endRemote(rm, EndPairingEnded, true)
		}
		return ErrNotFound
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(rm.ctx, cancel)
	defer stop()
	if !media {
		var cancelSmall context.CancelFunc
		ctx, cancelSmall = context.WithTimeout(ctx, smallReadTime)
		defer cancelSmall()
	}
	freeOne, ok := e.relaySlots.take(ctx, rm.view.ID)
	if !ok {
		return ErrServerBusy
	}
	defer freeOne()
	freeAll, ok := e.peerRelaySlots.take(ctx, rm.peer)
	if !ok {
		return ErrServerBusy
	}
	defer freeAll()
	if !media {
		gate := e.smallGate
		if path.Kind == MediaSheet {
			gate = e.sheetGate
		}
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		case <-ctx.Done():
			return ErrServerBusy
		}
	}
	res, err := e.relayGet(ctx, p, rm.base+name, rng)
	if err != nil {
		if ctx.Err() == nil {
			e.log.Info("msfp: a peer's media couldn't be fetched", "peer", rm.peer, "error", err)
		}
		return ErrNotFound
	}
	defer res.Body.Close()
	if ce := res.Header.Get("Content-Encoding"); res.Uncompressed || ce != "" && !strings.EqualFold(ce, "identity") {
		return ErrNotFound
	}
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusPartialContent:
		if rng == "" {
			return ErrNotFound
		}
	case http.StatusRequestedRangeNotSatisfiable:
		if rng != "" {
			return ErrRangeNotSatisfiable
		}
		return ErrNotFound
	case http.StatusServiceUnavailable:
		return ErrServerBusy
	default:
		return ErrNotFound
	}
	switch {
	case path.Kind == MediaFile:
		return e.relayFile(rm, path.Entry, res, rng, want, out)
	case media:
		return e.relayMedia(rm, res, rng, want, out)
	}
	return e.relaySmall(rm, path, res, out)
}

// entryBase is the folder of a stream of a playback on a peer, under its
// base: its own, or a queue entry's (media section 1).
func entryBase(entry int) string {
	if entry == 0 {
		return ""
	}
	return "queue/" + strconv.Itoa(entry-1) + "/"
}

// relayName is the name fetched under the peer's stream base for path;
// false for what the playback doesn't have.
func (e *Engine) relayName(rm *remote, path MediaPath) (string, bool) {
	rs := &e.remotes
	rs.mu.Lock()
	ent, has := rm.entries[path.Entry]
	rs.mu.Unlock()
	v := rm.view
	part := func() (string, bool) {
		if path.Whole {
			return "whole", path.Kind != MediaPictures
		}
		return strconv.Itoa(path.Part), path.Part >= 0 && path.Part <= 9999
	}
	switch path.Kind {
	case MediaFile:
		return entryBase(path.Entry) + "file", has && ent.protocol == "file"
	case MediaMaster:
		return entryBase(path.Entry) + "master.m3u8", has && ent.protocol == "hls"
	case MediaPlaylist, MediaSegment:
		return entryBase(path.Entry) + path.Name, has && ent.protocol == "hls" && wire.FileName(path.Name) && path.Name != "master.m3u8"
	case MediaSubtitles, MediaASS, MediaPictures:
		how, ok := rm.subtitles[path.Track]
		n, partOK := part()
		served := map[MediaKind]bool{MediaSubtitles: how.text, MediaASS: how.ass, MediaPictures: how.pictures}[path.Kind]
		if !ok || !served || !partOK || path.Entry != 0 {
			return "", false
		}
		return string(path.Kind) + "/" + path.Track + "/" + n, true
	case MediaFont:
		return "fonts/" + strconv.Itoa(path.Font), path.Font >= 0 && path.Font < v.Fonts && path.Entry == 0
	case MediaSheet:
		if rm.layout == nil || path.Sheet < 0 || path.Sheet >= v.Sheets || path.Entry != 0 {
			return "", false
		}
		return wire.SheetURL("", path.Sheet), true
	}
	return "", false
}

// relayGet fetches a URL of a peer's stream: a plain GET, nothing but
// Range, over a connection that needs a trusted or named certificate;
// refused for its certificate, a hello, then once more (MSFP media
// section 2). The URL holds the ticket: it is never logged.
func (e *Engine) relayGet(ctx context.Context, p *PeerRecord, url, rng string) (*http.Response, error) {
	get := func(p *PeerRecord) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, errors.New("a URL that isn't one")
		}
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		res, err := e.conns.mediaClient(p).Do(req)
		if err != nil {
			return nil, unwrapURL(err)
		}
		return res, nil
	}
	res, err := get(p)
	if err != nil && errors.Is(err, wire.ErrUntrustedCertificate) && ctx.Err() == nil {
		if _, herr := e.check1(ctx, p.ID, false); herr == nil {
			if q, qerr := e.peerRecord(ctx, p.ID); qerr == nil && e.usablePeer(q, wire.PartMedia) == nil {
				res, err = get(q)
			}
		}
	}
	return res, err
}

var contentRangeRe = regexp.MustCompile(`^bytes ([0-9]{1,15})-([0-9]{1,15})/([0-9]{1,15}|\*)$`)

// partialLength says whether a 206 answers the range asked (MSFP media
// section 2.1), and gives its length and its Content-Range.
func partialLength(h http.Header, want Range) (int64, string, bool) {
	ranges := h.Values("Content-Range")
	if len(ranges) != 1 {
		return 0, "", false
	}
	m := contentRangeRe.FindStringSubmatch(ranges[0])
	if m == nil {
		return 0, "", false
	}
	start, _ := strconv.ParseInt(m[1], 10, 64)
	end, _ := strconv.ParseInt(m[2], 10, 64)
	total := int64(-1)
	if m[3] != "*" {
		total, _ = strconv.ParseInt(m[3], 10, 64)
	}
	if start > end || total >= 0 && end >= total {
		return 0, "", false
	}
	switch {
	case want.Suffix > 0:
		if total < 0 || start != max(total-want.Suffix, 0) {
			return 0, "", false
		}
	case start != want.First || want.Last >= 0 && end > want.Last:
		return 0, "", false
	}
	n := end - start + 1
	if cl := h.Values("Content-Length"); len(cl) > 1 || len(cl) == 1 && cl[0] != strconv.FormatInt(n, 10) {
		return 0, "", false
	}
	return n, ranges[0], true
}

// strongETag is an ETag header's value, when it is a strong entity tag
// of an etag's form; "" otherwise.
func strongETag(h string) string {
	inner, ok := strings.CutPrefix(h, `"`)
	inner, ok2 := strings.CutSuffix(inner, `"`)
	if !ok || !ok2 || !wire.ID(inner) {
		return ""
	}
	return inner
}

// relayFile relays the file of a playback, or of a queue entry (MSFP
// media section 3.2): its answer checked against the copy's etag and
// exact size (learnt from the first answer when the catalog didn't give
// them), a 206 only for the range asked, never more than its length.
func (e *Engine) relayFile(rm *remote, entry int, res *http.Response, rng string, want Range, out ResponseSink) error {
	rs := &e.remotes
	rs.mu.Lock()
	ent := rm.entries[entry]
	rs.mu.Unlock()
	a := wire.MediaAnswer{Status: res.StatusCode, ETag: res.Header.Get("ETag"), ContentRange: res.Header.Get("Content-Range"),
		ContentLength: res.Header.Get("Content-Length")}
	if len(res.Header.Values("ETag")) > 1 || len(res.Header.Values("Content-Length")) > 1 || len(res.Header.Values("Content-Range")) > 1 {
		return ErrNotFound
	}
	if ent.etag == "" || ent.size < 0 {
		// Not known from the catalog: learnt from this first answer, then
		// held to.
		if ent.etag == "" {
			ent.etag = strongETag(a.ETag)
		}
		if ent.size < 0 {
			if res.StatusCode == http.StatusOK {
				ent.size, _ = strconv.ParseInt(a.ContentLength, 10, 64)
			} else if m := contentRangeRe.FindStringSubmatch(a.ContentRange); m != nil && m[3] != "*" {
				ent.size, _ = strconv.ParseInt(m[3], 10, 64)
			}
		}
		if ent.etag == "" || ent.size < 0 {
			return ErrNotFound
		}
	}
	if err := wire.CheckFileAnswer(rng, ent.size, ent.etag, a); err != nil {
		e.log.Info("msfp: a peer's file answer was refused", "peer", rm.peer, "error", err)
		return ErrNotFound
	}
	rs.mu.Lock()
	rm.entries[entry] = ent
	if entry == 0 {
		rm.view.Size = ent.size
	}
	rs.mu.Unlock()
	length, contentRange := ent.size, ""
	if res.StatusCode == http.StatusPartialContent {
		n, cr, ok := partialLength(res.Header, want)
		if !ok {
			return ErrNotFound
		}
		length, contentRange = n, cr
	}
	if es, ok := out.(ETagSink); ok {
		es.SetETag(ent.etag)
	}
	// The copy's own mediaType, from the catalog (MSFP media section 3).
	out.Start(res.StatusCode, wire.RelayedFileType(ent.mediaType), length, contentRange)
	_, err := io.Copy(out, io.LimitReader(res.Body, length))
	if err != nil && rm.ctx.Err() == nil {
		return fmt.Errorf("msfp: a relayed file was cut: %w", err)
	}
	return nil
}

// relayMedia relays a segment: a 206 only for the range asked, never
// more than its length, never a playlist under a media name, its type
// kept only when it is a plain video/ or audio/ one.
func (e *Engine) relayMedia(rm *remote, res *http.Response, rng string, want Range, out ResponseSink) error {
	length, contentRange := res.ContentLength, ""
	if res.StatusCode == http.StatusPartialContent {
		n, cr, ok := partialLength(res.Header, want)
		if !ok {
			e.log.Info("msfp: a peer's part of a stream wasn't the range asked", "peer", rm.peer)
			return ErrNotFound
		}
		length, contentRange = n, cr
	} else if cl := res.Header.Values("Content-Length"); len(cl) > 1 {
		return ErrNotFound
	}
	body := bufio.NewReaderSize(res.Body, 512)
	if start, _ := body.Peek(len("#EXTM3U")); string(start) == "#EXTM3U" {
		// Players read a playlist by what it holds: none passes unchecked.
		e.log.Info("msfp: a peer sent a playlist under a media name", "peer", rm.peer)
		return ErrNotFound
	}
	var r io.Reader = body
	if length >= 0 {
		r = io.LimitReader(body, length)
	}
	out.Start(res.StatusCode, wire.RelayedMediaType(res.Header.Get("Content-Type")), length, contentRange)
	_, err := io.Copy(out, r)
	if err != nil && rm.ctx.Err() == nil {
		return fmt.Errorf("msfp: a relayed stream was cut: %w", err)
	}
	return nil
}

// relaySmall relays what is read whole: playlists checked and kept to
// the subset, complete, their renditions the playback's audio tracks;
// WebVTT and ASS rebuilt; PGS checked; fonts checked; sheets checked and
// re-encoded (MSFP media section 3.1, video sections 6.2 to 6.6).
func (e *Engine) relaySmall(rm *remote, path MediaPath, res *http.Response, out ResponseSink) error {
	limit := int64(wire.MaxPlaylist)
	playlist := path.Kind == MediaMaster || path.Kind == MediaPlaylist
	var rebuild func([]byte) ([]byte, string, bool)
	switch path.Kind {
	case MediaSubtitles, MediaASS, MediaPictures, MediaFont:
		most, f := smallRules(path)
		limit, rebuild = int64(most), f
	case MediaSheet:
		limit = wire.MaxSheetBytes
	}
	if res.ContentLength > limit {
		return ErrNotFound
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return ErrNotFound
	}
	contentType := wire.HLSType
	switch {
	case playlist:
		subset := wire.MediaSubset
		if path.Kind == MediaMaster {
			subset = wire.MasterSubset
		}
		kept, err := wire.FilterPlaylist(body, subset, func(id string) bool { return rm.audio[id] })
		if err != nil {
			e.log.Info("msfp: a peer's playlist was refused", "peer", rm.peer, "error", err)
			return ErrNotFound
		}
		body = kept
		if path.Kind == MediaMaster {
			// What it names is fetched as a playlist, whatever its name.
			rs := &e.remotes
			rs.mu.Lock()
			if rm.playlists == nil {
				rm.playlists = map[string]bool{}
			}
			for _, n := range wire.URILines(kept) {
				rm.playlists[strconv.Itoa(path.Entry)+"/"+n] = true
			}
			rs.mu.Unlock()
		}
	case rebuild != nil:
		// An empty PGS part is a stretch with no subtitles: relayed as such.
		again, ct, ok := rebuild(body)
		if ok && path.Kind == MediaFont {
			rs := &e.remotes
			rs.mu.Lock()
			ok = fontsWithin(&rm.fontBytes, path.Font, len(again))
			rs.mu.Unlock()
		}
		if !ok {
			return ErrNotFound
		}
		body, contentType = again, ct
	case path.Kind == MediaSheet:
		tp := rm.layout
		w, h := tp.Columns*tp.TileWidth, tp.Rows*tp.TileHeight
		if !sheetFits(body, w, h) {
			return ErrNotFound
		}
		again, err := e.cfg.SheetEncoder.Reencode(body, w, h)
		if err != nil {
			return ErrNotFound
		}
		body, contentType = again, "image/jpeg"
	}
	out.Start(http.StatusOK, contentType, int64(len(body)), "")
	_, err = out.Write(body)
	return err
}

// isPlaylist says whether a name of a playback's stream was named by its
// master playlist, or ends in .m3u8: fetched and checked as a playlist.
func (e *Engine) isPlaylist(rm *remote, entry int, name string) bool {
	rs := &e.remotes
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rm.playlists[strconv.Itoa(entry)+"/"+name] || strings.HasSuffix(name, ".m3u8")
}
