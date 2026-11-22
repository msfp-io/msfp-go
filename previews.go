package msfp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/msfp-io/msfp-go/wire"
)

// The previews call (MSFP video section 5.6) and the lyrics call (music
// section 6), both ways: a copy's seek-bar previews before a first
// play, under a short ticket of their own that is no playback; a track's
// lyrics, with no ticket at all.

const (
	callPreviews = "previews"
	callLyrics   = "lyrics"
)

// previews answers a paired peer's previews call: the copy's trickplay
// object, its sheets under a ticket that opens them only, for 10
// minutes.
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
	if c == nil || !c.previews {
		return refuse(probNotFound)
	}
	info, err := e.cfg.Playback.Describe(ctx, c.handle)
	if err != nil {
		return e.hostRefusal(err, "describe")
	}
	t, ok := trickplayOf(info.Trickplay)
	if !ok {
		return refuse(probNotFound)
	}
	b, err := e.random(32)
	if err != nil {
		return refuse(probInternal)
	}
	ticket := wire.Encode64(b)
	e.images.add(ticket, imageTicket{kind: ticketPreviews, peer: p.ID, key: in.Title, handle: c.handle, sheets: t.Sheets,
		expires: e.now().Add(imageTicketTime)}, e.now())
	return answer(struct {
		Trickplay *trickplayWire `json:"trickplay"`
	}{sheetsWire(t, e.streamBase(req)+ticket+"/")})
}

// offeredCopy is the copy of that ID of a title the peer may play here,
// as the catalog sends it; nil for none.
func (e *Engine) offeredCopy(ctx context.Context, p *PeerRecord, key, id string) (*offeredCopy, error) {
	copies, _, err := e.offeredCopies(ctx, p, key)
	if err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(copies, func(c offeredCopy) bool { return c.id == id }); i >= 0 {
		return &copies[i], nil
	}
	return nil, nil
}

// servePreviewSheet serves a sheet under a previews ticket: only its
// copy's sheets, while the title is still sent to the peer (MSFP media
// section 4).
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
	if err != nil || !slices.ContainsFunc(copies, func(c offeredCopy) bool { return c.handle == it.handle && c.previews }) {
		notFound()
		return
	}
	free, ok := e.peerFetchSlots.take(ctx, it.peer)
	if !ok {
		e.busy(w, ctx)
		return
	}
	defer free()
	sink := &httpSink{w: w, ctx: ctx}
	err = e.cfg.Media.Serve(ctx, it.handle, MediaPath{Kind: MediaSheet, Sheet: path.Sheet, OfCopy: true}, nil, sink)
	if !sink.started && ctx.Err() == nil {
		e.mediaError(w, r, err)
	}
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

// PreviewSheets are a peer's copy's seek-bar previews, fetched before a
// first play: their layout, and each sheet, checked and re-encoded as a
// JPEG (sheet n at index n).
type PreviewSheets struct {
	Trickplay Trickplay
	Sheets    [][]byte
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

// Previews asks a peer for the seek-bar previews of one of its copies
// kept here whose description says it has some (MSFP video section
// 5.6), and fetches every sheet at once under the answer's ticket, each
// checked and re-encoded, so that a viewer sees them from the first
// play. The host keeps them while the copy's etag holds, and asks again
// only when it changes. One call at a time per peer. Errors:
// ErrNotFound (no such copy kept, none with previews, or the peer's
// refusal), ErrNotPaired, ErrUncallable, ErrSuspended, ErrNotSupported,
// ErrUnreachable (an answer or a sheet refused), ErrRateLimited,
// ErrBackingOff, ErrServerBusy.
func (e *Engine) Previews(ctx context.Context, peer, key, versionID string) (PreviewSheets, error) {
	var out PreviewSheets
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return out, err
	}
	if err := e.usablePeer(p, wire.PartCatalog); err != nil {
		return out, err
	}
	if !e.on(p, wire.PartMedia) || !e.on(p, wire.PartVideo) {
		return out, ErrNotSupported
	}
	t, err := e.keptTitle(ctx, peer, key)
	if err != nil {
		return out, err
	}
	v, ok := t.(*VideoTitle)
	if !ok || !slices.ContainsFunc(v.Copies, func(c wire.Copy) bool { return c.ID == versionID && c.Previews }) {
		return out, ErrNotFound
	}
	l := e.imageLock(peer)
	l.Lock()
	defer l.Unlock()
	var raw json.RawMessage
	r, err := e.call(ctx, p, callPreviews, wire.PreviewsRequest{Title: key, VersionID: versionID}, &raw, false)
	if err != nil {
		return out, err
	}
	if r.status != http.StatusOK {
		return out, e.refused(p, r)
	}
	root := e.peerStreamRoot(p)
	pv, err := wire.ReadPreviewsAnswer(raw, root)
	if err != nil {
		return out, ErrUnreachable.with(err)
	}
	if pv == nil {
		return out, ErrNotFound
	}
	tp := pv.Trickplay
	out.Trickplay = Trickplay{TileWidth: tp.TileWidth, TileHeight: tp.TileHeight, Columns: tp.Columns, Rows: tp.Rows,
		IntervalMs: tp.IntervalMs, TileCount: tp.TileCount, Sheets: tp.Sheets}
	w, h := tp.Columns*tp.TileWidth, tp.Rows*tp.TileHeight
	for _, u := range pv.URLs {
		sheet, err := e.fetchWhole(ctx, p, u, wire.MaxSheetBytes)
		if err != nil {
			return PreviewSheets{}, err
		}
		if !sheetFits(sheet, w, h) {
			return PreviewSheets{}, ErrUnreachable.with(errors.New("a sheet that isn't a JPEG within its layout"))
		}
		again, err := e.cfg.SheetEncoder.Reencode(sheet, w, h)
		if err != nil {
			return PreviewSheets{}, ErrUnreachable.with(err)
		}
		out.Sheets = append(out.Sheets, again)
	}
	return out, nil
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
