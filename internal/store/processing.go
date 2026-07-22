package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ResetDerived removes artifacts that are regenerated from the working video.
// It intentionally preserves the uploaded source, .info and user metadata.
func (s *Store) ResetDerived(id string) error {
	paths := []string{s.CFRPath(id), s.MetaPath(id), s.ThumbPath(id)}
	proxies, _ := filepath.Glob(filepath.Join(s.dir, id+".proxy_*.mp4"))
	temps, _ := filepath.Glob(filepath.Join(s.dir, id+".cfr.mp4.*.tmp"))
	paths = append(paths, proxies...)
	paths = append(paths, temps...)
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove derived artifact: %w", err)
		}
	}
	return nil
}

// ProcessingStatus is the lifecycle state of post-upload video preparation.
type ProcessingStatus string

const (
	ProcessingChecking   ProcessingStatus = "checking"
	ProcessingConverting ProcessingStatus = "converting"
	ProcessingReady      ProcessingStatus = "ready"
	ProcessingFailed     ProcessingStatus = "failed"
)

// WorkingSource identifies the file used by normal video operations.
type WorkingSource string

const (
	WorkingOriginal  WorkingSource = "original"
	WorkingConverted WorkingSource = "converted"
)

// Processing is persisted in an upload-specific sidecar.
type Processing struct {
	Status        ProcessingStatus `json:"status"`
	WorkingSource WorkingSource    `json:"working_source,omitempty"`
	Error         string           `json:"error,omitempty"`
}

// ProcessingPath returns the processing-state sidecar path.
func (s *Store) ProcessingPath(id string) string {
	return filepath.Join(s.dir, id+".processing.json")
}

// CFRPath returns the promoted constant-frame-rate working copy path.
func (s *Store) CFRPath(id string) string {
	return filepath.Join(s.dir, id+".cfr.mp4")
}

// Processing returns persisted processing state. A missing sidecar is a zero
// state so callers can retain compatibility with uploads created before CFR
// processing was introduced.
func (s *Store) Processing(id string) (Processing, error) {
	if _, err := os.Stat(s.InfoPath(id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Processing{}, ErrNotFound
		}
		return Processing{}, fmt.Errorf("stat upload: %w", err)
	}
	raw, err := os.ReadFile(s.ProcessingPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Processing{}, nil
	}
	if err != nil {
		return Processing{}, fmt.Errorf("read processing: %w", err)
	}
	var state Processing
	if err := json.Unmarshal(raw, &state); err != nil {
		return Processing{}, fmt.Errorf("parse processing: %w", err)
	}
	return state, nil
}

// SetProcessing persists processing state for an existing upload.
func (s *Store) SetProcessing(id string, state Processing) error {
	if _, err := os.Stat(s.InfoPath(id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("stat upload: %w", err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal processing: %w", err)
	}
	if err := os.WriteFile(s.ProcessingPath(id), raw, 0o644); err != nil {
		return fmt.Errorf("write processing: %w", err)
	}
	return nil
}

// WorkingPath returns the selected working file. Until an explicit ready /
// converted state exists, the uploaded source remains the selected path.
func (s *Store) WorkingPath(id string) string {
	state, err := s.Processing(id)
	if err == nil && state.Status == ProcessingReady && state.WorkingSource == WorkingConverted {
		return s.CFRPath(id)
	}
	return s.DataPath(id)
}
