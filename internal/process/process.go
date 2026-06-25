// Package process performs post-upload media processing: it extracts video
// metadata with ffprobe and generates a thumbnail with ffmpeg. Results are
// written as sidecar files next to the upload so they can be served later.
package process

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tus/tusd/v2/pkg/handler"

	"github.com/woto/camera-upload/internal/store"
)

const processTimeout = 2 * time.Minute

// Processor consumes completed-upload events and produces metadata + thumbnail
// sidecars.
type Processor struct {
	store      *store.Store
	thumbnails bool
	log        *slog.Logger
}

// New returns a Processor. If thumbnails is false, thumbnail generation is
// skipped but metadata is still extracted.
func New(s *store.Store, thumbnails bool, log *slog.Logger) *Processor {
	return &Processor{store: s, thumbnails: thumbnails, log: log}
}

// Run consumes completed-upload events from ch until ctx is cancelled. It is
// meant to be launched in its own goroutine and reads the handler's
// CompleteUploads channel.
func (p *Processor) Run(ctx context.Context, ch <-chan handler.HookEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			p.handle(ev.Upload.ID)
		}
	}
}

func (p *Processor) handle(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()

	dataPath := p.store.DataPath(id)
	if _, err := os.Stat(dataPath); err != nil {
		p.log.Error("processing: data file missing", "id", id, "err", err)
		return
	}

	if err := p.probe(ctx, id, dataPath); err != nil {
		p.log.Error("processing: ffprobe failed", "id", id, "err", err)
	}

	if p.thumbnails {
		if err := p.thumbnail(ctx, id, dataPath); err != nil {
			p.log.Error("processing: thumbnail failed", "id", id, "err", err)
		}
	}

	p.log.Info("processing: done", "id", id)
}

// probe runs ffprobe and stores the raw JSON output as the metadata sidecar.
func (p *Processor) probe(ctx context.Context, id, dataPath string) error {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		dataPath,
	)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("run ffprobe: %w", err)
	}
	// Validate that the output is JSON before persisting it.
	if !json.Valid(out) {
		return fmt.Errorf("ffprobe produced invalid json")
	}
	if err := os.WriteFile(p.store.MetaPath(id), out, 0o644); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	return nil
}

// thumbnail generates the default thumbnail (a frame at ~1s).
func (p *Processor) thumbnail(ctx context.Context, id, dataPath string) error {
	return WriteThumbnail(ctx, dataPath, p.store.ThumbPath(id), 1)
}

// WriteThumbnail extracts a frame at the given timestamp (seconds), scales it
// to a reasonable width and writes it as a JPEG to dst.
func WriteThumbnail(ctx context.Context, src, dst string, at float64) error {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y",
		"-ss", formatSeconds(at),
		"-i", src,
		"-frames:v", "1",
		"-vf", "scale=480:-2",
		dst,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("run ffmpeg: %w: %s", err, out)
	}
	return nil
}

// ExtractFrame returns a full-resolution JPEG of the frame at the given
// timestamp (seconds) without persisting anything to disk.
func ExtractFrame(ctx context.Context, src string, at float64) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-ss", formatSeconds(at),
		"-i", src,
		"-frames:v", "1",
		"-f", "image2",
		"-c:v", "mjpeg",
		"pipe:1",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run ffmpeg: %w: %s", err, stderr.String())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no frame")
	}
	return out, nil
}

// formatSeconds renders a timestamp for ffmpeg's -ss option.
func formatSeconds(s float64) string {
	if s < 0 {
		s = 0
	}
	return strconv.FormatFloat(s, 'f', 3, 64)
}
