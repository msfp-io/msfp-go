package msfp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"slices"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// The revision of version 0's titles: pages bounded by size, local
// titles shared only when allowed, extras under their titles, and the
// image of a local title.

// A page ends early at 3 MiB of answer, next set: the reader asks on,
// and keeps every title (MSFP catalog section 3.1).
func TestPagesBoundedBySize(t *testing.T) {
	h := newFakeHost()
	var chapters []wire.CopyChapter
	for i := range wire.MaxChapters {
		chapters = append(chapters, wire.CopyChapter{StartMs: int64(i) * 1000, Name: strings.Repeat("c", 60) + fmt.Sprint(i)})
	}
	for i := 1; i <= 300; i++ {
		key := fmt.Sprintf("movie/tmdb/%d", i)
		h.film("films", key, "en")
		h.titles[key].(*msfp.VideoTitle).Copies[0].Chapters = chapters
	}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if got := len(s.peerTitles(t)); got != 300 {
		t.Fatalf("B kept %d titles", got)
	}
	pages := s.a.exchanges("titles")
	if len(pages) < 2 {
		t.Fatalf("%d pages", len(pages))
	}
	after := ""
	for i, x := range pages {
		var ans struct {
			Titles []struct{ Key string }
			Next   *string
		}
		if err := json.Unmarshal([]byte(x.resBody), &ans); err != nil {
			t.Fatal(err)
		}
		last := i == len(pages)-1
		p := wire.Page{Titles: len(ans.Titles), Bytes: len(x.resBody), Remain: !last, After: after,
			LastKey: ans.Titles[len(ans.Titles)-1].Key, HasNext: ans.Next != nil}
		if ans.Next != nil {
			p.Next = *ans.Next
			after = *ans.Next
		}
		if err := wire.CheckPage(p); err != nil {
			t.Errorf("page %d (%d bytes, %d titles): %v", i, len(x.resBody), len(ans.Titles), err)
		}
	}
}

// localHost is a host with a film shared under TMDB and its extras, and
// a local film with its extra and its image.
func localHost(t *testing.T) (*fakeHost, string, []byte) {
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en")
	local := "movie/local/q3Zp0Lw8"
	extra := func(key, typ, name string) {
		h.titles[key] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: key, Kind: "extra", Libraries: []string{"films"}},
			ExtraType: typ, Name: name, Copies: oneCopy(key)}
	}
	extra("movie/tmdb/1/x/Tr7aQ1", "trailer", " Teaser\n")
	extra("movie/tmdb/1/x/Th3me", "themesong", "")
	extra("movie/tmdb/77/x/Orphan", "trailer", "")
	extra(local+"/x/Ex1", "featurette", "Making of")
	h.titles[local] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: local, Kind: "movie", Libraries: []string{"films"}},
		Name: "Summer\t2019", Year: 2019, Image: true, IDs: map[string]string{"imdb": "tt0133093"}, Copies: oneCopy(local)}
	h.titles["movie/local/NoName"] = &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: "movie/local/NoName", Kind: "movie",
		Libraries: []string{"films"}}, Copies: oneCopy("noname")}
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	// Bytes after the image's end, which re-encoding drops.
	b.WriteString("trailing")
	h.images = map[string][]byte{local: b.Bytes()}
	return h, local, b.Bytes()
}

// Local titles are shared only once the owner allows it, per pairing
// (MSFP catalog section 5): the change forces a whole reading. Extras go
// with their title only, never theme songs.
func TestLocalTitlesAndExtras(t *testing.T) {
	ctx := context.Background()
	h, local, _ := localHost(t)
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if got := keysOf(s.peerTitles(t)); !slices.Equal(got, []string{"movie/tmdb/1", "movie/tmdb/1/x/Tr7aQ1"}) {
		t.Fatalf("B keeps %v, before local titles are shared", got)
	}
	x := s.peerTitles(t)["movie/tmdb/1/x/Tr7aQ1"].(*msfp.VideoTitle)
	if x.ExtraType != "trailer" || x.Name != "Teaser" || len(x.Copies) != 1 {
		t.Errorf("an extra kept: %+v", x)
	}
	if err := s.a.eng.SetLocal(ctx, s.onA.ID, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.a.eng.Peer(ctx, s.onA.ID); !v.Local {
		t.Errorf("the view: %+v", v)
	}
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	last := s.a.exchanges("titles")
	if body := last[len(last)-1].resBody; strings.Contains(body, `"changes"`) || strings.Contains(body, "tt0133093") ||
		strings.Contains(body, "NoName") {
		t.Errorf("the reading after local titles were shared: %s", body)
	}
	got := s.peerTitles(t)
	if want := []string{local, local + "/x/Ex1", "movie/tmdb/1", "movie/tmdb/1/x/Tr7aQ1"}; !slices.Equal(keysOf(got), want) {
		t.Fatalf("B keeps %v, want %v", keysOf(got), want)
	}
	if v := got[local].(*msfp.VideoTitle); v.Name != "Summer 2019" || v.Year != 2019 || !v.Image || v.IDs != nil {
		t.Errorf("a local title kept: %+v", v)
	}
	// Off again: a whole reading drops them.
	if err := s.a.eng.SetLocal(ctx, s.onA.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(s.peerTitles(t)); !slices.Equal(got, []string{"movie/tmdb/1", "movie/tmdb/1/x/Tr7aQ1"}) {
		t.Errorf("B keeps %v, after local titles are no longer shared", got)
	}
	// The film gone: its extra goes with it.
	h.remove("movie/tmdb/1")
	s.a.eng.CatalogChanged()
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(s.peerTitles(t)); len(got) != 0 {
		t.Errorf("B keeps %v, the film gone", got)
	}
}

// A group's local titles (MSFP groups section 9): shared with the links
// through the group once the member allows it for that group.
func TestGroupLocalTitles(t *testing.T) {
	ctx := context.Background()
	h, local, _ := localHost(t)
	h.libs = []msfp.Library{{ID: "films", Name: "Films", Kind: "movies"}}
	a, b, n, g := trio(t, msfp.Config{}, msfp.Config{Catalog: h, Playback: h, Media: h}, msfp.Config{}, msfp.GroupSettingsDefault)
	gb, _ := b.eng.Group(ctx, g.ID)
	if err := b.eng.AcceptRequest(ctx, g.ID, gb.Requests[0].Mail); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n)
	if err := b.eng.SetGroupOffers(ctx, g.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	onN := peerNamed(t, n, "B")
	n.eng.Check(ctx, onN.ID)
	libs, err := n.eng.TheirLibraries(ctx, onN.ID)
	if err != nil || len(libs) != 1 {
		t.Fatalf("B's libraries, as N sees them: %+v %v", libs, err)
	}
	n.eng.SetWants(ctx, onN.ID, []string{libs[0].ID})
	kept := func() []string {
		t.Helper()
		if _, err := n.eng.ReadNow(ctx, onN.ID); err != nil {
			t.Fatal(err)
		}
		var keys []string
		n.store.Read(ctx, func(tx msfp.Tx) error {
			titles, err := tx.PeerTitles(onN.ID, "", 100)
			for _, ti := range titles {
				keys = append(keys, ti.Common().Key)
			}
			return err
		})
		return keys
	}
	if got := kept(); slices.Contains(got, local) {
		t.Errorf("N keeps %v before B shares local titles with the group", got)
	}
	if err := b.eng.SetGroupLocal(ctx, g.ID, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := b.eng.Group(ctx, g.ID); !v.Local {
		t.Errorf("the group's view: %+v", v)
	}
	if got := kept(); !slices.Contains(got, local) || !slices.Contains(got, local+"/x/Ex1") {
		t.Errorf("N keeps %v once B shares local titles with the group", got)
	}
}
