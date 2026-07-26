package store

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func validAnalysisInput() MotionAnalysisInput {
	return MotionAnalysisInput{
		SchemaVersion: MotionAnalysisSchemaVersion,
		AnalysisID:    "7f83b47e-5f97-4b24-a5e0-2c02b8ca1527",
		StartedAt:     100,
		Source:        ExportSource{FPS: 4, Width: 480, Gray: true},
		Duration:      20,
		Parameters: MotionAnalysisParameters{
			Method: "affine", Mask: true, ROI: "full", Enter: 2,
			Settle: .5, SettleSamples: 2, MinSegment: 1,
			Features: 500, MinInliers: 20,
		},
		Segments: []MotionSegment{
			{Start: 0, End: 8, Kind: "stable"},
			{Start: 8, End: 10, Kind: "transition"},
			{Start: 10, End: 20, Kind: "stable"},
		},
	}
}

func TestNormalizeExportUsesDefaultsAndProxyBounds(t *testing.T) {
	tests := []struct {
		name string
		in   ExportConfig
		fps  float64
		w    int
	}{
		{name: "defaults", in: ExportConfig{}, fps: .1, w: 480},
		{name: "lower bounds", in: ExportConfig{FPS: .01, Width: 1}, fps: .1, w: 64},
		{name: "upper bounds", in: ExportConfig{FPS: 99, Width: 9000}, fps: 60, w: 1920},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeExport(tt.in)
			if got.FPS != tt.fps || got.Width != tt.w {
				t.Fatalf("got %+v, want fps=%v width=%v", got, tt.fps, tt.w)
			}
		})
	}
}

func TestSetExportAnalysisPersistsCanonicalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)
	cfg, err := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}
	input := validAnalysisInput()
	input.Segments[0].Start = timelineTolerance / 2
	input.Segments[1].Start += timelineTolerance / 2
	input.Duration += .05
	input.Segments[len(input.Segments)-1].End = input.Duration
	stored, err := s.SetExportAnalysis("vid1", cfg.ID, input, 20)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Analysis == nil || stored.Analysis.CreatedAt == 0 {
		t.Fatalf("analysis missing server timestamp: %+v", stored)
	}
	if stored.Analysis.Duration != 20 || stored.Analysis.Segments[0].Start != 0 {
		t.Fatalf("duration/start not canonical: %+v", stored.Analysis)
	}
	for i := 1; i < len(stored.Analysis.Segments); i++ {
		if stored.Analysis.Segments[i].Start != stored.Analysis.Segments[i-1].End {
			t.Fatalf("boundary %d not canonical: %+v", i, stored.Analysis.Segments)
		}
	}
	if got := stored.Analysis.Segments[len(stored.Analysis.Segments)-1].End; got != 20 {
		t.Fatalf("final end=%v, want 20", got)
	}

	reloaded, ok, err := New(dir).Export("vid1", cfg.ID)
	if err != nil || !ok {
		t.Fatalf("reload: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(reloaded, stored) {
		t.Fatalf("round trip mismatch:\n got  %+v\n want %+v", reloaded, stored)
	}
}

func TestSetExportAnalysisRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*MotionAnalysisInput)
	}{
		{name: "schema", mutate: func(in *MotionAnalysisInput) { in.SchemaVersion++ }},
		{name: "uuid", mutate: func(in *MotionAnalysisInput) { in.AnalysisID = "not-a-uuid" }},
		{name: "started at zero", mutate: func(in *MotionAnalysisInput) { in.StartedAt = 0 }},
		{name: "started at nan", mutate: func(in *MotionAnalysisInput) { in.StartedAt = math.NaN() }},
		{name: "duration infinity", mutate: func(in *MotionAnalysisInput) { in.Duration = math.Inf(1) }},
		{name: "method", mutate: func(in *MotionAnalysisInput) { in.Parameters.Method = "unknown" }},
		{name: "roi", mutate: func(in *MotionAnalysisInput) { in.Parameters.ROI = "middle" }},
		{name: "enter", mutate: func(in *MotionAnalysisInput) { in.Parameters.Enter = 0 }},
		{name: "enter nan", mutate: func(in *MotionAnalysisInput) { in.Parameters.Enter = math.NaN() }},
		{name: "enter over max", mutate: func(in *MotionAnalysisInput) { in.Parameters.Enter = 100.000001 }},
		{name: "settle negative", mutate: func(in *MotionAnalysisInput) { in.Parameters.Settle = -1 }},
		{name: "settle infinity", mutate: func(in *MotionAnalysisInput) { in.Parameters.Settle = math.Inf(1) }},
		{name: "settle over max", mutate: func(in *MotionAnalysisInput) { in.Parameters.Settle = 100.000001 }},
		{name: "settle samples", mutate: func(in *MotionAnalysisInput) { in.Parameters.SettleSamples = 0 }},
		{name: "settle samples over max", mutate: func(in *MotionAnalysisInput) { in.Parameters.SettleSamples = 1001 }},
		{name: "min segment negative", mutate: func(in *MotionAnalysisInput) { in.Parameters.MinSegment = -1 }},
		{name: "min segment nan", mutate: func(in *MotionAnalysisInput) { in.Parameters.MinSegment = math.NaN() }},
		{name: "min segment over max", mutate: func(in *MotionAnalysisInput) { in.Parameters.MinSegment = 86400.000001 }},
		{name: "features", mutate: func(in *MotionAnalysisInput) { in.Parameters.Features = 0 }},
		{name: "features over max", mutate: func(in *MotionAnalysisInput) { in.Parameters.Features = 100001 }},
		{name: "min inliers", mutate: func(in *MotionAnalysisInput) { in.Parameters.MinInliers = 0 }},
		{name: "min inliers over max", mutate: func(in *MotionAnalysisInput) { in.Parameters.MinInliers = 100001 }},
		{name: "empty segments", mutate: func(in *MotionAnalysisInput) { in.Segments = nil }},
		{name: "too many segments", mutate: func(in *MotionAnalysisInput) { in.Segments = make([]MotionSegment, MaxMotionSegments+1) }},
		{name: "segment kind", mutate: func(in *MotionAnalysisInput) { in.Segments[0].Kind = "moving" }},
		{name: "segment start nan", mutate: func(in *MotionAnalysisInput) { in.Segments[0].Start = math.NaN() }},
		{name: "segment end infinity", mutate: func(in *MotionAnalysisInput) { in.Segments[0].End = math.Inf(1) }},
		{name: "negative start", mutate: func(in *MotionAnalysisInput) { in.Segments[0].Start = -1 }},
		{name: "empty interval", mutate: func(in *MotionAnalysisInput) { in.Segments[0].End = 0 }},
		{name: "initial gap", mutate: func(in *MotionAnalysisInput) { in.Segments[0].Start = .1 }},
		{name: "overlap", mutate: func(in *MotionAnalysisInput) { in.Segments[1].Start = 7 }},
		{name: "gap", mutate: func(in *MotionAnalysisInput) { in.Segments[1].Start = 9 }},
		{name: "incomplete duration", mutate: func(in *MotionAnalysisInput) { in.Segments[2].End = 19 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
			s := New(dir)
			cfg, err := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
			if err != nil {
				t.Fatal(err)
			}
			input := validAnalysisInput()
			tt.mutate(&input)
			if _, err := s.SetExportAnalysis("vid1", cfg.ID, input, 20); !errors.Is(err, ErrInvalidAnalysis) {
				t.Fatalf("error=%v, want ErrInvalidAnalysis", err)
			}
		})
	}
}

func TestSetExportAnalysisAcceptsParameterUpperBounds(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)
	cfg, err := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}
	input := validAnalysisInput()
	input.Parameters.Enter = 100
	input.Parameters.Settle = 100
	input.Parameters.SettleSamples = 1000
	input.Parameters.MinSegment = 86400
	input.Parameters.Features = 100000
	input.Parameters.MinInliers = 100000
	stored, err := s.SetExportAnalysis("vid1", cfg.ID, input, 20)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Analysis == nil || stored.Analysis.Parameters != input.Parameters {
		t.Fatalf("upper bounds not stored exactly: %+v", stored.Analysis)
	}
}

func TestSetExportAnalysisRejectsSourceAndDurationConflicts(t *testing.T) {
	tests := []struct {
		name                  string
		mutate                func(*MotionAnalysisInput)
		authoritativeDuration float64
		want                  error
	}{
		{name: "fps", mutate: func(in *MotionAnalysisInput) { in.Source.FPS = 5 }, authoritativeDuration: 20, want: ErrExportConflict},
		{name: "width", mutate: func(in *MotionAnalysisInput) { in.Source.Width = 640 }, authoritativeDuration: 20, want: ErrExportConflict},
		{name: "gray", mutate: func(in *MotionAnalysisInput) { in.Source.Gray = false }, authoritativeDuration: 20, want: ErrExportConflict},
		{name: "duration", mutate: func(in *MotionAnalysisInput) {}, authoritativeDuration: 21, want: ErrInvalidAnalysis},
		{name: "authoritative zero", mutate: func(in *MotionAnalysisInput) {}, authoritativeDuration: 0, want: ErrInvalidAnalysis},
		{name: "authoritative nan", mutate: func(in *MotionAnalysisInput) {}, authoritativeDuration: math.NaN(), want: ErrInvalidAnalysis},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
			s := New(dir)
			cfg, err := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
			if err != nil {
				t.Fatal(err)
			}
			input := validAnalysisInput()
			tt.mutate(&input)
			if _, err := s.SetExportAnalysis("vid1", cfg.ID, input, tt.authoritativeDuration); !errors.Is(err, tt.want) {
				t.Fatalf("error=%v, want %v", err, tt.want)
			}
		})
	}
}

func TestUpsertExportAnalysisRetentionAndInvalidation(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)
	cfg, _ := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
	withAnalysis, err := s.SetExportAnalysis("vid1", cfg.ID, validAnalysisInput(), 20)
	if err != nil {
		t.Fatal(err)
	}
	forged := &MotionAnalysis{CreatedAt: 1}
	same, err := s.UpsertExport("vid1", ExportConfig{ID: cfg.ID, FPS: 4, Width: 480, Gray: true, Analysis: forged})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(same.Analysis, withAnalysis.Analysis) {
		t.Fatalf("identical canonical settings did not preserve analysis: %+v", same)
	}
	if same.ID != cfg.ID {
		t.Fatalf("identical canonical settings rotated id: %s -> %s", cfg.ID, same.ID)
	}
	changed, err := s.UpsertExport("vid1", ExportConfig{ID: cfg.ID, FPS: 5, Width: 480, Gray: true, Analysis: forged})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Analysis != nil {
		t.Fatalf("changed settings retained/injected analysis: %+v", changed)
	}
	if changed.ID == cfg.ID {
		t.Fatalf("changed settings retained old id: %+v", changed)
	}
	if changed.CreatedAt <= same.CreatedAt {
		t.Fatalf("changed settings did not refresh created_at: old=%d new=%d", same.CreatedAt, changed.CreatedAt)
	}
	created, err := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Analysis: forged})
	if err != nil {
		t.Fatal(err)
	}
	if created.Analysis != nil {
		t.Fatalf("create injected caller analysis: %+v", created)
	}
}

func TestUpsertExportRotatesIdentityAcrossSourceABA(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)
	s1, err := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetExportAnalysis("vid1", s1.ID, validAnalysisInput(), 20); err != nil {
		t.Fatal(err)
	}

	s2, err := s.UpsertExport("vid1", ExportConfig{ID: s1.ID, FPS: 8, Width: 640, Gray: false})
	if err != nil {
		t.Fatal(err)
	}
	if s2.ID == s1.ID || s2.Analysis != nil {
		t.Fatalf("S1 -> S2 did not rotate cleanly: S1=%+v S2=%+v", s1, s2)
	}
	if s2.CreatedAt <= s1.CreatedAt {
		t.Fatalf("S2 created_at=%d, want after S1=%d", s2.CreatedAt, s1.CreatedAt)
	}
	if _, ok, err := s.Export("vid1", s1.ID); err != nil || ok {
		t.Fatalf("old S1 id survived: ok=%v err=%v", ok, err)
	}
	if _, err := s.SetExportAnalysis("vid1", s1.ID, validAnalysisInput(), 20); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("delivery to old S1 id: %v", err)
	}
	if _, err := s.UpsertExport("vid1", ExportConfig{ID: s1.ID, FPS: 4, Width: 480, Gray: true}); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("stale update to old S1 id: %v", err)
	}
	list, err := s.Exports("vid1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != s2.ID {
		t.Fatalf("stale update resurrected old S1: %+v", list)
	}

	s1Again, err := s.UpsertExport("vid1", ExportConfig{ID: s2.ID, FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}
	if s1Again.ID == s1.ID || s1Again.ID == s2.ID {
		t.Fatalf("S1 -> S2 -> S1 reused identity: %s, %s, %s", s1.ID, s2.ID, s1Again.ID)
	}
	if s1Again.CreatedAt <= s2.CreatedAt {
		t.Fatalf("new S1 created_at=%d, want after S2=%d", s1Again.CreatedAt, s2.CreatedAt)
	}
	if _, ok, err := s.Export("vid1", s2.ID); err != nil || ok {
		t.Fatalf("old S2 id survived: ok=%v err=%v", ok, err)
	}
	staleS2 := validAnalysisInput()
	staleS2.AnalysisID = "353eea14-c0d5-4919-baf5-ab7783f7dd16"
	staleS2.Source = ExportSource{FPS: 8, Width: 640, Gray: false}
	if _, err := s.SetExportAnalysis("vid1", s2.ID, staleS2, 20); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("delivery to old S2 id: %v", err)
	}
	list, err = s.Exports("vid1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != s1Again.ID || list[0].Analysis != nil {
		t.Fatalf("unexpected current export after ABA: %+v", list)
	}
}

func TestConcurrentExportUpdatesFromSameIdentityAllowOneWinner(t *testing.T) {
	for i := 0; i < 50; i++ {
		dir := t.TempDir()
		writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
		s := New(dir)
		original, err := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
		if err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		results := make([]ExportConfig, 2)
		errs := make([]error, 2)
		for worker := range 2 {
			worker := worker
			go func() {
				defer wg.Done()
				<-start
				results[worker], errs[worker] = s.UpsertExport("vid1", ExportConfig{
					ID: original.ID, FPS: float64(8 + worker), Width: 640 + worker, Gray: false,
				})
			}()
		}
		close(start)
		wg.Wait()

		winner := -1
		for worker, err := range errs {
			switch {
			case err == nil:
				if winner != -1 {
					t.Fatalf("both stale updates succeeded: %+v", results)
				}
				winner = worker
			case errors.Is(err, ErrExportNotFound):
			default:
				t.Fatalf("update %d error=%v", worker, err)
			}
		}
		if winner == -1 {
			t.Fatalf("neither update succeeded: %v", errs)
		}
		if results[winner].ID == original.ID {
			t.Fatalf("winner retained original id: %+v", results[winner])
		}
		list, err := s.Exports("vid1")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].ID != results[winner].ID || list[0].ID == original.ID {
			t.Fatalf("unexpected list after concurrent updates: %+v", list)
		}
	}
}

func TestSetExportAnalysisIdempotencyAndAttemptOrdering(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)
	cfg, _ := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
	first, err := s.SetExportAnalysis("vid1", cfg.ID, validAnalysisInput(), 20)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := validAnalysisInput()
	duplicate.StartedAt = 101
	duplicate.Parameters.Method = "flow"
	again, err := s.SetExportAnalysis("vid1", cfg.ID, duplicate, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Analysis, first.Analysis) {
		t.Fatalf("duplicate changed stored analysis:\n got  %+v\n want %+v", again.Analysis, first.Analysis)
	}

	older := validAnalysisInput()
	older.AnalysisID = "da7182af-467f-4f37-a673-b3197c164df7"
	older.StartedAt = 99
	if _, err := s.SetExportAnalysis("vid1", cfg.ID, older, 20); !errors.Is(err, ErrExportConflict) {
		t.Fatalf("older error=%v, want ErrExportConflict", err)
	}

	newer := validAnalysisInput()
	newer.AnalysisID = "353eea14-c0d5-4919-baf5-ab7783f7dd16"
	newer.StartedAt = 102
	newer.Parameters.Method = "ecc"
	replaced, err := s.SetExportAnalysis("vid1", cfg.ID, newer, 20)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Analysis.AnalysisID != newer.AnalysisID || replaced.Analysis.Parameters.Method != "ecc" {
		t.Fatalf("new attempt did not overwrite: %+v", replaced.Analysis)
	}
}

func TestSetExportAnalysisMissingExport(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)
	if _, err := s.SetExportAnalysis("vid1", "missing", validAnalysisInput(), 20); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("missing export error=%v, want ErrExportNotFound", err)
	}
	if _, err := s.SetExportAnalysis("missing", "missing", validAnalysisInput(), 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing upload error=%v, want ErrNotFound", err)
	}
}

func TestCorruptExportsJSONIsSurfacedAndNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)
	corrupt := []byte(`[{"id":"broken"`)
	if err := os.WriteFile(s.ExportsPath("vid1"), corrupt, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Exports("vid1"); err == nil {
		t.Fatal("Exports accepted corrupt JSON")
	}
	if _, _, err := s.Export("vid1", "broken"); err == nil {
		t.Fatal("Export accepted corrupt JSON")
	}
	if _, err := s.UpsertExport("vid1", ExportConfig{FPS: 4}); err == nil {
		t.Fatal("UpsertExport overwrote corrupt JSON")
	}
	if _, err := s.DeleteExport("vid1", "broken"); err == nil {
		t.Fatal("DeleteExport accepted corrupt JSON")
	}
	if _, err := s.SetExportAnalysis("vid1", "broken", validAnalysisInput(), 20); err == nil {
		t.Fatal("SetExportAnalysis accepted corrupt JSON")
	}
	got, err := os.ReadFile(s.ExportsPath("vid1"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, corrupt) {
		t.Fatalf("corrupt sidecar changed: got %q want %q", got, corrupt)
	}
}

func TestAtomicExportWriteFailurePreservesPriorSidecar(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can create files in a read-only directory")
	}
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)
	cfg, err := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.ExportsPath("vid1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Errorf("restore directory permissions: %v", err)
		}
	}()

	if _, err := s.UpsertExport("vid1", ExportConfig{ID: cfg.ID, FPS: 8, Width: 480}); err == nil {
		t.Fatal("update unexpectedly succeeded in read-only directory")
	}
	after, err := os.ReadFile(s.ExportsPath("vid1"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("prior sidecar changed after failed write:\n got  %q\n want %q", after, before)
	}
}

func TestSyncParentDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := syncParentDirectory(filepath.Join(dir, "exports.json")); err != nil {
		t.Fatalf("sync existing parent: %v", err)
	}
	if err := syncParentDirectory(filepath.Join(dir, "missing", "exports.json")); err == nil {
		t.Fatal("sync missing parent unexpectedly succeeded")
	}
}

func TestConcurrentExportUpdateAndAnalysisDelivery(t *testing.T) {
	for i := 0; i < 50; i++ {
		dir := t.TempDir()
		writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
		s := New(dir)
		cfg, _ := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var updated ExportConfig
		var updateErr, deliveryErr error
		go func() {
			defer wg.Done()
			<-start
			updated, updateErr = s.UpsertExport("vid1", ExportConfig{ID: cfg.ID, FPS: 5, Width: 480, Gray: true})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, deliveryErr = s.SetExportAnalysis("vid1", cfg.ID, validAnalysisInput(), 20)
		}()
		close(start)
		wg.Wait()
		if updateErr != nil {
			t.Fatal(updateErr)
		}
		if deliveryErr != nil && !errors.Is(deliveryErr, ErrExportNotFound) {
			t.Fatalf("delivery error=%v", deliveryErr)
		}
		if updated.ID == cfg.ID {
			t.Fatalf("update retained old id: %+v", updated)
		}
		if _, ok, err := s.Export("vid1", cfg.ID); err != nil || ok {
			t.Fatalf("old export survived update: ok=%v err=%v", ok, err)
		}
		got, ok, err := s.Export("vid1", updated.ID)
		if err != nil || !ok {
			t.Fatalf("reload: ok=%v err=%v", ok, err)
		}
		if got.FPS != 5 || got.Analysis != nil {
			t.Fatalf("lost update or stale analysis: %+v", got)
		}
	}
}

func TestConcurrentExportDeleteAndAnalysisDelivery(t *testing.T) {
	for i := 0; i < 50; i++ {
		dir := t.TempDir()
		writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
		s := New(dir)
		cfg, _ := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var deleted bool
		var deleteErr, deliveryErr error
		go func() {
			defer wg.Done()
			<-start
			deleted, deleteErr = s.DeleteExport("vid1", cfg.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, deliveryErr = s.SetExportAnalysis("vid1", cfg.ID, validAnalysisInput(), 20)
		}()
		close(start)
		wg.Wait()
		if deleteErr != nil || !deleted {
			t.Fatalf("delete: deleted=%v err=%v", deleted, deleteErr)
		}
		if deliveryErr != nil && !errors.Is(deliveryErr, ErrExportNotFound) {
			t.Fatalf("delivery error=%v", deliveryErr)
		}
		if _, ok, err := s.Export("vid1", cfg.ID); err != nil || ok {
			t.Fatalf("deleted export resurrected: ok=%v err=%v", ok, err)
		}
	}
}

func TestConcurrentUploadDeleteAndAnalysisDeliveryDoesNotOrphanExports(t *testing.T) {
	for i := 0; i < 100; i++ {
		dir := t.TempDir()
		writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
		s := New(dir)
		cfg, err := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
		if err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var deleteErr, deliveryErr error
		go func() {
			defer wg.Done()
			<-start
			deleteErr = s.Delete("vid1")
		}()
		go func() {
			defer wg.Done()
			<-start
			_, deliveryErr = s.SetExportAnalysis("vid1", cfg.ID, validAnalysisInput(), 20)
		}()
		close(start)
		wg.Wait()

		if deleteErr != nil {
			t.Fatal(deleteErr)
		}
		if deliveryErr != nil && !errors.Is(deliveryErr, ErrNotFound) {
			t.Fatalf("delivery error=%v, want nil or ErrNotFound", deliveryErr)
		}
		if _, err := os.Stat(s.InfoPath("vid1")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("upload info survived delete: %v", err)
		}
		if _, err := os.Stat(s.ExportsPath("vid1")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orphan export sidecar survived delete: %v", err)
		}
	}
}

func TestUpsertExportCreatesWithDefaults(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)

	cfg, err := s.UpsertExport("vid1", ExportConfig{}) // no params -> defaults
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID == "" {
		t.Error("expected a generated id")
	}
	if cfg.FPS != 0.1 || cfg.Width != 480 {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.CreatedAt == 0 {
		t.Error("expected created_at to be set")
	}

	list, err := s.Exports("vid1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != cfg.ID {
		t.Errorf("expected one stored record matching, got %+v", list)
	}
}

func TestUpsertExportUpdatesByID(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)

	first, _ := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
	updated, err := s.UpsertExport("vid1", ExportConfig{ID: first.ID, FPS: 8, Width: 640, Gray: false})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID == first.ID {
		t.Errorf("id did not change on source update: %s", first.ID)
	}
	list, _ := s.Exports("vid1")
	if len(list) != 1 {
		t.Fatalf("expected update not insert, got %d records", len(list))
	}
	if list[0].FPS != 8 || list[0].Width != 640 || list[0].Gray != false {
		t.Errorf("update not persisted: %+v", list[0])
	}
}

func TestUpsertExportNormalizesParams(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)

	cfg, _ := s.UpsertExport("vid1", ExportConfig{FPS: 0, Width: -5, Gray: true})
	if cfg.FPS != 0.1 || cfg.Width != 480 {
		t.Errorf("expected normalized defaults, got %+v", cfg)
	}
}

func TestDeleteExport(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)
	cfg, _ := s.UpsertExport("vid1", ExportConfig{})

	ok, err := s.DeleteExport("vid1", cfg.ID)
	if err != nil || !ok {
		t.Fatalf("delete present: ok=%v err=%v", ok, err)
	}
	if list, _ := s.Exports("vid1"); len(list) != 0 {
		t.Errorf("expected empty after delete, got %+v", list)
	}
	ok, _ = s.DeleteExport("vid1", cfg.ID)
	if ok {
		t.Error("deleting absent record should return false")
	}
}

func TestExportsPersistAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	cfg, _ := New(dir).UpsertExport("vid1", ExportConfig{FPS: 6})

	list, err := New(dir).Exports("vid1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != cfg.ID || list[0].FPS != 6 {
		t.Errorf("not persisted across instances: %+v", list)
	}
}

func TestExportsIsolatedPerUpload(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "a.mp4")
	writeUpload(t, dir, "vid2", 100, 100, "b.mp4")
	s := New(dir)
	s.UpsertExport("vid1", ExportConfig{FPS: 4})
	s.UpsertExport("vid2", ExportConfig{FPS: 8})

	if l1, _ := s.Exports("vid1"); len(l1) != 1 || l1[0].FPS != 4 {
		t.Errorf("vid1 isolation: %+v", l1)
	}
	if l2, _ := s.Exports("vid2"); len(l2) != 1 || l2[0].FPS != 8 {
		t.Errorf("vid2 isolation: %+v", l2)
	}
}

func TestExportSingleLookup(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "a.mp4")
	s := New(dir)
	cfg, _ := s.UpsertExport("vid1", ExportConfig{FPS: 8, Width: 640})

	got, ok, err := s.Export("vid1", cfg.ID)
	if err != nil || !ok {
		t.Fatalf("lookup present: ok=%v err=%v", ok, err)
	}
	if got.ID != cfg.ID || got.FPS != 8 || got.Width != 640 {
		t.Errorf("unexpected record: %+v", got)
	}

	if _, ok, _ := s.Export("vid1", "nope"); ok {
		t.Error("missing record should report ok=false")
	}
	if _, _, err := s.Export("missing", "x"); err != ErrNotFound {
		t.Errorf("unknown upload: expected ErrNotFound, got %v", err)
	}
}

func TestExportMethodsUnknownUpload(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Exports("missing"); err != ErrNotFound {
		t.Errorf("Exports: expected ErrNotFound, got %v", err)
	}
	if _, err := s.UpsertExport("missing", ExportConfig{}); err != ErrNotFound {
		t.Errorf("UpsertExport: expected ErrNotFound, got %v", err)
	}
	if _, err := s.DeleteExport("missing", "x"); err != ErrNotFound {
		t.Errorf("DeleteExport: expected ErrNotFound, got %v", err)
	}
}
