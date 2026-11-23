package describe

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a probe kept in testdata: what Parse reads again.
type fixture struct {
	MediaType string  `json:"mediaType"`
	Size      int64   `json:"size"`
	Outputs   Outputs `json:"outputs"`
}

func loadFixture(t *testing.T, name string) *Probe {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	pr, err := Parse(f.MediaType, f.Size, f.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

// TestMakeFixtures writes testdata/<name>.json from the media files in
// $DESCRIBE_MEDIA (made with ffmpeg: see testdata/README), with the frame
// read. Skipped without it.
func TestMakeFixtures(t *testing.T) {
	dir := os.Getenv("DESCRIBE_MEDIA")
	if dir == "" {
		t.Skip("DESCRIBE_MEDIA not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.*"))
	p := Prober{Frames: true}
	for _, path := range files {
		if strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".txt") || strings.HasSuffix(path, ".srt") ||
			strings.HasSuffix(path, ".png") || strings.HasSuffix(path, ".ttf") {
			continue
		}
		pr, err := p.Probe(context.Background(), path)
		if err != nil {
			t.Logf("%s: %v", filepath.Base(path), err)
			continue
		}
		b, _ := json.MarshalIndent(fixture{MediaType: pr.MediaType, Size: pr.Size, Outputs: pr.Outputs}, "", " ")
		name := strings.ReplaceAll(filepath.Base(path), ".", "-")
		if err := os.WriteFile(filepath.Join("testdata", name+".json"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
