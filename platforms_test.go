package msfp_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
)

// What MSFP says for media servers other than Jellyfin (decided on
// 2026-11-18, the spec's api/platforms.md): a copy of several parts,
// media servers' language codes.

// A copy of several files is never served as file (MSFP media section
// 1): the host is told its parts, and a file start for it is refused.
func TestSeveralPartsNeverFile(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	h.copyOf("movie/tmdb/949", 0).Parts = 2
	h.protocol = "file"
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	// Asked as file: refused before the host is asked.
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949",
		Delivery: msfp.DeliveryFile}); !errors.Is(err, msfp.ErrDeliveryRefused) {
		t.Fatalf("a copy of two parts asked as file: %v", err)
	}
	if _, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err == nil {
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
	if pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil || pb.Protocol != "hls" {
		t.Errorf("as hls: %+v, %v", pb, err)
	}
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: "movie/tmdb/949", VersionID: copyID(t, s, "movie/tmdb/949", 1), Delivery: msfp.DeliveryFile})
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
	}
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	v := s.peerTitles(t)[key].(*msfp.VideoTitle)
	if !slices.Equal(v.AudioLanguages(), []string{"en", "fr"}) || !slices.Equal(v.SubtitleLanguages(), []string{"de", "fr-CA"}) {
		t.Errorf("the title's languages: %q %q", v.AudioLanguages(), v.SubtitleLanguages())
	}
	pb, err := s.b.eng.Play(ctx, s.onB.ID, msfp.PlayRequest{Title: key, AudioTrack: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	// The first copy, the track named (MSFP playback section 2.6).
	if h.started[0].Copy != key+"#0" || pb.AudioTrack != "a1" || v.Copies[0].AudioTracks[1].Language != "fr" ||
		v.Copies[0].SubtitleTracks[0].Language != "de" || v.Copies[0].SubtitleTracks[1].Language != "fr-CA" {
		t.Errorf("the playback: %+v, started %+v", pb, h.started[0])
	}
	if body := s.a.exchanges("play")[0].resBody; strings.Contains(body, `"fre"`) || strings.Contains(body, `"und"`) {
		t.Errorf("a code left in the answer: %s", body)
	}
}

// copyID is the version ID of copy n of a title B keeps.
func copyID(t *testing.T, s *sharing, key string, n int) string {
	t.Helper()
	v, ok := s.peerTitles(t)[key].(*msfp.VideoTitle)
	if !ok || len(v.Copies) <= n {
		t.Fatalf("no copy %d of %s", n, key)
	}
	return v.Copies[n].ID
}
