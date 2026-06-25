package tus

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/woto/camera-upload/internal/config"
)

func meta(kv map[string]string) string {
	s := ""
	for k, v := range kv {
		if s != "" {
			s += ","
		}
		s += k + " " + base64.StdEncoding.EncodeToString([]byte(v))
	}
	return s
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	cfg := config.Config{DataDir: t.TempDir(), BasePath: "/files/", MaxUploadSize: 1 << 30}
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Drain completion notifications so the handler never blocks if a test
	// happens to complete an upload.
	go func() {
		for range h.CompleteUploads {
		}
	}()
	// tusd's routed handler expects the base path to be stripped, exactly as
	// the server wires it in production.
	return http.StripPrefix("/files", h.HTTP)
}

func TestCreateVideoUpload(t *testing.T) {
	h := newHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "1024")
	req.Header.Set("Upload-Metadata", meta(map[string]string{"filename": "clip.mp4", "filetype": "video/mp4"}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") == "" {
		t.Error("missing Location header")
	}
}

func TestRejectNonVideo(t *testing.T) {
	h := newHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "1024")
	req.Header.Set("Upload-Metadata", meta(map[string]string{"filename": "doc.pdf", "filetype": "application/pdf"}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415; body=%s", rec.Code, rec.Body.String())
	}
}
