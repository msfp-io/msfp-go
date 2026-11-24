package msfp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// Playback both ways: play, the choice of the copy and the tracks, the
// answer as the viewer's side keeps it, the media relayed, progress and
// stop, the limits counted in one step, and every way a playback ends.

const master = "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-STREAM-INF:BANDWIDTH=5000000\nv0.m3u8\n"

// filledMaster is master as the engine sends it of a film's copy left
// unchanged: CODECS and RESOLUTION filled from the copy (media 3.1).
const filledMaster = "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-STREAM-INF:BANDWIDTH=5000000,CODECS=\"avc1.640028,mp4a.40.2\",RESOLUTION=1920x1080\nv0.m3u8\n"
const media = "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6.0,\nseg0.m4s\n#EXT-X-ENDLIST\n"

func filmHost() *fakeHost {
	h := newFakeHost()
	h.film("films", "movie/tmdb/949", "en", "fr")
	h.files = map[string][]byte{"master.m3u8": []byte(master), "v0.m3u8": []byte(media), "init.mp4": []byte("init"),
		"seg0.m4s": []byte("0123456789abcdefghij"), "file": []byte("0123456789abcdefghij"), "subtitles/s0/0": []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<i>Bonjour</i>\n")}
	return h
}

func TestPlayAndRelay(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", VersionID: copyID(t, s, "movie/tmdb/949", 1),
		MaxBitrateKbps: 12345})
	if err != nil {
		t.Fatal(err)
	}
	if pb.Method != "direct" || pb.Protocol != "hls" || pb.AudioTrack != "a0" || pb.DurationMs != 5_400_000 ||
		pb.VersionID == "" || len(pb.Subtitles) != 2 || pb.Subtitles[1].Form != "picture" || strings.HasPrefix(pb.ID, "play_") {
		t.Errorf("the playback as B keeps it: %+v", pb)
	}
	// A heard the French copy and the cap, as asked; nothing of the device.
	if len(h.started) != 1 || h.started[0].Copy != "movie/tmdb/949#1" || h.started[0].MaxBitrateKbps != 12345 {
		t.Errorf("A started %+v", h.started)
	}
	plays := s.a.exchanges("play")
	if body := plays[0].reqBody; strings.Contains(body, "capabilities") || !strings.Contains(body, `"maxBitrateKbps":12345`) ||
		strings.Contains(plays[0].resBody, `"versions"`) {
		t.Errorf("the play request: %s", body)
	}
	if strings.Contains(plays[0].resBody, "movie/tmdb/949#") {
		t.Errorf("a handle left in the answer: %s", plays[0].resBody)
	}
	// The media, through B.
	var out sink
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &out); err != nil || string(out.body) != filledMaster ||
		out.contentType != "application/vnd.apple.mpegurl" {
		t.Errorf("master: %v %q %q", err, out.body, out.contentType)
	}
	out = sink{}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaSegment, Name: "v0.m3u8"}, "", &out); err != nil ||
		string(out.body) != media {
		t.Errorf("the media playlist, by a name the master named: %v %q", err, out.body)
	}
	out = sink{}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaSegment, Name: "seg0.m4s"}, "bytes=2-5", &out); err != nil ||
		out.status != 206 || string(out.body) != "2345" || out.contentRange != "bytes 2-5/20" || out.contentType != "video/mp4" {
		t.Errorf("a range: %v %+v", err, out)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaSegment, Name: "seg0.m4s"}, "bytes=0-1,4-5", &sink{}); !errors.Is(err, msfp.ErrRangeNotSatisfiable) {
		t.Errorf("several ranges: %v", err)
	}
	out = sink{}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s0", Part: 0}, "", &out); err != nil ||
		string(out.body) != "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<i>Bonjour</i>\n" || out.contentType != "text/vtt; charset=utf-8" {
		t.Errorf("subtitles: %v %q", err, out.body)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s1", Part: 0}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a PGS track asked as text: %v", err)
	}
	// Progress and stop.
	s.b.eng.Progress(pb.ID, 60_000, true)
	waitAll(s.b)
	if p := s.a.exchanges("progress"); len(p) != 1 || p[0].status != 200 || strings.Contains(p[0].reqBody, "paused") {
		t.Errorf("progress: %+v", p)
	}
	if v := s.a.eng.Playbacks(); len(v) != 1 || v[0].State != "active" || v[0].Title != "movie/tmdb/949" || v[0].PeerName == "" {
		t.Errorf("A's live view: %+v", v)
	}
	if err := s.b.eng.Stop(ctx, pb.ID, 61_000); err != nil {
		t.Fatal(err)
	}
	waitAll(s.a)
	if st := h.stops(); st["h1"] != msfp.EndStopped {
		t.Errorf("A's host was told %v", st)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a fetch after the stop: %v", err)
	}
	if _, ok := has[msfp.PlaybackEnded](s.a.events); !ok {
		t.Error("no PlaybackEnded")
	}
}

// Every host call given a copy's handle for a peer is given the peer and
// the host's libraries offered to it (sorted), those not offered left
// out: Describe, Start and Serve of a playback's media.
func TestHostCallsCarryOfferedLibraries(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	h.libs = []msfp.Library{{ID: "zz", Name: "Zz"}, {ID: "films", Name: "Films"}, {ID: "docs", Name: "Docs"}}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if err := s.a.eng.SetOffers(ctx, s.onA.ID, []string{"zz", "films"}); err != nil {
		t.Fatal(err)
	}
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", VersionID: copyID(t, s, "movie/tmdb/949", 0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &sink{}); err != nil {
		t.Fatal(err)
	}
	want := msfp.PeerRef{ID: s.onA.ID, Name: s.onA.Name, Libraries: []string{"films", "zz"}}
	same := func(p msfp.PeerRef) bool {
		return p.ID == want.ID && p.Name == want.Name && slices.Equal(p.Libraries, want.Libraries)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var calls []string
	for _, c := range h.peers {
		calls = append(calls, c.call)
		if !same(c.peer) {
			t.Errorf("%s was given %+v, not %+v", c.call, c.peer, want)
		}
	}
	if !slices.Contains(calls, "describe") || !slices.Contains(calls, "serve") {
		t.Errorf("the calls: %v", calls)
	}
	if len(h.started) != 1 || !same(h.started[0].Peer) {
		t.Errorf("Start was given %+v", h.started)
	}
}

// play's malformed requests are refused with 400, a key not offered with
// 404, nothing at all for a pairing with no streams, and a ticket works
// only as MSFP says.
func TestPlayRefusalsAndTickets(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	call := func(body string) int {
		var raw json.RawMessage
		code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "play", json.RawMessage(body), &raw)
		return code
	}
	for _, body := range []string{`{}`, `{"title":7}`, `{"title":"series/tmdb/1399"}`, `{"title":"movie/tmdb/0949"}`,
		`{"title":"movie/tmdb/949","startPositionMs":-1}`, `{"title":"movie/tmdb/949","audioTrack":"a01"}`,
		`{"title":"movie/tmdb/949","delivery":"transcode","transcode":{"burnSubtitle":"s007"}}`, `{"title":"movie/tmdb/949","delivery":"File"}`,
		`{"title":"movie/tmdb/949","delivery":"remux","mediaType":"video/MP4"}`, `{"title":"movie/tmdb/949","delivery":"remux"}`,
		`{"title":"movie/tmdb/949","maxBitrateKbps":10000001}`} {
		if code := call(body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	if code := call(`{"title":"movie/tmdb/9999999999"}`); code != 404 {
		t.Errorf("a title not offered: %d", code)
	}
	// Unknown values and members ignored; a track the copy hasn't, as absent.
	if code := call(`{"title":"movie/tmdb/949","capabilities":{"containers":["MP4","x"]},"audioTrack":"a7","other":1,
		"audioLanguage":"fr_CA","subtitleTrack":"s007","container":"x"}`); code != 200 {
		t.Errorf("unknown values: %d", code)
	}
	if got := h.started[0]; got.AudioTrack != "a0" {
		t.Errorf("A started %+v", got)
	}
	var ans struct {
		Stream struct{ URL string }
	}
	ans.Stream.URL = lastStream(t, s)
	if !strings.HasPrefix(ans.Stream.URL, s.a.srv.URL+"/api/v1/stream/") {
		t.Fatalf("stream URL %q", ans.Stream.URL)
	}
	get := func(url string, hdr ...string) int {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		if len(hdr) == 1 {
			req.Method = hdr[0]
		}
		res, err := s.a.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	base := strings.TrimSuffix(ans.Stream.URL, "master.m3u8")
	if code := get(ans.Stream.URL); code != 200 {
		t.Errorf("master: %d", code)
	}
	// A name no playlist gave yet: not served (MSFP media section 1).
	if code := get(base + "seg0.m4s"); code != 404 {
		t.Errorf("a segment before its playlist: %d", code)
	}
	if code := get(base + "v0.m3u8"); code != 200 {
		t.Errorf("the media playlist: %d", code)
	}
	cases := []struct {
		url  string
		hdr  []string
		want int
	}{
		{ans.Stream.URL + "?x=1", nil, 404}, {ans.Stream.URL, []string{"Origin", "https://example.org"}, 404},
		{ans.Stream.URL, []string{"Sec-Fetch-Mode", "cors"}, 404}, {ans.Stream.URL, []string{"Sec-Fetch-Site", "same-origin"}, 404},
		{ans.Stream.URL, []string{"POST"}, 405}, {ans.Stream.URL, []string{"HEAD"}, 405}, {base + "file", nil, 404},
		{base + "subtitles/s0/whole", []string{"HEAD"}, 405}, {base + "fonts/0", nil, 404}, {base + "ass/s0/0", nil, 404},
		{base + "queue/0/master.m3u8", nil, 404},
		{base + "%2e%2e/x", nil, 404}, {base + "seg0.m4s", []string{"Range", "bytes=-"}, 416},
		{base + "seg0.m4s", []string{"Range", "bytes=0-1,3-4"}, 416}, {base + "seg0.m4s", []string{"Range", "bytes=0-3"}, 206},
		{base + "seg0.m4s", []string{"Range", "bytes=40-"}, 416}, {base + "v0.m3u8", []string{"Range", "bytes=0-3"}, 200},
		{base + "trickplay/0", nil, 404}, {base + "subtitles/s0/01", nil, 404}, {base + "subtitles/s0/0", nil, 200},
		{base + "pictures/s0/0", nil, 404},
		{s.a.srv.URL + "/api/v1/stream/" + strings.Repeat("A", 43) + "/master.m3u8", nil, 404},
		// HEAD on a ticket never given, of every media form: 405 still.
		{s.a.srv.URL + "/api/v1/stream/" + strings.Repeat("A", 43) + "/master.m3u8", []string{"HEAD"}, 405},
		{s.a.srv.URL + "/api/v1/stream/" + strings.Repeat("A", 43) + "/file", []string{"HEAD"}, 405},
		{s.a.srv.URL + "/api/v1/stream/" + strings.Repeat("A", 43) + "/image", []string{"HEAD"}, 405},
		{s.a.srv.URL + "/api/v1/stream/" + strings.Repeat("A", 43) + "/queue/1/seg0.m4s", []string{"PUT"}, 405},
	}
	for _, c := range cases {
		if got := get(c.url, c.hdr...); got != c.want {
			t.Errorf("%s %v: %d, want %d", strings.TrimPrefix(c.url, base), c.hdr, got, c.want)
		}
	}
	// No stream at all for this pairing: refused before anything else.
	if err := s.a.eng.SetLimits(ctx, s.onA.ID, msfp.Limits{}); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "play", json.RawMessage(`{"title":"movie/tmdb/949"}`), &raw); code != 409 {
		t.Errorf("no streams: %d", code)
	}
}

// Two plays that arrive together never both take the last stream; a
// transcode past the pairing's transcodes is 503.
func TestLimitsAtOnce(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{NewPairing: &msfp.Limits{Streams: 1, Transcodes: 0}}, msfp.Config{})
	h.hold = make(chan struct{})
	var wg sync.WaitGroup
	var ok, limited atomic.Int32
	for range 2 {
		wg.Go(func() {
			_, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, msfp.ErrStreamLimitReached):
				limited.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	time.Sleep(200 * time.Millisecond)
	close(h.hold)
	wg.Wait()
	if ok.Load() != 1 || limited.Load() != 1 {
		t.Errorf("%d played, %d refused", ok.Load(), limited.Load())
	}
	for _, v := range s.a.eng.Playbacks() {
		s.a.eng.StopPlayback(v.ID, msfp.EndOperator)
	}
	h.mu.Lock()
	h.hold, h.method = nil, "transcode"
	h.mu.Unlock()
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); !errors.Is(err, msfp.ErrServerBusy) {
		t.Errorf("a transcode past the pairing's: %v", err)
	}
	// A host that doesn't call Admit: counted after, refused the same.
	h.mu.Lock()
	h.method, h.noAdmit = "direct", true
	h.mu.Unlock()
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil {
		t.Errorf("without Admit: %v", err)
	}
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); !errors.Is(err, msfp.ErrStreamLimitReached) {
		t.Errorf("without Admit, over the limit: %v", err)
	}
	waitAll(s.a)
	if len(h.stops()) == 0 {
		t.Error("the playback refused after it started wasn't stopped")
	}
}

// Every ending of MSFP playback section 6.3, each telling both sides.
func TestPlaybackEndings(t *testing.T) {
	ctx := context.Background()
	clockA := newClock(time.Now().Unix())
	h := filmHost()
	s := newSharing(t, h, msfp.Config{Clock: clockA}, msfp.Config{Clock: clockA})
	play := func() msfp.RemotePlayback {
		t.Helper()
		pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
		if err != nil {
			t.Fatal(err)
		}
		return pb
	}
	reason := func() msfp.EndReason {
		t.Helper()
		waitAll(s.a)
		ev, ok := has[msfp.PlaybackEnded](s.a.events)
		if !ok {
			t.Fatal("no PlaybackEnded")
		}
		return ev.Reason
	}
	// Expiry: two minutes without use.
	pb := play()
	clockA.Add(119 * time.Second)
	s.a.eng.RunOnce(ctx)
	if len(s.a.eng.Playbacks()) != 1 {
		t.Fatal("expired too soon")
	}
	clockA.Add(2 * time.Second)
	s.a.eng.RunOnce(ctx)
	if r := reason(); r != msfp.EndExpired || len(s.a.eng.Playbacks()) != 0 {
		t.Errorf("after 2 minutes: %v", r)
	}
	// The viewer's side learns at its next progress: 410.
	s.b.eng.Progress(pb.ID, 1000, true)
	waitAll(s.b)
	if ev, ok := has[msfp.RemotePlaybackEnded](s.b.events); !ok || ev.Playback != pb.ID {
		t.Errorf("B's playback after the 410: %+v %v", ev, ok)
	}
	// Too long.
	play()
	for range 25 {
		clockA.Add(time.Hour)
		for _, v := range s.a.eng.Playbacks() {
			s.a.eng.TouchForTest(v.ID)
		}
		s.a.eng.RunOnce(ctx)
	}
	if r := reason(); r != msfp.EndTooLong {
		t.Errorf("after 24 hours: %v", r)
	}
	// The library no longer offered.
	pb = play()
	if err := s.a.eng.SetOffers(ctx, s.onA.ID, nil); err != nil {
		t.Fatal(err)
	}
	if r := reason(); r != msfp.EndNotOffered {
		t.Errorf("offers withdrawn: %v", r)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a fetch after the offers ended: %v", err)
	}
	// The operator.
	s.a.eng.SetOffers(ctx, s.onA.ID, []string{"films"})
	play()
	s.a.eng.StopPlayback(s.a.eng.Playbacks()[0].ID, msfp.EndOperator)
	if r := reason(); r != msfp.EndOperator {
		t.Errorf("the operator: %v", r)
	}
	// The pairing ended, by the viewer's side: both end.
	pb = play()
	if err := s.b.eng.End(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	if r := reason(); r != msfp.EndPairingEnded {
		t.Errorf("the pairing ended: %v", r)
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a fetch after the pairing ended: %v", err)
	}
	if st := h.stops(); len(st) != 5 {
		t.Errorf("A's host told of %d endings: %v", len(st), st)
	}
}

// After a restart, the playbacks left on peers are stopped.
func TestLeftoversStopped(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil {
		t.Fatal(err)
	}
	s.b.reopen(t, msfp.Config{Store: s.b.store})
	if err := s.b.eng.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	waitAll(s.a)
	if len(s.a.eng.Playbacks()) != 0 || len(s.a.exchanges("stop")) != 1 {
		t.Errorf("after B's restart: %+v, %d stops", s.a.eng.Playbacks(), len(s.a.exchanges("stop")))
	}
	var left []msfp.Leftover
	s.b.store.Read(ctx, func(tx msfp.Tx) error { left, _ = tx.Leftovers(); return nil })
	if len(left) != 0 {
		t.Errorf("leftovers kept: %v", left)
	}
}

// A peer that says it can't be called is never fetched from: its
// playbacks end, its titles go, and nothing connects to it.
func TestNoMediaFromUncallable(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	var conns atomic.Int32
	cfgA := msfp.Config{Catalog: h, Playback: h, Media: h}
	h.libs = []msfp.Library{{ID: "films", Name: "Films"}}
	a := newServerWith(t, cfgA, serverOpts{conn: func() { conns.Add(1) }})
	b := newServer(t, msfp.Config{})
	onA, onB := pair(t, a, b)
	a.eng.Check(ctx, onA.ID)
	b.eng.Check(ctx, onB.ID)
	a.eng.SetOffers(ctx, onA.ID, []string{"films"})
	libs, _ := b.eng.TheirLibraries(ctx, onB.ID)
	b.eng.SetWants(ctx, onB.ID, []string{libs[0].ID})
	b.eng.ReadNow(ctx, onB.ID)
	pb, err := b.eng.Play(ctx, onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	// A can't be called now; its scheduled hello tells B.
	if err := a.eng.SetUncallable(ctx, true); err != nil {
		t.Fatal(err)
	}
	a.eng.RunOnce(ctx)
	waitAll(a, b)
	if v, _ := b.eng.Peer(ctx, onB.ID); v.Inbound != msfp.InboundNo {
		t.Fatalf("B sees A as %v", v.Inbound)
	}
	waitAll(b)
	before := conns.Load()
	if err := b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaMaster}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a fetch from a peer that can't be called: %v", err)
	}
	if _, err := b.eng.Play(ctx, onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); !errors.Is(err, msfp.ErrUncallable) {
		t.Errorf("a play on a peer that can't be called: %v", err)
	}
	if _, err := b.eng.ReadNow(ctx, onB.ID); !errors.Is(err, msfp.ErrUncallable) {
		t.Errorf("a reading of a peer that can't be called: %v", err)
	}
	if ok, err := b.eng.Playable(ctx, onB.ID, "movie"); ok || err != nil {
		t.Errorf("its titles playable: %v %v", ok, err)
	}
	if ok, err := b.eng.Playable(ctx, "nobody", "movie"); ok || !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("an unknown peer's titles: %v %v", ok, err)
	}
	if n := conns.Load() - before; n != 0 {
		t.Errorf("%d connections to a peer that can't be called", n)
	}
	var titles []msfp.Title
	b.store.Read(ctx, func(tx msfp.Tx) error { titles, _ = tx.PeerTitles(onB.ID, "", 10); return nil })
	if len(titles) != 0 {
		t.Errorf("its titles kept: %d", len(titles))
	}
}

// A music track plays audio only, counted apart when the limits say so.
func TestMusicPlay(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "music", Name: "Music"}}
	track := "track/mb/0c000000-0000-4000-8000-000000000003"
	h.titles[track] = musicTrack(track, "t")
	h.info["t"] = msfp.CopyInfo{Fonts: 2}
	s := newSharing(t, h, msfp.Config{NewPairing: &msfp.Limits{Streams: 0, Audio: true, AudioStreams: 1}}, msfp.Config{})
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: track, Delivery: msfp.DeliveryFile})
	if err != nil {
		t.Fatal(err)
	}
	if !h.started[0].AudioOnly || len(pb.Subtitles) != 0 || pb.Sheets != 0 || pb.Fonts != 0 || pb.Protocol != "file" ||
		pb.MimeType != "audio/flac" {
		t.Errorf("a track's playback: %+v / %+v", h.started[0], pb)
	}
	if strings.Contains(s.a.exchanges("play")[0].resBody, "subtitle") || strings.Contains(s.a.exchanges("play")[0].reqBody, "subtitle") {
		t.Errorf("subtitles with a track: %s", s.a.exchanges("play")[0].resBody)
	}
	// One audio stream: a second track waits for the first's stop.
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: track}); !errors.Is(err, msfp.ErrStreamLimitReached) {
		t.Errorf("a second track at once: %v", err)
	}
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: track, Delivery: msfp.DeliveryFile, Replaces: pb.ID}); err != nil {
		t.Errorf("the next track, replacing the first: %v", err)
	}
	// A film counts in streams: 0 here.
	h.film("music", "movie/tmdb/1", "en")
	var raw json.RawMessage
	if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "play", json.RawMessage(`{"title":"movie/tmdb/1"}`), &raw); code != 409 {
		t.Errorf("a film with no streams: %d", code)
	}
	if !strings.Contains(s.a.exchanges("libraries")[0].resBody, `"audioStreams":1`) {
		t.Errorf("the limits sent: %s", s.a.exchanges("libraries")[0].resBody)
	}
}

// The host's playlists are checked before they leave: one outside the
// subset is refused, with a warning, not sent.
func TestOwnPlaylistChecked(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	h.files["master.m3u8"] = []byte("#EXTM3U\n#EXT-X-KEY:METHOD=NONE\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n")
	h.files["v0.m3u8"] = []byte("#EXTM3U\n#EXT-X-PROGRAM-DATE-TIME:2020\n#EXTINF:6,\nseg0.m4s\n")
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []msfp.MediaPath{{Kind: msfp.MediaMaster}, {Kind: msfp.MediaPlaylist, Name: "v0.m3u8"}} {
		if err := s.b.eng.Fetch(ctx, pb.ID, p, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
			t.Errorf("%v: %v", p, err)
		}
	}
}

// Every fetch of a playlist under one ticket gives the same playlist
// (MSFP media section 3.1): the first served is kept, whatever the host
// makes after.
func TestPlaylistSameEachFetch(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil {
		t.Fatal(err)
	}
	url := lastStream(t, s)
	first, body := get(t, s, http.MethodGet, url)
	if first.StatusCode != 200 || body != filledMaster {
		t.Fatalf("the master playlist: %d %q", first.StatusCode, body)
	}
	h.mu.Lock()
	h.files["master.m3u8"] = []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nother.m3u8\n")
	h.mu.Unlock()
	if res, again := get(t, s, http.MethodGet, url); res.StatusCode != 200 || again != filledMaster {
		t.Errorf("fetched again: %d %q", res.StatusCode, again)
	}
}

// progress and stop of a playback not live, or another's; and an open
// response cut when its playback ends, on both sides.
func TestStatesAndCuts(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	call := func(name, body string) int {
		var raw json.RawMessage
		code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, name, json.RawMessage(body), &raw)
		return code
	}
	unknown := `{"id":"play_aaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	for _, c := range []struct {
		call, body string
		want       int
	}{
		{"progress", unknown, 410}, {"stop", unknown, 200}, {"progress", `{"id":"play.x"}`, 400},
		{"progress", `{}`, 400}, {"stop", `{"id":"play_aaaaaaaaaaaaaaaaaaaaaaaaaa","positionMs":-1}`, 400},
		{"progress", `{"id":"play_aaaaaaaaaaaaaaaaaaaaaaaaaa","positionMs":8640000001}`, 400},
	} {
		if got := call(c.call, c.body); got != c.want {
			t.Errorf("%s %s: %d, want %d", c.call, c.body, got, c.want)
		}
	}
	// A stopped playback: progress 410, stop 200 again.
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	var theirs struct{ ID string }
	json.Unmarshal([]byte(s.a.exchanges("play")[0].resBody), &theirs)
	if !strings.HasPrefix(theirs.ID, "play_") || len(theirs.ID) != 31 {
		t.Errorf("a playback ID: %q", theirs.ID)
	}
	// A second stop of the same playback is answered 200 too.
	stopped := call("stop", `{"id":"`+theirs.ID+`"}`)
	again := call("stop", `{"id":"`+theirs.ID+`"}`)
	if stopped != 200 || again != 200 || call("progress", `{"id":"`+theirs.ID+`"}`) != 410 {
		t.Error("a stopped playback's calls")
	}
	s.b.eng.Stop(ctx, pb.ID, -1)
	// A response held open: cut when the playback ends (the copy long
	// enough that its bytes don't end first).
	h.protocol = "file"
	h.mu.Lock()
	h.copyOf("movie/tmdb/949", 0).Size = 1 << 40
	h.copyOf("movie/tmdb/949", 1).Size = 1 << 40
	cut := make(chan struct{})
	h.serve = func(ctx context.Context, _ msfp.MediaPath, _ *msfp.Range, out msfp.ResponseSink) error {
		out.Start(200, "video/mp4", -1, "")
		for {
			if _, err := out.Write([]byte("x")); err != nil || ctx.Err() != nil {
				close(cut)
				return nil
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	h.mu.Unlock()
	pb, err = s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Delivery: msfp.DeliveryFile})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaFile}, "", &sink{}) }()
	time.Sleep(100 * time.Millisecond)
	if v := s.a.eng.Playbacks(); len(v) == 1 {
		s.a.eng.StopPlayback(v[0].ID, msfp.EndOperator)
	}
	select {
	case <-cut:
	case <-time.After(5 * time.Second):
		t.Fatal("the owning side's response wasn't cut")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay wasn't cut")
	}
}

// The viewer's side tells the owning server where its viewer is at the
// first report, at once on a pause or a resume, and every 45 seconds
// while the host reports, never more often (MSFP playback section 3);
// nothing once the host stops reporting.
func TestProgressPace(t *testing.T) {
	ctx := context.Background()
	clock := newClock(time.Now().Unix())
	h := filmHost()
	s := newSharing(t, h, msfp.Config{Clock: clock}, msfp.Config{Clock: clock})
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	sent := func() []int64 {
		t.Helper()
		waitAll(s.b, s.a)
		h.mu.Lock()
		defer h.mu.Unlock()
		return slices.Clone(h.progress)
	}
	report := func(after time.Duration, positionMs int64, paused bool) {
		clock.Add(after)
		s.b.eng.Progress(pb.ID, positionMs, paused)
		s.b.eng.RunOnce(ctx)
	}
	report(10*time.Second, 10_000, false)
	if got := sent(); !slices.Equal(got, []int64{10_000}) {
		t.Fatalf("the first report, soon after the start: %v", got)
	}
	report(10*time.Second, 20_000, false)
	report(10*time.Second, 30_000, false)
	report(10*time.Second, 40_000, false)
	report(10*time.Second, 50_000, false)
	if got := sent(); !slices.Equal(got, []int64{10_000}) {
		t.Fatalf("reports within 45 seconds: %v", got)
	}
	// 45 seconds after the first, with no report at that time: sent by
	// the work of each second, with the latest position.
	clock.Add(5 * time.Second)
	s.b.eng.RunOnce(ctx)
	if got := sent(); !slices.Equal(got, []int64{10_000, 50_000}) {
		t.Fatalf("45 seconds on: %v", got)
	}
	// A pause and a resume, each at once.
	report(time.Second, 51_000, true)
	report(time.Second, 51_000, false)
	if got := sent(); !slices.Equal(got, []int64{10_000, 50_000, 51_000, 51_000}) {
		t.Fatalf("a pause and a resume: %v", got)
	}
	// The host reports no more (the viewer's browser gone): nothing is
	// sent for it.
	clock.Add(2 * time.Minute)
	s.b.eng.RunOnce(ctx)
	if got := sent(); len(got) != 4 {
		t.Fatalf("without reports: %v", got)
	}
}

// A host ending a peer's playback by its own rules says why: the peer's
// ending is that one; a reason that isn't the host's is refused.
func TestStopPlaybackReason(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil {
		t.Fatal(err)
	}
	id := s.a.eng.Playbacks()[0].ID
	if err := s.a.eng.StopPlayback(id, msfp.EndStopped); !errors.Is(err, msfp.ErrInvalidReason) {
		t.Errorf("the viewer's ending, from the host: %v", err)
	}
	if err := s.a.eng.StopPlayback(id, msfp.EndExpired); err != nil {
		t.Fatal(err)
	}
	waitAll(s.a)
	if ev, ok := has[msfp.PlaybackEnded](s.a.events); !ok || ev.Reason != msfp.EndExpired {
		t.Errorf("the ending: %+v", ev)
	}
	if st := h.stops(); st["h1"] != msfp.EndExpired {
		t.Errorf("the host was told %v", st)
	}
	if err := s.a.eng.StopPlayback(id, msfp.EndExpired); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("again: %v", err)
	}
}

// Play's errors: what lasts says itself (here, B's key lost toward A,
// which never moved to it); only no answer, or none signed, is busy now
// (MSFP playback section 2.5).
func TestPlayErrorsToldApart(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	s := newSharing(t, filmHost(), msfp.Config{Clock: clock}, msfp.Config{Clock: clock})
	play := func() error {
		_, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
		return err
	}
	s.a.setDown(true)
	if err := play(); !errors.Is(err, msfp.ErrServerBusy) {
		t.Errorf("A down: %v", err)
	}
	// B rotates twice while A is down: A never knew B's new keys.
	for range 2 {
		if err := s.b.eng.Rotate(ctx, ""); err != nil {
			t.Fatal(err)
		}
		clock.Add(time.Hour)
	}
	s.a.setDown(false)
	if v := peerView(t, s.b, s.onB.ID); v.OurKey != msfp.KeyLost {
		t.Fatalf("A on B: %+v", v)
	}
	if err := play(); !errors.Is(err, msfp.ErrKeyLost) || errors.Is(err, msfp.ErrServerBusy) {
		t.Errorf("B's key lost toward A: %v", err)
	}
}

// The pairing ending while a playback starts ends it as the pairing's
// end, not as a copy no longer shared (MSFP playback section 6.3).
func TestPairingEndsWhilePlayStarts(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	h.mu.Lock()
	h.hold, h.entered = make(chan struct{}), make(chan struct{}, 1)
	h.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
		done <- err
	}()
	<-h.entered
	if err := s.a.eng.End(ctx, s.onA.ID); err != nil {
		t.Fatal(err)
	}
	close(h.hold)
	if err := <-done; err == nil {
		t.Error("played after the pairing ended")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if got := h.stopped["h1"]; got != msfp.EndPairingEnded {
		t.Errorf("the host's playback ended as %q", got)
	}
}

// musicTrack is a track of the album 0c…0002, with one FLAC copy of
// handle copy, 20 bytes long.
func musicTrack(key, copy string) *msfp.MusicTitle {
	return &msfp.MusicTitle{TitleCommon: msfp.TitleCommon{Key: key, Kind: "track", Libraries: []string{"music"}}, Name: "x",
		Album: "album/mb/0b000000-0000-4000-8000-000000000002", Disc: 1, Number: 1,
		Copies: []wire.MusicCopy{{ID: copy, DurationMs: 200_000, MediaType: "audio/flac", Codec: "flac", Size: 20, Etag: "m-" + copy}}}
}

// A host's start of another method than the engine decided is the
// host's failure: without delivery, a copy outside the baseline (here no
// colour stated, so not known BT.709) must be transcoded; a host that
// says remux is stopped, and the peer gets a 500, never the method
// (MSFP playback sections 2.3, 2.7).
func TestStartedMethodAsDecided(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	h.copyOf("movie/tmdb/949", 0).Video.Colour = nil
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	h.mu.Lock()
	h.method, h.lies = "remux", true
	h.mu.Unlock()
	_, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if !errors.Is(err, msfp.ErrPeerFailed) {
		t.Fatalf("a remux of a copy outside the baseline: %v", err)
	}
	h.mu.Lock()
	if len(h.started) != 1 || h.started[0].Served.Method != "transcode" || h.stopped["h1"] != msfp.EndStopped {
		t.Errorf("started %+v, stopped %v", h.started, h.stopped)
	}
	h.lies = false
	h.mu.Unlock()
	if plays := s.a.exchanges("play"); len(plays) != 1 || strings.Contains(plays[0].resBody, `"remux"`) {
		t.Errorf("the answers: %+v", plays)
	}
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil || pb.Method != "transcode" {
		t.Errorf("the transcode decided: %+v %v", pb, err)
	}
}
