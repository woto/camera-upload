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

func TestSeekCheckCommandUsesEmbeddedScript(t *testing.T) {
	cmd := newSeekCheckCommand(context.Background(), "video.mp4")
	if got, want := cmd.Args[1], "-"; got != want {
		t.Fatalf("script argument = %q, want %q", got, want)
	}
	if cmd.Stdin == nil {
		t.Fatal("seek-check command has no embedded script on stdin")
	}
	worker, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatalf("read embedded worker: %v", err)
	}
	if len(worker) == 0 {
		t.Fatal("embedded worker is empty")
	}
}

func TestCheckSeekRunsEmbeddedWorker(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if err := exec.Command("python3", "-c", "import cv2").Run(); err != nil {
		t.Skip("Python OpenCV not available")
	}

	video := t.TempDir() + "/video"
	makeTestVideo(t, video)
	result, err := CheckSeek(context.Background(), video)
	if err != nil {
		t.Fatalf("run embedded seek checker: %v", err)
	}
	if result.Samples <= 0 {
		t.Fatalf("samples = %d, want positive", result.Samples)
	}
}

func TestWriteCFRSupportsTemporaryOutputPath(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	dir := t.TempDir()
	src := dir + "/source"
	makeTestVideo(t, src)
	if err := WriteCFR(context.Background(), src, dir+"/converted.tmp"); err != nil {
		t.Fatalf("write CFR to temporary path: %v", err)
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
	if err := os.WriteFile(st.InfoPath(id), []byte(`{"ID":"vid1","Size":1,"Offset":1,"SizeIsDeferred":false,"MetaData":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(st, true, log)
	p.check = func(context.Context, string) (SeekCheckResult, error) { return SeekCheckResult{Samples: 1}, nil }
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

func TestMarkInterruptedFailsInProgressUpload(t *testing.T) {
	dir := t.TempDir()
	st := store.New(dir)
	id := "vid1"
	if err := os.WriteFile(st.InfoPath(id), []byte(`{"ID":"vid1","Size":1,"Offset":1,"SizeIsDeferred":false,"MetaData":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.DataPath(id), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProcessing(id, store.Processing{Status: store.ProcessingConverting}); err != nil {
		t.Fatal(err)
	}
	p := New(st, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.MarkInterrupted()
	state, err := st.Processing(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != store.ProcessingFailed || state.Error != "processing interrupted by restart" {
		t.Fatalf("state = %+v", state)
	}
}
