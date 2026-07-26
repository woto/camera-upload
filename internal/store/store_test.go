package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeUpload creates a fake tusd upload (data + .info) in dir.
func writeUpload(t *testing.T, dir, id string, size, offset int64, filename string) {
	t.Helper()
	info := `{"ID":"` + id + `","Size":` + itoa(size) + `,"Offset":` + itoa(offset) +
		`,"SizeIsDeferred":false,"MetaData":{"filename":"` + filename + `","filetype":"video/mp4"}}`
	if err := os.WriteFile(filepath.Join(dir, id+".info"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, offset)
	if err := os.WriteFile(filepath.Join(dir, id), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestGetProgress(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	tests := []struct {
		id            string
		size, offset  int64
		wantPercent   float64
		wantCompleted bool
	}{
		{"empty", 1000, 0, 0, false},
		{"half", 1000, 500, 50, false},
		{"full", 1000, 1000, 100, true},
	}
	for _, tc := range tests {
		writeUpload(t, dir, tc.id, tc.size, tc.offset, tc.id+".mp4")
	}

	for _, tc := range tests {
		up, err := s.Get(tc.id)
		if err != nil {
			t.Fatalf("Get(%s): %v", tc.id, err)
		}
		if up.Percent != tc.wantPercent {
			t.Errorf("%s: percent = %v, want %v", tc.id, up.Percent, tc.wantPercent)
		}
		if up.Completed != tc.wantCompleted {
			t.Errorf("%s: completed = %v, want %v", tc.id, up.Completed, tc.wantCompleted)
		}
		if up.Filename != tc.id+".mp4" {
			t.Errorf("%s: filename = %q", tc.id, up.Filename)
		}
	}
}

func TestGetNotFound(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Get("missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListSortedNewestFirst(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	writeUpload(t, dir, "old", 100, 100, "old.mp4")
	// Make sure timestamps differ.
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "old.info"), old, old)
	writeUpload(t, dir, "new", 100, 50, "new.mp4")

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d uploads, want 2", len(list))
	}
	if list[0].ID != "new" {
		t.Errorf("expected newest first, got %s", list[0].ID)
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	writeUpload(t, dir, "x", 10, 10, "x.mp4")
	// Add sidecars.
	_ = os.WriteFile(s.MetaPath("x"), []byte("{}"), 0o644)
	_ = os.WriteFile(s.ThumbPath("x"), []byte("jpg"), 0o644)

	if err := s.Delete("x"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.DataPath("x"), s.InfoPath("x"), s.MetaPath("x"), s.ThumbPath("x")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("file %s still exists", p)
		}
	}
	if err := s.Delete("x"); err != ErrNotFound {
		t.Errorf("second delete: expected ErrNotFound, got %v", err)
	}
}

func TestDeleteWaitsForExportOperations(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	writeUpload(t, dir, "x", 10, 10, "x.mp4")
	if _, err := s.UpsertExport("x", ExportConfig{FPS: 4, Width: 480}); err != nil {
		t.Fatal(err)
	}

	release := s.exportLocks.lock("x")
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	deleted := make(chan error, 1)
	go func() { deleted <- s.Delete("x") }()
	waitForLockRefs(t, &s.exportLocks, "x", 2)
	select {
	case err := <-deleted:
		t.Fatalf("Delete returned while the export lock was held: %v", err)
	default:
	}
	release()
	released = true
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Delete did not finish after the export lock was released")
	}
}

func TestUploadKeyedMutexKeepsWaitersOnOneEntryAndCleansUp(t *testing.T) {
	var locks uploadKeyedMutex
	firstRelease := locks.lock("same")

	secondAcquired := make(chan struct{})
	secondRelease := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		release := locks.lock("same")
		close(secondAcquired)
		<-secondRelease
		release()
		close(secondDone)
	}()
	waitForLockRefs(t, &locks, "same", 2)
	firstRelease()
	select {
	case <-secondAcquired:
	case <-time.After(time.Second):
		t.Fatal("first waiter did not acquire the keyed lock")
	}
	waitForLockRefs(t, &locks, "same", 1)

	thirdAcquired := make(chan struct{})
	thirdRelease := make(chan struct{})
	thirdDone := make(chan struct{})
	go func() {
		release := locks.lock("same")
		close(thirdAcquired)
		<-thirdRelease
		release()
		close(thirdDone)
	}()
	waitForLockRefs(t, &locks, "same", 2)
	select {
	case <-thirdAcquired:
		t.Fatal("a second keyed lock was created while the current holder was active")
	case <-time.After(20 * time.Millisecond):
	}
	close(secondRelease)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second holder did not release")
	}
	select {
	case <-thirdAcquired:
	case <-time.After(time.Second):
		t.Fatal("second waiter did not acquire the keyed lock")
	}
	waitForLockRefs(t, &locks, "same", 1)
	close(thirdRelease)
	select {
	case <-thirdDone:
	case <-time.After(time.Second):
		t.Fatal("third holder did not release")
	}

	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.locks) != 0 {
		t.Fatalf("released keyed lock was retained: %+v", locks.locks)
	}
}

func TestStoreExportLocksAreCleanedAfterOperations(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	for i := 0; i < 100; i++ {
		if _, err := s.Exports(fmt.Sprintf("missing-%d", i)); err != ErrNotFound {
			t.Fatalf("missing export list %d: %v", i, err)
		}
	}
	writeUpload(t, dir, "x", 10, 10, "x.mp4")
	cfg, err := s.UpsertExport("x", ExportConfig{FPS: 4, Width: 480})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Export("x", cfg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteExport("x", cfg.ID); err != nil {
		t.Fatal(err)
	}

	s.exportLocks.mu.Lock()
	defer s.exportLocks.mu.Unlock()
	if len(s.exportLocks.locks) != 0 {
		t.Fatalf("export lock table retained inactive IDs: %+v", s.exportLocks.locks)
	}
}

func waitForLockRefs(t *testing.T, locks *uploadKeyedMutex, id string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		locks.mu.Lock()
		entry := locks.locks[id]
		got := 0
		if entry != nil {
			got = entry.refs
		}
		locks.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("lock %q did not reach ref count %d", id, want)
}

func TestSetUserMetaAndTags(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	writeUpload(t, dir, "a", 100, 100, "a.mp4")
	writeUpload(t, dir, "b", 100, 100, "b.mp4")

	if err := s.SetUserMeta("a", "  Match 1  ", []string{"sport", "indoor", "sport", "", " beach "}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserMeta("b", "Match 2", []string{"indoor"}); err != nil {
		t.Fatal(err)
	}

	a, err := s.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != "Match 1" {
		t.Errorf("title = %q, want trimmed 'Match 1'", a.Title)
	}
	// Deduped, trimmed, empties dropped, order preserved.
	want := []string{"sport", "indoor", "beach"}
	if len(a.Tags) != len(want) {
		t.Fatalf("tags = %v, want %v", a.Tags, want)
	}
	for i := range want {
		if a.Tags[i] != want[i] {
			t.Errorf("tags[%d] = %q, want %q", i, a.Tags[i], want[i])
		}
	}

	all, err := s.AllTags()
	if err != nil {
		t.Fatal(err)
	}
	// Sorted union across both uploads.
	wantAll := []string{"beach", "indoor", "sport"}
	if len(all) != len(wantAll) {
		t.Fatalf("all tags = %v, want %v", all, wantAll)
	}
	for i := range wantAll {
		if all[i] != wantAll[i] {
			t.Errorf("all[%d] = %q, want %q", i, all[i], wantAll[i])
		}
	}
}

func TestSetUserMetaNotFound(t *testing.T) {
	s := New(t.TempDir())
	if err := s.SetUserMeta("missing", "x", nil); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestThumbnailTPersistsAndPreservesMeta(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	writeUpload(t, dir, "v", 100, 100, "v.mp4")

	if err := s.SetUserMeta("v", "Clip", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThumbnailT("v", 7.5); err != nil {
		t.Fatal(err)
	}

	up, err := s.Get("v")
	if err != nil {
		t.Fatal(err)
	}
	if up.ThumbnailT == nil || *up.ThumbnailT != 7.5 {
		t.Fatalf("thumbnail_t = %v, want 7.5", up.ThumbnailT)
	}
	// Title/tags must survive setting the thumbnail timestamp.
	if up.Title != "Clip" || len(up.Tags) != 1 || up.Tags[0] != "x" {
		t.Errorf("meta clobbered: title=%q tags=%v", up.Title, up.Tags)
	}

	// Editing title/tags must not wipe the saved thumbnail timestamp.
	if err := s.SetUserMeta("v", "Renamed", []string{"y", "z"}); err != nil {
		t.Fatal(err)
	}
	up, _ = s.Get("v")
	if up.ThumbnailT == nil || *up.ThumbnailT != 7.5 {
		t.Errorf("thumbnail_t lost after meta edit: %v", up.ThumbnailT)
	}
	if up.Title != "Renamed" {
		t.Errorf("title = %q", up.Title)
	}
}

func TestDurationFromMeta(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	writeUpload(t, dir, "v", 100, 100, "v.mp4")
	meta := `{"format":{"duration":"12.500000"},"streams":[]}`
	if err := os.WriteFile(s.MetaPath("v"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	up, err := s.Get("v")
	if err != nil {
		t.Fatal(err)
	}
	if up.Duration != 12.5 {
		t.Errorf("duration = %v, want 12.5", up.Duration)
	}
}

func TestCleanupIncomplete(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	writeUpload(t, dir, "done", 100, 100, "done.mp4")  // complete -> keep
	writeUpload(t, dir, "fresh", 100, 10, "fresh.mp4") // incomplete but new -> keep
	writeUpload(t, dir, "stale", 100, 10, "stale.mp4") // incomplete + old -> remove
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "stale.info"), old, old)

	n, err := s.CleanupIncomplete(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed %d, want 1", n)
	}
	if _, err := s.Get("stale"); err != ErrNotFound {
		t.Error("stale upload should be removed")
	}
	if _, err := s.Get("done"); err != nil {
		t.Error("done upload should be kept")
	}
}
