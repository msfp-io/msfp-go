package describe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// media makes a one-second Matroska file with a font attached and a
// chapter, in a test's folder; the test is skipped without ffmpeg and
// ffprobe.
func media(t *testing.T) (dir, path string) {
	t.Helper()
	for _, p := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skip(p + " not installed")
		}
	}
	dir = t.TempDir()
	path = filepath.Join(dir, "clip.mkv")
	font := filepath.Join(dir, "font.ttf")
	os.WriteFile(font, []byte(strings.Repeat("FONT", 4096)), 0o644)
	meta := filepath.Join(dir, "meta.txt")
	os.WriteFile(meta, []byte(";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=500\ntitle=Opening\n"), 0o644)
	out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=160x90:r=25:d=1",
		"-f", "lavfi", "-i", "sine=r=48000:d=1", "-i", meta, "-map", "0", "-map", "1", "-map_chapters", "2",
		"-c:v", "libx264", "-c:a", "aac", "-metadata", "title="+dir, "-attach", font, "-metadata:s:t", "mimetype=font/ttf",
		path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
	return dir, path
}

func TestProbe(t *testing.T) {
	dir, path := media(t)
	link := filepath.Join(t.TempDir(), "link.mkv")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	var p Prober
	c, pr, err := p.Describe(context.Background(), link, []External{{Language: "fr", Format: "subrip"}})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if c.MediaType != "video/matroska" || c.Size != st.Size() || c.DurationMs < 900 || c.Video == nil ||
		c.Video.Codecs == "" || c.Video.RefFrames == 0 || len(c.AudioTracks) != 1 || c.AudioTracks[0].Codecs != "mp4a.40.2" ||
		len(c.Chapters) != 1 || c.Chapters[0].Name != "Opening" {
		t.Errorf("%+v %+v", c, c.Video)
	}
	// The font is stream 2: the external track is s3.
	if len(c.SubtitleTracks) != 1 || c.SubtitleTracks[0].ID != "s3" {
		t.Errorf("%+v", c.SubtitleTracks)
	}
	if len(pr.Outputs.Data) != 2 {
		t.Errorf("%d data runs, want video and audio", len(pr.Outputs.Data))
	}
	for _, d := range pr.Outputs.Data {
		if strings.Contains(string(d), "464f4e54") || strings.Contains(string(d), "attachment") {
			t.Error("the attachment's data was read")
		}
	}
	raw, err := pr.Raw(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), dir) || strings.Contains(string(raw), "filename") || strings.Contains(string(raw), "attachment") ||
		!strings.Contains(string(raw), "extradata") {
		t.Errorf("raw: %s", raw)
	}
	var tree map[string]any
	if json.Unmarshal(raw, &tree) != nil {
		t.Error("raw isn't JSON")
	}
	// Parse reads the kept outputs to the same copy.
	again, err := Parse(pr.MediaType, pr.Size, pr.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	if b1, b2 := mustJSON(Shape(again, nil)), mustJSON(Shape(pr, nil)); b1 != b2 {
		t.Errorf("parsed again:\n%s\n%s", b1, b2)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestProbeRefuses(t *testing.T) {
	dir, path := media(t)
	ctx := context.Background()
	var p Prober
	if _, err := p.Probe(ctx, dir); !errors.Is(err, ErrNotRegular) {
		t.Errorf("a folder: %v", err)
	}
	playlist := filepath.Join(dir, "list.mkv")
	os.WriteFile(playlist, []byte("#EXTM3U\n#EXTINF:1,\nfile:///etc/passwd\n#EXT-X-ENDLIST\n"), 0o644)
	if _, err := p.Probe(ctx, playlist); !errors.Is(err, ErrUnknownType) {
		t.Errorf("a playlist: %v", err)
	}
	missing := filepath.Join(dir, "missing.mkv")
	if _, err := p.Probe(ctx, missing); err == nil || strings.Contains(err.Error(), dir) {
		t.Errorf("a missing file: %v", err)
	}
	small := Prober{MaxOutput: 100}
	if _, err := small.Probe(ctx, path); !errors.Is(err, ErrProbe) {
		t.Errorf("past the output cap: %v", err)
	}
	if _, err := (&Prober{FFprobe: filepath.Join(dir, "no-ffprobe")}).Probe(ctx, path); !errors.Is(err, ErrProbe) {
		t.Errorf("no ffprobe: %v", err)
	}
}

// The frame read on an HLG picture: asked only for HDR pictures.
func TestReadFrames(t *testing.T) {
	_, path := media(t)
	var p Prober
	pr, err := p.Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if NeedsFrames(pr) {
		t.Error("an SDR 8-bit picture")
	}
	hlg := filepath.Join(t.TempDir(), "hlg.mkv")
	out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=160x90:r=25:d=0.4",
		"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params",
		"log-level=error:colorprim=bt2020:transfer=bt2020-10:colormatrix=bt2020nc:atc-sei=18:pic-struct=0", hlg).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg (libx265): %v %s", err, out)
	}
	pr, err = p.Probe(context.Background(), hlg)
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsFrames(pr) {
		t.Fatal("a 10-bit BT.2020 picture")
	}
	if err := p.ReadFrames(context.Background(), hlg, pr); err != nil {
		t.Fatal(err)
	}
	if len(pr.Frames) == 0 || pr.Frames[0].ColorTransfer != "arib-std-b67" || NeedsFrames(pr) {
		t.Errorf("%+v", pr.Frames)
	}
	if c := Shape(pr, nil); *c.Video.Colour.Transfer != 18 {
		t.Errorf("%+v", c.Video.Colour)
	}
}
