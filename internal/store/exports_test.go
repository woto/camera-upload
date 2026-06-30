package store

import (
	"testing"
)

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
	if updated.ID != first.ID {
		t.Errorf("id changed on update: %s -> %s", first.ID, updated.ID)
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
