package msfp_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// The revision of version 0 (proposal 16): deliveries and their
// refusals, the file by byte ranges, restarts on the same etag,
// downloads and scans within their limits, a music queue, previews and
// lyrics before any playback, ASS and fonts.

// lastStream is the stream URL of the last play A answered.
func lastStream(t *testing.T, s *sharing) string {
	t.Helper()
	plays := s.a.exchanges("play")
	var ans struct{ Stream struct{ URL string } }
	if len(plays) == 0 || json.Unmarshal([]byte(plays[len(plays)-1].resBody), &ans) != nil || ans.Stream.URL == "" {
		t.Fatal("no play answered")
	}
	return ans.Stream.URL
}

// get fetches a URL of A's stream with headers (name, value…), as a
// viewer's server would, and gives the answer and its body.
func get(t *testing.T, s *sharing, method, url string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Add(hdr[i], hdr[i+1])
	}
	res, err := s.a.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b bytes.Buffer
	b.ReadFrom(res.Body)
	return res, b.String()
}

func TestDeliveries(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	play := func(r msfp.PlayRequest) (msfp.RemotePlayback, error) {
		r.Title = "movie/tmdb/949"
		pb, err := s.b.eng.Play(ctx, s.onB.ID, r)
		if err == nil {
			s.b.eng.Stop(ctx, pb.ID, -1)
		}
		return pb, err
	}
	// File: the copy as it is, its etag and exact size.
	pb, err := play(msfp.PlayRequest{Delivery: msfp.DeliveryFile})
	if err != nil || pb.Protocol != "file" || pb.Method != "direct" || pb.Etag != "e0" || pb.Size != 20 || pb.CopyChanged {
		t.Errorf("file: %+v %v", pb, err)
	}
	if got := h.started[len(h.started)-1]; got.Delivery != msfp.DeliveryFile || got.Purpose != msfp.PurposePlay {
		t.Errorf("the host started %+v", got)
	}
	// Remux needs a container; the host is told it.
	if _, err := play(msfp.PlayRequest{Delivery: msfp.DeliveryRemux}); !errors.Is(err, msfp.ErrInvalidRequest) {
		t.Errorf("remux without a container: %v", err)
	}
	if pb, err := play(msfp.PlayRequest{Delivery: msfp.DeliveryRemux, Container: "mp4"}); err != nil || pb.Method != "remux" ||
		h.started[len(h.started)-1].Container != "mp4" {
		t.Errorf("remux: %+v %v", pb, err)
	}
	// A subtitle burned in, of the copy; another refused by MSFP itself.
	pb, err = play(msfp.PlayRequest{Delivery: msfp.DeliveryTranscode, Transcode: &msfp.TranscodeTargets{BurnSubtitle: "s0", MaxHeight: 1000}})
	if err != nil || pb.BurnedTrack != "s0" || pb.SubtitleTrack != "s0" || h.started[len(h.started)-1].Transcode.MaxHeight != 720 {
		t.Errorf("a subtitle burned in: %+v %v", pb, err)
	}
	before := len(h.started)
	if _, err := play(msfp.PlayRequest{Delivery: msfp.DeliveryTranscode, Transcode: &msfp.TranscodeTargets{BurnSubtitle: "s9"}}); !errors.Is(err, msfp.ErrDeliveryRefused) {
		t.Errorf("a burnSubtitle the copy hasn't: %v", err)
	}
	// The host's own refusal.
	h.mu.Lock()
	h.startErr = msfp.ErrDeliveryRefused
	h.mu.Unlock()
	if _, err := play(msfp.PlayRequest{Delivery: msfp.DeliveryTranscode}); !errors.Is(err, msfp.ErrDeliveryRefused) {
		t.Errorf("the host's refusal: %v", err)
	}
	h.mu.Lock()
	h.startErr = nil
	h.mu.Unlock()
	// A file above the pairing's bitrate cap.
	h.mu.Lock()
	h.copyOf("movie/tmdb/949", 0).BitrateKbps = 50_000
	h.mu.Unlock()
	if err := s.a.eng.SetLimits(ctx, s.onA.ID, msfp.Limits{Streams: 2, Transcodes: 1, BitrateKbps: 10_000}); err != nil {
		t.Fatal(err)
	}
	if _, err := play(msfp.PlayRequest{Delivery: msfp.DeliveryFile}); !errors.Is(err, msfp.ErrDeliveryRefused) {
		t.Errorf("a file above the cap: %v", err)
	}
	if len(h.started) != before {
		t.Errorf("the host was asked %d times for deliveries MSFP refuses", len(h.started)-before-1)
	}
	// Combinations of purpose and delivery MSFP refuses, from the peer.
	var raw json.RawMessage
	for _, body := range []string{`{"title":"movie/tmdb/949","purpose":"scan","delivery":"remux","container":"mp4"}`,
		`{"title":"movie/tmdb/949","purpose":"download","delivery":"transcode"}`, `{"title":"movie/tmdb/949","delivery":"hls"}`,
		`{"title":"movie/tmdb/949","transcode":{"maxHeight":720}}`, `{"title":"movie/tmdb/949","delivery":"transcode","transcode":{"maxHeight":719}}`,
		`{"title":"movie/tmdb/949","delivery":"transcode","transcode":{"burnSubtitle":"off"}}`} {
		if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "play", json.RawMessage(body), &raw); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
}

// The file by byte ranges (MSFP media sections 2.1, 2.2 and 3.2), on the
// owning side and through the relay.
func TestFileRanges(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newHostile(t, h)
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Delivery: msfp.DeliveryFile})
	if err != nil {
		t.Fatal(err)
	}
	url := lastStream(t, s.sharing)
	for _, c := range []struct {
		hdr           []string
		status        int
		body, cr, len string
	}{
		{nil, 200, "0123456789abcdefghij", "", "20"},
		{[]string{"Range", "bytes=2-5"}, 206, "2345", "bytes 2-5/20", "4"},
		{[]string{"Range", "bytes=-3"}, 206, "hij", "bytes 17-19/20", "3"},
		{[]string{"Range", "bytes=15-99"}, 206, "fghij", "bytes 15-19/20", "5"},
		{[]string{"Range", "bytes=20-"}, 416, "", "bytes */20", ""},
		{[]string{"Range", "bytes=0-1,3-4"}, 416, "", "bytes */20", ""},
		{[]string{"Range", "bytes=0-1", "Range", "bytes=3-4"}, 416, "", "bytes */20", ""},
	} {
		res, body := get(t, s.sharing, http.MethodGet, url, c.hdr...)
		if res.StatusCode != c.status || body != c.body || res.Header.Get("Content-Range") != c.cr ||
			c.len != "" && res.Header.Get("Content-Length") != c.len {
			t.Errorf("%v: %d %q %q", c.hdr, res.StatusCode, body, res.Header.Get("Content-Range"))
		}
		if c.status != 416 && (res.Header.Get("ETag") != `"e0"` || res.Header.Get("Accept-Ranges") != "bytes") {
			t.Errorf("%v: the file's headers %v", c.hdr, res.Header)
		}
	}
	for _, m := range []string{http.MethodHead, http.MethodPost, http.MethodPut} {
		if res, _ := get(t, s.sharing, m, url); res.StatusCode != 405 || res.Header.Get("Allow") != "GET" {
			t.Errorf("%s: %d %q", m, res.StatusCode, res.Header.Get("Allow"))
		}
	}
	// Through the relay.
	fetch := func(rng string) (sink, error) {
		var out sink
		return out, s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaFile}, rng, &out)
	}
	if out, err := fetch("bytes=2-5"); err != nil || out.status != 206 || string(out.body) != "2345" || out.contentRange != "bytes 2-5/20" {
		t.Errorf("a range relayed: %v %+v", err, out)
	}
	if out, err := fetch(""); err != nil || out.status != 200 || string(out.body) != "0123456789abcdefghij" {
		t.Errorf("the whole file relayed: %v %+v", err, out)
	}
	if _, err := fetch("bytes=30-"); !errors.Is(err, msfp.ErrRangeNotSatisfiable) {
		t.Errorf("a range past the end: %v", err)
	}
	// Another etag, another size, a 200 to a range: the copy changed.
	for name, f := range map[string]func(h http.Header, b []byte) []byte{
		"another etag": func(h http.Header, b []byte) []byte { h.Set("ETag", `"e9"`); return b },
		"a weak etag":  func(h http.Header, b []byte) []byte { h.Set("ETag", `W/"e0"`); return b },
		"another size": func(h http.Header, b []byte) []byte { h.Set("Content-Range", "bytes 2-5/21"); return b },
		"a 200": func(h http.Header, b []byte) []byte {
			h.Del("Content-Range")
			return []byte("0123456789abcdefghij")
		},
	} {
		s.set("file", f)
		if _, err := fetch("bytes=2-5"); !errors.Is(err, msfp.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A file being read whose playback ended starts again on the same etag,
// and only on it (MSFP playback section 2.7).
func TestRestart(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	old, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Delivery: msfp.DeliveryFile})
	if err != nil {
		t.Fatal(err)
	}
	s.a.eng.StopPlayback(s.a.eng.Playbacks()[0].ID, msfp.EndExpired)
	waitAll(s.a)
	again, err := s.b.eng.Restart(ctx, old)
	if err != nil || again.ID == old.ID || again.Etag != old.Etag || again.VersionID != old.VersionID || again.Size != 20 {
		t.Fatalf("a restart: %+v %v", again, err)
	}
	if got := h.started[len(h.started)-1]; got.Delivery != msfp.DeliveryFile || got.Copy != "movie/tmdb/949#0" {
		t.Errorf("the restart started %+v", got)
	}
	var out sink
	if err := s.b.eng.Fetch(ctx, again.ID, msfp.MediaPath{Kind: msfp.MediaFile}, "bytes=10-", &out); err != nil || string(out.body) != "abcdefghij" {
		t.Errorf("reading on: %v %q", err, out.body)
	}
	// The copy changed meanwhile: no join, the new playback stopped.
	h.mu.Lock()
	h.copyOf("movie/tmdb/949", 0).ETag = "e9"
	h.mu.Unlock()
	stops := len(s.a.exchanges("stop"))
	if _, err := s.b.eng.Restart(ctx, again); !errors.Is(err, msfp.ErrCopyChanged) {
		t.Errorf("a restart of a changed copy: %v", err)
	}
	waitAll(s.a, s.b)
	if n := len(s.a.exchanges("stop")) - stops; n != 2 {
		t.Errorf("%d stops: the live playback's and the new one's", n)
	}
	if len(s.a.eng.Playbacks()) != 0 {
		t.Errorf("playbacks left: %+v", s.a.eng.Playbacks())
	}
	if _, err := s.b.eng.Restart(ctx, msfp.RemotePlayback{Peer: s.onB.ID, Title: "movie/tmdb/949", Etag: "e0"}); !errors.Is(err, msfp.ErrInvalidRequest) {
		t.Errorf("a restart of no file: %v", err)
	}
}

// Downloads: only from a peer that allows them, within its limits at
// once and a day, their ticket without the idle rule, their late reports
// after their end (MSFP playback section 7).
func TestDownloads(t *testing.T) {
	ctx := context.Background()
	clock := newClock(time.Now().Unix())
	h := filmHost()
	limits := &msfp.Limits{Streams: 2, Transcodes: 1, Downloads: 1, DownloadsPerDay: 2, DownloadKbps: 100_000}
	s := newSharing(t, h, msfp.Config{Clock: clock, NewPairing: limits}, msfp.Config{Clock: clock})
	if v := peerView(t, s.b, s.onB.ID); v.TheirLimits == nil || v.TheirLimits.Downloads != 1 || v.TheirLimits.DownloadsPerDay != 2 {
		t.Fatalf("the limits B keeps: %+v", v.TheirLimits)
	}
	if !strings.Contains(s.a.exchanges("libraries")[0].resBody, `"downloads":1,"downloadsPerDay":2,"downloadKbps":100000`) {
		t.Errorf("the limits sent: %s", s.a.exchanges("libraries")[0].resBody)
	}
	download := func() (msfp.RemotePlayback, error) {
		return s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Purpose: msfp.PurposeDownload,
			Delivery: msfp.DeliveryFile, StartMs: 60_000})
	}
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Purpose: msfp.PurposeDownload,
		Delivery: msfp.DeliveryRemux, Container: "mp4"}); !errors.Is(err, msfp.ErrInvalidRequest) {
		t.Errorf("a download as a remux: %v", err)
	}
	first, err := download()
	if err != nil {
		t.Fatal(err)
	}
	if got := h.started[len(h.started)-1]; got.Purpose != msfp.PurposeDownload || got.StartMs != 0 {
		t.Errorf("the host started %+v", got)
	}
	if v := s.a.eng.Playbacks(); len(v) != 1 || v[0].Purpose != msfp.PurposeDownload {
		t.Errorf("A's view: %+v", v)
	}
	if _, err := download(); !errors.Is(err, msfp.ErrStreamLimitReached) {
		t.Errorf("two downloads at once: %v", err)
	}
	// A download counts against no stream.
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil {
		t.Errorf("a play beside a download: %v", err)
	}
	// No idle rule: three minutes without a read, it lives on.
	clock.Add(3 * time.Minute)
	s.a.eng.RunOnce(ctx)
	if v := s.a.eng.Playbacks(); len(v) != 1 || v[0].Purpose != msfp.PurposeDownload {
		t.Fatalf("after three minutes unused: %+v", v)
	}
	var out sink
	if err := s.b.eng.Fetch(ctx, first.ID, msfp.MediaPath{Kind: msfp.MediaFile}, "bytes=0-3", &out); err != nil || string(out.body) != "0123" {
		t.Errorf("a download's read: %v %q", err, out.body)
	}
	if err := s.b.eng.Stop(ctx, first.ID, 0); err != nil {
		t.Fatal(err)
	}
	second, err := download()
	if err != nil {
		t.Fatalf("a second download in the day: %v", err)
	}
	s.b.eng.Stop(ctx, second.ID, -1)
	if _, err := download(); !errors.Is(err, msfp.ErrStreamLimitReached) {
		t.Errorf("a third in the day: %v", err)
	}
	// Late reports, once ended: kept for 30 days, the playback ended;
	// one older than what was kept (the stop's), ignored.
	waitAll(s.a)
	h.mu.Lock()
	reports := len(h.reports)
	h.mu.Unlock()
	if err := s.b.eng.ReportOffline(ctx, first.ID, msfp.OfflineReport{PositionMs: 1, At: clock.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("an older late progress: %v", err)
	}
	h.mu.Lock()
	if len(h.reports) != reports {
		t.Errorf("an older late report reached the host: %+v", h.reports[reports:])
	}
	h.mu.Unlock()
	clock.Add(10 * time.Minute)
	at := clock.Now().Add(-2 * time.Minute)
	if err := s.b.eng.ReportOffline(ctx, first.ID, msfp.OfflineReport{PositionMs: 1_000_000, At: at}); err != nil {
		t.Fatalf("a late progress: %v", err)
	}
	if err := s.b.eng.ReportOffline(ctx, first.ID, msfp.OfflineReport{PositionMs: 2_000_000, At: at.Add(time.Minute), Stop: true}); err != nil {
		t.Fatalf("a late stop: %v", err)
	}
	h.mu.Lock()
	last := h.reports[len(h.reports)-1]
	h.mu.Unlock()
	if !last.Late || !last.Stop || last.PositionMs != 2_000_000 || !last.At.Equal(at.Add(time.Minute)) || last.Handle != "h1" {
		t.Errorf("the host's late report: %+v", last)
	}
	if !strings.Contains(s.a.exchanges("progress")[0].reqBody, `"at":`) {
		t.Errorf("a late progress as sent: %s", s.a.exchanges("progress")[0].reqBody)
	}
	if err := s.b.eng.ReportOffline(ctx, first.ID, msfp.OfflineReport{At: clock.Now().Add(time.Hour)}); !errors.Is(err, msfp.ErrInvalidRequest) {
		t.Errorf("an at in the future: %v", err)
	}
	// A play's progress after its end is still refused.
	var raw json.RawMessage
	var theirs struct{ ID string }
	json.Unmarshal([]byte(s.a.exchanges("play")[2].resBody), &theirs)
	body := `{"id":"` + theirs.ID + `","at":` + jsonInt(clock.Now().Unix()) + `}`
	s.a.eng.StopPlayback(theirs.ID, msfp.EndOperator)
	if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "progress", json.RawMessage(body), &raw); code != 410 {
		t.Errorf("a play's late progress: %d", code)
	}
	// The 24 hours of a download's ticket.
	third, err := func() (msfp.RemotePlayback, error) {
		clock.Add(25 * time.Hour)
		return download()
	}()
	if err != nil {
		t.Fatalf("a download the next day: %v", err)
	}
	clock.Add(24 * time.Hour)
	s.a.eng.RunOnce(ctx)
	if v := s.a.eng.Playbacks(); len(v) != 0 {
		t.Errorf("a download after 24 hours: %+v", v)
	}
	_ = third
	// A peer without downloads: none asked.
	before := len(s.a.exchanges("play"))
	s.a.eng.SetLimits(ctx, s.onA.ID, msfp.Limits{Streams: 2, Transcodes: 1})
	s.b.eng.ReadNow(ctx, s.onB.ID)
	if _, err := download(); !errors.Is(err, msfp.ErrStreamLimitReached) || len(s.a.exchanges("play")) != before {
		t.Errorf("a download of a peer that allows none: %v", err)
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

// A download's reads are paced at its speed cap, a play's are not.
func TestDownloadPace(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	big := bytes.Repeat([]byte("0123456789"), 30_000) // 300,000 bytes
	h.files["file"] = big
	h.copyOf("movie/tmdb/949", 0).Size = int64(len(big))
	limits := &msfp.Limits{Streams: 1, Transcodes: 1, Downloads: 1, DownloadKbps: 800} // 100,000 bytes a second
	s := newSharing(t, h, msfp.Config{NewPairing: limits}, msfp.Config{})
	read := func(purpose string) time.Duration {
		t.Helper()
		pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Purpose: purpose, Delivery: msfp.DeliveryFile})
		if err != nil {
			t.Fatal(err)
		}
		defer s.b.eng.Stop(ctx, pb.ID, -1)
		start := time.Now()
		var out sink
		if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaFile}, "", &out); err != nil || !bytes.Equal(out.body, big) {
			t.Fatalf("%s: %v, %d bytes", purpose, err, len(out.body))
		}
		return time.Since(start)
	}
	if d := read(msfp.PurposeDownload); d < 1500*time.Millisecond {
		t.Errorf("a download of 300,000 bytes at 100,000 a second in %v", d)
	}
	if d := read(msfp.PurposePlay); d > time.Second {
		t.Errorf("a play's reads paced: %v", d)
	}
}

// Scans: a range of the file only, within 32 MiB per ticket, cut at the
// cap, then 416; a day's count; the idle rule (MSFP playback section 8,
// media section 4.2).
func TestScans(t *testing.T) {
	ctx := context.Background()
	clock := newClock(time.Now().Unix())
	h := filmHost()
	big := make([]byte, wire.ScanCap+1000)
	for i := range big {
		big[i] = byte(i)
	}
	h.files["file"] = big
	h.copyOf("movie/tmdb/949", 0).Size = int64(len(big))
	h.copyOf("movie/tmdb/949", 0).Previews = true
	h.info["movie/tmdb/949#0"] = msfp.CopyInfo{Trickplay: &msfp.Trickplay{TileWidth: 10, TileHeight: 10, Columns: 2, Rows: 1, IntervalMs: 10_000, TileCount: 3}}
	s := newSharing(t, h, msfp.Config{Clock: clock, NewPairing: &msfp.Limits{Streams: 1, Transcodes: 1, ScansPerDay: 2}}, msfp.Config{Clock: clock})
	scan, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Purpose: msfp.PurposeScan})
	if err != nil {
		t.Fatal(err)
	}
	if scan.Delivery != msfp.DeliveryFile || scan.Protocol != "file" || len(scan.SubtitleTracks) != 0 || scan.Trickplay != nil {
		t.Errorf("a scan's playback: %+v", scan)
	}
	if !strings.Contains(s.a.exchanges("play")[0].reqBody, `"purpose":"scan","delivery":"file"`) {
		t.Errorf("a scan's request: %s", s.a.exchanges("play")[0].reqBody)
	}
	// A scan counts against no stream.
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil {
		t.Errorf("a play beside a scan: %v", err)
	}
	url := lastStream(t, s)
	fetch := func(rng string) (sink, error) {
		var out sink
		return out, s.b.eng.Fetch(ctx, scan.ID, msfp.MediaPath{Kind: msfp.MediaFile}, rng, &out)
	}
	if _, err := fetch(""); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a scan's read without a range: %v", err)
	}
	if out, err := fetch("bytes=0-9"); err != nil || !bytes.Equal(out.body, big[:10]) {
		t.Errorf("a scan's first read: %v", err)
	}
	out, err := fetch("bytes=0-")
	if err != nil || out.status != 206 || int64(len(out.body)) != wire.ScanCap-10 || out.contentRange != "bytes 0-33554421/33555432" {
		t.Errorf("a read cut at the cap: %v %d %q", err, len(out.body), out.contentRange)
	}
	if _, err := fetch("bytes=0-0"); !errors.Is(err, msfp.ErrRangeNotSatisfiable) {
		t.Errorf("a read once the cap is spent: %v", err)
	}
	// Only the file opens under a scan's ticket; its progress changes
	// nothing.
	base := strings.TrimSuffix(lastStreamOf(t, s, 0), "file")
	for _, name := range []string{"subtitles/s0/0", "trickplay/0", "master.m3u8"} {
		if res, _ := get(t, s, http.MethodGet, base+name); res.StatusCode != 404 {
			t.Errorf("%s under a scan's ticket: %d", name, res.StatusCode)
		}
	}
	_ = url
	var raw json.RawMessage
	var theirs struct{ ID string }
	json.Unmarshal([]byte(s.a.exchanges("play")[0].resBody), &theirs)
	if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "progress", json.RawMessage(`{"id":"`+theirs.ID+`","positionMs":5}`), &raw); code != 200 {
		t.Errorf("a scan's progress: %d", code)
	}
	if h.progress != nil {
		t.Errorf("a scan's progress reached the host: %v", h.progress)
	}
	// The idle rule holds.
	clock.Add(3 * time.Minute)
	s.a.eng.RunOnce(ctx)
	if v := s.a.eng.Playbacks(); len(v) != 0 {
		t.Errorf("a scan after 3 minutes unused: %+v", v)
	}
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Purpose: msfp.PurposeScan}); err != nil {
		t.Errorf("a second scan: %v", err)
	}
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Purpose: msfp.PurposeScan}); !errors.Is(err, msfp.ErrStreamLimitReached) {
		t.Errorf("a third in the day: %v", err)
	}
}

// lastStreamOf is the stream URL of play answer n of A.
func lastStreamOf(t *testing.T, s *sharing, n int) string {
	t.Helper()
	var ans struct{ Stream struct{ URL string } }
	json.Unmarshal([]byte(s.a.exchanges("play")[n].resBody), &ans)
	return ans.Stream.URL
}

// A music play's queue: its entries prepared under one playback, each at
// its own URL by its place, a place left empty for a track not offered;
// the track playing in progress (MSFP music section 5.2).
func TestMusicQueue(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "music", Name: "Music", Kind: "music"}}
	key := func(n string) string { return "track/mb/0c000000-0000-4000-8000-00000000000" + n }
	for _, n := range []string{"3", "5", "6"} {
		h.titles[key(n)] = musicTrack(key(n), "c"+n)
	}
	h.protocol, h.queue = "file", true
	h.files["file"], h.files["queue/0/file"], h.files["queue/2/file"] = []byte("0123456789abcdefghij"), []byte("queue entry zero...."), []byte("queue entry two....!")
	s := newSharing(t, h, msfp.Config{NewPairing: &msfp.Limits{Streams: 1, Transcodes: 1}}, msfp.Config{})
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: key("3"), Delivery: msfp.DeliveryFile, Queue: []string{key("5"), key("7"), key("6")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pb.Queue) != 2 || pb.Queue[0].Place != 0 || pb.Queue[0].Title != key("5") || pb.Queue[1].Place != 2 || pb.Queue[1].DurationMs != 200_000 {
		t.Fatalf("the queue kept: %+v", pb.Queue)
	}
	if got := h.started[0].Queue; len(got) != 2 || got[0].Copy != "c5" || got[1].Place != 2 {
		t.Errorf("the host was asked %+v", got)
	}
	if body := s.a.exchanges("play")[0].resBody; !strings.Contains(body, "/queue/0/file") || !strings.Contains(body, "/queue/2/file") {
		t.Errorf("the queue's URLs: %s", body)
	}
	for entry, want := range map[int]string{0: "0123456789abcdefghij", 1: "queue entry zero....", 3: "queue entry two....!"} {
		var out sink
		if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaFile, Entry: entry}, "", &out); err != nil || string(out.body) != want {
			t.Errorf("entry %d: %v %q", entry, err, out.body)
		}
	}
	if err := s.b.eng.Fetch(ctx, pb.ID, msfp.MediaPath{Kind: msfp.MediaFile, Entry: 2}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a place left empty: %v", err)
	}
	// One stream for the whole queue.
	if v := s.a.eng.Playbacks(); len(v) != 1 {
		t.Errorf("playbacks: %+v", v)
	}
	// The track playing, in progress.
	if err := s.b.eng.PlayingTrack(pb.ID, key("7")); !errors.Is(err, msfp.ErrInvalidRequest) {
		t.Errorf("a track left out of the queue: %v", err)
	}
	if err := s.b.eng.PlayingTrack(pb.ID, key("6")); err != nil {
		t.Fatal(err)
	}
	s.b.eng.Progress(pb.ID, 5000, false)
	waitAll(s.b, s.a)
	h.mu.Lock()
	last := h.reports[len(h.reports)-1]
	h.mu.Unlock()
	if last.Title != key("6") || last.PositionMs != 5000 {
		t.Errorf("the host's report: %+v", last)
	}
	// A queue MSFP refuses.
	long := []string{key("5"), key("6"), key("7"), key("8"), key("9"), "track/mb/0c000000-0000-4000-8000-000000000010"}
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: key("3"), Queue: long, Replaces: pb.ID}); !errors.Is(err, msfp.ErrInvalidRequest) {
		t.Errorf("a queue of 6: %v", err)
	}
	var raw json.RawMessage
	if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "play", json.RawMessage(`{"title":"`+key("3")+`","queue":["`+key("3")+`"]}`), &raw); code != 400 {
		t.Errorf("a queue holding the title: %d", code)
	}
}

// previews before a first play, under a ticket of their own that is no
// playback; lyrics with none (MSFP video section 5.6, music section 6).
func TestPreviewsAndLyrics(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	h.libs = []msfp.Library{{ID: "films", Name: "Films", Kind: "movies"}, {ID: "music", Name: "Music", Kind: "music"}}
	h.copyOf("movie/tmdb/949", 0).Previews = true
	h.info["movie/tmdb/949#0"] = msfp.CopyInfo{Trickplay: &msfp.Trickplay{TileWidth: 10, TileHeight: 10, Columns: 2, Rows: 1, IntervalMs: 10_000, TileCount: 3}}
	sheet := jpegOf(t, 20, 10)
	h.files["trickplay/0"], h.files["trickplay/1"] = sheet, sheet
	track := "track/mb/0c000000-0000-4000-8000-000000000003"
	h.titles[track] = musicTrack(track, "t")
	h.titles[track].(*msfp.MusicTitle).Copies[0].Lyrics = "synced"
	zero, later := int64(0), int64(1500)
	h.lyrics = map[string][]wire.Lyrics{"t": {{Lang: "en", Kind: "main", Synced: true,
		Line: []wire.LyricsLine{{Start: &zero, Value: "Hello\u0007"}, {Start: &later, Value: ""}}}}}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	titles := s.peerTitles(t)
	copies := titles["movie/tmdb/949"].(*msfp.VideoTitle).Copies
	if !copies[0].Previews || copies[1].Previews {
		t.Fatalf("the copies kept: %+v", copies)
	}
	pv, err := s.b.eng.Previews(ctx, s.onB.ID, "movie/tmdb/949", copies[0].ID)
	if err != nil || pv.Trickplay.Sheets != 2 || len(pv.Sheets) != 2 || pv.Trickplay.TileCount != 3 {
		t.Fatalf("the previews: %+v %v", pv.Trickplay, err)
	}
	if len(s.a.eng.Playbacks()) != 0 || len(h.started) != 0 {
		t.Error("previews started a playback")
	}
	if _, err := s.b.eng.Previews(ctx, s.onB.ID, "movie/tmdb/949", copies[1].ID); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a copy without previews: %v", err)
	}
	// The ticket opens the sheets only, of that copy.
	var ans struct {
		Trickplay struct{ Sheets []string }
	}
	json.Unmarshal([]byte(s.a.exchanges("previews")[0].resBody), &ans)
	if len(ans.Trickplay.Sheets) != 2 {
		t.Fatalf("the previews answer: %s", s.a.exchanges("previews")[0].resBody)
	}
	base := strings.TrimSuffix(ans.Trickplay.Sheets[0], "trickplay/0")
	for name, want := range map[string]int{"trickplay/1": 200, "trickplay/2": 404, "file": 404, "master.m3u8": 404, "image": 404} {
		if res, _ := get(t, s, http.MethodGet, base+name); res.StatusCode != want {
			t.Errorf("%s under a previews ticket: %d, want %d", name, res.StatusCode, want)
		}
	}
	if res, _ := get(t, s, http.MethodHead, base+"trickplay/0"); res.StatusCode != 405 {
		t.Errorf("HEAD under a previews ticket: %d", res.StatusCode)
	}
	var raw json.RawMessage
	for body, want := range map[string]int{`{"title":"movie/tmdb/949"}`: 400, `{"title":"series/tmdb/1","versionId":"x"}`: 400,
		`{"title":"movie/tmdb/949","versionId":"nope"}`: 404, `{"title":"movie/tmdb/949","versionId":"` + copies[1].ID + `"}`: 404} {
		if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "previews", json.RawMessage(body), &raw); code != want {
			t.Errorf("previews %s: %d, want %d", body, code, want)
		}
	}
	// Lyrics, cleaned.
	mcopy := titles[track].(*msfp.MusicTitle).Copies[0]
	sets, err := s.b.eng.TrackLyrics(ctx, s.onB.ID, track, mcopy.ID)
	if err != nil || len(sets) != 1 || len(sets[0].Line) != 2 || sets[0].Line[0].Value != "Hello" || *sets[0].Line[1].Start != 1500 {
		t.Errorf("the lyrics: %+v %v", sets, err)
	}
	if _, err := s.b.eng.TrackLyrics(ctx, s.onB.ID, "movie/tmdb/949", copies[0].ID); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("lyrics of a film: %v", err)
	}
	for body, want := range map[string]int{`{"title":"` + track + `"}`: 400, `{"title":"` + track + `","versionId":"nope"}`: 404} {
		if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "lyrics", json.RawMessage(body), &raw); code != want {
			t.Errorf("lyrics %s: %d, want %d", body, code, want)
		}
	}
}

// ASS tracks and their fonts, whole text tracks: served from the host,
// rebuilt or checked on both sides (MSFP video sections 6.3 to 6.6).
func TestASSAndFonts(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("wire/testdata/v0/media.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		ASS []struct {
			Text, Output string
			Valid        bool
		}
		Fonts []struct {
			Hex, Type string
			Valid     bool
		}
	}
	json.Unmarshal(raw, &v)
	var ass, assOut, font, fontType string
	for _, c := range v.ASS {
		if c.Valid && ass == "" {
			ass, assOut = c.Text, c.Output
		}
	}
	for _, c := range v.Fonts {
		if c.Valid && font == "" {
			b, _ := hex.DecodeString(c.Hex)
			font, fontType = string(b), c.Type
		}
	}
	h := filmHost()
	c := h.copyOf("movie/tmdb/949", 0)
	c.SubtitleTracks = append(c.SubtitleTracks, wire.CopySubtitle{ID: "s2", Language: "ja", Format: "ass"}, wire.CopySubtitle{ID: "s3", Format: "other"})
	h.info["movie/tmdb/949#0"] = msfp.CopyInfo{Fonts: 1, NotApart: []string{"s1"}}
	h.files["ass/s2/0"], h.files["ass/s2/whole"], h.files["fonts/0"] = []byte(ass), []byte(ass), []byte(font)
	h.files["subtitles/s2/whole"] = []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<b>whole</b>\n")
	s := newHostile(t, h)
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]msfp.Track{}
	for _, t := range pb.SubtitleTracks {
		byID[t.ID] = t
	}
	if s2 := byID["s2"]; !s2.ASS || !s2.Text || s2.Format != "ass" || pb.Fonts != 1 || byID["s1"].Pictures || byID["s3"].Text || len(byID) != 4 {
		t.Fatalf("the subtitles kept: %+v, %d fonts", pb.SubtitleTracks, pb.Fonts)
	}
	fetch := func(p msfp.MediaPath) (sink, error) {
		var out sink
		return out, s.b.eng.Fetch(ctx, pb.ID, p, "", &out)
	}
	if out, err := fetch(msfp.MediaPath{Kind: msfp.MediaASS, Track: "s2"}); err != nil || string(out.body) != assOut || out.contentType != "text/x-ass; charset=utf-8" {
		t.Errorf("an ASS stretch: %v %q", err, out.body)
	}
	if _, err := fetch(msfp.MediaPath{Kind: msfp.MediaASS, Track: "s2", Whole: true}); err != nil {
		t.Errorf("a whole ASS track: %v", err)
	}
	if out, err := fetch(msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s2", Whole: true}); err != nil ||
		string(out.body) != "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<b>whole</b>\n" {
		t.Errorf("a whole text track: %v %q", err, out.body)
	}
	if out, err := fetch(msfp.MediaPath{Kind: msfp.MediaFont}); err != nil || out.contentType != fontType || string(out.body) != font {
		t.Errorf("a font: %v %q", err, out.contentType)
	}
	for _, p := range []msfp.MediaPath{{Kind: msfp.MediaFont, Font: 1}, {Kind: msfp.MediaASS, Track: "s0"},
		{Kind: msfp.MediaPictures, Track: "s1"}, {Kind: msfp.MediaPictures, Track: "s2", Whole: true}} {
		if _, err := fetch(p); !errors.Is(err, msfp.ErrNotFound) {
			t.Errorf("%+v: %v", p, err)
		}
	}
	// A hostile peer's: refused.
	s.body("fonts/0", "ttcf collection")
	if _, err := fetch(msfp.MediaPath{Kind: msfp.MediaFont}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a font collection: %v", err)
	}
	s.body("ass/s2/0", "not a script")
	if _, err := fetch(msfp.MediaPath{Kind: msfp.MediaASS, Track: "s2"}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("a hostile ASS part: %v", err)
	}
	// The host's own outside MSFP's rules: not sent.
	delete(s.rewrites, "ass/s2/0")
	h.mu.Lock()
	h.files["ass/s2/0"] = []byte("not a script")
	h.mu.Unlock()
	if _, err := fetch(msfp.MediaPath{Kind: msfp.MediaASS, Track: "s2"}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("the host's broken ASS: %v", err)
	}
}
