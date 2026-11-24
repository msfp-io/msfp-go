package msfp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// The catalog both ways: libraries offered by public IDs, titles paged,
// read whole then as changes, gone keys of titles sent only, keys
// checked, and what a reader keeps.

func TestLibrariesAndPaging(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "films", Name: " Films\u200b "}, {ID: "other", Name: "Other"}}
	for i := 1; i <= 12_001; i++ {
		key := fmt.Sprintf("movie/tmdb/%d", i)
		h.film("films", key, "en")
		h.copyOf(key, 0).Video = nil // small titles: three pages of them, read within the time of a reading
	}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	libs, err := s.b.eng.Wants(ctx, s.onB.ID)
	if err != nil || len(libs) != 2 {
		t.Fatalf("wants: %+v %v", libs, err)
	}
	for _, l := range libs {
		if l.ID == "films" || l.ID == "other" || !l.Wanted {
			t.Errorf("a library as B sees it: %+v (the host's IDs never leave)", l)
		}
	}
	if got := len(s.peerTitles(t)); got != 12_001 {
		t.Fatalf("B kept %d titles", got)
	}
	pages := s.a.exchanges("titles")
	if len(pages) != 3 {
		t.Fatalf("%d pages", len(pages))
	}
	var first struct {
		Titles []json.RawMessage
		Next   string
		Seq    int64
	}
	json.Unmarshal([]byte(pages[0].resBody), &first)
	if len(first.Titles) != 5000 || first.Next == "" || first.Seq < 1 {
		t.Errorf("the first page: %d titles, next %q, seq %d", len(first.Titles), first.Next, first.Seq)
	}
	if !strings.Contains(pages[2].resBody, `"titles":[`) || strings.Contains(pages[2].resBody, `"next"`) {
		t.Errorf("the last page: %.200s", pages[2].resBody)
	}
	// The limits, with playback on.
	v, _ := s.b.eng.Peer(ctx, s.onB.ID)
	if v.TheirLimits == nil || v.TheirLimits.Streams != 2 || v.TheirLimits.Transcodes != 1 {
		t.Errorf("A's limits as B knows them: %+v", v.TheirLimits)
	}
}

// A whole reading that fails halfway keeps the previous complete list:
// nothing of the pages it read (MSFP catalog section 3.2).
func TestWholeReadingFailsHalfway(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "films", Name: "Films"}, {ID: "more", Name: "More"}}
	for i := 1; i <= 6_000; i++ {
		h.film("films", fmt.Sprintf("movie/tmdb/%d", i), "en")
	}
	h.film("more", "movie/tmdb/9999999", "en")
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	before := keysOf(s.peerTitles(t))
	if len(before) != 6_001 {
		t.Fatalf("B kept %d titles", len(before))
	}
	// A's next list, whole since its offers changed: one title less, one
	// more, on its first page; its second page is broken.
	h.remove("movie/tmdb/1")
	h.film("films", "movie/tmdb/10000", "en")
	s.a.eng.CatalogChanged()
	if err := s.a.eng.SetOffers(ctx, s.onA.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	pages := 0
	s.a.setRewrite(func(path string, _ http.Header, body []byte) ([]byte, bool) {
		if !strings.HasSuffix(path, "/titles") {
			return nil, false
		}
		if pages++; pages == 2 {
			return []byte(`{"titles":`), true
		}
		return nil, false
	})
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil {
		t.Fatal("a broken page read")
	}
	if pages != 2 {
		t.Fatalf("%d pages", pages)
	}
	if got := keysOf(s.peerTitles(t)); !slices.Equal(got, before) {
		t.Errorf("B keeps %d titles, not the previous list of %d", len(got), len(before))
	}
}

func TestCatalogChanges(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "films", Name: "Films"}, {ID: "kept", Name: "Not offered"}}
	for _, k := range []string{"movie/tmdb/1", "movie/tmdb/2", "movie/tmdb/3"} {
		h.film("films", k, "en")
	}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	// Offer one library only: the other's titles are never sent.
	if err := s.a.eng.SetOffers(ctx, s.onA.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	h.film("kept", "movie/tmdb/9", "en")
	s.a.eng.CatalogChanged()
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(s.peerTitles(t)); !slices.Equal(got, []string{"movie/tmdb/1", "movie/tmdb/2", "movie/tmdb/3"}) {
		t.Fatalf("B keeps %v", got)
	}
	// One added, one removed, one whose ids changed (a change too), and
	// the unoffered one removed: never named in gone.
	h.film("films", "movie/tmdb/4", "en")
	h.remove("movie/tmdb/1")
	h.remove("movie/tmdb/9")
	h.mu.Lock()
	h.titles["movie/tmdb/2"].(*msfp.VideoTitle).IDs = map[string]string{"imdb": "tt0133093"}
	h.mu.Unlock()
	s.a.eng.CatalogChanged()
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last := s.a.exchanges("titles")
	x := last[len(last)-1]
	if !strings.Contains(x.reqBody, `"since":`) || !strings.Contains(x.resBody, `"changes":true`) {
		t.Fatalf("the second reading: %s → %s", x.reqBody, x.resBody)
	}
	var ans struct {
		Titles []struct{ Key string }
		Gone   []string
	}
	json.Unmarshal([]byte(x.resBody), &ans)
	var sent []string
	for _, ti := range ans.Titles {
		sent = append(sent, ti.Key)
	}
	if !slices.Equal(sent, []string{"movie/tmdb/2", "movie/tmdb/4"}) || !slices.Equal(ans.Gone, []string{"movie/tmdb/1"}) {
		t.Errorf("changes: titles %v, gone %v", sent, ans.Gone)
	}
	got := s.peerTitles(t)
	if !slices.Equal(keysOf(got), []string{"movie/tmdb/2", "movie/tmdb/3", "movie/tmdb/4"}) ||
		got["movie/tmdb/2"].(*msfp.VideoTitle).IDs != nil { // no ids on a film keyed under tmdb (video section 4.3)
		t.Errorf("B keeps %v", got)
	}
	// Offers changed: the next reading is a whole one.
	if err := s.a.eng.SetOffers(ctx, s.onA.ID, []string{"films", "kept"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last = s.a.exchanges("titles")
	if x := last[len(last)-1]; strings.Contains(x.resBody, `"changes":true`) {
		t.Errorf("a reading after the offers changed: %s", x.resBody)
	}
}

// A library no longer wanted, then wanted again before any reading: the
// next reading is whole (a host may have dropped its titles at once).
func TestWantsChangedReadWhole(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en")
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	wants, err := s.b.eng.Wants(ctx, s.onB.ID)
	if err != nil || len(wants) != 1 {
		t.Fatalf("B's wants: %+v %v", wants, err)
	}
	// Unchanged wants: a reading of changes.
	s.b.eng.SetWants(ctx, s.onB.ID, []string{wants[0].ID})
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last := s.a.exchanges("titles")
	if x := last[len(last)-1]; sinceOf(x.reqBody) == 0 {
		t.Fatalf("wants unchanged, a whole reading: %s", x.reqBody)
	}
	s.b.eng.SetWants(ctx, s.onB.ID, nil)
	s.b.eng.SetWants(ctx, s.onB.ID, []string{wants[0].ID})
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last = s.a.exchanges("titles")
	if x := last[len(last)-1]; sinceOf(x.reqBody) != 0 || strings.Contains(x.resBody, `"changes":true`) {
		t.Errorf("wanted again before any reading, a reading of changes: %s → %s", x.reqBody, x.resBody)
	}
}

// sinceOf is a titles request's since (0 when absent: a whole reading).
func sinceOf(body string) int64 {
	var r struct{ Since int64 }
	json.Unmarshal([]byte(body), &r)
	return r.Since
}

// A host's ChangeLog answers changes; one too old, the whole list.
type loggingHost struct {
	*fakeHost
	seq   int64
	since []int64
	old   bool
}

func (l *loggingHost) Seq(context.Context) (int64, error) { return l.seq, nil }

func (l *loggingHost) Changes(ctx context.Context, libs []string, since int64, after string, limit int) (msfp.ChangePage, error) {
	l.since = append(l.since, since)
	if l.old {
		return msfp.ChangePage{}, msfp.ErrTooOld
	}
	page, _ := l.Titles(ctx, libs, after, limit)
	return msfp.ChangePage{Present: page.Titles, Gone: []string{"movie/tmdb/77"}}, nil
}

func TestHostChangeLog(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/5", "en")
	l := &loggingHost{fakeHost: h, seq: 6}
	h.libs = []msfp.Library{{ID: "films", Name: "Films"}}
	a := newServer(t, msfp.Config{Catalog: l, Playback: h, Media: h})
	b := newServer(t, msfp.Config{})
	onA, onB := pair(t, a, b)
	a.eng.Check(ctx, onA.ID)
	b.eng.Check(ctx, onB.ID)
	a.eng.SetOffers(ctx, onA.ID, []string{"films"})
	libs, _ := b.eng.TheirLibraries(ctx, onB.ID)
	b.eng.SetWants(ctx, onB.ID, []string{libs[0].ID})
	// The offers were set at 6: a reading at 7 is after them.
	l.seq = 7
	if _, err := b.eng.ReadNow(ctx, onB.ID); err != nil {
		t.Fatal(err)
	}
	l.seq = 9
	if _, err := b.eng.ReadNow(ctx, onB.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(l.since, []int64{7}) {
		t.Errorf("changes asked since %v", l.since)
	}
	// movie/tmdb/77 was never sent: not in gone.
	x := a.exchanges("titles")
	if last := x[len(x)-1]; !strings.Contains(last.resBody, `"changes":true`) || strings.Contains(last.resBody, "movie/tmdb/77") {
		t.Errorf("changes: %s", last.resBody)
	}
	l.old = true
	if _, err := b.eng.ReadNow(ctx, onB.ID); err != nil {
		t.Fatal(err)
	}
	x = a.exchanges("titles")
	if last := x[len(x)-1]; strings.Contains(last.resBody, `"changes"`) || !strings.Contains(last.resBody, "movie/tmdb/5") {
		t.Errorf("a log too old: %s", last.resBody)
	}
}

// bumpingHost is a ChangeLog that takes change numbers itself
// (SeqBumper).
type bumpingHost struct {
	*loggingHost
	mu sync.Mutex
}

func (b *bumpingHost) Seq(context.Context) (int64, error) { return b.now(), nil }

func (b *bumpingHost) BumpSeq(context.Context) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	return b.seq, nil
}

func (b *bumpingHost) now() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// A host whose log takes a number after a change of offers: the reading
// after the next whole one is of changes, with no title changed between.
func TestHostChangeLogBumped(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/5", "en")
	l := &bumpingHost{loggingHost: &loggingHost{fakeHost: h, seq: 6}}
	h.libs = []msfp.Library{{ID: "films", Name: "Films"}}
	a := newServer(t, msfp.Config{Catalog: l, Playback: h, Media: h})
	b := newServer(t, msfp.Config{})
	onA, onB := pair(t, a, b)
	a.eng.Check(ctx, onA.ID)
	b.eng.Check(ctx, onB.ID)
	waitAll(a, b)
	before := l.now()
	a.eng.SetOffers(ctx, onA.ID, []string{"films"})
	after := l.now()
	if after != before+1 {
		t.Fatalf("the log after the offers: %d, was %d", after, before)
	}
	libs, _ := b.eng.TheirLibraries(ctx, onB.ID)
	b.eng.SetWants(ctx, onB.ID, []string{libs[0].ID})
	for range 2 {
		if _, err := b.eng.ReadNow(ctx, onB.ID); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(l.since, []int64{after}) {
		t.Errorf("changes asked since %v, the offers at %d", l.since, after)
	}
}

// Keys MSFP doesn't take never leave: leading zeros, another kind, an
// unknown scheme; members are cleaned before they go.
func TestOwnTitlesChecked(t *testing.T) {
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en", "fr")
	for _, k := range []string{"movie/tmdb/0949", "movie/imdb/949", "series/tmdb/1399/s01"} {
		h.titles[k] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: k, Kind: "movie", Libraries: []string{"films"}}}
	}
	h.titles["series/tmdb/1399"] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: "series/tmdb/1399", Kind: "movie",
		Libraries: []string{"films"}}}
	h.titles["series/tmdb/1399/e1"] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: "series/tmdb/1399/e1", Kind: "episode",
		Libraries: []string{"films"}}, Copies: []wire.Copy{{ID: "ep1", MediaType: "video/matroska", Size: 1, ETag: "x",
		AudioTracks: []wire.CopyAudio{{ID: "a0", Language: "eng", Codec: "aac"}, {ID: "a1", Language: "x", Codec: "aac"}}}},
		With: []string{"series/tmdb/1399/e3", "series/tmdb/2/e1", "series/tmdb/1399/e2"},
		IDs:  map[string]string{"imdb": "tt01", "tvdb": "81189", "other": "1"}, Place: &msfp.Place{Season: 1, Episode: 1}}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	got := s.peerTitles(t)
	if !slices.Equal(keysOf(got), []string{"movie/tmdb/1", "series/tmdb/1399/e1"}) {
		t.Fatalf("B keeps %v", keysOf(got))
	}
	ep := got["series/tmdb/1399/e1"].(*msfp.VideoTitle)
	if !slices.Equal(ep.AudioLanguages(), []string{"en"}) || ep.Copies[0].ID == "ep1" || !slices.Equal(ep.With, []string{"series/tmdb/1399/e2", "series/tmdb/1399/e3"}) ||
		len(ep.IDs) != 1 || ep.IDs["tvdb"] != "81189" {
		t.Errorf("an episode as sent: %+v", ep)
	}
}

// A titles answer comes after a change the host told of, even while the
// refresh CatalogChanged started in the background is still reading the
// host's titles: it waits for it, rather than answering from the log as
// it was (the cause of TestTitleCopies' and TestLocalTitlesAndExtras'
// rare failures).
func TestChangeWaitsForRefresh(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en")
	h.film("films", "movie/tmdb/2", "en")
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	hold, entered := make(chan struct{}), make(chan struct{})
	h.mu.Lock()
	h.titlesHold, h.titlesEntered = hold, entered
	h.mu.Unlock()
	h.remove("movie/tmdb/2")
	s.a.eng.CatalogChanged()
	<-entered // the background refresh is reading the host's titles
	read := make(chan error, 1)
	go func() {
		_, err := s.b.eng.ReadNow(ctx, s.onB.ID)
		read <- err
	}()
	select {
	case err := <-read:
		t.Fatalf("a reading answered during the refresh: %v, B keeps %v", err, keysOf(s.peerTitles(t)))
	case <-time.After(200 * time.Millisecond):
	}
	close(hold)
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	if got := keysOf(s.peerTitles(t)); !slices.Equal(got, []string{"movie/tmdb/1"}) {
		t.Errorf("B keeps %v after the film's removal", got)
	}
}

// A host's copy with more subtitle tracks than MSFP allows leaves with
// the first 64 (MSFP video section 3.2): the engine sends nothing a
// receiver would drop.
func TestOwnSubtitleTracksCapped(t *testing.T) {
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en")
	c := h.copyOf("movie/tmdb/1", 0)
	c.SubtitleTracks = nil
	for i := range 70 {
		c.SubtitleTracks = append(c.SubtitleTracks, wire.CopySubtitle{ID: "s" + strconv.Itoa(i), Language: "fr", Format: "subrip"})
	}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	got := s.peerTitles(t)["movie/tmdb/1"].(*msfp.VideoTitle)
	if n := len(got.Copies[0].SubtitleTracks); n != wire.MaxSubtitleTracks {
		t.Errorf("%d subtitle tracks kept", n)
	}
	last := s.a.exchanges("titles")
	if body := last[len(last)-1].resBody; strings.Count(body, `"format":"subrip"`) != wire.MaxSubtitleTracks || strings.Contains(body, `"s64"`) {
		t.Errorf("as sent: %s", body)
	}
}

// A hostile peer's titles: what a reader keeps is MSFP's, title by
// title; members of the wrong type at the top fail the reading, which
// keeps what was kept before.
func TestHostileTitles(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en")
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	lib := s.libraryPub
	page := `{"titles":[` +
		`{"key":"movie/tmdb/2","kind":"movie","library":"` + lib + `","copies":[{"id":"v1","durationMs":1000,"mediaType":"video/matroska","size":5,"etag":"t1","audioTracks":[{"id":"a0","codec":"aac","language":"fre"},{"id":"a1","codec":"aac","language":"fr"}]},{"id":"v1","durationMs":1,"mediaType":"video/x-matroska","size":1,"etag":"t","audioTracks":[]}],"ids":{"imdb":"tt0133093","x":1}},` +
		`{"key":"movie/tmdb/7","kind":"movie","library":"` + lib + `"},` +
		`{"key":"movie/tmdb/03","kind":"movie","library":"` + lib + `"},` +
		`{"key":"movie/tmdb/4","kind":"series","library":"` + lib + `"},` +
		`{"key":"movie/tmdb/5","kind":"movie","library":"elsewhere"},` +
		`{"key":"track/mb/0c000000-0000-4000-8000-000000000003","kind":"track","library":"` + lib + `","name":"<b>x</b>","album":"album/mb/0b000000-0000-4000-8000-000000000002","copies":[{"id":"t1","durationMs":1000,"mediaType":"audio/flac","codec":"flac","size":9,"etag":"e"}]},` +
		`7,{"key":6}],"seq":3}`
	s.a.setRewrite(func(path string, _ http.Header, body []byte) ([]byte, bool) {
		return []byte(page), strings.HasSuffix(path, "/titles")
	})
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	got := s.peerTitles(t)
	keys := keysOf(got)
	if !slices.Equal(keys, []string{"movie/tmdb/2", "track/mb/0c000000-0000-4000-8000-000000000003"}) {
		t.Fatalf("B keeps %v", keys)
	}
	if v := got["movie/tmdb/2"].(*msfp.VideoTitle); !slices.Equal(v.AudioLanguages(), []string{"fr"}) || len(v.IDs) != 0 ||
		len(v.Copies) != 1 || v.Copies[0].MediaType != "video/matroska" {
		t.Errorf("a title kept: %+v", v)
	}
	if m := got["track/mb/0c000000-0000-4000-8000-000000000003"].(*msfp.MusicTitle); m.Name != "<b>x</b>" {
		t.Errorf("a name is text, kept as text: %q", m.Name)
	}
	// Wrong types at the top: the reading fails, what was kept stays.
	for _, bad := range []string{`{"titles":{}}`, `{"titles":[],"next":7}`, `{"titles":[],"seq":"1"}`, `{"titles":[],"changes":1}`, `{"titles":[],"gone":"x"}`} {
		page = bad
		if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil {
			t.Errorf("%s: read", bad)
		}
		if n := len(s.peerTitles(t)); n != 2 {
			t.Errorf("%s: %d titles kept", bad, n)
		}
	}
	// A page of more than 5,000 keys: a peer filling the database.
	var many []string
	for i := range 5001 {
		many = append(many, fmt.Sprintf(`{"key":"movie/tmdb/%d","kind":"movie","library":"%s"}`, i+10, lib))
	}
	page = `{"titles":[` + strings.Join(many, ",") + `]}`
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil {
		t.Error("a page of 5,001 titles was read")
	}
	// Changes mixed with the whole list on later pages: a failure.
	calls := 0
	s.a.setRewrite(func(path string, _ http.Header, body []byte) ([]byte, bool) {
		if !strings.HasSuffix(path, "/titles") {
			return nil, false
		}
		calls++
		if calls == 1 {
			return []byte(`{"titles":[],"next":"movie/tmdb/1","changes":true,"gone":["movie/tmdb/2"],"seq":5}`), true
		}
		return []byte(`{"titles":[]}`), true
	})
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil {
		t.Error("a reading mixing changes and the whole list was read")
	}
}

func keysOf(m map[string]msfp.Title) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Music titles go with their descriptions, checked before they leave.
func TestMusicTitles(t *testing.T) {
	h := newFakeHost()
	h.libs = []msfp.Library{{ID: "music", Name: "Music"}}
	track := "track/mb/0c000000-0000-4000-8000-000000000003"
	h.titles[track] = &msfp.MusicTitle{TitleCommon: msfp.TitleCommon{Key: track, Kind: "track", Libraries: []string{"music"}},
		Name: "The\tBoxer", Artists: []string{"artist/mb/0a000000-0000-4000-8000-000000000001", "nope"},
		ArtistNames: "Simon & Garfunkel", Album: "album/mb/0b000000-0000-4000-8000-000000000002", Disc: 1, Number: 2,
		Date: "1970", Genres: []string{"Folk"}, Copies: []wire.MusicCopy{{ID: "boxer.flac", DurationMs: 308000,
			MediaType: "audio/flac", Codec: "flac", Size: 36214455, Etag: "7f3a9c21", SampleRate: 44100, BitDepth: 16, Channels: 2}},
		IDs: map[string]string{"isrc": "USSM17000123", "imdb": "tt0133093", "mbrecording": "0e000000-0000-4000-8000-000000000006"}}
	album := "album/mb/0b000000-0000-4000-8000-000000000002"
	h.titles[album] = &msfp.MusicTitle{TitleCommon: msfp.TitleCommon{Key: album, Kind: "album", Libraries: []string{"music"}},
		Name: "Bridge over Troubled Water", PrimaryType: "album", SecondaryTypes: []string{"soundtrack"}, Date: "2011-03-08", OriginalDate: "1970-01-26",
		Genres: []string{"Folk rock", "folk ROCK"}, Image: true, IDs: map[string]string{"mbreleasegroup": "0d000000-0000-4000-8000-000000000005"}}
	nameless := "track/mb/0c000000-0000-4000-8000-000000000004"
	h.titles[nameless] = &msfp.MusicTitle{TitleCommon: msfp.TitleCommon{Key: nameless, Kind: "track", Libraries: []string{"music"}}}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	got := s.peerTitles(t)
	m, ok := got[track].(*msfp.MusicTitle)
	if !ok || len(got) != 2 {
		t.Fatalf("B keeps %v", keysOf(got))
	}
	if m.Name != "The Boxer" || !slices.Equal(m.Artists, []string{"artist/mb/0a000000-0000-4000-8000-000000000001"}) ||
		m.ArtistNames != "Simon & Garfunkel" || m.Disc != 1 || m.Number != 2 || m.Date != "1970" || !slices.Equal(m.Genres, []string{"Folk"}) ||
		len(m.Copies) != 1 || m.Copies[0].DurationMs != 308000 || m.Copies[0].MediaType != "audio/flac" || !strings.HasPrefix(m.Copies[0].ID, "ver_") ||
		len(m.IDs) != 1 || m.Libraries[0] != s.libraryPub {
		t.Errorf("a track kept: %+v", m)
	}
	// An album's description; an image only for a local one.
	if a := got[album].(*msfp.MusicTitle); a.PrimaryType != "album" || len(a.SecondaryTypes) != 1 || a.OriginalDate != "1970-01-26" ||
		!slices.Equal(a.Genres, []string{"Folk rock"}) || a.Image || a.IDs["mbreleasegroup"] == "" {
		t.Errorf("an album kept: %+v", a)
	}
	last := s.a.exchanges("titles")
	if body := last[len(last)-1].resBody; strings.Contains(body, "boxer.flac") || strings.Contains(body, `"year"`) {
		t.Errorf("as sent: %s", body)
	}
	// A server without music: none of it is sent.
	h2 := newFakeHost()
	h2.libs = h.libs
	h2.titles[track] = h.titles[track]
	s2 := newSharing(t, h2, msfp.Config{}, msfp.Config{Parts: []string{"catalog", "playback", "media", "video"}})
	if got := s2.peerTitles(t); len(got) != 0 {
		t.Errorf("B without music keeps %v", keysOf(got))
	}
}

// A film's copies (MSFP video section 3.2): sent best first, under
// version IDs, cleaned as a receiver keeps them; the best picture and the
// languages are worked out from them; a copy's new etag is a change of
// the title.
func TestTitleCopies(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en", "fr")
	h.film("films", "movie/tmdb/2", "en")
	h.mu.Lock()
	m1 := h.titles["movie/tmdb/1"].(*msfp.VideoTitle)
	m1.Copies[0].Video = &wire.VideoStream{Codec: "h264", Width: 1920, Height: 1080}
	m1.Copies[1].Video = &wire.VideoStream{Codec: "hevc", Width: 3840, Height: 1600, HDR: []string{"hdr10plus", "dv", "hdr10plus"}}
	m1.Copies[1].AudioTracks[0].Role = "commentary"
	m1.Copies[1].AudioTracks = append(m1.Copies[1].AudioTracks, wire.CopyAudio{ID: "a1", Language: "ger", Codec: "dts", Codecs: "dtsh"})
	m1.Copies[0].Chapters = []wire.CopyChapter{{StartMs: 0, Name: "Start"}, {StartMs: 0}, {StartMs: 60_000}}
	h.titles["movie/tmdb/2"].(*msfp.VideoTitle).Copies[0].Size = 0 // a receiver would skip it: so is the title
	h.mu.Unlock()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	got := s.peerTitles(t)
	if _, ok := got["movie/tmdb/2"]; ok {
		t.Error("a film with no copy a receiver keeps was sent")
	}
	v := got["movie/tmdb/1"].(*msfp.VideoTitle)
	// The host's order, its preference (MSFP video section 3.7); hdr
	// sorted.
	first, ok := v.BestCopy()
	if !ok || len(v.Copies) != 2 || first.Video == nil || first.Video.Height != 1080 || v.Copies[1].Video == nil ||
		!slices.Equal(v.Copies[1].Video.HDR, []string{"dv", "hdr10plus"}) {
		t.Fatalf("the host's order: %+v", v.Copies)
	}
	best := v.Copies[1]
	if best.AudioTracks[0].Role != "commentary" || best.AudioTracks[1].Codecs != "dtsh" || best.AudioTracks[1].Language != "de" {
		t.Errorf("tracks as sent: %+v", best)
	}
	if !slices.Equal(v.AudioLanguages(), []string{"en", "fr", "de"}) || !slices.Equal(v.SubtitleLanguages(), []string{"fr"}) {
		t.Errorf("languages: %v %v", v.AudioLanguages(), v.SubtitleLanguages())
	}
	if c := v.Copies[0]; len(c.Chapters) != 2 || c.Size != 20 || c.ETag != "e0" {
		t.Errorf("the other copy: %+v", c)
	}
	for _, c := range v.Copies {
		if !strings.HasPrefix(c.ID, "ver_") {
			t.Errorf("a copy's ID as sent: %q", c.ID)
		}
	}
	last := s.a.exchanges("titles")
	if body := last[len(last)-1].resBody; strings.Contains(body, "movie/tmdb/1#") || strings.Contains(body, "GRP") {
		t.Errorf("as sent: %s", body)
	}
	// A copy replaced (its etag): the title is sent again in a changes
	// reading, alone.
	h.mu.Lock()
	h.titles["movie/tmdb/1"].(*msfp.VideoTitle).Copies[0].ETag = "e9"
	h.mu.Unlock()
	s.a.eng.CatalogChanged()
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last = s.a.exchanges("titles")
	if x := last[len(last)-1]; !strings.Contains(x.resBody, `"changes":true`) || !strings.Contains(x.resBody, `"key":"movie/tmdb/1"`) {
		t.Errorf("a changes reading: %s", x.resBody)
	}
	if v := s.peerTitles(t)["movie/tmdb/1"].(*msfp.VideoTitle); v.Copies[0].ETag != "e9" {
		t.Errorf("the new copy kept: %+v", v.Copies)
	}
}

// An episode keyed by its TMDB ID carries its place in TMDB's numbering
// (MSFP video section 4.1): the host's goes with it; one the host can't
// place isn't sent; place on other titles never leaves; and a new place
// is a change of the title.
func TestTitlePlaces(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	episode := func(key string, place *msfp.Place) {
		h.titles[key] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: key, Kind: "episode", Libraries: []string{"films"}},
			Place: place, Copies: oneCopy(key)}
	}
	h.film("films", "movie/tmdb/1", "en")
	h.titles["movie/tmdb/1"].(*msfp.VideoTitle).Place = &msfp.Place{Season: 1, Episode: 2}
	episode("series/tmdb/1399/e1", &msfp.Place{Season: 1, Episode: 1})
	episode("series/tmdb/1399/e2", &msfp.Place{}) // season 0, episode 0
	episode("series/tmdb/1399/e3", nil)
	episode("series/tmdb/1399/e4", &msfp.Place{Season: 10000, Episode: 1})
	episode("series/tmdb/1399/e5", &msfp.Place{Season: 1, Episode: -1})
	episode("series/tmdb/1399/s1/e9", &msfp.Place{Season: 1, Episode: 9})
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	got := s.peerTitles(t)
	if want := []string{"movie/tmdb/1", "series/tmdb/1399/e1", "series/tmdb/1399/e2", "series/tmdb/1399/s1/e9"}; !slices.Equal(keysOf(got), want) {
		t.Fatalf("B keeps %v, want %v", keysOf(got), want)
	}
	if p := got["series/tmdb/1399/e1"].(*msfp.VideoTitle).Place; p == nil || *p != (msfp.Place{Season: 1, Episode: 1}) {
		t.Errorf("e1's place: %+v", p)
	}
	if p := got["series/tmdb/1399/e2"].(*msfp.VideoTitle).Place; p == nil || *p != (msfp.Place{}) {
		t.Errorf("e2's place: %+v", p)
	}
	last := s.a.exchanges("titles")
	body := last[len(last)-1].resBody
	if !strings.Contains(body, `"key":"series/tmdb/1399/e1","library":"`) || strings.Contains(body, `"kind"`) ||
		!strings.Contains(body, `"place":{"season":1,"episode":1}`) || !strings.Contains(body, `"place":{"season":0,"episode":0}`) ||
		strings.Count(body, `"place"`) != 2 {
		t.Errorf("as sent: %s", body)
	}
	// TMDB renumbers e1: the same key, a new place, sent again.
	h.mu.Lock()
	h.titles["series/tmdb/1399/e1"].(*msfp.VideoTitle).Place = &msfp.Place{Season: 2, Episode: 1}
	h.mu.Unlock()
	s.a.eng.CatalogChanged()
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last = s.a.exchanges("titles")
	if x := last[len(last)-1]; !strings.Contains(x.resBody, `"changes":true`) || !strings.Contains(x.resBody, `"key":"series/tmdb/1399/e1"`) ||
		strings.Contains(x.resBody, `"key":"series/tmdb/1399/e2"`) {
		t.Errorf("a changes reading: %s", x.resBody)
	}
	if p := s.peerTitles(t)["series/tmdb/1399/e1"].(*msfp.VideoTitle).Place; p == nil || *p != (msfp.Place{Season: 2, Episode: 1}) {
		t.Errorf("the new place kept: %+v", p)
	}
	// The host can't place it any more: it is gone from the peer.
	h.mu.Lock()
	h.titles["series/tmdb/1399/e1"].(*msfp.VideoTitle).Place = nil
	h.mu.Unlock()
	s.a.eng.CatalogChanged()
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last = s.a.exchanges("titles")
	if x := last[len(last)-1]; !strings.Contains(x.resBody, `"gone":["series/tmdb/1399/e1"]`) {
		t.Errorf("a changes reading: %s", x.resBody)
	}
	if _, ok := s.peerTitles(t)["series/tmdb/1399/e1"]; ok {
		t.Error("an episode no longer placed still kept")
	}
}

// oneCopy is a title's one copy, of the least a copy carries.
func oneCopy(handle string) []wire.Copy {
	return []wire.Copy{{ID: handle, DurationMs: 60_000, MediaType: "video/mp4", Size: 100, ETag: "e1", AudioTracks: []wire.CopyAudio{}}}
}
