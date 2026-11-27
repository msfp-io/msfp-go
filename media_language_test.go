package msfp_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// A play that replaces a live playback of the same peer counts as it:
// with one stream, the new one starts; the old one ends at once, on both
// sides; a replaces that names no live playback of the caller is ignored,
// and a malformed one refused (MSFP Media playing 2.2 and 2.3).
func TestReplaces(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{NewPairing: &msfp.Limits{Streams: 1, Transcodes: 1}}, msfp.Config{})
	old, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Delivery: msfp.DeliveryFile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); !errors.Is(err, msfp.ErrStreamLimitReached) {
		t.Fatalf("a second stream: %v", err)
	}
	theirOld := s.a.eng.Playbacks()[0].ID
	stops := len(s.a.exchanges("stop"))
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", Replaces: old.ID})
	if err != nil {
		t.Fatalf("a play replacing the live one: %v", err)
	}
	waitAll(s.a, s.b)
	if p := s.a.exchanges("play"); !strings.Contains(p[len(p)-1].reqBody, `"replaces":"`+theirOld+`"`) {
		t.Errorf("the play sent: %s", p[len(p)-1].reqBody)
	}
	if got := h.started[len(h.started)-1].Replaces; got == "" {
		t.Errorf("the host's start doesn't name the playback replaced: %q", got)
	}
	if st := h.stops(); !slices.Contains(slices.Collect(maps.Values(st)), msfp.EndReplaced) {
		t.Errorf("the host's stops: %v", st)
	}
	if n := len(s.a.exchanges("stop")) - stops; n != 0 {
		t.Errorf("%d stops sent: replacing needs none", n)
	}
	if v := s.a.eng.Playbacks(); len(v) != 1 || v[0].ID == theirOld {
		t.Errorf("A's playbacks: %+v", v)
	}
	if ev, ok := has[msfp.PlaybackEnded](s.a.events); !ok || ev.Playback != theirOld || ev.Reason != msfp.EndReplaced {
		t.Errorf("the old one's ending: %+v", ev)
	}
	if err := s.b.eng.Fetch(ctx, old.ID, msfp.MediaPath{Kind: msfp.MediaFile}, "", &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("the old one, on B: %v", err)
	}
	if err := s.b.eng.Stop(ctx, pb.ID, -1); err != nil {
		t.Fatal(err)
	}
	waitAll(s.a)
	// By hand: one live, a replaces of no live playback of B's is ignored,
	// and the limit refuses; a malformed one is refused.
	live, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(body string) int {
		var raw json.RawMessage
		code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "play", json.RawMessage(body), &raw)
		return code
	}
	if code := call(`{"title":"movie/tmdb/949","replaces":"play_none"}`); code != 409 {
		t.Errorf("a replaces of no live playback: %d", code)
	}
	for _, r := range []string{`"x y"`, `""`, `7`} {
		if code := call(`{"title":"movie/tmdb/949","replaces":` + r + `}`); code != 400 {
			t.Errorf("replaces %s: %d", r, code)
		}
	}
	if err := s.b.eng.Stop(ctx, live.ID, -1); err != nil {
		t.Fatal(err)
	}
}

// progress's paused is a boolean (MSFP Media playing 3): the host hears
// it; another type is refused.
func TestProgressPaused(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil {
		t.Fatal(err)
	}
	id := s.a.eng.Playbacks()[0].ID
	call := func(body string) int {
		var out struct{}
		code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "progress", json.RawMessage(body), &out)
		return code
	}
	if code := call(`{"id":"` + id + `","positionMs":1000,"paused":true}`); code != 200 {
		t.Fatalf("a paused report: %d", code)
	}
	h.mu.Lock()
	last := h.reports[len(h.reports)-1]
	h.mu.Unlock()
	if !last.Paused || last.PositionMs != 1000 {
		t.Errorf("the host heard %+v", last)
	}
	for _, v := range []string{`"yes"`, `1`} {
		if code := call(`{"id":"` + id + `","paused":` + v + `}`); code != 400 {
			t.Errorf("paused %s: %d", v, code)
		}
	}
}

// titles: libraries absent lists every library offered; kind and under
// list only the titles that pass; under always answers the whole list
// (MSFP Media listing 3 and 3.6).
func TestTitlesFilters(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	video := func(key, kind string, place *msfp.Place, copies bool) {
		v := &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: key, Kind: kind, Libraries: []string{"films"}}, Place: place}
		if copies {
			v.Copies = oneCopy(key)
		}
		if kind == "extra" {
			v.ExtraType = "trailer"
		}
		h.titles[key] = v
	}
	h.film("films", "movie/tmdb/1", "en")
	video("movie/tmdb/1/x/trailer1", "extra", nil, true)
	video("series/tmdb/1399", "series", nil, false)
	video("series/tmdb/1399/s1", "season", nil, false)
	video("series/tmdb/1399/s1/e2", "episode", nil, true)
	video("series/tmdb/1399/e63056", "episode", &msfp.Place{Season: 1, Episode: 1}, true)
	video("series/tmdb/1399/e63099", "episode", &msfp.Place{Season: 2, Episode: 1}, true)
	video("series/tmdb/1399/s2", "season", nil, false)
	video("series/tmdb/1399/x/making", "extra", nil, true)
	video("series/tmdb/2", "series", nil, false)
	video("series/tmdb/2/s1", "season", nil, false)
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	keys := func(body string) ([]string, string) {
		t.Helper()
		var raw json.RawMessage
		code, err := s.b.eng.CallForTest(ctx, s.onB.ID, "titles", json.RawMessage(body), &raw)
		if err != nil || code != 200 {
			t.Fatalf("%s: %d %v", body, code, err)
		}
		var a struct {
			Titles []struct {
				Key string `json:"key"`
			} `json:"titles"`
			Changes bool `json:"changes"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range a.Titles {
			out = append(out, x.Key)
		}
		kind := "whole"
		if a.Changes {
			kind = "changes"
		}
		return out, kind
	}
	all, _ := keys(`{"after":""}`)
	if len(all) != 11 {
		t.Fatalf("libraries absent: %v", all)
	}
	for _, c := range []struct{ body, want string }{
		{`{"kind":"season"}`, "series/tmdb/1399/s1 series/tmdb/1399/s2 series/tmdb/2/s1"},
		{`{"kind":"extra"}`, "movie/tmdb/1/x/trailer1 series/tmdb/1399/x/making"},
		{`{"kind":"toaster"}`, ""},
		{`{"under":"movie/tmdb/1"}`, "movie/tmdb/1/x/trailer1"},
		{`{"under":"series/tmdb/1399"}`, "series/tmdb/1399/s1 series/tmdb/1399/s2 series/tmdb/1399/x/making"},
		{`{"under":"series/tmdb/1399/s1"}`, "series/tmdb/1399/e63056 series/tmdb/1399/s1/e2"},
		{`{"under":"series/tmdb/1399/s1","kind":"extra"}`, ""},
		{`{"under":"series/tmdb/1399/s1/e2"}`, ""},
		{`{"under":"not a key"}`, ""},
		{`{"under":""}`, ""},
	} {
		got, _ := keys(c.body)
		if strings.Join(got, " ") != c.want {
			t.Errorf("%s: %v, want %s", c.body, got, c.want)
		}
	}
	var raw json.RawMessage
	for _, body := range []string{`{"kind":"Season"}`, `{"kind":5}`, `{"under":7}`, `{"libraries":"films"}`, `{"libraries":[7]}`,
		`{"under":"` + strings.Repeat("a", 513) + `"}`} {
		if code, _ := s.b.eng.CallForTest(ctx, s.onB.ID, "titles", json.RawMessage(body), &raw); code != 404 {
			t.Errorf("%s: %d", body, code)
		}
	}
	// A seq kept: since with kind gives changes, since with under the whole
	// list.
	var first struct {
		Seq int64 `json:"seq"`
	}
	if _, err := s.b.eng.CallForTest(ctx, s.onB.ID, "titles", json.RawMessage(`{"kind":"season"}`), &first); err != nil || first.Seq == 0 {
		t.Fatalf("the seq: %+v %v", first, err)
	}
	since := `,"since":` + itoa(first.Seq) + `}`
	if _, kind := keys(`{"kind":"season"` + since); kind != "changes" {
		t.Errorf("since with kind: a %s reading", kind)
	}
	if got, kind := keys(`{"under":"series/tmdb/2"` + since); kind != "whole" || !slices.Equal(got, []string{"series/tmdb/2/s1"}) {
		t.Errorf("since with under: a %s reading of %v", kind, got)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// The discovery document names the MSFP Media versions its parts carry
// (MSFP core section 11).
func TestDiscoveryMSFPMedia(t *testing.T) {
	sv := newServer(t, msfp.Config{Discovery: true})
	rec := httptest.NewRecorder()
	sv.eng.DiscoveryHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, wire.DiscoveryPath, nil))
	body := rec.Body.Bytes()
	d, err := wire.ReadDiscovery(body)
	if err != nil || !slices.Equal(d.MSFPMedia, []int{0}) {
		t.Errorf("%s: %+v %v", body, d, err)
	}
}
