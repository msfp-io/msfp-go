package wire

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Video titles (MSFP video, sections 3 and 4): their keys, of the tmdb,
// tvdb and local schemes and of extras, and what a server keeps of a
// title a peer lists in titles (catalog section 3.3, video section 3.1).
// Copies are in copies.go, the previews call in video_previews.go.

// The schemes of video keys (video section 4).
const (
	SchemeTMDB  = "tmdb"
	SchemeTVDB  = "tvdb"
	SchemeLocal = "local"
)

// VideoKey is a video title key, parsed (video section 4): movie/tmdb/949,
// series/tmdb/1399, series/tmdb/1399/s1, series/tmdb/1399/e63056 (an
// episode by TMDB's ID of it), series/tmdb/1399/s1/e2 (an episode by its
// numbers), series/tvdb/81189/a27 (by its absolute number),
// movie/local/q3Zp0Lw8, movie/tmdb/348/x/Tr7aQ1 (an extra).
type VideoKey struct {
	Kind   string // movie, series, season, episode or extra
	Scheme string // SchemeTMDB, SchemeTVDB or SchemeLocal
	// ID is the film's or the series' TMDB or TheTVDB ID; Local its
	// opaque identifier under the local scheme.
	ID    int64
	Local string
	// Season and Episode: a season's number, an episode's numbers when
	// it is named by them; with Absolute, Episode is TheTVDB's absolute
	// number.
	Season, Episode int
	Absolute        bool
	// EpisodeID is TMDB's ID of an episode named by it; 0 for one named
	// by its numbers.
	EpisodeID int64
	// Extra is an extra's opaque part, after x/; the other fields are
	// then those of the title it belongs to, of kind ParentKind (movie,
	// series or season).
	Extra      string
	ParentKind string
}

// ParseVideoKey reads a video title key in its one canonical form (video
// section 4): the kind, the scheme (tmdb, tvdb for series only, local),
// the ID (1 to 9,999,999,999) or the local identifier (OpaqueKeyPart),
// then for a season s<season> (0, or 1 to 4 digits), for an episode
// e<episode id> (tmdb only, 1 to 10 digits), s<season>/e<episode>
// (episode 0, or 1 to 5 digits) or a<number> (tvdb only, as episodes);
// never a leading zero. An extra is the key of a film, a series or a
// season, then x/<opaque>. Any other key is false: another form of the
// same title is another string, which a receiver skips.
func ParseVideoKey(key string) (VideoKey, bool) {
	parts := strings.Split(key, "/")
	if n := len(parts); n >= 5 && parts[n-2] == "x" {
		k, ok := ParseVideoKey(strings.Join(parts[:n-2], "/"))
		if !ok || !OpaqueKeyPart(parts[n-1]) || k.Kind != "movie" && k.Kind != "series" && k.Kind != "season" {
			return VideoKey{}, false
		}
		k.ParentKind, k.Kind, k.Extra = k.Kind, "extra", parts[n-1]
		return k, true
	}
	if len(parts) < 3 || len(parts) > 5 {
		return VideoKey{}, false
	}
	k := VideoKey{Kind: parts[0], Scheme: parts[1]}
	var ok bool
	switch k.Scheme {
	case SchemeTMDB, SchemeTVDB:
		k.ID, ok = decimal(parts[2], 10, false)
	case SchemeLocal:
		k.Local, ok = parts[2], OpaqueKeyPart(parts[2])
	}
	switch {
	case !ok:
		return VideoKey{}, false
	case k.Kind == "movie" && len(parts) == 3 && k.Scheme != SchemeTVDB, k.Kind == "series" && len(parts) == 3:
		return k, true
	case k.Kind != "series":
		return VideoKey{}, false
	}
	if len(parts) == 4 {
		if digits, ok := strings.CutPrefix(parts[3], "e"); ok && k.Scheme == SchemeTMDB {
			k.Kind = "episode"
			k.EpisodeID, ok = decimal(digits, 10, false)
			return k, ok
		}
		if digits, ok := strings.CutPrefix(parts[3], "a"); ok && k.Scheme == SchemeTVDB {
			n, ok := decimal(digits, 5, true)
			k.Kind, k.Episode, k.Absolute = "episode", int(n), true
			return k, ok
		}
	}
	digits, ok := strings.CutPrefix(parts[3], "s")
	season, okSeason := decimal(digits, 4, true)
	if !ok || !okSeason {
		return VideoKey{}, false
	}
	k.Kind, k.Season = "season", int(season)
	if len(parts) == 4 {
		return k, true
	}
	digits, ok = strings.CutPrefix(parts[4], "e")
	episode, okEpisode := decimal(digits, 5, true)
	if !ok || !okEpisode {
		return VideoKey{}, false
	}
	k.Kind, k.Episode = "episode", int(episode)
	return k, true
}

// decimal reads 1 to most digits without a leading zero; "0" itself only
// when zero is allowed.
func decimal(s string, most int, zero bool) (int64, bool) {
	if s == "" || len(s) > most || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	if s[0] == '0' && (len(s) > 1 || !zero) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// String is the key, as ParseVideoKey reads it.
func (k VideoKey) String() string {
	if k.Kind == "extra" {
		parent, _ := k.Parent()
		return parent.String() + "/x/" + k.Extra
	}
	base := k.Series()
	switch {
	case k.Kind == "movie" || k.Kind == "series":
		return base
	case k.Kind == "season":
		return base + "/s" + strconv.Itoa(k.Season)
	case k.EpisodeID > 0:
		return base + "/e" + strconv.FormatInt(k.EpisodeID, 10)
	case k.Absolute:
		return base + "/a" + strconv.Itoa(k.Episode)
	}
	return base + "/s" + strconv.Itoa(k.Season) + "/e" + strconv.Itoa(k.Episode)
}

// Series is the key of the film or the series k is, or is under: its
// first three parts (movie/tmdb/949, series/tvdb/81189).
func (k VideoKey) Series() string {
	kind := "series"
	if k.Kind == "movie" || k.Kind == "extra" && k.ParentKind == "movie" {
		kind = "movie"
	}
	id := k.Local
	if k.Scheme != SchemeLocal {
		id = strconv.FormatInt(k.ID, 10)
	}
	return kind + "/" + k.Scheme + "/" + id
}

// Parent is, for an extra, the title it belongs to (video section 4.6);
// false for any other title.
func (k VideoKey) Parent() (VideoKey, bool) {
	if k.Kind != "extra" {
		return VideoKey{}, false
	}
	k.Kind, k.ParentKind, k.Extra = k.ParentKind, "", ""
	return k, true
}

// Playable says whether play takes the title, and whether it carries
// copies: a film, an episode or an extra (video section 5).
func (k VideoKey) Playable() bool {
	return k.Kind == "movie" || k.Kind == "episode" || k.Kind == "extra"
}

// MaxTags is how many language tags a list of them holds at most
// (SentTags).
const MaxTags = 12

func containsFold(list []string, s string) bool {
	for _, o := range list {
		if strings.EqualFold(o, s) {
			return true
		}
	}
	return false
}

// MaxWith is how many other episodes' keys an episode's with lists at
// most (video section 4.2).
const MaxWith = 16

// CleanWith keeps of an episode's with the keys of other episodes of the
// same series (the same scheme and ID), each once, at most MaxWith; a bad
// key is skipped alone.
func CleanWith(episode VideoKey, keys []string) []string {
	var out []string
	self := episode.String()
	for _, s := range keys {
		if len(out) == MaxWith {
			break
		}
		k, ok := ParseVideoKey(s)
		if !ok || k.Kind != "episode" || k.Series() != episode.Series() || s == self || slices.Contains(out, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Other identifiers of a film, a series or an episode (video section
// 4.3), by scheme: IMDb's and TheTVDB's.
var otherIDs = map[string]*regexp.Regexp{
	"imdb": regexp.MustCompile(`^tt[0-9]{7,10}$`),
	"tvdb": regexp.MustCompile(`^[1-9][0-9]{0,9}$`),
}

// OtherID says whether value is an identifier of a known scheme, in its
// form: what a sender sends, and a receiver keeps.
func OtherID(scheme, value string) bool {
	re, known := otherIDs[scheme]
	return known && re.MatchString(value)
}

// The heights a picture is stepped to (video section 5.1's coarsening of
// maxHeight); a copy's HDR formats are HDRFormats (names.go).
var Heights = []int{480, 720, 1080, 1440, 2160, 4320}

// StepHeight steps a height down to the greatest of Heights not above
// it, 480 below 480 (video section 5.1's coarsening); 0 stays 0, unknown.
func StepHeight(h int) int {
	if h <= 0 {
		return 0
	}
	out := Heights[0]
	for _, s := range Heights {
		if s <= h {
			out = s
		}
	}
	return out
}

// CleanHDR keeps of formats those HDRFormats lists, each once, in its
// order (video section 3.3); none for standard range.
func CleanHDR(formats []string) []string {
	var out []string
	for _, f := range HDRFormats {
		if slices.Contains(formats, f) {
			out = append(out, f)
		}
	}
	return out
}

// Place is an episode's place in TMDB's numbering (video section 4.1):
// TMDB's current season and episode numbers, sent with each episode keyed
// by its TMDB episode ID.
type Place struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

// The greatest numbers a place may carry (video section 3.1); both may
// be 0 (season 0 holds specials, episode 0 a pilot or prologue).
const (
	MaxPlaceSeason  = 9999
	MaxPlaceEpisode = 99999
)

// Valid says whether p's numbers are in range.
func (p Place) Valid() bool {
	return p.Season >= 0 && p.Season <= MaxPlaceSeason && p.Episode >= 0 && p.Episode <= MaxPlaceEpisode
}

// readPlace reads place as a receiver keeps it (video section 3.1): nil
// unless it is an object whose season and episode are MSFP integers in
// range (core section 2.3: 1.0 and "1" aren't); other members are
// ignored.
func readPlace(v any) *Place {
	o, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	season, ok1 := videoInt(o["season"], 0, MaxPlaceSeason)
	episode, ok2 := videoInt(o["episode"], 0, MaxPlaceEpisode)
	if !ok1 || !ok2 {
		return nil
	}
	return &Place{Season: int(season), Episode: int(episode)}
}

// The length of an extra's name, in code points (video section 3.9).
const MaxExtraName = 64

// VideoTitle is a peer's video title, as kept (video section 3.1): its
// key, its kind, the library it gives, and the members video adds,
// cleaned. Copies are set for films, episodes and extras, and always for
// them; Place for episodes keyed by their TMDB episode ID, and always
// for them; ExtraType for extras, and always for them, with Name, an
// extra's own name, when it has one; Description for local films and
// series, and always for them.
type VideoTitle struct {
	Key         string
	Kind        string
	Library     string
	Copies      []Copy
	Place       *Place
	With        []string
	IDs         map[string]string
	ExtraType   string
	Name        string
	Description *Description
}

// ReadVideoTitle reads one entry of a titles answer as catalog section
// 3.3 and video sections 3.1, 3.8, 3.9 and 3.10 say: false, to skip it,
// when it isn't an object whose key, kind and library are strings, when
// the key isn't a canonical video key of a known scheme, when its kind
// isn't the key's, when it is a film, an episode or an extra whose
// copies isn't an array or keeps no copy (ReadCopy), an episode keyed
// by its TMDB episode ID without a valid place, or a local film or
// series without a name. A member of the wrong type otherwise counts as
// absent; a bad entry of one of its lists is skipped alone; members a
// title may not carry are ignored. Whether library is one asked for is
// the caller's to check.
func ReadVideoTitle(raw json.RawMessage) (VideoTitle, bool) {
	t, err := readVideoTitle(raw)
	return t, err == nil
}

// readVideoTitle is ReadVideoTitle, with why it skips a title.
func readVideoTitle(raw json.RawMessage) (VideoTitle, error) {
	tree, err := strictTree(raw, true)
	o, ok := tree.(map[string]any)
	if err != nil || !ok {
		return VideoTitle{}, errors.New("not an object")
	}
	key, ok1 := o["key"].(string)
	kind, ok2 := o["kind"].(string)
	library, ok3 := o["library"].(string)
	if !ok1 || !ok2 || !ok3 {
		return VideoTitle{}, errors.New("key, kind or library isn't a string")
	}
	k, ok := ParseVideoKey(key)
	if !ok {
		return VideoTitle{}, errors.New("not a canonical video key")
	}
	if kind != k.Kind {
		return VideoTitle{}, errors.New("a kind other than the key's")
	}
	t := VideoTitle{Key: key, Kind: kind, Library: library}
	// Extras (section 3.9).
	if k.Kind == "extra" {
		t.ExtraType = "clip"
		if s, ok := o["extraType"].(string); ok && slices.Contains(ExtraTypes, s) {
			t.ExtraType = s
		}
		if s, ok := o["name"].(string); ok {
			t.Name = CleanText(s, MaxExtraName)
		}
	}
	// The description of a local film or series (section 3.10).
	if k.Scheme == SchemeLocal && (k.Kind == "movie" || k.Kind == "series") {
		d, ok := ReadDescription(raw, LocalDescription)
		if !ok {
			return VideoTitle{}, errors.New("a local film or series without a name")
		}
		t.Description = &d
	}
	// An episode keyed by its TMDB episode ID can't be placed from its
	// key: without a place, it is skipped (section 4.1).
	if k.EpisodeID > 0 {
		if t.Place = readPlace(o["place"]); t.Place == nil {
			return VideoTitle{}, errors.New("an episode keyed by its TMDB ID without a valid place")
		}
	}
	if k.Kind == "episode" {
		t.With = CleanWith(k, videoStrings(o["with"]))
	}
	// ids (section 4.3): films, series and episodes of a public database.
	if ids, ok := o["ids"].(map[string]any); ok && k.Scheme != SchemeLocal && k.Kind != "season" && k.Kind != "extra" {
		for scheme, v := range ids {
			if value, ok := v.(string); ok && OtherID(scheme, value) {
				if t.IDs == nil {
					t.IDs = map[string]string{}
				}
				t.IDs[scheme] = value
			}
		}
	}
	// Copies (sections 3.2 and 3.8).
	if k.Playable() {
		list, ok := o["copies"].([]any)
		if _, present := o["copies"]; present && !ok {
			return VideoTitle{}, errors.New("copies isn't an array")
		}
		if t.Copies = readCopies(list); len(t.Copies) == 0 {
			return VideoTitle{}, errors.New("no copy left")
		}
	}
	return t, nil
}

// stringsOf reads a list's strings: none when it isn't a list, an entry
// that isn't a string skipped.
func stringsOf(raw json.RawMessage) []string {
	var list []any
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	return videoStrings(list)
}

// videoStrings is stringsOf of a value already read (strictTree).
func videoStrings(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// videoInt reads an MSFP integer (core section 2.3) from lo to hi, of a
// value read by strictTree; false for any other value.
func videoInt(v any, lo, hi int64) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok || !isInteger(string(n)) {
		return 0, false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return i, err == nil && i >= lo && i <= hi
}
