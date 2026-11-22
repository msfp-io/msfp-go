package msfp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// The catalog, on the owning side (MSFP catalog sections 2 and 3): what
// this server offers each peer, the libraries and titles calls, and the
// change log of its own titles, kept by the host (ChangeLog) or by the
// engine from snapshots (OwnLogTx).

// titlesPage is how many keys a titles page holds at most (MSFP catalog
// section 3.1).
const titlesPage = 5000

// changesKept is how long the engine keeps its own change log (catalog
// section 3.2: at least 30 days).
const changesKept = 30 * 24 * time.Hour

// The calls of the catalog and playback parts.
const (
	callLibraries = "libraries"
	callTitles    = "titles"
	callPlay      = "play"
	callProgress  = "progress"
	callStop      = "stop"
)

// ownLog is what the engine knows of its own catalog's log: whether it
// was refreshed since the start, whether the host said it changed since,
// and the highest change number given out (seqSeen): a change of what a
// peer sees is noted at least there, so that a reading before it is
// never answered with changes after it.
type ownLog struct {
	mu        sync.Mutex // one refresh at a time
	refreshed atomic.Bool
	dirty     atomic.Bool
	seqSeen   atomic.Int64
}

// cleanKind is a library's kind as sent and kept (catalog section 2): a
// token, known or not; "" (absent) for anything else.
func cleanKind(kind string) string { return wire.LibraryKind(kind) }

// HostLibraries are this server's libraries, as the host's catalog gives
// them: what it may offer, to a server or a group (the panel lists them
// before a group exists, to create or join it with what it shares).
func (e *Engine) HostLibraries(ctx context.Context) ([]Library, error) {
	return e.hostLibraries(ctx)
}

// hostLibraries are the host's libraries, none without a Catalog.
func (e *Engine) hostLibraries(ctx context.Context) ([]Library, error) {
	if e.cfg.Catalog == nil {
		return nil, nil
	}
	return e.cfg.Catalog.Libraries(ctx)
}

// publicIDs gives each host library its public ID, made when it is first
// offered: 96 random bits, never reused (MSFP catalog section 2.1).
func (e *Engine) publicIDs(tx Tx, hostIDs []string) (map[string]string, error) {
	ids, err := tx.LibraryIDs()
	if err != nil {
		return nil, err
	}
	for _, id := range hostIDs {
		if _, ok := ids[id]; ok {
			continue
		}
		b, err := e.random(12)
		if err != nil {
			return nil, err
		}
		pub := wire.Encode64(b)
		if err := tx.PutLibraryID(id, pub); err != nil {
			return nil, err
		}
		ids[id] = pub
	}
	return ids, nil
}

// Offers lists the host's libraries, those offered to the peer marked.
// Error: ErrNotFound.
func (e *Engine) Offers(ctx context.Context, peer string) ([]Library, error) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return nil, err
	}
	libs, err := e.hostLibraries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range libs {
		libs[i].Offered = slices.Contains(p.Offers, libs[i].ID)
	}
	return libs, nil
}

// SetOffers sets the host libraries offered to a paired peer (IDs that
// aren't the host's are left out). The peer's next reading is a whole
// one, and its playbacks from libraries no longer offered end at once
// (MSFP playback section 6.3). Errors: ErrNotFound, ErrNotPaired.
func (e *Engine) SetOffers(ctx context.Context, peer string, libraries []string) error {
	ours, err := e.hostLibraries(ctx)
	if err != nil {
		return err
	}
	var keep []string
	for _, l := range ours {
		if slices.Contains(libraries, l.ID) && !slices.Contains(keep, l.ID) {
			keep = append(keep, l.ID)
		}
	}
	slices.Sort(keep)
	mark, err := e.seqMark(ctx)
	if err != nil {
		return err
	}
	changed := false
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		p, err := tx.Peer(peer)
		switch {
		case err != nil:
			return err
		case p == nil:
			return ErrNotFound
		case p.State != Paired:
			return ErrNotPaired
		}
		had := slices.Sorted(slices.Values(p.Offers))
		if changed = !slices.Equal(had, keep); !changed {
			return nil
		}
		if _, err := e.publicIDs(tx, keep); err != nil {
			return err
		}
		if p.OffersSeq, err = e.offersSeq(tx, mark); err != nil {
			return err
		}
		p.Offers = keep
		return tx.PutPeer(*p)
	})
	if err != nil || !changed {
		return err
	}
	e.bumpLog(ctx)
	e.endPlaybacks(func(pb *playback) bool { return pb.peer == peer && !offeredIn(pb.libraries, keep) }, EndNotOffered)
	return nil
}

// SetLocal chooses whether the individual relation with a paired peer
// shares the host's local titles too (MSFP catalog section 5), in the
// libraries offered to it: off by default. A change forces the peer's
// next reading to be a whole one. Errors: ErrNotFound, ErrNotPaired.
func (e *Engine) SetLocal(ctx context.Context, peer string, on bool) error {
	mark, err := e.seqMark(ctx)
	if err != nil {
		return err
	}
	changed := false
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		p, err := tx.Peer(peer)
		switch {
		case err != nil:
			return err
		case p == nil:
			return ErrNotFound
		case p.State != Paired:
			return ErrNotPaired
		}
		if changed = p.Local != on; !changed {
			return nil
		}
		if p.OffersSeq, err = e.offersSeq(tx, mark); err != nil {
			return err
		}
		p.Local = on
		return tx.PutPeer(*p)
	})
	if err == nil && changed {
		e.bumpLog(ctx)
	}
	return err
}

// OpaqueKeyPart is an opaque part of a title key the host may use for
// one of its titles (MSFP catalog section 4.2): the identifier of a local
// title, or an extra's part after x/, made from the host's own
// identifier of it (its media server's item ID, say) as a keyed MAC
// under this server's IDs secret: 22 characters of base64url, stable as
// long as id is, and never computable by a peer from it.
func (e *Engine) OpaqueKeyPart(id string) string {
	m := hmac.New(sha256.New, e.ourKeys().ids)
	m.Write([]byte("key part\n" + id))
	return wire.Encode64(m.Sum(nil)[:16])
}

// LibraryRemoved is the host saying one of its libraries is gone: it is
// offered to nobody from then on, and peers' playbacks from it end. Its
// public ID is kept, never given to another library.
func (e *Engine) LibraryRemoved(ctx context.Context, id string) error {
	mark, err := e.seqMark(ctx)
	if err != nil {
		return err
	}
	removed := false
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		removed = false
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		for _, p := range peers {
			if i := slices.Index(p.Offers, id); i >= 0 {
				removed = true
				if p.OffersSeq, err = e.offersSeq(tx, mark); err != nil {
					return err
				}
				p.Offers = slices.Delete(p.Offers, i, i+1)
				if err := tx.PutPeer(p); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil && removed {
		e.bumpLog(ctx)
	}
	e.endPlaybacks(func(pb *playback) bool {
		return len(pb.libraries) > 0 && !slices.ContainsFunc(pb.libraries, func(l string) bool { return l != id })
	}, EndNotOffered)
	return err
}

// SetLimits sets what a peer may use of this server at once (MSFP
// playback section 5), from its next play on. Errors: ErrNotFound,
// ErrInvalidLimits.
func (e *Engine) SetLimits(ctx context.Context, peer string, l Limits) error {
	if err := l.Check(); err != nil {
		return err
	}
	if !l.Audio {
		l.AudioStreams, l.AudioTranscodes = 0, 0
	}
	return e.updatePeer(ctx, peer, func(p *PeerRecord) { p.Limits = l })
}

// CatalogChanged is the host saying its titles changed: a hint. When the
// engine keeps the change log, it reads the titles again before its next
// titles answer, and in the background now.
func (e *Engine) CatalogChanged() {
	if !e.keepsLog() {
		return
	}
	e.own.dirty.Store(true)
	e.background(func(ctx context.Context) {
		if err := e.refreshOwn(ctx); err != nil {
			e.log.Warn("msfp: the catalog's change log couldn't be refreshed", "error", err)
		}
	})
}

// changeLog is the host's own change log, nil when it keeps none.
func (e *Engine) changeLog() ChangeLog {
	cl, _ := e.cfg.Catalog.(ChangeLog)
	return cl
}

// keepsLog says whether the engine keeps the change log: the host has a
// catalog without one, and the store can keep it.
func (e *Engine) keepsLog() bool { return e.logKept }

// catalogSeq is the catalog's change number now, and the oldest it still
// answers changes since (floor); 0, 0 when no log is kept.
func (e *Engine) catalogSeq(ctx context.Context) (seq, floor int64, err error) {
	if cl := e.changeLog(); cl != nil {
		seq, err = cl.Seq(ctx)
		e.sawSeq(seq)
		return seq, 0, err
	}
	if !e.keepsLog() {
		return 0, 0, nil
	}
	if !e.own.refreshed.Load() || e.own.dirty.Load() {
		if err := e.refreshOwn(ctx); err != nil {
			return 0, 0, err
		}
	}
	err = e.cfg.Store.Read(ctx, func(tx Tx) error {
		log := tx.(OwnLogTx)
		var err error
		if seq, err = log.OwnSeq(); err != nil {
			return err
		}
		floor, err = log.OwnFloor()
		return err
	})
	e.sawSeq(seq)
	return seq, floor, err
}

func (e *Engine) sawSeq(seq int64) {
	for {
		old := e.own.seqSeen.Load()
		if seq <= old || e.own.seqSeen.CompareAndSwap(old, seq) {
			return
		}
	}
}

// offersSeq is the change number from which a peer's readings know of a
// change of what it sees (its offers, the parts on), set as its
// OffersSeq inside the change's transaction: changes are answered only
// since it, or later (MSFP catalog section 3.2). With the engine's own
// log, a number of its own; with the host's, one above every number
// given out, and above the host's now (mark, read before the
// transaction): until the host's log moves past it, readings are whole.
func (e *Engine) offersSeq(tx Tx, mark int64) (int64, error) {
	if log, ok := tx.(OwnLogTx); ok && e.keepsLog() {
		return log.BumpOwnSeq()
	}
	return mark + 1, nil
}

// bumpLog moves the host's change log past the change numbers noted for
// a change of what peers see, when it can (SeqBumper): their readings are
// of changes again after one whole reading.
func (e *Engine) bumpLog(ctx context.Context) {
	b, ok := e.changeLog().(SeqBumper)
	if !ok {
		return
	}
	seq, err := b.BumpSeq(ctx)
	if err != nil {
		e.log.Warn("msfp: the catalog's change log couldn't take a number", "error", err)
		return
	}
	e.sawSeq(seq)
}

// seqMark is the highest change number given out, at least the host's
// catalog's now: read before a transaction that changes what a peer sees.
func (e *Engine) seqMark(ctx context.Context) (int64, error) {
	if e.keepsLog() {
		return 0, nil
	}
	if _, _, err := e.catalogSeq(ctx); err != nil {
		return 0, err
	}
	return e.own.seqSeen.Load(), nil
}

// refreshOwn reads the host's titles whole and notes what changed since
// the snapshot, all under one new change number.
func (e *Engine) refreshOwn(ctx context.Context) error {
	e.own.mu.Lock()
	defer e.own.mu.Unlock()
	e.own.dirty.Store(false)
	libs, err := e.hostLibraries(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, len(libs))
	for i, l := range libs {
		ids[i] = l.ID
	}
	now := map[string]Title{}
	for after := ""; len(ids) > 0; {
		page, err := e.cfg.Catalog.Titles(ctx, ids, after, titlesPage)
		if err != nil {
			return err
		}
		for _, t := range page.Titles {
			c := t.Common()
			if c.Key <= after {
				return ErrInternal.with(errors.New("the host's titles aren't in key order"))
			}
			slices.Sort(c.Libraries)
			now[c.Key], after = t, c.Key
		}
		if !page.More || len(page.Titles) == 0 {
			break
		}
	}
	at := e.now()
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		log := tx.(OwnLogTx)
		was, err := log.OwnSnapshot()
		if err != nil {
			return err
		}
		seq, err := log.OwnSeq()
		if err != nil {
			return err
		}
		seq++
		var changes []OwnChange
		for key, t := range now {
			if old, ok := was[key]; !ok || !sameTitle(old, t) {
				changes = append(changes, OwnChange{Seq: seq, Key: key, Title: t, At: at})
			}
		}
		for key := range was {
			if _, ok := now[key]; !ok {
				changes = append(changes, OwnChange{Seq: seq, Key: key, At: at})
			}
		}
		if err := log.TrimOwnChanges(at.Add(-changesKept)); err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		if err := log.AppendOwnChanges(changes); err != nil {
			return err
		}
		return log.PutOwnSnapshot(now)
	})
	if err == nil {
		e.own.refreshed.Store(true)
	}
	return err
}

// sameTitle says whether two titles would be sent alike.
func sameTitle(a, b Title) bool {
	x, err1 := MarshalTitle(a)
	y, err2 := MarshalTitle(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// libraries answers a paired peer with what this server offers it
// (MSFP catalog section 2), and, with playback on, its limits.
func (e *Engine) libraries(req *request) response {
	p := req.peer
	if p == nil || p.State != Paired || wire.DecodeJSON(req.body, &struct{}{}) != nil {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartCatalog) {
		return refuse(probNotSupported)
	}
	ctx := req.r.Context()
	ours, err := e.hostLibraries(ctx)
	if err != nil {
		e.log.Warn("msfp: the host's libraries couldn't be read", "error", err)
		return refuse(probInternal)
	}
	var ids map[string]string
	if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		ids, err = tx.LibraryIDs()
		return err
	}); err != nil {
		return refuse(probInternal)
	}
	out := librariesAnswer{Name: e.name(), Libraries: []libraryWire{}}
	for _, l := range ours {
		if pub, ok := ids[l.ID]; ok && slices.Contains(offered(p), l.ID) {
			out.Libraries = append(out.Libraries, libraryWire{ID: pub, Name: wire.CleanText(l.Name, maxName), Kind: cleanKind(l.Kind)})
		}
	}
	if e.on(p, wire.PartPlayback) {
		out.Limits = limitsOut(generous(p), e.on(p, wire.PartMusic))
	}
	return answer(out)
}

type librariesAnswer struct {
	Name      string        `json:"name"`
	Libraries []libraryWire `json:"libraries"`
	Limits    *limitsWire   `json:"limits,omitempty"`
}

type libraryWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

// limitsWire is a limits object as sent (MSFP playback section 5, music
// section 5.1).
type limitsWire struct {
	Streams         int  `json:"streams"`
	Transcodes      int  `json:"transcodes"`
	BitrateKbps     int  `json:"bitrateKbps"`
	Downloads       int  `json:"downloads,omitempty"`
	DownloadsPerDay int  `json:"downloadsPerDay,omitempty"`
	DownloadKbps    int  `json:"downloadKbps,omitempty"`
	ScansPerDay     int  `json:"scansPerDay,omitempty"`
	ScanBytes       int  `json:"scanBytes,omitempty"`
	AudioStreams    *int `json:"audioStreams,omitempty"`
	AudioTranscodes *int `json:"audioTranscodes,omitempty"`
}

func limitsOut(l Limits, music bool) *limitsWire {
	w := &limitsWire{Streams: l.Streams, Transcodes: l.Transcodes, BitrateKbps: l.BitrateKbps, Downloads: l.Downloads,
		DownloadsPerDay: l.DownloadsPerDay, DownloadKbps: l.DownloadKbps, ScansPerDay: l.ScansPerDay}
	if l.ScansPerDay > 0 {
		w.ScanBytes = l.ScanBytes
	}
	if l.Downloads == 0 {
		w.DownloadsPerDay, w.DownloadKbps = 0, 0
	}
	if music && l.Audio {
		w.AudioStreams, w.AudioTranscodes = &l.AudioStreams, &l.AudioTranscodes
	}
	return w
}

// titlesRequest is a titles request (MSFP catalog section 3).
type titlesRequest struct {
	Libraries *[]string `json:"libraries"`
	After     string    `json:"after"`
	Since     int64     `json:"since"`
}

// titlesAnswer is a page of titles, each title as JSON already. Titles
// is never null.
type titlesAnswer struct {
	Titles  []json.RawMessage `json:"titles"`
	Next    string            `json:"next,omitempty"`
	Changes bool              `json:"changes,omitempty"`
	Gone    []string          `json:"gone,omitempty"`
	Seq     int64             `json:"seq,omitempty"`
}

// titles answers a paired peer with a page of the titles in the
// libraries it asks for, of those offered to it, of the content parts on
// between the two: the whole list, or what changed since its last
// reading (MSFP catalog section 3).
func (e *Engine) titles(req *request) response {
	p := req.peer
	var in titlesRequest
	if p == nil || p.State != Paired || wire.DecodeJSON(req.body, &in) != nil || in.Libraries == nil || len(in.After) > wire.MaxAfter {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartCatalog) {
		return refuse(probNotSupported)
	}
	out := titlesAnswer{Titles: []json.RawMessage{}}
	if e.cfg.Catalog == nil {
		return answer(out)
	}
	ctx := req.r.Context()
	var ids map[string]string
	if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		ids, err = tx.LibraryIDs()
		return err
	}); err != nil {
		return refuse(probInternal)
	}
	// Those asked for that are offered to it, by their public IDs: any
	// other is ignored.
	byPublic := map[string]string{}
	for _, l := range offered(p) {
		if pub, ok := ids[l]; ok {
			byPublic[l] = pub
		}
	}
	var libs []string
	for _, asked := range *in.Libraries {
		for host, pub := range byPublic {
			if pub == asked && !slices.Contains(libs, host) {
				libs = append(libs, host)
			}
		}
	}
	slices.Sort(libs)
	seq, floor, err := e.catalogSeq(ctx)
	if err != nil {
		e.log.Warn("msfp: the catalog's change number couldn't be read", "error", err)
		return refuse(probInternal)
	}
	out.Seq = seq
	if len(libs) == 0 {
		return answer(out)
	}
	s := &sender{e: e, ctx: ctx, p: p, libs: libs, local: localOffered(p), pub: byPublic, parents: map[string]string{}}
	var sent []string
	if in.Since > 0 && in.Since >= floor && in.Since <= seq && in.Since >= p.OffersSeq {
		ok, err := s.changesPage(in, &out, &sent)
		if err != nil {
			e.log.Warn("msfp: the catalog's changes couldn't be read", "error", err)
			return refuse(probInternal)
		}
		if !ok {
			out = titlesAnswer{Titles: []json.RawMessage{}, Seq: seq}
			sent = nil
		}
	}
	if !out.Changes {
		if err := s.wholePage(in.After, &out, &sent); err != nil {
			e.log.Warn("msfp: the catalog's titles couldn't be read", "error", err)
			return refuse(probInternal)
		}
	}
	if len(sent) > 0 {
		if err := e.cfg.Store.Update(ctx, func(tx Tx) error { return tx.MarkSent(p.ID, sent) }); err != nil {
			return refuse(probInternal)
		}
	}
	return answer(out)
}

// sender writes the host's titles for one titles answer to a peer: the
// host libraries asked (offered to it), those of them whose local titles
// it is shared, their public IDs, and the extras' titles looked at, by
// key, with the library each was sent in ("" for not sent).
type sender struct {
	e       *Engine
	ctx     context.Context
	p       *PeerRecord
	libs    []string
	local   []string
	pub     map[string]string
	parents map[string]string
}

// wholePage fills a whole-list page: the titles after after, up to 5,000
// and 3 MiB of answer (MSFP catalog section 3.1), next set whenever a
// title remains after the page. Titles the peer can't be sent are
// skipped, and the host asked for more.
func (s *sender) wholePage(after string, out *titlesAnswer, sent *[]string) error {
	budget := wire.NewPageBudget(wire.PageReserve)
	last := ""
	for {
		page, err := s.e.cfg.Catalog.Titles(s.ctx, s.libs, after, titlesPage)
		if err != nil {
			return err
		}
		for _, t := range page.Titles {
			key := t.Common().Key
			if key <= after {
				return ErrInternal.with(errors.New("the host's titles aren't in key order"))
			}
			after = key
			raw, ok, err := s.title(t)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if !budget.Add(len(raw)) {
				if budget.Keys() == 0 {
					s.e.log.Warn("msfp: a title of the host's catalog passes 3 MiB alone; it isn't sent", "key", key)
					continue
				}
				out.Next = last
				return nil
			}
			out.Titles = append(out.Titles, raw)
			*sent = append(*sent, key)
			last = key
		}
		if !page.More || len(page.Titles) == 0 {
			return nil
		}
	}
}

// changesPage fills a changes page: what changed after in.Since, titles
// new or changed and keys gone (of those the peer was ever sent), in one
// byte order, 5,000 keys looked at at most, and within 5,000 keys and 3
// MiB of answer; false when the log doesn't reach back that far.
func (s *sender) changesPage(in titlesRequest, out *titlesAnswer, sent *[]string) (bool, error) {
	e := s.e
	var present []Title
	var gone []string
	var last string
	more := false
	if cl := e.changeLog(); cl != nil {
		page, err := cl.Changes(s.ctx, s.libs, in.Since, in.After, titlesPage)
		if errors.Is(err, ErrTooOld) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if len(page.Present)+len(page.Gone) > titlesPage {
			return false, ErrInternal.with(fmt.Errorf("a page of changes of more than %d keys", titlesPage))
		}
		present, gone, last, more = page.Present, page.Gone, page.Last, page.More
	} else {
		var changes []OwnChange
		err := e.cfg.Store.Read(s.ctx, func(tx Tx) error {
			var err error
			changes, err = tx.(OwnLogTx).OwnChanges(in.Since, in.After, titlesPage)
			return err
		})
		if err != nil {
			return false, err
		}
		for _, c := range changes {
			last = c.Key
			if c.Title != nil && overlaps(c.Title.Common().Libraries, s.libs) {
				present = append(present, c.Title)
			} else {
				gone = append(gone, c.Key)
			}
		}
		more = len(changes) == titlesPage
	}
	out.Changes = true
	type entry struct {
		key string
		raw json.RawMessage // nil: gone
	}
	var entries []entry
	for _, t := range present {
		key := t.Common().Key
		raw, ok, err := s.title(t)
		if err != nil {
			return false, err
		}
		if ok {
			entries = append(entries, entry{key: key, raw: raw})
		} else {
			gone = append(gone, key)
		}
	}
	// A title's extras go with it (MSFP catalog section 3.5), whether the
	// host still lists them or not.
	for _, k := range slices.Clone(gone) {
		extras, err := s.extrasOf(k)
		if err != nil {
			return false, err
		}
		for _, x := range extras {
			if !slices.Contains(gone, x) {
				gone = append(gone, x)
			}
		}
	}
	entries = slices.DeleteFunc(entries, func(en entry) bool { return slices.Contains(gone, en.key) })
	// gone names only titles the peer was told of (MSFP catalog section
	// 3.2), else it would learn of titles it never could see.
	if len(gone) > 0 {
		err := e.cfg.Store.Read(s.ctx, func(tx Tx) error {
			var err error
			gone, err = tx.Sent(s.p.ID, gone)
			return err
		})
		if err != nil {
			return false, err
		}
		for _, k := range gone {
			entries = append(entries, entry{key: k})
		}
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.key, b.key) })
	budget := wire.NewPageBudget(wire.PageReserve)
	included := ""
	for _, en := range entries {
		size := len(en.raw)
		if en.raw == nil {
			size = len(en.key) + 2 // its quotes; keys need no escaping
		}
		if !budget.Add(size) {
			if budget.Keys() == 0 {
				e.log.Warn("msfp: a title of the host's catalog passes 3 MiB alone; it isn't sent", "key", en.key)
				continue
			}
			// The page ends before it: the next asks on from the last key
			// of this one.
			out.Next = included
			return true, nil
		}
		included = en.key
		if en.raw == nil {
			out.Gone = append(out.Gone, en.key)
		} else {
			out.Titles = append(out.Titles, en.raw)
			*sent = append(*sent, en.key)
		}
	}
	if more && last > in.After {
		out.Next = last
	}
	return true, nil
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// title is a title of the host as sent to the peer, as JSON: false when
// its key isn't canonical or isn't of its kind (with a warning: the
// host's bug), of a content part off with the peer, in none of the
// libraries asked, local while its libraries share no local titles with
// the peer (MSFP catalog section 5), an extra whose title isn't sent in
// the same library (catalog section 3.5), or left with nothing a
// receiver would keep. Its library is the one asked whose public ID
// comes first in byte order (MSFP catalog section 3).
func (s *sender) title(t Title) (json.RawMessage, bool, error) {
	e, c := s.e, t.Common()
	k, ok := wire.ParseKey(c.Key)
	if !ok || k.Kind != c.Kind {
		e.log.Warn("msfp: a title of the host's catalog has a key MSFP doesn't take; it isn't sent", "key", c.Key)
		return nil, false, nil
	}
	if !e.on(s.p, k.Part) {
		return nil, false, nil
	}
	libs := s.libs
	if localKey(c.Key) {
		libs = slices.DeleteFunc(slices.Clone(libs), func(l string) bool { return !slices.Contains(s.local, l) })
	}
	library := ""
	for _, l := range c.Libraries {
		if slices.Contains(libs, l) && (library == "" || s.pub[l] < library) {
			library = s.pub[l]
		}
	}
	defer func() {
		if k.Kind != "extra" {
			s.parents[c.Key] = library
		}
	}()
	if library == "" {
		return nil, false, nil
	}
	var w any
	switch t := t.(type) {
	case *VideoTitle:
		vk, _ := wire.ParseVideoKey(c.Key)
		if parent, ok := vk.Parent(); ok {
			in, err := s.parentLibrary(parent.String())
			if err != nil || in != library {
				return nil, false, err
			}
		}
		w, ok = s.videoOut(vk, t, library)
	case *MusicTitle:
		w, ok = s.musicOut(t, library)
	}
	if !ok {
		library = ""
		e.log.Debug("msfp: a title of the host's catalog has nothing MSFP takes; it isn't sent", "key", c.Key)
		return nil, false, nil
	}
	raw, err := marshal(w)
	if err != nil {
		library = ""
		e.log.Warn("msfp: a title of the host's catalog couldn't be written; it isn't sent", "key", c.Key, "error", err)
		return nil, false, nil
	}
	return raw, true, nil
}

// localKey says whether a title key is of the local scheme, of either
// content part.
func localKey(key string) bool {
	if k, ok := wire.ParseVideoKey(key); ok {
		return k.Scheme == wire.SchemeLocal
	}
	k, ok := wire.ParseMusicKey(key)
	return ok && k.Local()
}

// parentLibrary is the library an extra's title is sent in to the peer,
// "" when it isn't sent: noted when the title came before in this
// answer, else looked up in the host's catalog.
func (s *sender) parentLibrary(key string) (string, error) {
	if l, ok := s.parents[key]; ok {
		return l, nil
	}
	t, err := s.e.hostTitle(s.ctx, key, s.libs)
	if err != nil || t == nil {
		s.parents[key] = ""
		return "", err
	}
	if _, _, err := s.title(t); err != nil {
		return "", err
	}
	return s.parents[key], nil
}

// extrasOf are the keys of the extras the host lists under a film, a
// series or a season (MSFP video section 4.6); none for another key.
func (s *sender) extrasOf(key string) ([]string, error) {
	k, ok := wire.ParseVideoKey(key)
	if !ok || k.Kind != "movie" && k.Kind != "series" && k.Kind != "season" {
		return nil, nil
	}
	var out []string
	prefix := key + "/x/"
	for after := prefix; ; {
		page, err := s.e.cfg.Catalog.Titles(s.ctx, s.libs, after, 64)
		if err != nil {
			return nil, err
		}
		for _, t := range page.Titles {
			k := t.Common().Key
			if !strings.HasPrefix(k, prefix) || k <= after {
				return out, nil
			}
			out, after = append(out, k), k
		}
		if !page.More || len(page.Titles) == 0 {
			return out, nil
		}
	}
}

// hostTitle is the host's title of that key in one of these libraries;
// nil for none. The host's catalog gives titles after a key: it is asked
// for the first after the greatest string below key that no key can be
// (keys are of A–Z a–z 0–9 _ - /, all below ~).
func (e *Engine) hostTitle(ctx context.Context, key string, libs []string) (Title, error) {
	if e.cfg.Catalog == nil || key == "" || len(libs) == 0 {
		return nil, nil
	}
	before := key[:len(key)-1] + string(key[len(key)-1]-1) + "~"
	page, err := e.cfg.Catalog.Titles(ctx, libs, before, 1)
	if err != nil || len(page.Titles) == 0 || page.Titles[0].Common().Key != key {
		return nil, err
	}
	return page.Titles[0], nil
}

// videoTitleWire is a video title as sent (MSFP video section 3), its
// members in the order of the specification's table.
type videoTitleWire struct {
	Key       string            `json:"key"`
	Kind      string            `json:"kind"`
	Library   string            `json:"library"`
	Copies    []wire.Copy       `json:"copies,omitempty"`
	Place     *wire.Place       `json:"place,omitempty"`
	With      []string          `json:"with,omitempty"`
	IDs       map[string]string `json:"ids,omitempty"`
	ExtraType string            `json:"extraType,omitempty"`
	Name      string            `json:"name,omitempty"`
	Year      int               `json:"year,omitempty"`
	Image     bool              `json:"image,omitempty"`
}

// videoOut is the host's video title as sent: its copies made ready to
// leave (sendCopy), then the whole read back as a receiver reads it
// (wire.ReadVideoTitle), so that nothing leaves that a receiver would
// skip or drop, and nothing MSFP doesn't take. False when a receiver
// would skip it: a film, an episode or an extra with no copy left, an
// episode keyed by its TMDB episode ID without a valid place (MSFP video
// sections 3.1 and 4.1), a local film or series without a name, or an
// extra whose type isn't a token. Theme songs and videos are extras like
// any (video section 3.9): sent when the host gives them. The copies
// keep the host's order, its preference (video section 3.7).
func (s *sender) videoOut(k wire.VideoKey, t *VideoTitle, library string) (videoTitleWire, bool) {
	in := videoTitleWire{Key: t.Key, Kind: t.Kind, Library: library, With: t.With, IDs: t.IDs,
		ExtraType: t.ExtraType, Name: t.Name, Year: t.Year, Image: t.Image}
	for _, c := range t.Copies {
		in.Copies = append(in.Copies, s.e.sendCopy(s.p.ID, c))
	}
	if t.Place != nil {
		in.Place = &wire.Place{Season: t.Place.Season, Episode: t.Place.Episode}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return videoTitleWire{}, false
	}
	v, ok := wire.ReadVideoTitle(raw)
	if !ok {
		return videoTitleWire{}, false
	}
	w := videoTitleWire{Key: v.Key, Kind: v.Kind, Library: library, Copies: v.Copies, Place: v.Place,
		With: v.With, IDs: v.IDs, ExtraType: v.ExtraType, Name: v.Name}
	slices.Sort(w.With)
	if d := v.Description; d != nil {
		w.Name, w.Year, w.Image = d.Name, d.Year, d.Image
	}
	return w, true
}

// sendCopy is one of the host's copies as it leaves toward a peer: its
// ID the version ID play gives for it, never the host's handle; its
// language tags as wire.SentTag gives them; its name and its tracks'
// titles as Config.CopyName and Config.TrackTitle keep them (MSFP video
// section 3.6). What remains out of MSFP's bounds is dropped when it is
// read back.
func (e *Engine) sendCopy(peer string, c wire.Copy) wire.Copy {
	c.ID = e.versionID(peer, c.ID)
	c.Name = e.cfg.CopyName(c.Name)
	c.Parts = slices.Clone(c.Parts)
	c.Chapters = slices.Clone(c.Chapters)
	c.Segments = slices.Clone(c.Segments)
	if c.Video != nil {
		v := *c.Video
		v.HDR = slices.Clone(v.HDR)
		c.Video = &v
	}
	audio := make([]wire.CopyAudio, 0, len(c.AudioTracks))
	for _, a := range c.AudioTracks {
		a.Language, _ = wire.SentTag(a.Language)
		a.Title = e.cfg.TrackTitle(a.Title)
		audio = append(audio, a)
	}
	c.AudioTracks = audio
	var subs []wire.CopySubtitle
	for _, t := range c.SubtitleTracks {
		t.Language, _ = wire.SentTag(t.Language)
		t.Title = e.cfg.TrackTitle(t.Title)
		subs = append(subs, t)
	}
	c.SubtitleTracks = subs
	return c
}

// sendMusicCopy is sendCopy for a track's copy (MSFP music section 3.3).
func (e *Engine) sendMusicCopy(peer string, c wire.MusicCopy) wire.MusicCopy {
	c.ID = e.versionID(peer, c.ID)
	c.Name = e.cfg.CopyName(c.Name)
	if c.ReplayGain != nil {
		g := *c.ReplayGain
		c.ReplayGain = &g
	}
	return c
}

// musicOut is the host's music title as sent, its copies made ready to
// leave, read back as a receiver reads it: what a receiver would skip or
// drop isn't sent.
func (s *sender) musicOut(t *MusicTitle, library string) (wire.MusicTitle, bool) {
	w := wire.MusicTitle{Key: t.Key, Kind: t.Kind, Library: library, Name: t.Name, SortName: t.SortName,
		Artists: t.Artists, ArtistNames: t.ArtistNames, PrimaryType: t.PrimaryType, SecondaryTypes: t.SecondaryTypes, Compilation: t.Compilation,
		Date: t.Date, OriginalDate: t.OriginalDate, Genres: t.Genres, Album: t.Album, Disc: t.Disc,
		Number: t.Number, IDs: t.IDs}
	if t.Image {
		w.Image = &t.Image
	}
	for _, c := range t.Copies {
		w.Copies = append(w.Copies, s.e.sendMusicCopy(s.p.ID, c))
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return wire.MusicTitle{}, false
	}
	m, ok := wire.ReadMusicTitle(raw)
	if !ok {
		return wire.MusicTitle{}, false
	}
	m.Library = library
	return m, true
}
