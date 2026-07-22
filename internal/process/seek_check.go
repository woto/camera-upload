package process

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// seekCheckWorker is passed to Python over stdin, keeping the worker bundled
// with the Go binary regardless of whether it runs locally or in Docker.
//
//go:embed check_seek.py
var seekCheckWorker []byte

// SeekCheckResult is emitted by the bundled OpenCV worker.
type SeekCheckResult struct {
	Samples    int   `json:"samples"`
	Mismatches []int `json:"mismatches"`
}

func (r SeekCheckResult) NeedsCFR() bool { return len(r.Mismatches) > 0 }

func newSeekCheckCommand(ctx context.Context, src string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "python3", "-", "--video", src, "--samples", "60")
	cmd.Stdin = bytes.NewReader(seekCheckWorker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return cmd
}

func parseSeekCheck(raw []byte) (SeekCheckResult, error) {
	var result SeekCheckResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return SeekCheckResult{}, fmt.Errorf("parse seek check: %w", err)
	}
	if result.Samples <= 0 {
		return SeekCheckResult{}, fmt.Errorf("seek check returned invalid sample count")
	}
	return result, nil
}

// CheckSeek runs the OpenCV worker against a video file.
func CheckSeek(ctx context.Context, src string) (SeekCheckResult, error) {
	cmd := newSeekCheckCommand(ctx, src)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return SeekCheckResult{}, fmt.Errorf("run seek check: %w: %s", err, stderr.String())
	}
	return parseSeekCheck(out)
}
