package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// The folder's titles: films by TMDB ID, loose or in a folder with its
// extras; a series with its seasons, an episode by its TMDB episode ID
// (placed) and one by its numbers; a local film with its image; copies
// with their exact sizes and etags, subtitles and previews.
func TestFolderTitles(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Heat (1995) [tmdbid-949].mkv", "heat")
	write("Heat (1995) [tmdbid-949].fr.vtt", "WEBVTT\n")
	write("Heat (1995) [tmdbid-949].trickplay/layout.json",
		`{"tileWidth":160,"tileHeight":90,"columns":10,"rows":10,"intervalMs":10000,"tileCount":12}`)
	write("Alien (1979) [tmdbid-348]/Alien.mp4", "alien film")
	write("Alien (1979) [tmdbid-348]/sample.mp4", "short")
	write("Alien (1979) [tmdbid-348]/Trailers/Teaser.mp4", "teaser")
	write("Severance (2022) [tmdbid-95396]/Season 1/S01E01 [tmdbid-1949537].mkv", "e1")
	write("Severance (2022) [tmdbid-95396]/Season 1/S01E02.mkv", "e2")
	write("Holidays (2019).mp4", "home")
	write("Holidays (2019).jpg", "picture")
	write("Lain [tvdbid-78914].mkv", "a film by TheTVDB: none")

	f := &folder{dir: dir, opaque: func(s string) string { return "o" + string(rune('A'+len(s)%26)) }}
	page, err := f.Titles(context.Background(), nil, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]*msfp.VideoTitle{}
	var keys []string
	for _, tt := range page.Titles {
		v := tt.(*msfp.VideoTitle)
		byKey[v.Key] = v
		keys = append(keys, v.Key)
	}
	for _, k := range []string{"movie/tmdb/949", "movie/tmdb/348", "series/tmdb/95396", "series/tmdb/95396/s1",
		"series/tmdb/95396/e1949537", "series/tmdb/95396/s1/e2"} {
		if byKey[k] == nil {
			t.Errorf("%s missing from %v", k, keys)
		}
	}
	if len(keys) != 8 || !slices.IsSorted(keys) {
		t.Errorf("titles: %v", keys)
	}
	heat := byKey["movie/tmdb/949"]
	if c := heat.Copies[0]; c.Size != 4 || !wire.ID(c.ETag) || c.MediaType != "video/matroska" || len(c.SubtitleTracks) != 1 ||
		c.SubtitleTracks[0].Language != "fr" || c.PreviewLayout == nil || c.PreviewLayout.TileCount != 12 ||
		c.ID != "Heat (1995) [tmdbid-949].mkv" {
		t.Errorf("Heat's copy: %+v", c)
	}
	if c := byKey["movie/tmdb/348"].Copies[0]; c.ID != filepath.Join("Alien (1979) [tmdbid-348]", "Alien.mp4") {
		t.Errorf("Alien's copy, its largest file: %+v", c)
	}
	if e := byKey["series/tmdb/95396/e1949537"]; e.Place == nil || *e.Place != (msfp.Place{Season: 1, Episode: 1}) {
		t.Errorf("the episode by TMDB ID: %+v", e)
	}
	var extra, local *msfp.VideoTitle
	for k, v := range byKey {
		switch {
		case strings.HasPrefix(k, "movie/tmdb/348/x/"):
			extra = v
		case strings.HasPrefix(k, "movie/local/"):
			local = v
		}
	}
	if extra == nil || extra.Kind != "extra" || extra.ExtraType != "trailer" || extra.Name != "" {
		t.Errorf("Alien's trailer: %+v", extra)
	}
	if local == nil || local.Name != "Holidays" || local.Year != 2019 || !local.Image {
		t.Fatalf("the local film: %+v", local)
	}
	if img, err := f.Image(context.Background(), local.Key); err != nil || string(img) != "picture" {
		t.Errorf("its image: %q %v", img, err)
	}
	if _, err := f.Image(context.Background(), "movie/tmdb/949"); err == nil {
		t.Error("an image of a title not local")
	}
	// The etag follows the bytes: the same content, the same etag.
	again, _ := f.copy(heat.Copies[0].ID)
	if again.ETag != heat.Copies[0].ETag {
		t.Error("the etag changed with no change of bytes")
	}
}
