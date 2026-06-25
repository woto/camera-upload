package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/woto/camera-upload/internal/config"
	"github.com/woto/camera-upload/internal/store"
)

func newTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, BasePath: "/files/"}
	st := store.New(dir)
	tusStub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, st, tusStub, log), dir
}

func writeUpload(t *testing.T, dir, id string, size, offset int64) {
	t.Helper()
	info := `{"ID":"` + id + `","Size":` + strconv.FormatInt(size, 10) +
		`,"Offset":` + strconv.FormatInt(offset, 10) +
		`,"SizeIsDeferred":false,"MetaData":{"filename":"clip.mp4","filetype":"video/mp4"}}`
	if err := os.WriteFile(filepath.Join(dir, id+".info"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id), make([]byte, offset), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHealth(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
}

func TestClientServed(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
}

func TestListAndGetUpload(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "abc", 100, 50)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var listResp struct {
		Uploads []store.Upload `json:"uploads"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp.Uploads) != 1 || listResp.Uploads[0].ID != "abc" {
		t.Fatalf("unexpected list: %+v", listResp.Uploads)
	}
	if listResp.Uploads[0].Percent != 50 {
		t.Errorf("percent = %v, want 50", listResp.Uploads[0].Percent)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/abc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
}

func TestGetUploadNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteUpload(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "del", 100, 100)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/uploads/del", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/del", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("after delete status = %d, want 404", rec.Code)
	}
}

func TestDownloadIncomplete(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "part", 100, 50) // not complete

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/part/download", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestDownloadComplete(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "full", 100, 100)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/full/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd == "" {
		t.Error("missing Content-Disposition")
	}
}

func TestUpdateUploadAndTags(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "u1", 100, 100)

	body := strings.NewReader(`{"title":"My Clip","tags":["a","b","a"]}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/uploads/u1", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d", rec.Code)
	}
	var up store.Upload
	_ = json.Unmarshal(rec.Body.Bytes(), &up)
	if up.Title != "My Clip" {
		t.Errorf("title = %q", up.Title)
	}
	if len(up.Tags) != 2 {
		t.Errorf("tags = %v, want deduped [a b]", up.Tags)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tags", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("tags status = %d", rec.Code)
	}
	var tagsResp struct {
		Tags []string `json:"tags"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tagsResp)
	if len(tagsResp.Tags) != 2 {
		t.Errorf("tags = %v, want [a b]", tagsResp.Tags)
	}
}

func TestListFilterAndPaginate(t *testing.T) {
	srv, dir := newTestServer(t)
	// Five completed uploads with titles/tags.
	for i := 1; i <= 5; i++ {
		id := "id" + strconv.Itoa(i)
		writeUpload(t, dir, id, 100, 100)
	}
	s := store.New(dir)
	_ = s.SetUserMeta("id1", "Beach Final", []string{"beach", "2026"})
	_ = s.SetUserMeta("id2", "Indoor Final", []string{"indoor", "2026"})
	_ = s.SetUserMeta("id3", "Beach Semi", []string{"beach"})
	_ = s.SetUserMeta("id4", "Indoor Semi", []string{"indoor"})
	_ = s.SetUserMeta("id5", "Practice", []string{"beach", "2026"})

	get := func(query string) map[string]any {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d for %q", rec.Code, query)
		}
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp
	}

	// Name filter (case-insensitive substring on title).
	r := get("?q=beach")
	if int(r["total"].(float64)) != 2 {
		t.Errorf("q=beach total = %v, want 2", r["total"])
	}

	// Single tag.
	r = get("?tag=beach")
	if int(r["total"].(float64)) != 3 {
		t.Errorf("tag=beach total = %v, want 3", r["total"])
	}

	// Multiple tags (AND).
	r = get("?tag=beach&tag=2026")
	if int(r["total"].(float64)) != 2 {
		t.Errorf("tag=beach&2026 total = %v, want 2", r["total"])
	}

	// Pagination.
	r = get("?page=1&page_size=2")
	if int(r["total"].(float64)) != 5 || int(r["pages"].(float64)) != 3 {
		t.Errorf("pagination total/pages = %v/%v, want 5/3", r["total"], r["pages"])
	}
	if got := len(r["uploads"].([]any)); got != 2 {
		t.Errorf("page 1 items = %d, want 2", got)
	}
	r = get("?page=3&page_size=2")
	if got := len(r["uploads"].([]any)); got != 1 {
		t.Errorf("page 3 items = %d, want 1", got)
	}
	// Out-of-range page returns empty, not an error.
	r = get("?page=99&page_size=2")
	if got := len(r["uploads"].([]any)); got != 0 {
		t.Errorf("page 99 items = %d, want 0", got)
	}
}

func TestFrameIncomplete(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "p", 100, 50) // not complete
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/p/frame?t=2", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestSetThumbnailIncomplete(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "p", 100, 50)
	body := strings.NewReader(`{"t":3}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/uploads/p/thumbnail", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestTusMounted(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/files/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("tus stub status = %d, want 204", rec.Code)
	}
}
