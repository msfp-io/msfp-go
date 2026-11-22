package msfp_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// What MSFP says for media servers other than Jellyfin (decided on
// 2026-11-18, the spec's api/platforms.md): a copy of several parts,
// media servers' language codes, a marker whose end isn't known.

// A copy of several files is never served as file (MSFP media section
// 1): the host is told its parts, and a file start for it is refused.
func TestSeveralPartsNeverFile(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	info := h.info["movie/tmdb/949#0"]
	info.Parts = 2
	h.info["movie/tmdb/949#0"] = info
	h.protocol = "file"
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", AudioLanguage: "en"}); err == nil {
		t.Fatal("a copy of two parts played as file")
	}
	if len(h.started) != 1 || h.started[0].Parts != 2 {
		t.Fatalf("the host started %+v", h.started)
	}
	if st := h.stops(); st["h1"] != msfp.EndStopped {
		t.Errorf("the host's playback wasn't stopped: %v", st)
	}
	if v := s.a.eng.Playbacks(); len(v) != 0 {
		t.Errorf("a playback left: %+v", v)
	}
	// As hls, it plays; a copy of one file still plays as file.
	h.protocol = "hls"
	if pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", AudioLanguage: "en"}); err != nil || pb.Protocol != "hls" {
		t.Errorf("as hls: %+v, %v", pb, err)
	}
	h.protocol = "file"
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", AudioLanguage: "fr"})
	if err != nil || pb.Protocol != "file" || h.started[len(h.started)-1].Parts != 1 {
		t.Errorf("a copy of one file: %+v, %v, %+v", pb, err, h.started)
	}
}

// A media server's ISO 639-2 codes go out as ISO 639-1 tags (MSFP
// catalog section 3), in titles, tracks and the choice of the copy.
func TestMediaServerLanguageCodes(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	key := "movie/tmdb/949"
	for i, code := range []string{"eng", "fre"} {
		c := &h.titles[key].(*msfp.VideoTitle).Copies[i]
		c.AudioTracks[0].Language = code
		c.AudioTracks = append(c.AudioTracks, wire.CopyAudio{ID: "a1", Language: []string{"fra", "und"}[i], Codec: "aac"})
		c.SubtitleTracks = []wire.CopySubtitle{{ID: "s0", Language: "ger", Format: "subrip"}, {ID: "s1", Language: "fre-ca", Format: "subrip"}}
		h.copies[key][i].Audio = []string{code}
		handle := h.copies[key][i].Handle
		info := h.info[handle]
		info.AudioTracks = []msfp.Track{{ID: "a0", Label: "Audio", Language: code, Default: true}}
		info.SubtitleTracks = []msfp.Track{{ID: "s0", Label: "Deutsch", Language: "GER"}, {ID: "s1", Label: "?", Language: "und"}}
		h.info[handle] = info
	}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	v := s.peerTitles(t)[key].(*msfp.VideoTitle)
	if !slices.Equal(v.AudioLanguages(), []string{"en", "fr"}) || !slices.Equal(v.SubtitleLanguages(), []string{"de", "fr-CA"}) {
		t.Errorf("the title's languages: %q %q", v.AudioLanguages(), v.SubtitleLanguages())
	}
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: key, AudioLanguage: "fr", SubtitleLanguage: "de"})
	if err != nil {
		t.Fatal(err)
	}
	if h.started[0].Copy != key+"#1" || pb.AudioTracks[0].Language != "fr" || pb.SubtitleTrack != "s0" ||
		pb.SubtitleTracks[0].Language != "de" || pb.SubtitleTracks[1].Language != "" {
		t.Errorf("the playback: %+v, started %+v", pb, h.started[0])
	}
	if body := s.a.exchanges("play")[0].resBody; strings.Contains(body, `"fre"`) || strings.Contains(body, `"und"`) {
		t.Errorf("a code left in the answer: %s", body)
	}
}

// A marker whose end the host doesn't know ends at the copy's length
// (MSFP video section 5.3).
func TestMarkerToTheEnd(t *testing.T) {
	h := filmHost()
	h.segments = []msfp.Segment{{Kind: "intro", StartMs: 0, EndMs: 60_000}, {Kind: "outro", StartMs: 5_300_000}}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	pb, err := s.b.eng.Play(context.Background(), s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pb.Segments) != 2 || pb.Segments[1] != (msfp.Segment{Kind: "outro", StartMs: 5_300_000, EndMs: 5_400_000}) {
		t.Errorf("the markers: %+v", pb.Segments)
	}
}
