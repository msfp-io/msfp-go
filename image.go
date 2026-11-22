package msfp

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// The image of a local title (MSFP catalog section 4.4, media sections
// 3.4 and 4): the image call, served with a ticket of its own that opens
// that image only for 10 minutes, and asked for as a viewer, the image
// checked and re-encoded before the host gets it.
//
// The short tickets of media section 4, of an image call and of a
// previews call (previews.go), are kept here, apart from playbacks'.

const callImage = "image"

// imageTicketTime is how long an image ticket works (MSFP media section
// 4).
const imageTicketTime = 10 * time.Minute

// imageTicket is what a short ticket opens, for one peer, for 10
// minutes: of kind image, one title's image, while the library it was
// sent in is still shared with it; of kind previews, one copy's sheets
// (its handle, their count and layout), while the title is still sent to it.
type imageTicket struct {
	kind      string
	peer, key string
	handle    string
	sheets    int
	// sheetWidth and sheetHeight bound a sheet: its layout's full sheet.
	sheetWidth, sheetHeight int
	expires                 time.Time
}

// The kinds of short tickets.
const (
	ticketImage    = "image"
	ticketPreviews = "previews"
)

// imageTickets are the live image tickets, by the SHA-256 of each, and
// the locks of the image calls made, by peer.
type imageTickets struct {
	mu       sync.Mutex
	byTicket map[[32]byte]imageTicket
	locks    map[string]*sync.Mutex
}

// add keeps a new ticket, dropping those that expired.
func (t *imageTickets) add(ticket string, it imageTicket, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byTicket == nil {
		t.byTicket = map[[32]byte]imageTicket{}
	}
	for h, o := range t.byTicket {
		if !now.Before(o.expires) {
			delete(t.byTicket, h)
		}
	}
	t.byTicket[sha256.Sum256([]byte(ticket))] = it
}

// get is the live ticket of that text.
func (t *imageTickets) get(ticket string, now time.Time) (imageTicket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := sha256.Sum256([]byte(ticket))
	it, ok := t.byTicket[h]
	if ok && !now.Before(it.expires) {
		delete(t.byTicket, h)
		return imageTicket{}, false
	}
	return it, ok
}

// sharedImage is the host's title of that key whose image the peer may
// be given now: a local film, series, artist or album, of a content part
// on with it, in a library whose local titles it is shared, sent to it
// as it would be in titles, with its image shared; nil for none.
func (e *Engine) sharedImage(ctx context.Context, p *PeerRecord, key string) (Title, error) {
	if _, ok := e.cfg.Catalog.(Images); !ok || p.State != Paired || !p.Suspended.IsZero() {
		return nil, nil
	}
	libs := slices.DeleteFunc(offered(p), func(l string) bool { return !slices.Contains(localOffered(p), l) })
	slices.Sort(libs)
	t, err := e.hostTitle(ctx, key, libs)
	if err != nil || t == nil {
		return nil, err
	}
	s, err := e.senderFor(ctx, p, libs)
	if err != nil {
		return nil, err
	}
	raw, ok, err := s.title(t)
	if err != nil || !ok {
		return nil, err
	}
	var sent struct {
		Image bool `json:"image"`
	}
	if json.Unmarshal(raw, &sent) != nil || !sent.Image {
		return nil, nil
	}
	return t, nil
}

// image answers a paired peer's image call (MSFP catalog section 4.4):
// the URL of a local title's image, under a ticket of its own.
func (e *Engine) image(req *request) response {
	p := req.peer
	if p == nil || p.State != Paired {
		return refuse(probNotFound)
	}
	ctx := req.r.Context()
	var failed error
	on := func(part string) bool {
		return e.on(p, wire.PartCatalog) && e.on(p, wire.PartMedia) && e.on(p, part)
	}
	title, status, problem := wire.CheckImageRequest(on, req.body, func(key string) bool {
		t, err := e.sharedImage(ctx, p, key)
		failed = err
		return t != nil
	})
	switch {
	case failed != nil:
		e.log.Warn("msfp: the host's title couldn't be read for its image", "error", failed)
		return refuse(probInternal)
	case status != http.StatusOK:
		return refuse(problemType(problem))
	}
	b, err := e.random(32)
	if err != nil {
		return refuse(probInternal)
	}
	ticket := wire.Encode64(b)
	e.images.add(ticket, imageTicket{kind: ticketImage, peer: p.ID, key: title, expires: e.now().Add(imageTicketTime)}, e.now())
	return answer(struct {
		URL string `json:"url"`
	}{wire.ImageURL(e.streamBase(req), ticket)})
}

// serveImage serves an image ticket's one URL, <ticket>/image, checked
// as every media request is (MSFP media section 2), and the host's image
// checked before it leaves: what a peer would refuse isn't sent. false
// when ticket isn't an image ticket.
func (e *Engine) serveImage(w http.ResponseWriter, r *http.Request, ticket, name string) bool {
	it, ok := e.images.get(ticket, e.now())
	if !ok {
		return false
	}
	notFound := func() { e.cfg.NotFound.ServeHTTP(w, r) }
	if !mediaMethod(w, r) {
		return true
	}
	if it.kind == ticketPreviews {
		e.servePreviewSheet(w, r, it, name)
		return true
	}
	if name != "image" || !plainMediaRequest(r) {
		notFound()
		return true
	}
	ctx := r.Context()
	p, err := e.peerRecord(ctx, it.peer)
	if err != nil {
		notFound()
		return true
	}
	// The pairing ended, or the title is no longer shared with it: the
	// ticket works no more (MSFP media section 4).
	t, err := e.sharedImage(ctx, p, it.key)
	if err != nil || t == nil {
		notFound()
		return true
	}
	free, ok := e.peerFetchSlots.take(ctx, it.peer)
	if !ok {
		e.busy(w, ctx)
		return true
	}
	defer free()
	img, err := e.cfg.Catalog.(Images).Image(ctx, it.key)
	if err != nil {
		e.mediaError(w, r, err)
		return true
	}
	format, err := wire.CheckImage(img)
	if err != nil {
		e.log.Warn("msfp: the host's image of a local title isn't one MSFP takes; it isn't sent", "key", it.key, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return true
	}
	h := w.Header()
	h.Set("Content-Type", "image/"+format)
	h.Set("Content-Length", strconv.Itoa(len(img)))
	w.WriteHeader(http.StatusOK)
	w.Write(img)
	return true
}

// TitleImage asks a peer for the image of one of its local titles kept
// here (a film, a series, an artist or an album whose description says
// it has one: MSFP catalog section 4.4), fetches it under the answer's
// ticket, checks it (wire.CheckImage) and re-encodes it, so that only
// its pixels reach this server's people. It gives the image and its
// format, wire.ImageJPEG or wire.ImagePNG. The host keeps it, marked as
// the owner's, with the title, and asks again only when the title
// changes. One image at a time per peer. Errors: ErrNotFound (no such
// title kept, none with an image, or the peer's refusal), ErrNotPaired,
// ErrUncallable, ErrSuspended, ErrNotSupported, ErrUnreachable (an
// answer or an image refused), ErrRateLimited, ErrBackingOff.
func (e *Engine) TitleImage(ctx context.Context, peer, key string) ([]byte, string, error) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return nil, "", err
	}
	tk, ok := wire.ParseKey(key)
	if !ok || !localKey(key) || tk.Kind != "movie" && tk.Kind != "series" && tk.Kind != "artist" && tk.Kind != "album" {
		return nil, "", ErrNotFound
	}
	if err := e.usablePeer(p, wire.PartCatalog); err != nil {
		return nil, "", err
	}
	if !e.on(p, wire.PartMedia) || !e.on(p, tk.Part) {
		return nil, "", ErrNotSupported
	}
	var t Title
	if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		t, err = tx.PeerTitle(peer, key)
		return err
	}); err != nil {
		return nil, "", err
	}
	switch t := t.(type) {
	case *VideoTitle:
		ok = t.Image
	case *MusicTitle:
		ok = t.Image
	default:
		ok = false
	}
	if !ok {
		return nil, "", ErrNotFound
	}
	l := e.imageLock(peer)
	l.Lock()
	defer l.Unlock()
	var raw json.RawMessage
	r, err := e.call(ctx, p, callImage, struct {
		Title string `json:"title"`
	}{key}, &raw, false)
	if err != nil {
		return nil, "", err
	}
	if r.status != http.StatusOK {
		return nil, "", e.refused(p, r)
	}
	ticket, ok := wire.ReadImageAnswer(raw, e.peerStreamRoot(p))
	if !ok {
		return nil, "", ErrUnreachable.with(errors.New("an image answer not under its stream root"))
	}
	ctx, cancel := context.WithTimeout(ctx, smallReadTime)
	defer cancel()
	res, err := e.relayGet(ctx, p, wire.ImageURL(e.peerStreamRoot(p), ticket), "")
	if err != nil {
		return nil, "", ErrUnreachable.with(err)
	}
	defer res.Body.Close()
	if ce := res.Header.Get("Content-Encoding"); res.Uncompressed || ce != "" && !strings.EqualFold(ce, "identity") {
		return nil, "", ErrUnreachable.with(errors.New("a coded image"))
	}
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusServiceUnavailable:
		return nil, "", ErrServerBusy
	default:
		return nil, "", ErrNotFound
	}
	if res.ContentLength > wire.MaxImageBytes {
		return nil, "", ErrUnreachable.with(errors.New("an image over 1 MiB"))
	}
	img, err := io.ReadAll(io.LimitReader(res.Body, wire.MaxImageBytes+1))
	if err != nil {
		return nil, "", ErrUnreachable.with(err)
	}
	format, err := wire.CheckImage(img)
	if err != nil {
		return nil, "", ErrUnreachable.with(err)
	}
	again, err := e.reencodeImage(img, format)
	if err != nil {
		return nil, "", ErrUnreachable.with(err)
	}
	return again, format, nil
}

// reencodeImage decodes a checked image and encodes its pixels again in
// its format: JPEGs with the SheetEncoder, PNGs with image/png.
func (e *Engine) reencodeImage(img []byte, format string) ([]byte, error) {
	if format == wire.ImageJPEG {
		return e.cfg.SheetEncoder.Reencode(img, wire.MaxImageSide, wire.MaxImageSide)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(img))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > wire.MaxImageSide || cfg.Height > wire.MaxImageSide {
		return nil, ErrNotAnImage.with(errors.New("not a PNG within 4,096 pixels"))
	}
	decoded, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		return nil, ErrNotAnImage.with(err)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, decoded); err != nil {
		return nil, ErrNotAnImage.with(err)
	}
	return out.Bytes(), nil
}

// peerStreamRoot is a peer's stream root: its address, its prefix and
// the stream route (MSFP media section 1).
func (e *Engine) peerStreamRoot(p *PeerRecord) string {
	prefix := strings.TrimSuffix(cmp.Or(p.Base, defaultBase), "/api/v1/federation/")
	return p.Address + prefix + streamRoute
}

// imageLock is the lock of a peer's image calls: one at a time.
func (e *Engine) imageLock(peer string) *sync.Mutex {
	e.images.mu.Lock()
	defer e.images.mu.Unlock()
	if e.images.locks == nil {
		e.images.locks = map[string]*sync.Mutex{}
	}
	l := e.images.locks[peer]
	if l == nil {
		l = &sync.Mutex{}
		e.images.locks[peer] = l
	}
	return l
}
