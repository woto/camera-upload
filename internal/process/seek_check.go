package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// SeekCheckResult is emitted by the bundled OpenCV worker.
type SeekCheckResult struct {
	Samples    int   `json:"samples"`
	Mismatches []int `json:"mismatches"`
}

func (r SeekCheckResult) NeedsCFR() bool { return len(r.Mismatches) > 0 }

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
	cmd := exec.CommandContext(ctx, "python3", "/app/check_seek.py", "--video", src, "--samples", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return SeekCheckResult{}, fmt.Errorf("run seek check: %w: %s", err, stderr.String())
	}
	return parseSeekCheck(out)
}
