package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// sink is a ResponseSink that keeps what it is sent.
type sink struct{ bytes.Buffer }

func (*sink) Start(int, string, int64, string) {}

// A file's own subtitle tracks listed with the .vtt beside it (MSFP Media video
// 3.3: a copy lists all its subtitle tracks), told not served
// apart; an AVI read. With ffprobe, on the media in $EXAMPLE_MEDIA:
// subs.mkv (h264, aac, SubRip fr, ASS en forced) and film.avi (mpeg4,
// mp3), made with ffmpeg (testsrc and sine, 2 s); skipped otherwise.
func TestFolderFileTracks(t *testing.T) {
	media := os.Getenv("EXAMPLE_MEDIA")
	if media == "" {
		t.Skip("EXAMPLE_MEDIA not set")
	}
	dir := t.TempDir()
	put := func(from, to string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(media, from))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, to), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("subs.mkv", "Heat (1995) [tmdbid-949].mkv")
	put("film.avi", "Blade Runner (1982) [tmdbid-78].avi")
	if err := os.WriteFile(filepath.Join(dir, "Heat (1995) [tmdbid-949].de.vtt"),
		[]byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHallo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &folder{dir: dir}
	page, err := f.Titles(context.Background(), nil, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]*msfp.VideoTitle{}
	for _, tt := range page.Titles {
		byKey[tt.(*msfp.VideoTitle).Key] = tt.(*msfp.VideoTitle)
	}
	heat := byKey["movie/tmdb/949"].Copies[0]
	want := []wire.CopySubtitle{
		{ID: "s2", Format: "subrip", Language: "fr"},
		{ID: "s3", Format: "ass", Language: "en", Forced: true},
		{ID: "s4", Format: "webvtt", Language: "de", External: true},
	}
	if !slices.Equal(heat.SubtitleTracks, want) {
		t.Errorf("Heat's subtitle tracks: %+v", heat.SubtitleTracks)
	}
	peer := msfp.PeerRef{ID: "peer", Libraries: []string{"folder"}}
	if info, _ := f.Describe(context.Background(), peer, heat.ID); !slices.Equal(info.NotApart, []string{"s2", "s3"}) {
		t.Errorf("not served apart: %v", info.NotApart)
	}
	var out sink
	if err := f.Serve(context.Background(), peer, "file "+heat.ID, msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s4", Whole: true}, nil, &out); err != nil ||
		!strings.Contains(out.String(), "Hallo") {
		t.Errorf("the .vtt as s4: %q %v", out.String(), err)
	}
	// A peer the folder isn't offered to finds no copy of it.
	other := msfp.PeerRef{ID: "other", Libraries: []string{}}
	if _, err := f.Describe(context.Background(), other, heat.ID); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("Describe for a peer not offered the folder: %v", err)
	}
	if err := f.Serve(context.Background(), other, "file "+heat.ID, msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: "s4", Whole: true}, nil, &sink{}); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("Serve for a peer not offered the folder: %v", err)
	}
	avi := byKey["movie/tmdb/78"]
	if avi == nil {
		t.Fatal("the AVI isn't read")
	}
	if c := avi.Copies[0]; c.MediaType != "video/avi" || c.Video == nil || c.Video.Codec != "mpeg4" || len(c.AudioTracks) != 1 ||
		c.AudioTracks[0].ID != "a1" || c.DurationMs < 1900 {
		t.Errorf("the AVI's copy: %+v", c)
	}
}

// A copy lists at most 64 subtitle tracks (MSFP Media video 3.2): of
// 70 .vtt files beside a film, the first 64 by name.
func TestFolderSubtitlesCapped(t *testing.T) {
	dir := t.TempDir()
	film := filepath.Join(dir, "Heat (1995) [tmdbid-949].mkv")
	if err := os.WriteFile(film, []byte("heat"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 70 {
		name := filepath.Join(dir, "Heat (1995) [tmdbid-949]."+strconv.Itoa(100+i)+".vtt")
		if err := os.WriteFile(name, []byte("WEBVTT\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &folder{dir: dir}
	c, ok := f.copy(filepath.Base(film))
	if !ok || len(c.SubtitleTracks) != wire.MaxSubtitleTracks {
		t.Fatalf("%d subtitle tracks listed", len(c.SubtitleTracks))
	}
	if first, last := c.SubtitleTracks[0], c.SubtitleTracks[63]; first.ID != "s0" || last.ID != "s63" || !last.External {
		t.Errorf("the tracks kept: %+v … %+v", first, last)
	}
}

func TestFolderForgetsRemovedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Heat (1995) [tmdbid-949].mkv", "Alien (1979) [tmdbid-348].mp4"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &folder{dir: dir, opaque: func(s string) string { return "o" }}
	if _, err := f.Titles(context.Background(), nil, "", 100); err != nil {
		t.Fatal(err)
	}
	if n := len(f.probes.done); n != 2 {
		t.Fatalf("probes after the first reading: %d, want 2", n)
	}
	if err := os.Remove(filepath.Join(dir, "Heat (1995) [tmdbid-949].mkv")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Titles(context.Background(), nil, "", 100); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.probes.done[filepath.Join(dir, "Heat (1995) [tmdbid-949].mkv")]; ok || len(f.probes.done) != 1 {
		t.Fatalf("probes after Heat left: %v", f.probes.done)
	}
}
