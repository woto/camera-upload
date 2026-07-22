package process

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/tus/tusd/v2/pkg/handler"

	"github.com/woto/camera-upload/internal/store"
)

func TestSeekCheckParsesMismatchResult(t *testing.T) {
	got, err := parseSeekCheck([]byte(`{"samples":60,"mismatches":[100,200]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !got.NeedsCFR() {
		t.Fatal("expected CFR conversion")
	}
}

func TestSeekCheckRejectsInvalidWorkerJSON(t *testing.T) {
	if _, err := parseSeekCheck([]byte("not-json")); err == nil {
		t.Fatal("expected error")
	}
}

// makeTestVideo generates a tiny test video and stores it at dst, which has no
// file extension (mirroring tusd's filestore naming). ffmpeg needs an explicit
// extension to choose a muxer, so we generate to a .mp4 temp file and rename.
func makeTestVideo(t *testing.T, dst string) {
	t.Helper()
	tmp := dst + ".mp4"
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-pix_fmt", "yuv420p",
		tmp,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg gen failed: %v\n%s", err, out)
	}
	if err := os.Rename(tmp, dst); err != nil {
		t.Fatal(err)
	}
}

func TestProcessorMetadataAndThumbnail(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}

	dir := t.TempDir()
	st := store.New(dir)
	id := "vid1"
	makeTestVideo(t, st.DataPath(id))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(st, true, log)
	p.handle(id)

	// Metadata sidecar should exist and contain ffprobe's "streams".
	metaRaw, err := os.ReadFile(st.MetaPath(id))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("meta is not valid json: %v", err)
	}
	if _, ok := meta["streams"]; !ok {
		t.Errorf("meta missing streams: %v", meta)
	}

	// Thumbnail should have been generated.
	if fi, err := os.Stat(st.ThumbPath(id)); err != nil || fi.Size() == 0 {
		t.Errorf("thumbnail missing or empty: err=%v", err)
	}
}

func TestProcessorRunStops(t *testing.T) {
	st := store.New(t.TempDir())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(st, false, log)

	ch := make(chan handler.HookEvent)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx, ch); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after context cancel")
	}
}
