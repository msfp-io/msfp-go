package msfp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// The previews call (MSFP video section 5.6) and the lyrics call (music
// section 6), both ways: a ticket for a copy's seek-bar previews outside
// a playback, which is no playback; a track's lyrics, with no ticket at
// all.

const (
	callPreviews = "previews"
	callLyrics   = "lyrics"
)

// previews answers a paired peer's previews call (MSFP video section
// 5.6): a ticket that opens the copy's sheets only, for 10 minutes, and
// the copy's etag as the host holds it now; 404 for a copy without a
// previewLayout in the catalog. The layout is the catalog's: the answer
// carries no URL.
func (e *Engine) previews(req *request) response {
	p := req.peer
	if p == nil || p.State != Paired {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartVideo) {
		return refuse(probNotSupported)
	}
	in, err := wire.ReadPreviewsRequest(req.body)
	if err != nil {
		return refuse(probInvalid)
	}
	if e.cfg.Playback == nil || e.cfg.Media == nil {
		return refuse(probNotFound)
	}
	ctx := req.r.Context()
	c, err := e.offeredCopy(ctx, p, in.Title, in.VersionID)
	if err != nil {
		return refuse(probInternal)
	}
	if c == nil || c.copy.PreviewLayout == nil {
		return refuse(probNotFound)
	}
	t := c.copy.PreviewLayout
	b, err := e.random(32)
	if err != nil {
		return refuse(probInternal)
	}
	ticket := wire.Encode64(b)
	e.images.add(ticket, imageTicket{kind: ticketPreviews, peer: p.ID, key: in.Title, handle: c.handle, sheets: t.SheetCount(),
		sheetWidth: t.Columns * t.TileWidth, sheetHeight: t.Rows * t.TileHeight, expires: e.now().Add(imageTicketTime)}, e.now())
	return answer(wire.PreviewsAnswer{Ticket: ticket, ETag: c.copy.ETag})
}

// offeredCopy is the copy of that ID of a title the peer may play here,
// as the catalog sends it; nil for none.
func (e *Engine) offeredCopy(ctx context.Context, p *PeerRecord, key, id string) (*offeredCopy, error) {
	copies, _, err := e.offeredCopies(ctx, p, key)
	if err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(copies, func(c offeredCopy) bool { return c.id() == id }); i >= 0 {
		return &copies[i], nil
	}
	return nil, nil
}

// servePreviewSheet serves a sheet under a previews ticket: only its
// copy's sheets, below the sheet count of its layout, while the title
// is still sent to the peer with that layout (MSFP media section 4).
func (e *Engine) servePreviewSheet(w http.ResponseWriter, r *http.Request, it imageTicket, name string) {
	notFound := func() { e.cfg.NotFound.ServeHTTP(w, r) }
	path, ok := ParseMediaPath(name)
	if !plainMediaRequest(r) || !ok || path.Kind != MediaSheet || path.Sheet >= it.sheets || e.cfg.Media == nil {
		notFound()
		return
	}
	ctx := r.Context()
	p, err := e.peerRecord(ctx, it.peer)
	if err != nil || p.State != Paired || !p.Suspended.IsZero() || !e.on(p, wire.PartVideo) {
		notFound()
		return
	}
	copies, _, err := e.offeredCopies(ctx, p, it.key)
	if err != nil || !slices.ContainsFunc(copies, func(c offeredCopy) bool {
		l := c.copy.PreviewLayout
		return c.handle == it.handle && l != nil && l.SheetCount() == it.sheets
	}) {
		notFound()
		return
	}
	free, ok := e.peerFetchSlots.take(ctx, it.peer)
	if !ok {
		e.busy(w, ctx)
		return
	}
	defer free()
	e.serveSheet(w, r, it.handle, MediaPath{Kind: MediaSheet, Sheet: path.Sheet, OfCopy: true}, it.sheetWidth, it.sheetHeight)
}

// lyrics answers a paired peer's lyrics call (MSFP music section 6): the
// host's lyrics of a copy whose catalog description says it has some,
// cleaned as a viewer's server keeps them.
func (e *Engine) lyrics(req *request) response {
	p := req.peer
	if p == nil || p.State != Paired {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartMusic) {
		return refuse(probNotSupported)
	}
	in, ok := wire.ReadLyricsRequest(req.body)
	if !ok {
		return refuse(probInvalid)
	}
	host, ok := e.cfg.Catalog.(Lyrics)
	if !ok {
		return refuse(probNotFound)
	}
	ctx := req.r.Context()
	c, err := e.offeredCopy(ctx, p, in.Title, in.VersionID)
	if err != nil {
		return refuse(probInternal)
	}
	if c == nil || c.lyrics == "" {
		return refuse(probNotFound)
	}
	sets, err := host.Lyrics(ctx, c.handle)
	if err != nil {
		return e.hostRefusal(err, "lyrics")
	}
	raw, err := json.Marshal(wire.LyricsAnswer{StructuredLyrics: sets})
	if err != nil {
		return refuse(probInternal)
	}
	kept, err := wire.ReadLyrics(raw)
	if err != nil {
		e.log.Warn("msfp: the host's lyrics are outside what MSFP allows; they aren't sent", "error", err)
		return refuse(probNotFound)
	}
	return answer(wire.LyricsAnswer{StructuredLyrics: kept})
}

// keptTitle is a peer's title as this server keeps it; nil for none.
func (e *Engine) keptTitle(ctx context.Context, peer, key string) (Title, error) {
	var t Title
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		t, err = tx.PeerTitle(peer, key)
		return err
	})
	return t, err
}

// sheetCacheBytes bounds the sheets kept of peers' copies (MSFP video
// section 5.5: caching recommended, keyed by the copy's etag and its
// previewLayout).
const sheetCacheBytes = 64 << 20

// sheetCache are the sheets fetched of peers' copies, re-encoded, and the
// previews tickets of copies, each used until it expires.
type sheetCache struct {
	mu      sync.Mutex
	sheets  map[string][]byte
	order   []string // first kept first, for room
	bytes   int
	tickets map[string]previewsTicket
}

// previewsTicket is a peer's previews ticket for a copy, and the etag its
// answer gave.
type previewsTicket struct {
	ticket, etag string
	expires      time.Time
}

func (c *sheetCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.sheets[key]
	return b, ok
}

func (c *sheetCache) put(key string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sheets == nil {
		c.sheets = map[string][]byte{}
	}
	if _, ok := c.sheets[key]; ok || len(b) > sheetCacheBytes {
		return
	}
	for c.bytes+len(b) > sheetCacheBytes && len(c.order) > 0 {
		c.bytes -= len(c.sheets[c.order[0]])
		delete(c.sheets, c.order[0])
		c.order = c.order[1:]
	}
	c.sheets[key], c.order, c.bytes = b, append(c.order, key), c.bytes+len(b)
}

// PreviewSheet fetches sheet n of the seek-bar previews of a peer's copy
// kept here with a previewLayout (MSFP video section 5.5), checked
// against that layout and re-encoded as a JPEG. Previews are fetched on
// demand, for a copy someone opened (its page, its chapters, a
// playback, a scrub), never for a whole library nor in bulk for copies
// nobody opened: call it only so (fetching a copy's sheets together
// hides where the viewer is). During a live playback of the copy the
// playback's ticket opens its sheets; outside one, a previews call is
// made once per copy and its ticket used for all viewers until it
// expires, at most one call per copy at a time. Sheets fetched are kept
// (in memory, within 64 MiB) while the copy's etag and layout hold.
// Errors: ErrNotFound (no such copy kept, no layout, a sheet past its
// count, or the peer's refusal), ErrNotPaired, ErrUncallable,
// ErrSuspended, ErrNotSupported, ErrCopyChanged (the previews answer's
// etag isn't the catalog's: the title is read again), ErrUnreachable (an
// answer or a sheet refused), ErrRateLimited, ErrBackingOff (a 429: no
// previews of that peer until then), ErrServerBusy.
func (e *Engine) PreviewSheet(ctx context.Context, peer, key, versionID string, n int) ([]byte, error) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return nil, err
	}
	if err := e.usablePeer(p, wire.PartCatalog); err != nil {
		return nil, err
	}
	if !e.on(p, wire.PartMedia) || !e.on(p, wire.PartVideo) {
		return nil, ErrNotSupported
	}
	c, known := e.catalogCopy(ctx, peer, key, versionID)
	l := c.PreviewLayout
	if !known || l == nil || n < 0 || n >= l.SheetCount() {
		return nil, ErrNotFound
	}
	cacheKey := fmt.Sprintf("%s\n%s\n%s\n%s\n%+v\n%d", peer, key, versionID, c.ETag, *l, n)
	if b, ok := e.sheets.get(cacheKey); ok {
		return b, nil
	}
	base := ""
	if rm := e.playingCopy(peer, key, versionID); rm != nil {
		base = rm.base
	} else {
		t, err := e.previewsTicket(ctx, p, key, versionID, c.ETag)
		if err != nil {
			return nil, err
		}
		base = wire.TicketBase(e.peerStreamRoot(p), t)
	}
	sheet, err := e.fetchWhole(ctx, p, wire.SheetURL(base, n), wire.MaxSheetBytes)
	if err != nil {
		return nil, err
	}
	w, h := l.Columns*l.TileWidth, l.Rows*l.TileHeight
	if !sheetFits(sheet, w, h) {
		return nil, ErrUnreachable.with(errors.New("a sheet that isn't a JPEG within its layout"))
	}
	again, err := e.cfg.SheetEncoder.Reencode(sheet, w, h)
	if err != nil {
		return nil, ErrUnreachable.with(err)
	}
	e.sheets.put(cacheKey, again)
	return again, nil
}

// playingCopy is a live playback on a peer of that copy whose ticket
// opens its sheets; nil for none.
func (e *Engine) playingCopy(peer, key, versionID string) *remote {
	rs := &e.remotes
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, rm := range rs.byID {
		if rm.peer == peer && rm.view.Title == key && rm.view.VersionID == versionID && rm.layout != nil {
			return rm
		}
	}
	return nil
}

// previewsTicket is a previews ticket of a peer's copy: the one kept
// while it works, else a new previews call's, one call per copy at a
// time (MSFP video section 5.6).
func (e *Engine) previewsTicket(ctx context.Context, p *PeerRecord, key, versionID, etag string) (string, error) {
	id := p.ID + "\n" + key + "\n" + versionID
	l := e.imageLock("previews\n" + id)
	l.Lock()
	defer l.Unlock()
	sc := &e.sheets
	sc.mu.Lock()
	t, ok := sc.tickets[id]
	sc.mu.Unlock()
	if ok && e.now().Before(t.expires) && t.etag == etag {
		return t.ticket, nil
	}
	var raw json.RawMessage
	r, err := e.call(ctx, p, callPreviews, wire.PreviewsRequest{Title: key, VersionID: versionID}, &raw, false)
	if err != nil {
		return "", err
	}
	if r.status != http.StatusOK {
		return "", e.refused(p, r)
	}
	a, err := wire.ReadPreviewsAnswer(raw)
	if err != nil {
		return "", ErrUnreachable.with(err)
	}
	if a.ETag != etag {
		// The copy changed: the title is read again before any sheet.
		e.readSoon(p.ID)
		return "", ErrCopyChanged
	}
	sc.mu.Lock()
	if sc.tickets == nil {
		sc.tickets = map[string]previewsTicket{}
	}
	now := e.now()
	for k, old := range sc.tickets {
		if !now.Before(old.expires) {
			delete(sc.tickets, k)
		}
	}
	// A little short of the ticket's 10 minutes, for the time a fetch takes.
	sc.tickets[id] = previewsTicket{ticket: a.Ticket, etag: a.ETag, expires: now.Add(imageTicketTime - 30*time.Second)}
	sc.mu.Unlock()
	return a.Ticket, nil
}

// fetchWhole fetches a URL of a peer's stream whole, within most bytes
// and the time of a small read: a plain GET, nothing coded.
func (e *Engine) fetchWhole(ctx context.Context, p *PeerRecord, url string, most int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, smallReadTime)
	defer cancel()
	res, err := e.relayGet(ctx, p, url, "")
	if err != nil {
		return nil, ErrUnreachable.with(err)
	}
	defer res.Body.Close()
	if ce := res.Header.Get("Content-Encoding"); res.Uncompressed || ce != "" && !strings.EqualFold(ce, "identity") {
		return nil, ErrUnreachable.with(errors.New("a coded answer"))
	}
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusServiceUnavailable:
		return nil, ErrServerBusy
	default:
		return nil, ErrNotFound
	}
	if res.ContentLength > most {
		return nil, ErrUnreachable.with(errors.New("an answer over its bound"))
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, most+1))
	if err != nil || int64(len(body)) > most {
		return nil, ErrUnreachable.with(errors.New("an answer over its bound, or cut"))
	}
	return body, nil
}

// TrackLyrics asks a peer for the lyrics of one of its tracks' copies
// kept here whose description says it has some (MSFP music section 6),
// cleaned as music section 6 says (wire.ReadLyrics). The host keeps them
// while the copy's etag holds. Errors: ErrNotFound (no such copy kept,
// none with lyrics, or the peer's refusal), ErrNotPaired, ErrUncallable,
// ErrSuspended, ErrNotSupported, ErrUnreachable (an answer refused),
// ErrRateLimited, ErrBackingOff.
func (e *Engine) TrackLyrics(ctx context.Context, peer, key, versionID string) ([]wire.Lyrics, error) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return nil, err
	}
	if err := e.usablePeer(p, wire.PartCatalog); err != nil {
		return nil, err
	}
	if !e.on(p, wire.PartMusic) {
		return nil, ErrNotSupported
	}
	t, err := e.keptTitle(ctx, peer, key)
	if err != nil {
		return nil, err
	}
	m, ok := t.(*MusicTitle)
	if !ok || !slices.ContainsFunc(m.Copies, func(c wire.MusicCopy) bool { return c.ID == versionID && c.Lyrics != "" }) {
		return nil, ErrNotFound
	}
	var raw json.RawMessage
	r, err := e.call(ctx, p, callLyrics, wire.LyricsRequest{Title: key, VersionID: versionID}, &raw, false)
	if err != nil {
		return nil, err
	}
	if r.status != http.StatusOK {
		return nil, e.refused(p, r)
	}
	sets, err := wire.ReadLyrics(raw)
	if err != nil {
		return nil, ErrUnreachable.with(err)
	}
	return sets, nil
}
