package describe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Prober runs ffprobe on local files, as MSFP's facts need it and no
// more: the file's real path resolved, a regular file opened read-only
// and checked, its media type sniffed from its first bytes (a type
// Sniff doesn't give is refused, unprobed), then ffprobe in a child
// process with the sniffed demuxer (-f), the file protocol only, bounded
// probe size and analysis, a time limit and an output cap, its JSON
// parsed strictly:
//
//  1. -show_format -show_streams -show_chapters, without data;
//  2. -show_streams -show_data on the video streams (V: attached
//     pictures left out), and on the audio streams when one is AAC: the
//     codec records. Attachments (fonts) and cover art are never read.
//
// With Frames, a third run reads the first frames of an HDR picture
// (NeedsFrames) for HDR10+ and the alternative transfer
// characteristics SEI. The zero Prober is ready: ffprobe from PATH and
// the defaults below.
type Prober struct {
	// FFprobe is the ffprobe program; "" is "ffprobe", looked up in PATH.
	FFprobe string
	// Timeout bounds each run; 0 is DefaultTimeout.
	Timeout time.Duration
	// MaxOutput bounds each run's output, in bytes; 0 is
	// DefaultMaxOutput. A run past it is stopped and fails.
	MaxOutput int
	// ProbeSize and AnalyzeDuration are ffprobe's -probesize (bytes) and
	// -analyzeduration; 0 is DefaultProbeSize, DefaultAnalyzeDuration.
	ProbeSize       int64
	AnalyzeDuration time.Duration
	// Frames asks for the frame read on an HDR picture (NeedsFrames).
	Frames bool
}

// The Prober's defaults.
const (
	DefaultTimeout         = 60 * time.Second
	DefaultMaxOutput       = 8 << 20
	DefaultProbeSize       = 32 << 20
	DefaultAnalyzeDuration = 30 * time.Second
	// FrameCount is how many frames the frame read decodes.
	FrameCount = 5
)

// Errors of Probe. Each is wrapped with what failed; none carries the
// file's path.
var (
	// ErrNotRegular is a path that isn't a regular file once its links
	// are resolved: a directory, a FIFO, a device, a socket.
	ErrNotRegular = errors.New("describe: not a regular file")
	// ErrUnknownType is a file whose first bytes say no media type Sniff
	// knows: it isn't probed.
	ErrUnknownType = errors.New("describe: unknown media type")
	// ErrProbe is a run of ffprobe that failed: its exit, its time
	// limit, its output cap, or JSON that isn't ffprobe's.
	ErrProbe = errors.New("describe: ffprobe failed")
	// ErrNoStreams is a file ffprobe found no stream in.
	ErrNoStreams = errors.New("describe: no streams")
)

// Probe is what ffprobe said of one file: its media type (sniffed),
// size, format, streams (each with its record when the second run read
// one), chapters and, when the frame read was made, its frames.
type Probe struct {
	MediaType string
	Size      int64
	Format    Format
	Streams   []Stream
	Chapters  []Chapter
	// Frames are the frame read's frames; nil when it wasn't made.
	Frames []Frame
	// Outputs are ffprobe's outputs as they came, for Raw and for
	// tests; they hold no path but /dev/fd/3 (format.filename).
	Outputs Outputs
}

// Outputs are ffprobe's JSON outputs for one file: Streams the first
// run's (-show_format -show_streams -show_chapters), Data the second
// run's (-show_streams -show_data on some streams), Frames the frame
// read's (-show_frames), nil when it wasn't made.
type Outputs struct {
	Streams json.RawMessage   `json:"streams"`
	Data    []json.RawMessage `json:"data,omitempty"`
	Frames  json.RawMessage   `json:"frames,omitempty"`
}

// Probe reads the file at path: links resolved, a regular file opened
// read-only, its first bytes sniffed, then ffprobe's runs. ctx bounds
// it all; each run has its own Timeout too.
func (p *Prober) Probe(ctx context.Context, path string) (*Probe, error) {
	f, real, size, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, SniffSize)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, fmt.Errorf("describe: reading the file: %w", pathless(err))
	}
	mediaType := Sniff(head[:n])
	demuxer := Demuxer(mediaType)
	if demuxer == "" {
		return nil, ErrUnknownType
	}
	r := run{p: p, f: f, demuxer: demuxer, path: childPath(real)}
	var out Outputs
	if out.Streams, err = r.do(ctx, "-show_format", "-show_streams", "-show_chapters"); err != nil {
		return nil, err
	}
	var first output
	if err := strictJSON(out.Streams, &first); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbe, err)
	}
	video, aac := false, false
	for _, s := range first.Streams {
		video = video || s.CodecType == "video" && s.Disposition.AttachedPic == 0
		aac = aac || s.CodecType == "audio" && s.CodecName == "aac"
	}
	for _, sel := range []struct {
		spec string
		want bool
	}{{"V", video}, {"a", aac}} {
		if !sel.want {
			continue
		}
		data, err := r.do(ctx, "-select_streams", sel.spec, "-show_streams", "-show_data",
			"-show_entries", "stream=index,extradata_size,extradata")
		if err != nil {
			return nil, err
		}
		out.Data = append(out.Data, data)
	}
	pr, err := Parse(mediaType, size, out)
	if err != nil {
		return nil, err
	}
	if p.Frames && NeedsFrames(pr) {
		if err := p.readFrames(ctx, r, pr); err != nil {
			return nil, err
		}
	}
	return pr, nil
}

// ReadFrames makes the frame read for a probe already made (by Probe
// without Frames, or by Parse from earlier outputs): the first frames
// of the file at path, its first picture stream, when NeedsFrames says
// it is worth it; pr's Frames and Outputs.Frames are set.
func (p *Prober) ReadFrames(ctx context.Context, path string, pr *Probe) error {
	if !NeedsFrames(pr) {
		return nil
	}
	f, real, _, err := openRegular(path)
	if err != nil {
		return err
	}
	defer f.Close()
	demuxer := Demuxer(pr.MediaType)
	if demuxer == "" {
		demuxer = Demuxer(AudioOrVideo(pr.MediaType, false)) // audio/mp4 and the like
	}
	if demuxer == "" {
		return ErrUnknownType
	}
	return p.readFrames(ctx, run{p: p, f: f, demuxer: demuxer, path: childPath(real)}, pr)
}

func (p *Prober) readFrames(ctx context.Context, r run, pr *Probe) error {
	b, err := r.do(ctx, "-select_streams", "V:0", "-read_intervals", "%+#"+strconv.Itoa(FrameCount), "-show_frames",
		"-show_entries", "frame=color_transfer:frame_side_data=side_data_type")
	if err != nil {
		return err
	}
	var o output
	if err := strictJSON(b, &o); err != nil {
		return fmt.Errorf("%w: %v", ErrProbe, err)
	}
	pr.Frames = o.Frames
	if pr.Frames == nil {
		pr.Frames = []Frame{}
	}
	pr.Outputs.Frames = b
	return nil
}

// NeedsFrames says whether a probe's picture is worth the frame read:
// a PQ or HLG transfer (HDR10+ rides on PQ; the SEI may confirm HLG), or
// a 10-bit picture whose transfer is BT.2020's or BT.709's (broadcast
// HLG signals those in the VUI and 18 in the SEI), and the read not made
// yet.
func NeedsFrames(pr *Probe) bool {
	if pr.Frames != nil {
		return false
	}
	s, ok := pr.picture()
	if !ok {
		return false
	}
	switch s.ColorTransfer {
	case "smpte2084", "arib-std-b67":
		return true
	case "bt2020-10", "bt709":
		return VideoBitDepth(s) >= 10
	}
	return false
}

// Parse reads ffprobe's outputs for a file of the given media type and
// size, as Probe made them (or as a host kept them): strict JSON, the
// records of the second run set on their streams.
func Parse(mediaType string, size int64, out Outputs) (*Probe, error) {
	var first output
	if err := strictJSON(out.Streams, &first); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbe, err)
	}
	if len(first.Streams) == 0 {
		return nil, ErrNoStreams
	}
	pr := &Probe{MediaType: mediaType, Size: size, Streams: first.Streams, Chapters: first.Chapters, Outputs: out}
	if first.Format != nil {
		pr.Format = *first.Format
	}
	for _, d := range out.Data {
		var o struct {
			Streams []dataStream `json:"streams"`
		}
		if err := strictJSON(d, &o); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProbe, err)
		}
		for _, ds := range o.Streams {
			rec, ok := hexDump(ds.Extradata, ds.ExtradataSize)
			if !ok || ds.ExtradataSize == 0 {
				continue
			}
			for i := range pr.Streams {
				if pr.Streams[i].Index == ds.Index && pr.Streams[i].CodecType != "attachment" {
					pr.Streams[i].Record = rec
				}
			}
		}
	}
	if out.Frames != nil {
		var o output
		if err := strictJSON(out.Frames, &o); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProbe, err)
		}
		pr.Frames = o.Frames
		if pr.Frames == nil {
			pr.Frames = []Frame{}
		}
	}
	return pr, nil
}

// picture is the probe's picture: its first video stream that isn't an
// attached picture (cover art) and has a size.
func (pr *Probe) picture() (Stream, bool) {
	for _, s := range pr.Streams {
		if s.CodecType == "video" && s.Disposition.AttachedPic == 0 && s.Disposition.StillImage == 0 && s.Width > 0 && s.Height > 0 {
			return s, true
		}
	}
	return Stream{}, false
}

// Raw is the probe's output kept for the caller (an identifier reading
// the title tag and embedded IDs): the first run's JSON, each stream of
// the second run's records ("extradata", as ffprobe dumps it) merged
// into its stream, without format.filename, without attachment streams,
// and without any string holding the file's directory. Cover art and
// attachment data are never in it: no run reads them. Tags are kept.
func (pr *Probe) Raw(dir string) (json.RawMessage, error) {
	var tree map[string]any
	d := json.NewDecoder(bytes.NewReader(pr.Outputs.Streams))
	d.UseNumber()
	if err := d.Decode(&tree); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbe, err)
	}
	if f, ok := tree["format"].(map[string]any); ok {
		delete(f, "filename")
	}
	data := map[string]map[string]any{}
	for _, b := range pr.Outputs.Data {
		var o struct {
			Streams []map[string]any `json:"streams"`
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if d.Decode(&o) == nil {
			for _, s := range o.Streams {
				data[fmt.Sprint(s["index"])] = s
			}
		}
	}
	if list, ok := tree["streams"].([]any); ok {
		kept := []any{}
		for _, e := range list {
			s, ok := e.(map[string]any)
			if !ok || s["codec_type"] == "attachment" {
				continue
			}
			if ds, ok := data[fmt.Sprint(s["index"])]; ok {
				for _, k := range []string{"extradata", "extradata_size"} {
					if v, ok := ds[k]; ok {
						s[k] = v
					}
				}
			}
			kept = append(kept, s)
		}
		tree["streams"] = kept
	}
	dir = filepath.Clean(dir)
	if dir != "." && dir != string(filepath.Separator) && dir != "" {
		scrub(tree, dir)
	}
	return json.Marshal(tree)
}

// scrub removes from a JSON tree every string member or entry that
// holds dir.
func scrub(v any, dir string) {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			if s, ok := e.(string); ok && strings.Contains(s, dir) {
				delete(t, k)
			} else {
				scrub(e, dir)
			}
		}
	case []any:
		for i, e := range t {
			if s, ok := e.(string); ok && strings.Contains(s, dir) {
				t[i] = ""
			} else {
				scrub(e, dir)
			}
		}
	}
}

// openRegular resolves path's links and opens the real file read-only:
// it must be a regular file, and the file opened the one the real path
// names. Errors never carry the path.
func openRegular(path string) (*os.File, string, int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", 0, fmt.Errorf("describe: %w", pathless(err))
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, "", 0, fmt.Errorf("describe: resolving the path: %w", pathless(err))
	}
	st, err := os.Lstat(real)
	if err != nil {
		return nil, "", 0, fmt.Errorf("describe: %w", pathless(err))
	}
	if !st.Mode().IsRegular() {
		return nil, "", 0, ErrNotRegular
	}
	f, err := os.OpenFile(real, openFlags, 0)
	if err != nil {
		return nil, "", 0, fmt.Errorf("describe: opening the file: %w", pathless(err))
	}
	fst, err := f.Stat()
	if err != nil || !fst.Mode().IsRegular() || !os.SameFile(st, fst) {
		f.Close()
		return nil, "", 0, ErrNotRegular
	}
	return f, real, fst.Size(), nil
}

// pathless is err without the path a *PathError or *LinkError names.
func pathless(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}

// run is one file's ffprobe runs: the open file handed to the child as
// its descriptor 3, read by path.
type run struct {
	p       *Prober
	f       *os.File
	demuxer string
	path    string
}

// do runs ffprobe with the common arguments and args, and returns its
// output.
func (r run) do(ctx context.Context, args ...string) ([]byte, error) {
	p := r.p
	timeout := cmp(p.Timeout, DefaultTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	prog := p.FFprobe
	if prog == "" {
		prog = "ffprobe"
	}
	full := []string{"-v", "error", "-hide_banner", "-of", "json",
		"-probesize", strconv.FormatInt(cmp(p.ProbeSize, DefaultProbeSize), 10),
		"-analyzeduration", strconv.FormatInt(cmp(p.AnalyzeDuration, DefaultAnalyzeDuration).Microseconds(), 10),
		"-protocol_whitelist", "file", "-f", r.demuxer}
	full = append(full, args...)
	full = append(full, "-i", "file:"+r.path)
	cmd := exec.CommandContext(ctx, prog, full...)
	cmd.ExtraFiles = []*os.File{r.f}
	cmd.Env = []string{}
	limit := cmp(p.MaxOutput, DefaultMaxOutput)
	stdout := &capped{max: limit, cancel: cancel}
	stderr := &capped{max: 2048, quiet: true}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	switch {
	case stdout.over:
		return nil, fmt.Errorf("%w: output past %d bytes", ErrProbe, limit)
	case ctx.Err() == context.DeadlineExceeded:
		return nil, fmt.Errorf("%w: past %s", ErrProbe, timeout)
	case err != nil:
		msg := strings.ReplaceAll(strings.TrimSpace(stderr.b.String()), r.path, "file")
		if msg != "" {
			return nil, fmt.Errorf("%w: %v: %s", ErrProbe, err, msg)
		}
		return nil, fmt.Errorf("%w: %v", ErrProbe, err)
	}
	return stdout.b.Bytes(), nil
}

// cmp is v, or def when v is 0.
func cmp[T int | int64 | time.Duration](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}

// capped keeps at most max bytes: past it, it fails (and stops the
// child) or, quiet, drops the rest.
type capped struct {
	b      bytes.Buffer
	max    int
	over   bool
	quiet  bool
	cancel func()
}

func (c *capped) Write(p []byte) (int, error) {
	if c.b.Len()+len(p) > c.max {
		if c.quiet {
			c.b.Write(p[:max(0, c.max-c.b.Len())])
			return len(p), nil
		}
		c.over = true
		c.cancel()
		return 0, errors.New("output cap")
	}
	return c.b.Write(p)
}
