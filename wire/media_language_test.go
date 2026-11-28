package wire

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// MSFP Media's additions to playing: progressSeconds, paused, replaces,
// the capability list as MSFP refuses it and as a player's carrier reads
// it.

func TestProgressSeconds(t *testing.T) {
	for _, c := range []struct{ n, want int }{{0, 60}, {44, 60}, {45, 45}, {90, 90}, {120, 120}, {121, 60}} {
		if got := ProgressInterval(c.n); got != time.Duration(c.want)*time.Second {
			t.Errorf("%d: %v", c.n, got)
		}
		if got := IdleLimit(c.n); got != 2*time.Duration(c.want)*time.Second {
			t.Errorf("%d: idle %v", c.n, got)
		}
	}
	if IdleEnd != IdleLimit(DefaultProgressSeconds) {
		t.Errorf("IdleEnd %v", IdleEnd)
	}
	base := `{"id":"play_1","ticket":"t1","protocol":"hls","method":"transcode","etag":"e1","positionMs":0,"versionId":"v1"`
	for body, want := range map[string]int{base + `}`: 60, base + `,"progressSeconds":45}`: 45,
		base + `,"progressSeconds":120}`: 120, base + `,"progressSeconds":121}`: 60, base + `,"progressSeconds":44}`: 60,
		base + `,"progressSeconds":"90"}`: 60, base + `,"progressSeconds":90.5}`: 60} {
		a, err := ReadPlayAnswer([]byte(body), Asked{})
		if err != nil || a.ProgressSeconds != want {
			t.Errorf("%s: %d %v", body, a.ProgressSeconds, err)
		}
	}
}

func TestReportPaused(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r, err := ReadReport(CallProgress, []byte(`{"id":"play_1","paused":true}`), now)
	if err != nil || !r.Paused {
		t.Errorf("paused true: %+v %v", r, err)
	}
	for _, body := range []string{`{"id":"play_1","paused":1}`, `{"id":"play_1","paused":"true"}`, `{"id":"play_1","paused":null}`} {
		if _, err := ReadReport(CallProgress, []byte(body), now); err == nil {
			t.Errorf("%s: kept", body)
		}
	}
	// stop knows no paused: an unknown member, ignored.
	if _, err := ReadReport(CallStop, []byte(`{"id":"play_1","paused":1}`), now); err != nil {
		t.Errorf("stop with paused: %v", err)
	}
}

func TestPlayReplacesAndCapabilities(t *testing.T) {
	bad := func(body string) bool {
		_, err := ReadPlay([]byte(body), nil)
		var pr *PlayRefusal
		return errors.As(err, &pr) && pr.Status == 400
	}
	r, err := ReadPlay([]byte(`{"title":"movie/tmdb/949","replaces":"play_abc-_1"}`), nil)
	if err != nil || r.Replaces != "play_abc-_1" {
		t.Errorf("replaces: %+v %v", r, err)
	}
	for _, body := range []string{`{"title":"movie/tmdb/949","replaces":""}`, `{"title":"movie/tmdb/949","replaces":"a b"}`,
		`{"title":"movie/tmdb/949","replaces":7}`, `{"title":"movie/tmdb/949","replaces":null}`,
		// MSFP refuses a capability list, whatever it holds.
		`{"title":"movie/tmdb/949","capabilities":{}}`, `{"title":"movie/tmdb/949","capabilities":"x"}`} {
		if !bad(body) {
			t.Errorf("%s: not refused 400", body)
		}
	}
	player := PlayReader{Purposes: []string{PurposePlay, PurposeDownload}, Capabilities: true}
	good := `{"title":"movie/tmdb/949","capabilities":{"files":["video/mp4","video/matroska"],"segments":["video/mp4"],
		"video":[{"codec":"hevc","codecs":["hvc1.2.4.L153.B0"],"maxWidth":3840,"maxHeight":2160,"maxBitDepth":10,
		"transfers":[1,14,15,16,18],"hdr":["hdr10plus"],"dv":[5,8],"maxFps":"60/1","threeD":["sbs"]},
		{"codec":"h264","codecs":["avc1.640033"],"maxRefFrames":16,"interlaced":true},{"codec":"newcodec"}],
		"audio":[{"codec":"aac","codecs":["mp4a.40.2"],"maxChannels":6},{"codec":"eac3","objects":["dolby-atmos"],
		"maxSampleRate":48000,"maxBitDepth":24}],"subtitles":["subrip","hdmv_pgs_subtitle"],"future":1},"maxBitrateKbps":40000}`
	r, err = player.Read([]byte(good))
	if err != nil || r.Capabilities == nil || len(r.Capabilities.Video) != 3 || r.Capabilities.Video[0].MaxBitDepth != 10 ||
		!slices.Equal(r.Capabilities.Video[0].DV, []int{5, 8}) || r.Capabilities.Audio[1].MaxSampleRate != 48000 ||
		r.MaxBitrateKbps != 40000 {
		t.Fatalf("a capability list: %+v %v", r.Capabilities, err)
	}
	for _, body := range []string{
		`{"title":"movie/tmdb/949","purpose":"scan","delivery":"file"}`, // a purpose this carrier doesn't take
		`{"title":"movie/tmdb/949","delivery":"file","capabilities":{}}`,
		`{"title":"movie/tmdb/949","purpose":"download","capabilities":{}}`,
		`{"title":"movie/tmdb/949","capabilities":[]}`,
		`{"title":"movie/tmdb/949","capabilities":{"files":"video/mp4"}}`,
		`{"title":"movie/tmdb/949","capabilities":{"files":["Video/MP4"]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"segments":["video/mp4","video/mp2t","audio/mp4","audio/mp2t","x/y"]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"video":[{"codecs":["avc1.640033"]}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"video":[{"codec":"h264"}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"video":[{"codec":"hevc","codecs":["hvc1 2"]}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"video":[{"codec":"vp8","maxWidth":0}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"video":[{"codec":"vp8","maxFps":"60"}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"video":[{"codec":"vp8","maxRefFrames":4}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"video":[{"codec":"vp8","dv":[128]}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"video":[{"codec":"vp8","interlaced":1}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"audio":[{"codec":"aac"}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"audio":[{"codec":"opus","maxChannels":65}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"audio":[{"codec":"opus","maxSampleRate":7999}]}}`,
		`{"title":"movie/tmdb/949","capabilities":{"subtitles":["SubRip"]}}`,
	} {
		_, err := player.Read([]byte(body))
		var pr *PlayRefusal
		if !errors.As(err, &pr) || pr.Status != 400 {
			t.Errorf("%s: %v", body, err)
		}
	}
	// With a capability list, the answer's method is the source's pick:
	// file with direct, HLS otherwise.
	base := `{"id":"play_1","ticket":"t1","etag":"e1","positionMs":0,"versionId":"v1",`
	for body, ok := range map[string]bool{base + `"protocol":"file","method":"direct"}`: true,
		base + `"protocol":"hls","method":"remux"}`: true, base + `"protocol":"hls","method":"direct"}`: false,
		base + `"protocol":"file","method":"transcode"}`: false} {
		if _, err := ReadPlayAnswer([]byte(body), Asked{Capabilities: true}); (err == nil) != ok {
			t.Errorf("%s: %v", body, err)
		}
	}
}

func TestTargetRefusals(t *testing.T) {
	c := Copy{ID: "v1", DurationMs: 1000, Size: 100, MediaType: "video/matroska",
		Video:       &VideoStream{Codec: "h264", Codecs: "avc1.640028"},
		AudioTracks: []CopyAudio{{ID: "a0", Codec: "dts", Codecs: "dtsh"}}, Parts: 2}
	for _, x := range []struct {
		r       PlayRequest
		problem string
	}{
		{PlayRequest{Delivery: "teleport"}, ProblemDeliveryRefused},
		{PlayRequest{Delivery: DeliveryFile}, ProblemDeliveryRefused},
		{PlayRequest{Delivery: DeliveryRemux, MediaType: "video/matroska"}, ProblemTargetRefused},
		{PlayRequest{Delivery: DeliveryRemux, MediaType: "video/mp2t"}, ProblemTargetRefused},
		{PlayRequest{Delivery: DeliveryTranscode, Transcode: &TranscodeTargets{VideoCodec: "vvc"}}, ProblemTargetRefused},
		{PlayRequest{Delivery: DeliveryTranscode, Transcode: &TranscodeTargets{Range: "keep"}}, ProblemTargetRefused},
		{PlayRequest{Delivery: DeliveryTranscode, Transcode: &TranscodeTargets{BurnSubtitle: "s3"}}, ProblemTargetRefused},
	} {
		_, err := Serve(x.r, c, "a0", 0, Encoders{})
		var pr *PlayRefusal
		if !errors.As(err, &pr) || pr.Status != 409 || pr.Problem != x.problem {
			t.Errorf("%+v: %v, want %s", x.r, err, x.problem)
		}
	}
}

func TestProblemsAndMeaning(t *testing.T) {
	for typ, status := range map[string]int{ProblemGone: 404, ProblemTargetRefused: 409, ProblemUnreachable: 503,
		ProblemTicketRefused: 404, ProblemStreamLimit: 409} {
		if s, _, ok := Problem(typ); !ok || s != status {
			t.Errorf("%s: %d %v", typ, s, ok)
		}
	}
	if !LanguageProblem(ProblemGone) || LanguageProblem(ProblemStreamLimit) || !Temporary(ProblemUnreachable) || Temporary(ProblemGone) {
		t.Error("the table's columns")
	}
	for _, c := range []struct {
		status int
		typ    string
		play   bool
		want   string
	}{
		{404, ProblemGone, true, MeansGone}, {404, "something-new", false, MeansNotFound},
		{409, ProblemTargetRefused, true, MeansAskElse}, {409, ProblemDeliveryRefused, true, MeansAskElse},
		{409, ProblemNotPlayable, true, MeansNotPlayable}, {409, ProblemStreamLimit, true, MeansBusy},
		{409, "something-new", true, MeansBusy}, {409, "", true, MeansBusy}, {410, "x", false, MeansStopped},
		{400, "x", false, MeansBadAsk}, {429, "", false, MeansBusy}, {503, ProblemUnreachable, true, MeansBusy},
		{500, "", false, MeansBusy}, {404, ProblemNotSupported, true, MeansOther}, {418, "", true, MeansOther},
		// A known type with another status is read by its status.
		{409, ProblemGone, true, MeansBusy},
	} {
		if got := Meaning(c.status, c.typ, c.play); got != c.want {
			t.Errorf("%d %q: %s, want %s", c.status, c.typ, got, c.want)
		}
	}
}

func TestReadInstead(t *testing.T) {
	body := `{"type":"target-refused","title":"x","status":409,"instead":[
		{"delivery":"transcode","transcode":{"videoCodec":"h264","maxHeight":1080}},
		7,
		{"delivery":"remux"},
		{"delivery":"remux","mediaType":"video/mp4"},
		{"delivery":"transcode","transcode":{"maxHeight":1000}},
		{"delivery":"file"}]}`
	got := ReadInstead([]byte(body), "")
	if len(got) != 2 || got[0].Delivery != DeliveryTranscode || got[0].Transcode.MaxHeight != 1080 ||
		got[1].MediaType != "video/mp4" {
		t.Errorf("instead: %+v", got)
	}
	// For a download, a remux isn't one it takes.
	if got := ReadInstead([]byte(`{"instead":[{"delivery":"remux","mediaType":"video/mp4"},{"delivery":"file"}]}`), PurposeDownload); len(got) != 1 || got[0].Delivery != DeliveryFile {
		t.Errorf("a download's: %+v", got)
	}
	for _, body := range []string{`{"instead":{}}`, `[]`, `x`, `{"instead":[null]}`} {
		if got := ReadInstead([]byte(body), ""); len(got) != 0 {
			t.Errorf("%s: %+v", body, got)
		}
	}
}

func TestTitlesRequest(t *testing.T) {
	r, err := ReadTitlesRequest([]byte(`{}`))
	if err != nil || r.Libraries != nil || !r.Filter.None() {
		t.Errorf("{}: %+v %v", r, err)
	}
	r, err = ReadTitlesRequest([]byte(`{"libraries":[],"kind":"season","under":"series/tmdb/1","after":"a","since":-3}`))
	if err != nil || r.Libraries == nil || len(r.Libraries) != 0 || r.Filter.Kind != "season" || !r.Filter.HasUnder || r.Since != 0 {
		t.Errorf("filters: %+v %v", r, err)
	}
	for _, body := range []string{`[]`, `{"libraries":null}`, `{"libraries":"x"}`, `{"libraries":[1]}`, `{"kind":""}`,
		`{"kind":"Movie"}`, `{"under":1}`, `{"after":1}`, `{"since":1.5}`, `{"since":"1"}`} {
		if _, err := ReadTitlesRequest([]byte(body)); !errors.Is(err, ErrTitlesRequest) {
			t.Errorf("%s: %v", body, err)
		}
	}
	b, _ := TitlesRequest{Libraries: []string{}, Filter: TitleFilter{Under: "", HasUnder: true}}.MarshalJSON()
	if string(b) != `{"libraries":[],"under":"","after":""}` {
		t.Errorf("as sent: %s", b)
	}
	b, _ = TitlesRequest{Filter: TitleFilter{Kind: "track"}, Since: 4}.MarshalJSON()
	if string(b) != `{"kind":"track","after":"","since":4}` {
		t.Errorf("as sent: %s", b)
	}
	a := TitlesRequest{Libraries: []string{"x", "y"}, Filter: TitleFilter{Kind: "movie"}}
	if !SinceAllowed(a, TitlesRequest{Libraries: []string{"y", "x"}, Filter: TitleFilter{Kind: "movie"}}) ||
		SinceAllowed(a, TitlesRequest{Libraries: []string{"x"}, Filter: TitleFilter{Kind: "movie"}}) ||
		SinceAllowed(a, TitlesRequest{Libraries: []string{"x", "y"}}) ||
		SinceAllowed(TitlesRequest{}, TitlesRequest{Libraries: []string{}}) ||
		SinceAllowed(TitlesRequest{Filter: TitleFilter{Under: "x", HasUnder: true}}, TitlesRequest{Filter: TitleFilter{Under: "x", HasUnder: true}}) {
		t.Error("SinceAllowed")
	}
}

func TestTitleFilterPasses(t *testing.T) {
	mb := func(kind, id string) string { return kind + "/mb/" + id }
	album, artist := mb("album", "11111111-1111-1111-1111-111111111111"), mb("artist", "22222222-2222-2222-2222-222222222222")
	track := mb("track", "33333333-3333-3333-3333-333333333333")
	under := func(u string) TitleFilter { return TitleFilter{Under: u, HasUnder: true} }
	for _, c := range []struct {
		f    TitleFilter
		t    FilterFacts
		want bool
	}{
		{TitleFilter{}, FilterFacts{Key: "movie/tmdb/1"}, true},
		{TitleFilter{}, FilterFacts{Key: "not a key"}, false},
		{TitleFilter{Kind: "movie"}, FilterFacts{Key: "movie/tmdb/1"}, true},
		{TitleFilter{Kind: "series"}, FilterFacts{Key: "movie/tmdb/1"}, false},
		{under("movie/tmdb/1"), FilterFacts{Key: "movie/tmdb/1/x/a"}, true},
		{under("movie/tmdb/1"), FilterFacts{Key: "movie/tmdb/1"}, false},
		{under("movie/tmdb/1"), FilterFacts{Key: "movie/tmdb/12/x/a"}, false},
		{under("series/tvdb/7"), FilterFacts{Key: "series/tvdb/7/a12"}, true},
		{under("series/tvdb/7"), FilterFacts{Key: "series/tvdb/7/s1"}, true},
		{under("series/tvdb/7"), FilterFacts{Key: "series/tvdb/7/s1/e1"}, false},
		{under("series/tvdb/7"), FilterFacts{Key: "series/tvdb/7/s1/x/b"}, false},
		{under("series/tvdb/7/s1"), FilterFacts{Key: "series/tvdb/7/s1/x/b"}, true},
		{under("series/tvdb/7/s1"), FilterFacts{Key: "series/tvdb/7/s1/e1"}, true},
		{under("series/tvdb/7/s1"), FilterFacts{Key: "series/tvdb/7/s2/e1"}, false},
		{under("series/tvdb/7/s1"), FilterFacts{Key: "series/tvdb/7/a1"}, false},
		{under("series/tmdb/7/s1"), FilterFacts{Key: "series/tmdb/7/e99", PlaceSeason: 1, HasPlace: true}, true},
		{under("series/tmdb/7/s0"), FilterFacts{Key: "series/tmdb/7/e99"}, false},
		{under("series/tmdb/7/s2"), FilterFacts{Key: "series/tmdb/7/e99", PlaceSeason: 1, HasPlace: true}, false},
		{under("series/tmdb/8/s1"), FilterFacts{Key: "series/tmdb/7/e99", PlaceSeason: 1, HasPlace: true}, false},
		{under("series/tmdb/7/s1/e1"), FilterFacts{Key: "series/tmdb/7/s1/e1"}, false},
		{under(album), FilterFacts{Key: track, Album: album}, true},
		{under(album), FilterFacts{Key: track}, false},
		{under(artist), FilterFacts{Key: album, Artists: []string{artist}}, true},
		{under(artist), FilterFacts{Key: track, Artists: []string{artist}}, false},
		{under(""), FilterFacts{Key: "movie/tmdb/1"}, false},
		{TitleFilter{Kind: "extra", Under: "movie/tmdb/1", HasUnder: true}, FilterFacts{Key: "movie/tmdb/1/x/a"}, true},
	} {
		if got := c.f.Passes(c.t); got != c.want {
			t.Errorf("%+v %+v: %v", c.f, c.t, got)
		}
	}
	if p := under("series/tmdb/7/s1").UnderPrefix(); p != "series/tmdb/7/" {
		t.Errorf("prefix %q", p)
	}
	if p := under(album).UnderPrefix(); p != "" {
		t.Errorf("prefix %q", p)
	}
}

func TestDiscoveryMSFPMedia(t *testing.T) {
	d, err := ReadDiscovery([]byte(`{"core":[0],"msfpMedia":[0,"x",1,1]}`))
	if err != nil || !slices.Equal(d.MSFPMedia, []int{0, 1}) {
		t.Errorf("%+v %v", d, err)
	}
	if d, err := ReadDiscovery([]byte(`{"core":[0]}`)); err != nil || d.MSFPMedia != nil {
		t.Errorf("absent: %+v %v", d, err)
	}
	if _, err := ReadDiscovery([]byte(`{"msfpMedia":0}`)); err == nil {
		t.Error("msfpMedia not an array: kept")
	}
}

// The titles request as the owning server reads it (listing.json,
// titleOffer and titleRequests): a source of the offer, run with
// ReadTitlesRequest and TitleFilter.
func TestTitleRequestVectors(t *testing.T) {
	var v struct {
		Offer struct {
			Visible []string `json:"visible"`
			Titles  []struct {
				Key       string   `json:"key"`
				Libraries []string `json:"libraries"`
				Season    *int     `json:"season"`
				Album     string   `json:"album"`
				Artists   []string `json:"artists"`
			} `json:"titles"`
		} `json:"titleOffer"`
		Requests []struct {
			Name   string   `json:"name"`
			Parts  []string `json:"parts"`
			Body   string   `json:"body"`
			Valid  bool     `json:"valid"`
			Output string   `json:"output"`
		} `json:"titleRequests"`
	}
	readVectors(t, "media/v0/listing.json", &v)
	if len(v.Requests) != 48 || len(v.Offer.Titles) == 0 {
		t.Fatal("not every case read")
	}
	type listed struct {
		Key     string `json:"key"`
		Library string `json:"library"`
	}
	for _, c := range v.Requests {
		r, err := ReadTitlesRequest([]byte(c.Body))
		if (err == nil) != c.Valid {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		if err != nil {
			continue
		}
		out := struct {
			Titles       []listed `json:"titles"`
			MayBeChanges bool     `json:"mayBeChanges"`
		}{Titles: []listed{}, MayBeChanges: r.Since > 0 && r.Filter.Changes()}
		for _, ti := range v.Offer.Titles {
			k, ok := ParseKey(ti.Key)
			if !ok || !slices.Contains(c.Parts, k.Part) || ti.Key <= r.After {
				continue
			}
			lib := ""
			for _, l := range ti.Libraries {
				if slices.Contains(v.Offer.Visible, l) && (r.Libraries == nil || slices.Contains(r.Libraries, l)) && (lib == "" || l < lib) {
					lib = l
				}
			}
			f := FilterFacts{Key: ti.Key, Album: ti.Album, Artists: ti.Artists}
			if ti.Season != nil {
				f.PlaceSeason, f.HasPlace = *ti.Season, true
			}
			if lib != "" && r.Filter.Passes(f) {
				out.Titles = append(out.Titles, listed{ti.Key, lib})
			}
		}
		slices.SortFunc(out.Titles, func(a, b listed) int { return strings.Compare(a.Key, b.Key) })
		pbSame(t, c.Name, out, c.Output)
	}
}
