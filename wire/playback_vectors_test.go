package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"
)

// The playback vectors (vectors/v0/playback.json): every array, run
// against this package's rules.

type pbVectors struct {
	Context  pbContext       `json:"context"`
	Requests []pbCase        `json:"requests"`
	Delivers []pbCase        `json:"deliveries"`
	Baseline []pbCase        `json:"baseline"`
	Downs    []pbCase        `json:"downloads"`
	Scans    []pbCase        `json:"scans"`
	Reports  []pbCase        `json:"reports"`
	Queue    []pbQueueCase   `json:"queueEntries"`
	Answers  []pbAnswerCase  `json:"answers"`
	Restarts []pbRestartCase `json:"restarts"`
	Limits   []pbLimitsCase  `json:"limits"`
}

type pbContext struct {
	Copies        map[string][]pbCopy `json:"copies"`
	VideoEncoders []string            `json:"videoEncoders"`
	AudioEncoders []string            `json:"audioEncoders"`
	Ranges        []string            `json:"ranges"`
	SegmentCodecs map[string][]string `json:"segmentCodecs"`
	OwnCapKbps    int                 `json:"ownCapKbps"`
	ScanBytes     int64               `json:"scanBytes"`
	Limits        Limits              `json:"limits"`
	InUse         pbUse               `json:"inUse"`
	StreamRoot    string              `json:"streamRoot"`
	Now           int64               `json:"now"`
}

type pbCopy struct {
	ID         string       `json:"id"`
	Parts      int          `json:"parts"`
	Etag       string       `json:"etag"`
	Size       int64        `json:"size"`
	DurationMs int64        `json:"durationMs"`
	Video      *VideoStream `json:"video"`
	Audio      []CopyAudio  `json:"audio"`
	Subtitles  []string     `json:"subtitles"`
}

// copy is the context's copy as the catalog gives it.
func (c pbCopy) copy() Copy {
	p := Copy{ID: c.ID, Parts: c.Parts, ETag: c.Etag, Size: c.Size, DurationMs: c.DurationMs, Video: c.Video, AudioTracks: c.Audio}
	if p.Parts == 1 {
		p.Parts = 0
	}
	for _, s := range c.Subtitles {
		p.SubtitleTracks = append(p.SubtitleTracks, CopySubtitle{ID: s})
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
	Name      string   `json:"name"`
	Call      string   `json:"call"`
	Text      string   `json:"text"`
	Valid     bool     `json:"valid"`
	Status    int      `json:"status"`
	Type      string   `json:"type"`
	Output    string   `json:"output"`
	Limits    *Limits  `json:"limits"`
	InUse     *pbUse   `json:"inUse"`
	Ranges    []string `json:"ranges"`
	ScanBytes *int64   `json:"scanBytes"`
	Playback  *pbState `json:"playback"`
}

type pbQueueCase struct {
	Name    string `json:"name"`
	Request string `json:"request"`
	Entry   string `json:"entry"`
	Valid   bool   `json:"valid"`
	Status  int    `json:"status"`
	Type    string `json:"type"`
	Output  string `json:"output"`
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
	Name  string `json:"name"`
	Text  string `json:"text"`
	Asked struct {
		Purpose          string   `json:"purpose"`
		Delivery         string   `json:"delivery"`
		Part             string   `json:"part"`
		CatalogEtag      string   `json:"catalogEtag"`
		CatalogSubtitles []string `json:"catalogSubtitles"`
	} `json:"asked"`
	Valid  bool   `json:"valid"`
	Output string `json:"output"`
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
	if len(v.Requests) != 95 || len(v.Delivers) != 107 || len(v.Baseline) != 21 || len(v.Downs) != 36 || len(v.Scans) != 19 ||
		len(v.Reports) != 57 || len(v.Queue) != 21 || len(v.Answers) != 79 || len(v.Restarts) != 17 || len(v.Limits) != 21 {
		t.Fatal("not every case read")
	}
	now := time.Unix(v.Context.Now, 0)
	for _, list := range [][]pbCase{v.Requests, v.Delivers, v.Baseline, v.Downs, v.Scans, v.Reports} {
		for _, c := range list {
			switch {
			case c.Call == "play":
				out, err := pbPlay(v.Context, c, []byte(c.Text), nil)
				pbCheck(t, c, out, err)
			default:
				out, err := pbReport(c, now)
				pbCheck(t, c, out, err)
			}
		}
	}
	for _, q := range v.Queue {
		c := pbCase{Name: q.Name, Valid: q.Valid, Status: q.Status, Type: q.Type, Output: q.Output}
		var req struct {
			Title string          `json:"title"`
			Queue json.RawMessage `json:"queue"`
		}
		_ = json.Unmarshal([]byte(q.Request), &req)
		r, err := ReadPlay([]byte(q.Request), pbPlayableKey)
		if err != nil {
			t.Fatalf("%s: the request: %v", q.Name, err)
		}
		var ask QueueAsk
		asked, err := CheckQueue(req.Title, json.RawMessage("["+q.Entry+"]"))
		if err == nil {
			ask = asked[0]
		}
		var out any
		if err == nil {
			var entry PlayRequest
			if entry, err = ask.Request(r); err == nil {
				out, err = pbPlay(v.Context, c, nil, &entry)
			}
		}
		if m, ok := out.(map[string]any); ok {
			delete(m, "counts")
			delete(m, "idleSeconds")
		}
		pbCheck(t, c, out, err)
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
// context's copies and limits: of body, or of a queue entry's request.
func pbPlay(ctx pbContext, c pbCase, body []byte, entry *PlayRequest) (any, error) {
	lim, use := ctx.Limits, ctx.InUse
	if c.Limits != nil {
		lim = *c.Limits
	}
	if c.InUse != nil {
		use = *c.InUse
	}
	ranges, scanBytes := ctx.Ranges, ctx.ScanBytes
	if c.Ranges != nil {
		ranges = c.Ranges
	}
	if c.ScanBytes != nil {
		scanBytes = *c.ScanBytes
	}
	u := LimitUse(use)
	// 1.
	var r PlayRequest
	if entry != nil {
		r = *entry
	} else {
		var err error
		if r, err = ReadPlay(body, pbPlayableKey); err != nil {
			return nil, err
		}
	}
	// 2.
	if err := lim.Open(r.Purpose); err != nil {
		return nil, err
	}
	// 3, 4.
	var copies []Copy
	for _, c := range ctx.Copies[r.Title] {
		copies = append(copies, c.copy())
	}
	i := PickCopy(copies, r.VersionID)
	if i < 0 {
		return nil, &PlayRefusal{404, ProblemNotFound, "no copy"}
	}
	if !r.TrackIDsOK() {
		return nil, badPlay("a malformed track ID")
	}
	cp := copies[i]
	audio := DefaultAudio(cp.AudioTracks, r.AudioTrack)
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
	enc := Encoders{Video: ctx.VideoEncoders, Audio: ctx.AudioEncoders, Ranges: ranges, Segments: ctx.SegmentCodecs}
	served, err := Serve(r, cp, audio, capKbps, enc)
	if err != nil {
		return nil, err
	}
	// 8.
	transcodes := ""
	if served.Transcodes {
		if err := lim.TranscodeRoom(r.Purpose, u); err != nil {
			return nil, err
		}
		transcodes = "pairing and server"
		if r.Purpose == PurposeDownload {
			transcodes = "server"
		}
	}
	out := map[string]any{"title": r.Title, "purpose": r.Purpose, "versionId": cp.ID, "audioTrack": audio,
		"startPositionMs": r.StartPositionMs, "transcodes": transcodes}
	set := func(k string, v any, ok bool) {
		if ok {
			out[k] = v
		}
	}
	set("delivery", r.Delivery, r.Delivery != "")
	set("mediaType", r.MediaType, r.MediaType != "")
	set("transcode", served.Transcode, served.Transcode != nil)
	set("audioRenditions", true, r.AudioRenditions)
	if r.Delivery == "" {
		b := map[string]any{"audio": served.Audio, "method": served.Method}
		set := func(k, v string) {
			if v != "" {
				b[k] = v
			}
		}
		set("video", served.Video)
		out["baseline"] = b
	}
	set("bitrateCapKbps", capKbps, capKbps > 0)
	set("idleSeconds", int(IdleEnd.Seconds()), Idles(r.Purpose))
	set("ticketSeconds", int(Life(r.Purpose).Seconds()), Life(r.Purpose) > 0)
	switch r.Purpose {
	case PurposePlay:
		out["counts"] = "streams"
	case PurposeDownload:
		out["counts"], out["speedCapKbps"], out["startPositionMs"] = "downloads", lim.DownloadKbps, 0
	case PurposeScan:
		out["counts"], out["scanBytes"] = "scansPerDay", scanBytes
	}
	return out, nil
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
		ask := Asked{Purpose: c.Asked.Purpose, Delivery: c.Asked.Delivery, Part: c.Asked.Part, CatalogEtag: c.Asked.CatalogEtag}
		for _, s := range c.Asked.CatalogSubtitles {
			ask.Subtitles = append(ask.Subtitles, CopySubtitle{ID: s})
		}
		a, err := ReadPlayAnswer([]byte(c.Text), ask)
		switch {
		case !c.Valid && err == nil:
			t.Errorf("%s: kept %+v", c.Name, a)
		case c.Valid && err != nil:
			t.Errorf("%s: refused", c.Name)
		case c.Valid:
			out := struct {
				PlayAnswer
				URLs []string `json:"urls"`
			}{a, a.URLs(v.Context.StreamRoot, 0)}
			pbSame(t, c.Name, out, c.Output)
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
		o := Restart(c.Reading, req, c.Status, []byte(c.Answer))
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
	in := PlayRequest{Title: "movie/tmdb/949", Delivery: DeliveryTranscode,
		Transcode: &TranscodeTargets{VideoCodec: CodecCopy, AudioCodec: "opus", BurnSubtitle: "s4", Range: RangeKeep}, MaxBitrateKbps: 25999, AudioTrack: "a2"}
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
	if got.BitrateCap(0, 0) != 25999 || (PlayRequest{}).BitrateCap(0, 0) != 0 {
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
		func(l *Limits) { l.Streams = 1001 },
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
	if ScanCap(0) != MinScanBytes || ScanCap(MaxScanBytes+1) != MinScanBytes || ScanCap(32<<20) != 32<<20 {
		t.Error("ScanCap")
	}
}
