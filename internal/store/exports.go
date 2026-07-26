package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

const MotionAnalysisSchemaVersion = 1
const MaxMotionSegments = 100_000
const timelineTolerance = 1e-6

var ErrExportNotFound = errors.New("export not found")
var ErrExportConflict = errors.New("export conflict")
var ErrInvalidAnalysis = errors.New("invalid motion analysis")

// ExportSource identifies the canonical proxy settings analyzed by camera-motion.
type ExportSource struct {
	FPS   float64 `json:"fps"`
	Width int     `json:"width"`
	Gray  bool    `json:"gray"`
}

// MotionSegment is one contiguous classified interval in the analyzed video.
type MotionSegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Kind  string  `json:"kind"`
}

// MotionAnalysisParameters records the detector settings used for an analysis.
type MotionAnalysisParameters struct {
	Method        string  `json:"method"`
	Mask          bool    `json:"mask"`
	ROI           string  `json:"roi"`
	Enter         float64 `json:"enter"`
	Settle        float64 `json:"settle"`
	SettleSamples int     `json:"settle_samples"`
	MinSegment    float64 `json:"min_segment"`
	Features      int     `json:"features"`
	MinInliers    int     `json:"min_inliers"`
}

// MotionAnalysisInput is the analyzer-supplied portion of a motion analysis.
type MotionAnalysisInput struct {
	SchemaVersion int                      `json:"schema_version"`
	AnalysisID    string                   `json:"analysis_id"`
	StartedAt     float64                  `json:"started_at"`
	Source        ExportSource             `json:"source"`
	Duration      float64                  `json:"duration"`
	Parameters    MotionAnalysisParameters `json:"parameters"`
	Segments      []MotionSegment          `json:"segments"`
}

// MotionAnalysis is a validated input plus the server acceptance timestamp.
type MotionAnalysis struct {
	MotionAnalysisInput
	CreatedAt int64 `json:"created_at"`
}

// ExportConfig is a saved set of proxy/processing parameters for an upload. Each
// record has its own id so it can later be handed to camera-motion and tracked
// independently. Stored in the {id}.exports.json sidecar.
type ExportConfig struct {
	ID        string          `json:"id"`
	FPS       float64         `json:"fps"`
	Width     int             `json:"width"`
	Gray      bool            `json:"gray"`
	CreatedAt int64           `json:"created_at"`
	Analysis  *MotionAnalysis `json:"analysis,omitempty"`
}

// ExportsPath returns the path to an upload's export-records sidecar.
func (s *Store) ExportsPath(id string) string {
	return filepath.Join(s.dir, id+".exports.json")
}

// Exports returns the saved export records for an upload (empty if none). The
// upload must exist.
func (s *Store) Exports(id string) ([]ExportConfig, error) {
	unlock := s.exportLocks.lock(id)
	defer unlock()

	if err := s.requireUpload(id); err != nil {
		return nil, err
	}
	return s.readExports(id)
}

// Export returns a single export record by id. The bool reports whether it was
// found. The upload must exist.
func (s *Store) Export(id, exportID string) (ExportConfig, bool, error) {
	unlock := s.exportLocks.lock(id)
	defer unlock()

	if err := s.requireUpload(id); err != nil {
		return ExportConfig{}, false, err
	}
	list, err := s.readExports(id)
	if err != nil {
		return ExportConfig{}, false, err
	}
	for _, cfg := range list {
		if cfg.ID == exportID {
			return cfg, true, nil
		}
	}
	return ExportConfig{}, false, nil
}

// UpsertExport creates a new export record (when cfg.ID is empty) or updates the
// matching one. Params are normalized to sane defaults. The upload must exist.
// Analyses can only be supplied through SetExportAnalysis.
func (s *Store) UpsertExport(id string, cfg ExportConfig) (ExportConfig, error) {
	unlock := s.exportLocks.lock(id)
	defer unlock()

	if err := s.requireUpload(id); err != nil {
		return ExportConfig{}, err
	}
	cfg = NormalizeExport(cfg)
	cfg.Analysis = nil
	list, err := s.readExports(id)
	if err != nil {
		return ExportConfig{}, err
	}
	if cfg.ID == "" {
		cfg.ID = newExportID()
		cfg.CreatedAt = time.Now().Unix()
		list = append(list, cfg)
	} else {
		found := false
		for i := range list {
			if list[i].ID != cfg.ID {
				continue
			}
			cfg.CreatedAt = list[i].CreatedAt
			if sameExportSource(cfg, list[i]) {
				cfg.Analysis = list[i].Analysis
			}
			list[i] = cfg
			found = true
			break
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
	unlock := s.exportLocks.lock(id)
	defer unlock()

	if err := s.requireUpload(id); err != nil {
		return false, err
	}
	list, err := s.readExports(id)
	if err != nil {
		return false, err
	}
	for i := range list {
		if list[i].ID == exportID {
			list = append(list[:i], list[i+1:]...)
			return true, s.writeExports(id, list)
		}
	}
	return false, nil
}

// SetExportAnalysis validates and atomically stores the latest analysis attempt
// for one export version.
func (s *Store) SetExportAnalysis(uploadID, exportID string, input MotionAnalysisInput, authoritativeDuration float64) (ExportConfig, error) {
	unlock := s.exportLocks.lock(uploadID)
	defer unlock()

	if err := s.requireUpload(uploadID); err != nil {
		return ExportConfig{}, err
	}
	list, err := s.readExports(uploadID)
	if err != nil {
		return ExportConfig{}, err
	}
	exportIndex := -1
	for i := range list {
		if list[i].ID == exportID {
			exportIndex = i
			break
		}
	}
	if exportIndex < 0 {
		return ExportConfig{}, ErrExportNotFound
	}
	cfg := list[exportIndex]
	if cfg.Analysis != nil && input.AnalysisID == cfg.Analysis.AnalysisID {
		return cfg, nil
	}
	if err := validateMotionAnalysis(cfg, input, authoritativeDuration); err != nil {
		return ExportConfig{}, err
	}
	if cfg.Analysis != nil && input.StartedAt < cfg.Analysis.StartedAt {
		return ExportConfig{}, ErrExportConflict
	}

	input.Segments = append([]MotionSegment(nil), input.Segments...)
	input.Duration = authoritativeDuration
	input.Segments[0].Start = 0
	for i := 1; i < len(input.Segments); i++ {
		input.Segments[i].Start = input.Segments[i-1].End
	}
	input.Segments[len(input.Segments)-1].End = authoritativeDuration
	for _, segment := range input.Segments {
		if segment.Start < 0 || segment.Start >= segment.End {
			return ExportConfig{}, invalidAnalysis("canonical timeline is invalid")
		}
	}

	cfg.Analysis = &MotionAnalysis{
		MotionAnalysisInput: input,
		CreatedAt:           time.Now().Unix(),
	}
	list[exportIndex] = cfg
	if err := s.writeExports(uploadID, list); err != nil {
		return ExportConfig{}, err
	}
	return cfg, nil
}

func (s *Store) requireUpload(id string) error {
	_, err := os.Stat(s.InfoPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("stat upload info: %w", err)
	}
	return nil
}

func (s *Store) readExports(id string) ([]ExportConfig, error) {
	raw, err := os.ReadFile(s.ExportsPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return []ExportConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read exports: %w", err)
	}
	var list []ExportConfig
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse exports: %w", err)
	}
	if list == nil {
		list = []ExportConfig{}
	}
	for i := range list {
		list[i] = NormalizeExport(list[i])
	}
	return list, nil
}

func (s *Store) writeExports(id string, list []ExportConfig) (err error) {
	path := s.ExportsPath(id)
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary exports: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod temporary exports: %w", err)
	}
	if err := json.NewEncoder(tmp).Encode(list); err != nil {
		return fmt.Errorf("encode exports: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync exports: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close exports: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace exports: %w", err)
	}
	return nil
}

// NormalizeExport applies proxy defaults and clamps the parameters to the
// supported canonical range.
func NormalizeExport(cfg ExportConfig) ExportConfig {
	if !isFinite(cfg.FPS) || cfg.FPS <= 0 {
		cfg.FPS = 0.1
	}
	if cfg.Width <= 0 {
		cfg.Width = 480
	}
	if cfg.FPS < 0.1 {
		cfg.FPS = 0.1
	} else if cfg.FPS > 60 {
		cfg.FPS = 60
	}
	if cfg.Width < 64 {
		cfg.Width = 64
	} else if cfg.Width > 1920 {
		cfg.Width = 1920
	}
	return cfg
}

func validateMotionAnalysis(cfg ExportConfig, input MotionAnalysisInput, authoritativeDuration float64) error {
	if input.SchemaVersion != MotionAnalysisSchemaVersion {
		return invalidAnalysis("unsupported schema version")
	}
	if !isUUIDShaped(input.AnalysisID) {
		return invalidAnalysis("analysis_id is not UUID-shaped")
	}
	if !isFinitePositive(input.StartedAt) {
		return invalidAnalysis("started_at must be finite and positive")
	}
	if input.Source.FPS != cfg.FPS || input.Source.Width != cfg.Width || input.Source.Gray != cfg.Gray {
		return fmt.Errorf("%w: source does not match export", ErrExportConflict)
	}
	if !isFinitePositive(input.Duration) || !isFinitePositive(authoritativeDuration) {
		return invalidAnalysis("duration must be finite and positive")
	}
	allowedDurationDifference := math.Max(0.1, 1/input.Source.FPS)
	if math.Abs(input.Duration-authoritativeDuration) > allowedDurationDifference {
		return invalidAnalysis("duration does not match upload")
	}

	p := input.Parameters
	if !oneOf(p.Method, "affine", "flow", "edges", "lines", "ecc") {
		return invalidAnalysis("unsupported method")
	}
	if !oneOf(p.ROI, "full", "top", "bottom") {
		return invalidAnalysis("unsupported roi")
	}
	if !isFinitePositive(p.Enter) {
		return invalidAnalysis("enter must be finite and positive")
	}
	if !isFiniteNonnegative(p.Settle) || !isFiniteNonnegative(p.MinSegment) {
		return invalidAnalysis("settle and min_segment must be finite and nonnegative")
	}
	if p.SettleSamples <= 0 || p.Features <= 0 || p.MinInliers <= 0 {
		return invalidAnalysis("analysis counters must be positive")
	}
	if len(input.Segments) == 0 || len(input.Segments) > MaxMotionSegments {
		return invalidAnalysis("invalid segment count")
	}
	for i, segment := range input.Segments {
		if !oneOf(segment.Kind, "stable", "transition") {
			return invalidAnalysis("unsupported segment kind")
		}
		if !isFinite(segment.Start) || !isFinite(segment.End) || segment.Start < 0 || segment.Start >= segment.End {
			return invalidAnalysis("invalid segment interval")
		}
		if i == 0 {
			if math.Abs(segment.Start) > timelineTolerance {
				return invalidAnalysis("timeline does not start at zero")
			}
			continue
		}
		if math.Abs(segment.Start-input.Segments[i-1].End) > timelineTolerance {
			return invalidAnalysis("timeline is not contiguous")
		}
	}
	if math.Abs(input.Segments[len(input.Segments)-1].End-input.Duration) > timelineTolerance {
		return invalidAnalysis("timeline does not cover duration")
	}
	return nil
}

func sameExportSource(a, b ExportConfig) bool {
	return a.FPS == b.FPS && a.Width == b.Width && a.Gray == b.Gray
}

func invalidAnalysis(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidAnalysis, reason)
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func isFinitePositive(value float64) bool {
	return isFinite(value) && value > 0
}

func isFiniteNonnegative(value float64) bool {
	return isFinite(value) && value >= 0
}

func isUUIDShaped(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if value[i] != '-' {
				return false
			}
			continue
		}
		if !isHex(value[i]) {
			return false
		}
	}
	return true
}

func isHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func newExportID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
