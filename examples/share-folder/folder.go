package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// folder is a folder of films and series, shared as one library: the
// engine's Catalog (with Images), Playback and Media. What it reads:
//
//	Heat (1995) [tmdbid-949].mkv             a film, by its TMDB ID
//	Heat (1995) [tmdbid-949].fr.vtt          its subtitles, a file per language
//	Heat (1995) [tmdbid-949].hls/            an HLS copy made beforehand (master.m3u8 and what it names)
//	Heat (1995) [tmdbid-949].trickplay/      seek-bar previews: layout.json, 0.jpg, 1.jpg…
//	Alien (1979) [tmdbid-348]/               a film in a folder of its own (its largest video file)
//	    Trailers/  Extras/                   with its extras: trailers, featurettes
//	Severance (2022) [tmdbid-95396]/         a series: its episodes S<n>E<n> anywhere inside
//	    Season 1/S01E01 [tmdbid-1949537].mkv an episode by its TMDB episode ID, placed at S01E01
//	    Season 1/S01E02.mkv                  one by its numbers (TMDB doesn't list it yet)
//	    Trailers/  Extras/                   the series' extras
//	Lain [tvdbid-78914]/                     a series by its TheTVDB ID: episodes by their numbers
//	Holidays (2019).mp4                      a local film: no public database knows it
//	Holidays (2019).jpg                      its image (.jpg or .png, beside it), optional
//	Home videos (2020)/                      a local series (poster.jpg inside, its image)
//
// Local titles are shared only with the peers and groups the operator
// allows them (local, group-local). Their keys and the extras' are made
// with the engine's OpaqueKeyPart from their paths, so a peer can't tell
// a path from them; moving a file gives it a new key.
//
// Files are served as they are: by byte ranges (delivery file), or as
// their HLS copy (a remux made beforehand) when the viewer doesn't play
// their container. A play without delivery of a copy outside the
// baseline is transcoded by ffmpeg before the answer (baseline.go). A
// copy is described from its file (probe.go).
type folder struct {
	dir string
	// opaque is the engine's OpaqueKeyPart, set once it is open.
	opaque func(string) string
	probes probes
	// made are the baseline HLS copies ffmpeg made, by playback handle:
	// their temporary folders, removed when the playback stops.
	mu   sync.Mutex
	made map[string]string
	n    int
}

// item is a title the folder holds: its title; for a film, an episode
// or an extra, its video file (its copy's handle, a path in the
// folder); for a local film or series, its image file.
type item struct {
	title *msfp.VideoTitle
	video string
	image string
}

var (
	videoExt = regexp.MustCompile(`\.(mp4|m4v|mkv|webm|mov|ts|avi)$`)
	// named reads "Name (Year) [tmdbid-N]", the year and the ID optional.
	named   = regexp.MustCompile(`^(.*?)\s*(?:\((\d{4})\))?\s*(?:\[(tmdbid|tvdbid)-([1-9][0-9]{0,9})\])?$`)
	episode = regexp.MustCompile(`(?i)\bS(\d{1,4})E(\d{1,5})\b`)
	epID    = regexp.MustCompile(`\[tmdbid-([1-9][0-9]{0,9})\]`)
	extras  = map[string]string{"Trailers": "trailer", "Extras": "featurette"}
)

// sideDir says whether a folder is a video file's own (its HLS copy, its
// previews), not a title's.
func sideDir(name string) bool {
	return strings.HasSuffix(name, ".hls") || strings.HasSuffix(name, ".trickplay")
}

// scan reads the folder: every title it holds, by key.
func (f *folder) scan() map[string]item {
	out := map[string]item{}
	entries, _ := os.ReadDir(f.dir)
	for _, en := range entries {
		switch name := en.Name(); {
		case en.IsDir() && !sideDir(name):
			f.scanDir(out, name)
		case !en.IsDir() && videoExt.MatchString(name):
			base := videoExt.ReplaceAllString(name, "")
			if t := f.film(base, name); t != nil {
				out[t.Key] = item{title: t, video: name, image: localImage(t, f.dir, base)}
			}
		}
	}
	return out
}

// film is the title of a film named base: by its TMDB ID, or local
// (its key made from where it lies: rel); nil for one named by TheTVDB,
// which keys no films.
func (f *folder) film(base, rel string) *msfp.VideoTitle {
	m := named.FindStringSubmatch(base)
	t := &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Kind: "movie", Libraries: []string{"folder"}}}
	switch m[3] {
	case "tmdbid":
		t.Key = "movie/tmdb/" + m[4]
	case "tvdbid":
		return nil
	default:
		t.Key, t.Name, t.Year = "movie/local/"+f.opaque(rel), m[1], year(m[2])
	}
	return t
}

// scanDir reads a title's folder: a series when it holds episodes, a
// film otherwise (its largest video file); and their extras.
func (f *folder) scanDir(out map[string]item, dir string) {
	top := filepath.Join(f.dir, dir)
	var episodes, videos []string
	filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(f.dir, path)
		switch {
		case err != nil:
			return nil
		case d.IsDir() && path != top && (sideDir(d.Name()) || extras[d.Name()] != ""):
			return filepath.SkipDir
		case d.IsDir() || !videoExt.MatchString(d.Name()):
		case episode.MatchString(d.Name()):
			episodes = append(episodes, rel)
		case filepath.Dir(rel) == dir:
			videos = append(videos, rel)
		}
		return nil
	})
	var t *msfp.VideoTitle
	switch {
	case len(episodes) > 0:
		t = f.series(out, dir, episodes)
	case len(videos) > 0:
		slices.SortFunc(videos, func(a, b string) int { return cmp.Compare(size(f.dir, b), size(f.dir, a)) })
		if t = f.film(dir, dir); t != nil {
			out[t.Key] = item{title: t, video: videos[0]}
		}
	}
	if t == nil {
		return
	}
	it := out[t.Key]
	it.image = localImage(t, top, "poster")
	out[t.Key] = it
	for folderName, kind := range extras {
		files, _ := os.ReadDir(filepath.Join(top, folderName))
		for _, x := range files {
			if x.IsDir() || !videoExt.MatchString(x.Name()) {
				continue
			}
			rel := filepath.Join(dir, folderName, x.Name())
			key := t.Key + "/x/" + f.opaque(rel)
			out[key] = item{video: rel, title: &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: key, Kind: "extra",
				Libraries: []string{"folder"}}, ExtraType: kind}} // an extra carries no name
		}
	}
}

// series adds a series, its seasons and its episodes, and gives the
// series.
func (f *folder) series(out map[string]item, dir string, episodes []string) *msfp.VideoTitle {
	m := named.FindStringSubmatch(dir)
	lib := []string{"folder"}
	s := &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Kind: "series", Libraries: lib}}
	switch m[3] {
	case "tmdbid":
		s.Key = "series/tmdb/" + m[4]
	case "tvdbid":
		s.Key = "series/tvdb/" + m[4]
	default:
		s.Key, s.Name, s.Year = "series/local/"+f.opaque(dir), m[1], year(m[2])
	}
	out[s.Key] = item{title: s}
	for _, rel := range episodes {
		n := episode.FindStringSubmatch(filepath.Base(rel))
		season, number := atoi(n[1]), atoi(n[2])
		seasonKey := s.Key + "/s" + strconv.Itoa(season)
		out[seasonKey] = item{title: &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: seasonKey, Kind: "season", Libraries: lib}}}
		e := &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: seasonKey + "/e" + strconv.Itoa(number), Kind: "episode", Libraries: lib}}
		if id := epID.FindStringSubmatch(filepath.Base(rel)); id != nil && m[3] == "tmdbid" {
			// Listed by TMDB: keyed by its episode ID, placed by its numbers.
			e.Key, e.Place = s.Key+"/e"+id[1], &msfp.Place{Season: season, Episode: number}
		}
		out[e.Key] = item{title: e, video: rel}
	}
	return s
}

func (f *folder) Libraries(context.Context) ([]msfp.Library, error) {
	return []msfp.Library{{ID: "folder", Name: filepath.Base(f.dir)}}, nil
}

func (f *folder) Titles(_ context.Context, _ []string, after string, limit int) (msfp.TitlePage, error) {
	items := f.scan()
	var page msfp.TitlePage
	for _, key := range slices.Sorted(maps.Keys(items)) {
		if key <= after {
			continue
		}
		if len(page.Titles) == limit {
			page.More = true
			break
		}
		it := items[key]
		t := *it.title
		t.Image = it.image != ""
		if it.video != "" {
			c, ok := f.copy(it.video)
			if !ok {
				continue
			}
			t.Copies = []wire.Copy{c}
		}
		page.Titles = append(page.Titles, &t)
	}
	return page, nil
}

// Image gives a local film's or series' image (MSFP catalog section 4.4).
func (f *folder) Image(_ context.Context, key string) ([]byte, error) {
	it, ok := f.scan()[key]
	if !ok || it.image == "" {
		return nil, msfp.ErrNotFound
	}
	return os.ReadFile(it.image)
}

// copy describes a video file as the catalog lists a copy (MSFP video
// section 3.2): its exact size, its media type and etag, what ffprobe
// says of it (probe.go, with the describe package), its .vtt subtitles
// (numbered after the file's streams), its previews' layout. Its ID is its handle: its path in the
// folder.
func (f *folder) copy(rel string) (wire.Copy, bool) {
	path := filepath.Join(f.dir, rel)
	st, err := os.Stat(path)
	if err != nil || st.Size() < 1 {
		return wire.Copy{}, false
	}
	c, streams := f.probes.describe(path, st)
	if c.ETag == "" {
		return wire.Copy{}, false
	}
	c.ID, c.Size = rel, st.Size()
	c.SubtitleTracks = slices.Clip(c.SubtitleTracks) // the probe's copy is shared: appends copy it
	for i, s := range subtitles(path) {
		c.SubtitleTracks = append(c.SubtitleTracks, wire.CopySubtitle{ID: "s" + strconv.Itoa(streams+i), Language: language(s),
			Format: "webvtt", External: true})
	}
	c.PreviewLayout = previews(path)
	return c, true
}

// subtitles are the .vtt files beside a video file, s<n> by position.
func subtitles(path string) []string {
	found, _ := filepath.Glob(escapeGlob(videoExt.ReplaceAllString(path, "")) + ".*.vtt")
	return found
}

// language is a subtitles file's language, from its name ("fr").
func language(path string) string {
	tag, _ := wire.SentTag(strings.TrimPrefix(filepath.Ext(strings.TrimSuffix(path, ".vtt")), "."))
	return tag
}

// previews reads a video file's previews' layout, layout.json in its
// .trickplay folder: {"tileWidth":320,"tileHeight":180,"columns":10,
// "rows":10,"intervalMs":10000,"tileCount":540}, its sheets beside it
// named 0.jpg, 1.jpg… (the engine re-encodes each before it leaves).
func previews(path string) *wire.PreviewLayout {
	b, err := os.ReadFile(videoExt.ReplaceAllString(path, "") + ".trickplay/layout.json")
	var t wire.PreviewLayout
	if err != nil || json.Unmarshal(b, &t) != nil || !t.InBounds() {
		return nil
	}
	return &t
}

// offeredHere says the folder's one library is among those offered to
// the peer a call is made for: a copy's handle (its file's path) is
// resolved only then.
func offeredHere(peer msfp.PeerRef) bool { return slices.Contains(peer.Libraries, "folder") }

// Describe tells what the catalog doesn't: no fonts; every .vtt file is
// served apart (as text: video section 5.4), the subtitle tracks inside
// the file aren't (they play with the file as it is).
func (f *folder) Describe(_ context.Context, peer msfp.PeerRef, id string) (msfp.CopyInfo, error) {
	var info msfp.CopyInfo
	if !offeredHere(peer) {
		return info, msfp.ErrNotFound
	}
	if c, ok := f.copy(id); ok {
		for _, s := range c.SubtitleTracks {
			if !s.External {
				info.NotApart = append(info.NotApart, s.ID)
			}
		}
	}
	return info, nil
}

// Start serves what the engine decided (req.Served), or refuses: the
// file when the viewer's server asks it (delivery file, always that of a
// download or a scan); its HLS copy, made beside it, for a remux to its
// segments' media type (video/mp2t for .ts segments, video/mp4
// otherwise), and without delivery when the engine found the copy within
// the baseline (H.264 High 4.1 8-bit SDR BT.709 and AAC-LC stereo, the
// HLS copy then taken to be the same streams); without delivery outside
// it, a transcode ffmpeg makes (baseline.go). It makes no transcode
// asked by delivery, and no audio renditions, which the answer then says
// (Started.AudioRenditions false: MSFP playback section 2.3). The handle
// is the protocol and the file's path.
func (f *folder) Start(ctx context.Context, req msfp.StartRequest) (msfp.Started, error) {
	if !filepath.IsLocal(req.Copy) || !offeredHere(req.Peer) {
		return msfp.Started{}, msfp.ErrNotFound
	}
	path := filepath.Join(f.dir, req.Copy)
	hlsDir := videoExt.ReplaceAllString(path, "") + ".hls"
	_, err := os.Stat(hlsDir + "/master.m3u8")
	hls := err == nil
	method, protocol := "direct", "file"
	switch req.Delivery {
	case msfp.DeliveryFile:
	case msfp.DeliveryTranscode:
		return msfp.Started{}, msfp.ErrDeliveryRefused
	case msfp.DeliveryRemux:
		segments := "mp4"
		if ts, _ := filepath.Glob(escapeGlob(hlsDir) + "/*.ts"); len(ts) > 0 {
			segments = "mp2t"
		}
		if !hls || req.Served.Segments != segments {
			return msfp.Started{}, msfp.ErrDeliveryRefused
		}
		method, protocol = "remux", "hls"
	default:
		if req.Served.Method == wire.MethodTranscode {
			return f.baseline(ctx, req, path)
		}
		if !hls {
			return msfp.Started{}, msfp.ErrNotPlayable
		}
		method, protocol = "remux", "hls"
	}
	if _, err := req.Admit(method); err != nil {
		return msfp.Started{}, err
	}
	return msfp.Started{Handle: protocol + " " + req.Copy, Method: method, Protocol: protocol, PositionMs: req.StartMs}, nil
}

func (*folder) Progress(context.Context, msfp.Report) error { return nil }

func (f *folder) Stop(_ context.Context, handle string, _ msfp.EndReason) error {
	f.mu.Lock()
	dir, ok := f.made[handle]
	delete(f.made, handle)
	f.mu.Unlock()
	if ok {
		return os.RemoveAll(dir)
	}
	return nil
}

// Serve answers what a playback's ticket opens (or, for a sheet under a
// previews ticket, OfCopy, a copy's: its handle is then the file's path).
func (f *folder) Serve(_ context.Context, peer msfp.PeerRef, handle string, p msfp.MediaPath, rng *msfp.Range, out msfp.ResponseSink) error {
	rel := handle
	if !p.OfCopy {
		_, rel, _ = strings.Cut(handle, " ")
	}
	if !filepath.IsLocal(rel) || p.Entry != 0 || !offeredHere(peer) {
		return msfp.ErrNotFound
	}
	path := filepath.Join(f.dir, rel)
	hlsDir := videoExt.ReplaceAllString(path, "") + ".hls"
	base := videoExt.ReplaceAllString(path, "")
	if !p.OfCopy {
		f.mu.Lock()
		if dir, ok := f.made[handle]; ok {
			hlsDir = dir
		}
		f.mu.Unlock()
	}
	switch p.Kind {
	case msfp.MediaFile:
		return serveFile(path, rng, out)
	case msfp.MediaMaster:
		return serveFile(filepath.Join(hlsDir, "master.m3u8"), nil, out)
	case msfp.MediaPlaylist, msfp.MediaSegment:
		return serveFile(filepath.Join(hlsDir, p.Name), rng, out)
	case msfp.MediaSheet:
		return serveFile(filepath.Join(base+".trickplay", strconv.Itoa(p.Sheet)+".jpg"), nil, out)
	case msfp.MediaSubtitles:
		subs := subtitles(path)
		st, err := os.Stat(path)
		if err != nil {
			return msfp.ErrNotFound
		}
		_, streams := f.probes.describe(path, st)
		n, _ := strconv.Atoi(strings.TrimPrefix(p.Track, "s"))
		if n -= streams; n < 0 || n >= len(subs) {
			return msfp.ErrNotFound
		}
		vtt, err := os.ReadFile(subs[n])
		if err != nil {
			return msfp.ErrNotFound
		}
		if !p.Whole {
			var ok bool
			if vtt, ok = wire.WebVTTPart(vtt, p.Part); !ok {
				return msfp.ErrNotFound
			}
		}
		out.Start(http.StatusOK, "text/vtt; charset=utf-8", int64(len(vtt)), "")
		_, err = out.Write(vtt)
		return err
	}
	return msfp.ErrNotFound
}

// serveFile answers a file, or the range of it asked. For the copy's
// file the engine gives an exact range, and writes the headers itself.
func serveFile(path string, rng *msfp.Range, out msfp.ResponseSink) error {
	file, err := os.Open(path)
	if err != nil {
		return msfp.ErrNotFound
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return err
	}
	size, first, last := st.Size(), int64(0), st.Size()-1
	types := map[string]string{".m3u8": "application/vnd.apple.mpegurl", ".ts": "video/mp2t", ".m4s": "video/iso.segment",
		".jpg": "image/jpeg", ".mp4": "video/mp4"}
	status, contentType, contentRange := http.StatusOK, cmp.Or(types[filepath.Ext(path)], "application/octet-stream"), ""
	if rng != nil {
		first, last = rng.First, min(rng.Last, size-1)
		if rng.Suffix > 0 {
			first, last = max(size-rng.Suffix, 0), size-1
		} else if rng.Last < 0 {
			last = size - 1
		}
		if first >= size {
			return msfp.ErrRangeNotSatisfiable
		}
		status, contentRange = http.StatusPartialContent, fmt.Sprintf("bytes %d-%d/%d", first, last, size)
	}
	out.Start(status, contentType, last-first+1, contentRange)
	_, err = io.Copy(out, io.NewSectionReader(file, first, last-first+1))
	return err
}

// localImage is a local film's or series' image: base.jpg or base.png
// in dir; "" for none, and for every other title.
func localImage(t *msfp.VideoTitle, dir, base string) string {
	if !strings.Contains(t.Key, "/local/") {
		return ""
	}
	for _, ext := range []string{".jpg", ".png"} {
		if p := filepath.Join(dir, base+ext); size("", p) > 0 {
			return p
		}
	}
	return ""
}

func size(dir, rel string) int64 {
	st, err := os.Stat(filepath.Join(dir, rel))
	if err != nil {
		return 0
	}
	return st.Size()
}

func year(s string) int {
	if y := atoi(s); y >= 1800 && y <= 9999 {
		return y
	}
	return 0
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// escapeGlob escapes what filepath.Glob reads as a pattern, so that a
// name like "Heat [tmdbid-949]" matches only itself.
func escapeGlob(s string) string {
	return strings.NewReplacer(`[`, `\[`, `]`, `\]`, `*`, `\*`, `?`, `\?`).Replace(s)
}
