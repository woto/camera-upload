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
	"syscall"
	"time"

	"github.com/tus/tusd/v2/pkg/handler"

	"github.com/woto/camera-upload/internal/store"
)

const processTimeout = 30 * time.Minute

// Processor consumes completed-upload events and produces metadata + thumbnail
// sidecars.
type Processor struct {
	store      *store.Store
	thumbnails bool
	log        *slog.Logger
	check      func(context.Context, string) (SeekCheckResult, error)
	transcode  func(context.Context, string, string) error
}

// New returns a Processor. If thumbnails is false, thumbnail generation is
// skipped but metadata is still extracted.
func New(s *store.Store, thumbnails bool, log *slog.Logger) *Processor {
	return &Processor{store: s, thumbnails: thumbnails, log: log, check: CheckSeek, transcode: WriteCFR}
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
		p.fail(id, err)
		return
	}
	if err := p.store.SetProcessing(id, store.Processing{Status: store.ProcessingChecking}); err != nil {
		p.log.Error("processing: persist checking", "id", id, "err", err)
		return
	}
	result, err := p.check(ctx, dataPath)
	if err != nil {
		p.fail(id, err)
		return
	}
	if result.NeedsCFR() {
		if err := p.store.SetProcessing(id, store.Processing{Status: store.ProcessingConverting}); err != nil {
			p.fail(id, err)
			return
		}
		tmp := fmt.Sprintf("%s.%d.tmp", p.store.CFRPath(id), time.Now().UnixNano())
		defer os.Remove(tmp)
		if err := p.transcode(ctx, dataPath, tmp); err != nil {
			p.fail(id, err)
			return
		}
		if err := os.Rename(tmp, p.store.CFRPath(id)); err != nil {
			p.fail(id, err)
			return
		}
		if err := p.store.SetProcessing(id, store.Processing{Status: store.ProcessingReady, WorkingSource: store.WorkingConverted}); err != nil {
			p.fail(id, err)
			return
		}
	} else if err := p.store.SetProcessing(id, store.Processing{Status: store.ProcessingReady, WorkingSource: store.WorkingOriginal}); err != nil {
		p.fail(id, err)
		return
	}
	working := p.store.WorkingPath(id)
	if err := p.probe(ctx, id, working); err != nil {
		p.fail(id, err)
		return
	}
	if p.thumbnails {
		if err := p.thumbnail(ctx, id, working); err != nil {
			p.fail(id, err)
			return
		}
	}
	p.log.Info("processing: done", "id", id)
}

func (p *Processor) fail(id string, err error) {
	p.log.Error("processing failed", "id", id, "err", err)
	_ = p.store.SetProcessing(id, store.Processing{Status: store.ProcessingFailed, Error: err.Error()})
}

// Retry restarts a failed upload's processing without touching its source file.
func (p *Processor) Retry(id string) error {
	state, err := p.store.Processing(id)
	if err != nil {
		return err
	}
	if state.Status != store.ProcessingFailed {
		return fmt.Errorf("processing is not failed")
	}
	if err := p.store.ResetDerived(id); err != nil {
		return err
	}
	if err := p.store.SetProcessing(id, store.Processing{Status: store.ProcessingChecking}); err != nil {
		return err
	}
	go p.handle(id)
	return nil
}

// MarkInterrupted marks jobs abandoned by a prior server process as failed.
// They are retried only through the explicit Retry action.
func (p *Processor) MarkInterrupted() {
	uploads, err := p.store.List()
	if err != nil {
		p.log.Error("list interrupted processing", "err", err)
		return
	}
	for _, up := range uploads {
		if up.Processing.Status != store.ProcessingChecking && up.Processing.Status != store.ProcessingConverting {
			continue
		}
		if err := p.store.SetProcessing(up.ID, store.Processing{
			Status: store.ProcessingFailed, Error: "processing interrupted by restart",
		}); err != nil {
			p.log.Error("mark interrupted processing", "id", up.ID, "err", err)
		}
	}
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

// WriteCFR creates a broadly decodable 30 fps H.264/AAC working copy.
func WriteCFR(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-y", "-i", src,
		"-map", "0:v:0", "-map", "0:a?",
		"-vf", "fps=30", "-fps_mode", "cfr",
		"-c:v", "libx264", "-preset", "medium", "-crf", "18",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "192k",
		"-movflags", "+faststart",
		"-f", "mp4", dst)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("run cfr conversion: %w: %s", err, out)
	}
	return nil
}

// WriteProxy transcodes src into a compact "proxy" clip at dst: decimated to
// the given fps, scaled to the given width and optionally grayscale, encoded
// with H.264. This lets the heavy full decode happen locally (the file is on
// disk here) so only a few-MB proxy travels the network to analysis services.
func WriteProxy(ctx context.Context, src, dst string, fps float64, width int, gray bool) error {
	vf := fmt.Sprintf("fps=%s,scale=%d:-2", strconv.FormatFloat(fps, 'g', -1, 64), width)
	if gray {
		vf += ",format=gray"
	}
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", src,
		"-vf", vf,
		"-an",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "28",
		"-pix_fmt", "yuv420p", // keep broadly decodable even when grayscale
		"-movflags", "+faststart",
		"-f", "mp4", // explicit: the temp output path has no .mp4 extension
		dst,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	// Kill this (long-running) ffmpeg if the server process dies, so a restart
	// can't leave an orphan still writing the output.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
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
