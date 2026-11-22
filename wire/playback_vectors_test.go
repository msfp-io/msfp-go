package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The playback vectors (vectors/v0/playback.json): every array, run
// against this package's rules.

type pbVectors struct {
	Context  pbContext       `json:"context"`
	Requests []pbCase        `json:"requests"`
	Delivers []pbCase        `json:"deliveries"`
	Downs    []pbCase        `json:"downloads"`
	Scans    []pbCase        `json:"scans"`
	Reports  []pbCase        `json:"reports"`
	Answers  []pbAnswerCase  `json:"answers"`
	Restarts []pbRestartCase `json:"restarts"`
	Limits   []pbLimitsCase  `json:"limits"`
}

type pbContext struct {
	Copies     map[string][]pbCopy `json:"copies"`
	OwnCapKbps int                 `json:"ownCapKbps"`
	Limits     Limits              `json:"limits"`
	InUse      pbUse               `json:"inUse"`
	StreamRoot string              `json:"streamRoot"`
	Now        int64               `json:"now"`
}

type pbCopy struct {
	ID          string `json:"id"`
	Parts       int    `json:"parts"`
	BitrateKbps int    `json:"bitrateKbps"`
	Audio       []struct {
		ID       string `json:"id"`
		Language string `json:"language"`
		Role     string `json:"role"`
		Default  bool   `json:"default"`
	} `json:"audio"`
	Subtitles []string `json:"subtitles"`
}

func (c pbCopy) play() PlayCopy {
	p := PlayCopy{ID: c.ID, Parts: c.Parts, BitrateKbps: c.BitrateKbps, Subtitles: c.Subtitles}
	for _, a := range c.Audio {
		p.Audio = append(p.Audio, CopyAudio{ID: a.ID, Language: a.Language, Role: a.Role, Default: a.Default})
	}
	return p
}

type pbUse struct {
	Streams              int `json:"streams"`
	Transcodes           int `json:"transcodes"`
	Downloads            int `json:"downloads"`
	DownloadsToday       int `json:"downloadsToday"`
	ScansToday           int `json:"scansToday"`
	ServerTranscodesFree int `json:"serverTranscodesFree"`
}

type pbCase struct {
	Name     string   `json:"name"`
	Call     string   `json:"call"`
	By       string   `json:"by"`
	Text     string   `json:"text"`
	Valid    bool     `json:"valid"`
	Status   int      `json:"status"`
	Type     string   `json:"type"`
	Output   string   `json:"output"`
	Limits   *Limits  `json:"limits"`
	InUse    *pbUse   `json:"inUse"`
	Playback *pbState `json:"playback"`
}

type pbState struct {
	State    string   `json:"state"`
	Purpose  string   `json:"purpose"`
	Title    string   `json:"title"`
	Queue    []string `json:"queue"`
	PlayedAt int64    `json:"playedAt"`
	LastAt   int64    `json:"lastAt"`
}

type pbAnswerCase struct {
	Name        string `json:"name"`
	Text        string `json:"text"`
	Delivery    string `json:"delivery"`
	CatalogEtag string `json:"catalogEtag"`
	Valid       bool   `json:"valid"`
	Output      string `json:"output"`
}

type pbRestartCase struct {
	Name    string  `json:"name"`
	Reading Reading `json:"reading"`
	Request string  `json:"request"`
	Status  int     `json:"status"`
	Answer  string  `json:"answer"`
	Valid   bool    `json:"valid"`
	Output  string  `json:"output"`
}

type pbLimitsCase struct {
	Name   string `json:"name"`
	Text   string `json:"text"`
	Valid  bool   `json:"valid"`
	Output string `json:"output"`
}

// Title keys as the vectors write them (video section 4, music section
// 2), the kinds play takes. The key rules are video's and music's; this
// mirrors them for the vectors' keys, local schemes and extras included.
var (
	pbID       = `[1-9][0-9]{0,9}`
	pbOpaque   = `[A-Za-z0-9_-]{1,64}`
	pbSeries   = `series/(tmdb|tvdb)/` + pbID + `|series/local/` + pbOpaque
	pbFilm     = `movie/tmdb/` + pbID + `|movie/local/` + pbOpaque
	pbSeason   = `/s(0|[1-9][0-9]{0,3})`
	pbPlayable = regexp.MustCompile(`^(` + pbFilm +
		`|(` + pbSeries + `)` + pbSeason + `/e(0|[1-9][0-9]{0,4})|series/tmdb/` + pbID + `/e` + pbID + `|series/tvdb/` + pbID + `/a(0|[1-9][0-9]{0,4})` +
		`|(` + pbFilm + `|(` + pbSeries + `)(` + pbSeason + `)?)/x/` + pbOpaque +
		`|track/(mb/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|local/` + pbOpaque + `))$`)
)

func pbPlayableKey(key string) bool { return pbPlayable.MatchString(key) }

// pbStatus is an error's status and problem type, 200 for none.
func pbStatus(t *testing.T, err error) (int, string) {
	if err == nil {
		return 200, ""
	}
	var r *PlayRefusal
	if !errors.As(err, &r) {
		t.Fatalf("not a refusal: %v", err)
	}
	return r.Status, r.Problem
}

// pbSame compares two JSON texts, members whose value is a zero (false,
// 0, "") left out of both: the Go values here don't tell a zero from a
// member left out.
func pbSame(t *testing.T, name string, got any, want string) {
	t.Helper()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	g, w := pbTree(t, b), pbTree(t, []byte(want))
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", name, b, want)
	}
}

func pbTree(t *testing.T, b []byte) any {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var drop func(any) any
	drop = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if e == false || e == float64(0) || e == "" {
					delete(x, k)
					continue
				}
				x[k] = drop(e)
			}
		case []any:
			for i, e := range x {
				x[i] = drop(e)
			}
		}
		return v
	}
	return drop(v)
}

func TestPlaybackVectors(t *testing.T) {
	var v pbVectors
	readVectors(t, "playback.json", &v)
	if len(v.Requests)+len(v.Delivers)+len(v.Downs)+len(v.Scans) != 131+58+36+16 || len(v.Reports) != 49 ||
		len(v.Answers) != 59 || len(v.Restarts) != 16 || len(v.Limits) != 17 {
		t.Fatal("not every case read")
	}
	now := time.Unix(v.Context.Now, 0)
	for _, list := range [][]pbCase{v.Requests, v.Delivers, v.Downs, v.Scans, v.Reports} {
		for _, c := range list {
			switch {
			case c.By == "viewer":
				pbSame(t, c.Name, pbCoarsen(t, c.Text), c.Output)
			case c.Call == "play":
				out, err := pbPlay(v.Context, c, []byte(c.Text))
				pbCheck(t, c, out, err)
			default:
				out, err := pbReport(c, now)
				pbCheck(t, c, out, err)
			}
		}
	}
}

func pbCheck(t *testing.T, c pbCase, out any, err error) {
	t.Helper()
	status, problem := pbStatus(t, err)
	if status != c.Status || problem != c.Type {
		t.Errorf("%s: %d %s (%v), want %d %s", c.Name, status, problem, err, c.Status, c.Type)
		return
	}
	if err == nil {
		pbSame(t, c.Name, out, c.Output)
	}
}

// pbPlay is an owning server's play, section 2.2's steps, of the
// context's copies and limits.
func pbPlay(ctx pbContext, c pbCase, body []byte) (any, error) {
	lim, use := ctx.Limits, ctx.InUse
	if c.Limits != nil {
		lim = *c.Limits
	}
	if c.InUse != nil {
		use = *c.InUse
	}
	u := LimitUse(use)
	// 1.
	r, err := ReadPlay(body, pbPlayableKey)
	if err != nil {
		return nil, err
	}
	// 2.
	if err := lim.Open(r.Purpose); err != nil {
		return nil, err
	}
	// 3, 4.
	var copies []PlayCopy
	for _, c := range ctx.Copies[r.Title] {
		copies = append(copies, c.play())
	}
	i := ChooseCopy(copies, r.VersionID, r.AudioLanguage)
	if i < 0 {
		return nil, &PlayRefusal{404, ProblemNotFound, "no copy"}
	}
	if !r.TrackIDsOK() {
		return nil, badPlay("a malformed track ID")
	}
	cp := copies[i]
	audio := ChooseAudio(cp.Audio, r.AudioTrack, r.AudioLanguage)
	// 5.
	if err := lim.Room(r.Purpose, u); err != nil {
		return nil, err
	}
	// 6.
	capKbps := 0
	if r.Purpose == PurposePlay {
		capKbps = r.BitrateCap(ctx.OwnCapKbps, lim.BitrateKbps)
	}
	// 7.
	if err := RefuseDelivery(r, cp, capKbps); err != nil {
		return nil, err
	}
	// 8.
	transcodes := ""
	if r.Delivery == DeliveryTranscode {
		if err := lim.TranscodeRoom(r.Purpose, u); err != nil {
			return nil, err
		}
		transcodes = "pairing and server"
		if r.Purpose == PurposeDownload {
			transcodes = "server"
		}
	}
	out := struct {
		PlayRequest
		Copy     string `json:"versionId"`
		Audio    string `json:"audioTrack"`
		Start    int64  `json:"startPositionMs"`
		Cap      int    `json:"bitrateCapKbps,omitempty"`
		Speed    int    `json:"speedCapKbps,omitempty"`
		Counts   string `json:"counts"`
		Codes    string `json:"transcodes,omitempty"`
		Ticket   int    `json:"ticketSeconds,omitempty"`
		Idle     int    `json:"idleSeconds,omitempty"`
		ReadCap  int    `json:"readCapBytes,omitempty"`
		NoTitle  string `json:"title"`
		NoSubLan string `json:"subtitleLanguage,omitempty"`
	}{PlayRequest: r, Copy: cp.ID, Audio: audio, Start: r.StartPositionMs, Cap: capKbps, Codes: transcodes,
		Ticket: int(Life(r.Purpose).Seconds()), NoTitle: r.Title}
	// The vectors' output holds no request member it doesn't list.
	out.PlayRequest.AudioLanguage, out.PlayRequest.SubtitleTrack, out.PlayRequest.MaxBitrateKbps = "", "", 0
	if Idles(r.Purpose) {
		out.Idle = int(IdleEnd.Seconds())
	}
	switch r.Purpose {
	case PurposePlay:
		out.Counts = "streams"
	case PurposeDownload:
		out.Counts, out.Speed, out.Start = "downloads", lim.DownloadKbps, 0
	case PurposeScan:
		out.Counts, out.ReadCap = "scansPerDay", ScanReadCap
	}
	return out, nil
}

// pbCoarsen is what a viewer's server sends of a device's description:
// {title, capabilities, maxBitrateKbps}, read leniently (it is its own).
func pbCoarsen(t *testing.T, text string) any {
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	var in map[string]any
	if err := d.Decode(&in); err != nil {
		t.Fatal(err)
	}
	m, _ := in["capabilities"].(map[string]any)
	strs := func(name string) []string {
		var out []string
		list, _ := m[name].([]any)
		for _, e := range list {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	num := func(v any) int { n, _ := integerOf(v); return int(n) }
	c := Capabilities{Containers: strs("containers"), VideoCodecs: strs("videoCodecs"), AudioCodecs: strs("audioCodecs"),
		AudioProfiles: strs("audioProfiles"), HDR: strs("hdr"), MaxHeight: num(m["maxHeight"]),
		MaxAudioChannels: num(m["maxAudioChannels"]), MaxAudioSampleRate: num(m["maxAudioSampleRate"]),
		MaxAudioBitDepth: num(m["maxAudioBitDepth"]), MaxBitrateKbps: num(m["maxBitrateKbps"])}
	list, _ := m["dvProfiles"].([]any)
	for _, e := range list {
		if n, ok := integerOf(e); ok {
			c.DVProfiles = append(c.DVProfiles, int(n))
		}
	}
	out := map[string]any{"title": in["title"]}
	if cc := c.Coarsen(); !cc.Empty() {
		out["capabilities"] = cc
	}
	if v, ok := in["maxBitrateKbps"]; ok {
		out["maxBitrateKbps"] = CoarsenBitrate(num(v))
	}
	return out
}

// pbReport is an owning server's progress or stop.
func pbReport(c pbCase, now time.Time) (any, error) {
	r, err := ReadReport(c.Call, []byte(c.Text), now)
	if err != nil {
		return nil, err
	}
	s := PlaybackState{State: c.Playback.State, Purpose: c.Playback.Purpose, Title: c.Playback.Title, Queue: c.Playback.Queue}
	if c.Playback.PlayedAt != 0 {
		s.PlayedAt = time.Unix(c.Playback.PlayedAt, 0)
	}
	if c.Playback.LastAt != 0 {
		s.LastAt = time.Unix(c.Playback.LastAt, 0)
	}
	o, err := s.Apply(c.Call, r, now)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"effect": o.Effect, "state": o.State}
	if o.Effect == EffectRecorded || o.Effect == EffectRecordedAndEnded {
		out["title"], out["at"] = o.Title, o.At.Unix()
		if o.HasPosition {
			out["positionMs"] = o.PositionMs
		}
	}
	return out, nil
}

func TestPlaybackAnswerVectors(t *testing.T) {
	var v pbVectors
	readVectors(t, "playback.json", &v)
	for _, c := range v.Answers {
		a, err := ReadPlayAnswer([]byte(c.Text), c.Delivery, v.Context.StreamRoot, c.CatalogEtag)
		switch {
		case !c.Valid && err == nil:
			t.Errorf("%s: kept %+v", c.Name, a)
		case c.Valid && err != nil:
			t.Errorf("%s: refused", c.Name)
		case c.Valid:
			pbSame(t, c.Name, a, c.Output)
		}
	}
}

func TestPlaybackRestartVectors(t *testing.T) {
	var v pbVectors
	readVectors(t, "playback.json", &v)
	for _, c := range v.Restarts {
		var req PlayRequest
		if err := json.Unmarshal([]byte(c.Request), &req); err != nil {
			t.Fatal(err)
		}
		o := Restart(c.Reading, req, c.Status, []byte(c.Answer), v.Context.StreamRoot)
		if o.Join != c.Valid {
			t.Errorf("%s: join %v", c.Name, o.Join)
		}
		pbSame(t, c.Name, o, c.Output)
	}
}

func TestLimitsVectors(t *testing.T) {
	var v pbVectors
	readVectors(t, "playback.json", &v)
	for _, c := range v.Limits {
		k, ok := ReadLimits([]byte(c.Text))
		if ok != c.Valid {
			t.Errorf("%s: %v", c.Name, ok)
			continue
		}
		if !ok {
			continue
		}
		out := map[string]any{"downloads": k.Downloads, "downloadsPerDay": k.DownloadsPerDay, "downloadKbps": k.DownloadKbps,
			"scansPerDay": k.ScansPerDay, "offersDownloads": k.OffersDownloads(), "offersScans": k.OffersScans()}
		for _, m := range []struct {
			name string
			has  bool
			v    int
		}{{"streams", k.HasStreams, k.Streams}, {"transcodes", k.HasTranscodes, k.Transcodes}, {"bitrateKbps", k.HasBitrate, k.BitrateKbps},
			{"audioStreams", k.HasAudioStreams, k.AudioStreams}, {"audioTranscodes", k.HasAudioTranscodes, k.AudioTranscodes}} {
			if m.has {
				out[m.name] = m.v
			}
		}
		b, _ := json.Marshal(out)
		var got, want any
		_ = json.Unmarshal(b, &got)
		_ = json.Unmarshal([]byte(c.Output), &want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %s\nwant %s", c.Name, b, c.Output)
		}
	}
}

// A request a viewer's server builds is one ReadPlay reads back as it is.
func TestPlayRequestRoundTrip(t *testing.T) {
	caps := Capabilities{Containers: []string{"MP4", "ts", "dash"}, HDR: []string{"dv"}, DVProfiles: []int{8, 5},
		MaxHeight: 1600, MaxAudioChannels: 7, MaxBitrateKbps: 25999}.Coarsen()
	in := PlayRequest{Title: "movie/tmdb/949", Delivery: DeliveryTranscode, Capabilities: &caps,
		Transcode: &TranscodeTargets{MaxHeight: 1080, BurnSubtitle: "s4"}, MaxBitrateKbps: CoarsenBitrate(25999), AudioTrack: "a2"}
	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("null")) {
		t.Fatal(string(b))
	}
	got, err := ReadPlay(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	in.Purpose = PurposePlay
	if !reflect.DeepEqual(got, in) || !got.TrackIDsOK() {
		t.Errorf("%+v\n%+v", got, in)
	}
	if got.BitrateCap(0, 0) != 25000 {
		t.Error(got.BitrateCap(0, 0))
	}
}

func TestLimitsCheck(t *testing.T) {
	ok := Limits{Streams: 2, Transcodes: 1, Downloads: 1, DownloadsPerDay: 10, DownloadKbps: 20_000, ScansPerDay: 1000}
	if ok.Check() != nil {
		t.Fatal("refused")
	}
	for _, bad := range []func(*Limits){
		func(l *Limits) { l.Transcodes = 3 },
		func(l *Limits) { l.Streams = 51 },
		func(l *Limits) { l.ScansPerDay = 1_000_001 },
		func(l *Limits) { l.DownloadKbps = -1 },
		func(l *Limits) { l.AudioTranscodes = 1 },
	} {
		l := ok
		bad(&l)
		if l.Check() == nil {
			t.Errorf("%+v accepted", l)
		}
	}
}

func TestStepDown(t *testing.T) {
	for _, c := range []struct{ v, want int }{{0, 0}, {-3, 0}, {1, 16}, {16, 16}, {23, 16}, {24, 24}, {64, 32}} {
		if got := StepDown(c.v, BitDepthSteps); got != c.want {
			t.Errorf("%d: %d", c.v, got)
		}
	}
}
