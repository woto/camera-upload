package store

import (
	"os"
	"testing"
)

func TestProcessingStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	writeUpload(t, dir, "vid1", 100, 100, "vid1.mp4")

	want := Processing{Status: ProcessingChecking}
	if err := s.SetProcessing("vid1", want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Processing("vid1")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("processing = %+v, want %+v", got, want)
	}
}

func TestWorkingPathUsesConvertedFileOnlyWhenReadyAndConverted(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	writeUpload(t, dir, "vid1", 100, 100, "vid1.mp4")
	if err := s.SetProcessing("vid1", Processing{
		Status: ProcessingReady, WorkingSource: WorkingConverted,
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.WorkingPath("vid1"); got != s.CFRPath("vid1") {
		t.Fatalf("working path = %q, want %q", got, s.CFRPath("vid1"))
	}
}

func TestDeleteRemovesProcessingAndConvertedFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	writeUpload(t, dir, "vid1", 100, 100, "vid1.mp4")
	if err := s.SetProcessing("vid1", Processing{Status: ProcessingReady}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.CFRPath("vid1"), []byte("cfr"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("vid1"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.ProcessingPath("vid1"), s.CFRPath("vid1")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, err=%v", path, err)
		}
	}
}
