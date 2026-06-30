package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ExportConfig is a saved set of proxy/processing parameters for an upload. Each
// record has its own id so it can later be handed to camera-motion and tracked
// independently. Stored in the {id}.exports.json sidecar.
type ExportConfig struct {
	ID        string  `json:"id"`
	FPS       float64 `json:"fps"`
	Width     int     `json:"width"`
	Gray      bool    `json:"gray"`
	CreatedAt int64   `json:"created_at"`
}

// ExportsPath returns the path to an upload's export-records sidecar.
func (s *Store) ExportsPath(id string) string {
	return filepath.Join(s.dir, id+".exports.json")
}

// Exports returns the saved export records for an upload (empty if none). The
// upload must exist.
func (s *Store) Exports(id string) ([]ExportConfig, error) {
	if _, err := os.Stat(s.InfoPath(id)); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return s.readExports(id), nil
}

// Export returns a single export record by id. The bool reports whether it was
// found. The upload must exist.
func (s *Store) Export(id, exportID string) (ExportConfig, bool, error) {
	if _, err := os.Stat(s.InfoPath(id)); errors.Is(err, os.ErrNotExist) {
		return ExportConfig{}, false, ErrNotFound
	}
	for _, c := range s.readExports(id) {
		if c.ID == exportID {
			return c, true, nil
		}
	}
	return ExportConfig{}, false, nil
}

// UpsertExport creates a new export record (when cfg.ID is empty) or updates the
// matching one. Params are normalized to sane defaults. The upload must exist.
func (s *Store) UpsertExport(id string, cfg ExportConfig) (ExportConfig, error) {
	if _, err := os.Stat(s.InfoPath(id)); errors.Is(err, os.ErrNotExist) {
		return ExportConfig{}, ErrNotFound
	}
	cfg = normalizeExport(cfg)
	list := s.readExports(id)
	if cfg.ID == "" {
		cfg.ID = newExportID()
		cfg.CreatedAt = time.Now().Unix()
		list = append(list, cfg)
	} else {
		found := false
		for i := range list {
			if list[i].ID == cfg.ID {
				cfg.CreatedAt = list[i].CreatedAt
				list[i] = cfg
				found = true
				break
			}
		}
		if !found {
			cfg.CreatedAt = time.Now().Unix()
			list = append(list, cfg)
		}
	}
	if err := s.writeExports(id, list); err != nil {
		return ExportConfig{}, err
	}
	return cfg, nil
}

// DeleteExport removes the record with the given id, reporting whether one was
// removed. The upload must exist.
func (s *Store) DeleteExport(id, exportID string) (bool, error) {
	if _, err := os.Stat(s.InfoPath(id)); errors.Is(err, os.ErrNotExist) {
		return false, ErrNotFound
	}
	list := s.readExports(id)
	for i := range list {
		if list[i].ID == exportID {
			list = append(list[:i], list[i+1:]...)
			return true, s.writeExports(id, list)
		}
	}
	return false, nil
}

func (s *Store) readExports(id string) []ExportConfig {
	var list []ExportConfig
	if raw, err := os.ReadFile(s.ExportsPath(id)); err == nil {
		_ = json.Unmarshal(raw, &list)
	}
	return list
}

func (s *Store) writeExports(id string, list []ExportConfig) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return os.WriteFile(s.ExportsPath(id), raw, 0o644)
}

func normalizeExport(cfg ExportConfig) ExportConfig {
	if cfg.FPS <= 0 {
		cfg.FPS = 0.1
	}
	if cfg.Width <= 0 {
		cfg.Width = 480
	}
	return cfg
}

func newExportID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
