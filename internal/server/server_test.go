package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/woto/camera-upload/internal/config"
	"github.com/woto/camera-upload/internal/store"
)

func newTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, BasePath: "/files/", InternalToken: "test-internal-token"}
	st := store.New(dir)
	tusStub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, st, tusStub, log), dir
}

func writeDuration(t *testing.T, dir, id string, duration float64) {
	t.Helper()
	raw := fmt.Sprintf(`{"format":{"duration":%q}}`, strconv.FormatFloat(duration, 'f', -1, 64))
	if err := os.WriteFile(filepath.Join(dir, id+".meta.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

func validAnalysisJSON() string {
	return `{"schema_version":1,"analysis_id":"7f83b47e-5f97-4b24-a5e0-2c02b8ca1527","started_at":100,"source":{"fps":4,"width":480,"gray":true},"duration":20,"parameters":{"method":"affine","mask":true,"roi":"full","enter":2,"settle":0.5,"settle_samples":2,"min_segment":1,"features":500,"min_inliers":20},"segments":[{"start":0,"end":8,"kind":"stable"},{"start":8,"end":10,"kind":"transition"},{"start":10,"end":20,"kind":"stable"}]}`
}

func analysisReq(srv http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
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
	body := rec.Body.String()
	if !strings.Contains(body, "<title>Camera Upload</title>") {
		t.Errorf("client title does not identify Camera Upload")
	}
	if !strings.Contains(body, "<h1>Camera Upload</h1>") {
		t.Errorf("client header does not identify Camera Upload")
	}
}

func TestClientReadsAnalysisBadgeLocally(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "!!rec.analysis") {
		t.Error("local analysis badge missing")
	}
	if strings.Contains(body, "'/results?ids='") {
		t.Error("legacy presence request remains")
	}
}

func TestClientUsesCanonicalVersionProxyURL(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "'/exports/' + rec.id + '/proxy'") {
		t.Error("export row does not use the canonical version-proxy URL")
	}
}

func TestClientRefreshesExportAfterSavingSettings(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "if (response.ok) await renderExports(id, box);") {
		t.Error("saving export settings does not refresh the row from the returned server state")
	}
}

func TestClientInjectsCameraMotionURL(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, BasePath: "/files/", CameraMotionExternalURL: "http://motion.example:7000", InternalToken: "test-internal-token"}
	st := store.New(dir)
	tusStub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, tusStub, log)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "http://motion.example:7000") {
		t.Errorf("served client does not contain the configured camera-motion URL")
	}
	if strings.Contains(body, "__CAMERA_MOTION_EXTERNAL_URL__") {
		t.Errorf("served client still contains the unreplaced placeholder")
	}
}

func TestClientInjectsCameraFisheyeURL(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, BasePath: "/files/", CameraFisheyeExternalURL: "http://fisheye.example:7400", InternalToken: "test-internal-token"}
	st := store.New(dir)
	tusStub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, tusStub, log)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "http://fisheye.example:7400") {
		t.Errorf("served client does not contain the configured camera-fisheye URL")
	}
	if strings.Contains(body, "__CAMERA_FISHEYE_EXTERNAL_URL__") {
		t.Errorf("served client still contains the unreplaced placeholder")
	}
}

func TestClientInjectsCameraSAM3URL(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, BasePath: "/files/", CameraSAM3ExternalURL: "http://sam3.example:8500", InternalToken: "test-internal-token"}
	st := store.New(dir)
	tusStub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, tusStub, log)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "http://sam3.example:8500") {
		t.Errorf("served client does not contain the configured camera-sam3 URL")
	}
	if strings.Contains(body, "__CAMERA_SAM3_EXTERNAL_URL__") {
		t.Errorf("served client still contains the unreplaced placeholder")
	}
}

func TestClientOpensCameraSAM3ObjectSearch(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, BasePath: "/files/", CameraSAM3ExternalURL: "http://sam3.example:8500", InternalToken: "test-internal-token"}
	st := store.New(dir)
	tusStub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, tusStub, log)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "CAMERA_SAM3_EXTERNAL_URL + '/object-search'") {
		t.Errorf("served client does not open the camera-sam3 object-search page")
	}
	if strings.Contains(body, "CAMERA_SAM3_EXTERNAL_URL + '/client'") {
		t.Errorf("served client still opens the camera-sam3 landing page")
	}
}

func doReq(srv http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func TestExportRecordsCRUD(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "vid1", 100, 100)

	rec := doReq(srv, http.MethodPost, "/uploads/vid1/exports", `{"fps":4,"width":480,"gray":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d", rec.Code)
	}
	var created store.ExportConfig
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == "" || created.FPS != 4 {
		t.Fatalf("unexpected created record: %+v", created)
	}

	rec = doReq(srv, http.MethodGet, "/uploads/vid1/exports", "")
	var listResp struct {
		Exports []store.ExportConfig `json:"exports"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp.Exports) != 1 || listResp.Exports[0].ID != created.ID {
		t.Fatalf("unexpected list: %+v", listResp.Exports)
	}

	rec = doReq(srv, http.MethodPut, "/uploads/vid1/exports/"+created.ID, `{"fps":8,"width":640,"gray":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d", rec.Code)
	}
	var updated store.ExportConfig
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.ID != created.ID || updated.FPS != 8 || updated.Width != 640 {
		t.Fatalf("update not applied: %+v", updated)
	}

	rec = doReq(srv, http.MethodDelete, "/uploads/vid1/exports/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	var del struct {
		Deleted bool `json:"deleted"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &del)
	if !del.Deleted {
		t.Errorf("expected deleted=true")
	}

	rec = doReq(srv, http.MethodGet, "/uploads/vid1/exports", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp.Exports) != 0 {
		t.Errorf("expected empty after delete, got %+v", listResp.Exports)
	}
}

func TestGetSingleExport(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "vid1", 100, 100)

	rec := doReq(srv, http.MethodPost, "/uploads/vid1/exports", `{"fps":8,"width":640,"gray":false}`)
	var created store.ExportConfig
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = doReq(srv, http.MethodGet, "/uploads/vid1/exports/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get existing: status = %d", rec.Code)
	}
	var got store.ExportConfig
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ID != created.ID || got.FPS != 8 {
		t.Errorf("unexpected record: %+v", got)
	}

	if rec := doReq(srv, http.MethodGet, "/uploads/vid1/exports/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get missing record: status = %d", rec.Code)
	}
}

func TestPutExportAnalysisRequiresExactBearerToken(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "vid1", 100, 100)
	writeDuration(t, dir, "vid1", 20)
	cfg, err := store.New(dir).UpsertExport("vid1", store.ExportConfig{FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}
	path := "/uploads/vid1/exports/" + cfg.ID + "/analysis"

	for _, token := range []string{"", "wrong-token", "test-internal-token "} {
		rec := analysisReq(srv, http.MethodPut, path, validAnalysisJSON(), token)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want 401", token, rec.Code)
		}
	}
}

func TestPutExportAnalysisPersistsAuthorizedResult(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "vid1", 100, 100)
	writeDuration(t, dir, "vid1", 20)
	cfg, err := store.New(dir).UpsertExport("vid1", store.ExportConfig{FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}

	rec := analysisReq(srv, http.MethodPut, "/uploads/vid1/exports/"+cfg.ID+"/analysis", validAnalysisJSON(), "test-internal-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got store.ExportConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Analysis == nil || got.Analysis.AnalysisID == "" || got.Analysis.Duration != 20 {
		t.Fatalf("analysis response = %+v", got.Analysis)
	}

	rec = doReq(srv, http.MethodGet, "/uploads/vid1/exports/"+cfg.ID, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Analysis == nil {
		t.Fatal("analysis missing from export detail")
	}
}

func TestPutExportAnalysisRoundTripsThroughExportList(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "vid1", 100, 100)
	writeDuration(t, dir, "vid1", 20)
	cfg, err := store.New(dir).UpsertExport("vid1", store.ExportConfig{FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}

	put := analysisReq(srv, http.MethodPut, "/uploads/vid1/exports/"+cfg.ID+"/analysis", validAnalysisJSON(), "test-internal-token")
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", put.Code, put.Body.String())
	}

	list := doReq(srv, http.MethodGet, "/uploads/vid1/exports", "")
	if list.Code != http.StatusOK {
		t.Fatalf("GET list status = %d, body = %s", list.Code, list.Body.String())
	}
	var response struct {
		Exports []store.ExportConfig `json:"exports"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Exports) != 1 {
		t.Fatalf("exports = %+v, want one record", response.Exports)
	}
	analysis := response.Exports[0].Analysis
	if analysis == nil {
		t.Fatal("analysis missing from export list")
	}
	if analysis.AnalysisID != "7f83b47e-5f97-4b24-a5e0-2c02b8ca1527" {
		t.Errorf("analysis_id = %q", analysis.AnalysisID)
	}
	if len(analysis.Segments) != 3 || analysis.Segments[0].Kind != "stable" || analysis.Segments[2].End != 20 {
		t.Errorf("segments = %+v", analysis.Segments)
	}
	if analysis.CreatedAt <= 0 {
		t.Errorf("created_at = %d, want positive server timestamp", analysis.CreatedAt)
	}
}

func TestPutExportAnalysisReturnsNotFound(t *testing.T) {
	srv, dir := newTestServer(t)
	if rec := analysisReq(srv, http.MethodPut, "/uploads/missing/exports/nope/analysis", validAnalysisJSON(), "test-internal-token"); rec.Code != http.StatusNotFound {
		t.Errorf("missing upload: status = %d, want 404", rec.Code)
	}

	writeUpload(t, dir, "vid1", 100, 100)
	writeDuration(t, dir, "vid1", 20)
	if rec := analysisReq(srv, http.MethodPut, "/uploads/vid1/exports/nope/analysis", validAnalysisJSON(), "test-internal-token"); rec.Code != http.StatusNotFound {
		t.Errorf("missing export: status = %d, want 404", rec.Code)
	}
}

func TestPutExportAnalysisChecksExportBeforeDurationAvailability(t *testing.T) {
	corruptMetadata := `{"format":`
	for _, tt := range []struct {
		name           string
		meta           *string
		exportState    string
		wantStatusCode int
	}{
		{name: "existing export and missing metadata", exportState: "exists", wantStatusCode: http.StatusServiceUnavailable},
		{name: "existing export and corrupt metadata", meta: &corruptMetadata, exportState: "exists", wantStatusCode: http.StatusServiceUnavailable},
		{name: "missing export and missing metadata", exportState: "missing", wantStatusCode: http.StatusNotFound},
		{name: "missing export and corrupt metadata", meta: &corruptMetadata, exportState: "missing", wantStatusCode: http.StatusNotFound},
		{name: "corrupt exports and missing metadata", exportState: "corrupt", wantStatusCode: http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, dir := newTestServer(t)
			writeUpload(t, dir, "vid1", 100, 100)
			if tt.meta != nil {
				if err := os.WriteFile(filepath.Join(dir, "vid1.meta.json"), []byte(*tt.meta), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			exportID := "missing"
			switch tt.exportState {
			case "exists":
				cfg, err := store.New(dir).UpsertExport("vid1", store.ExportConfig{FPS: 4, Width: 480, Gray: true})
				if err != nil {
					t.Fatal(err)
				}
				exportID = cfg.ID
			case "corrupt":
				if err := os.WriteFile(filepath.Join(dir, "vid1.exports.json"), []byte(`[{`), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			rec := analysisReq(srv, http.MethodPut, "/uploads/vid1/exports/"+exportID+"/analysis", validAnalysisJSON(), "test-internal-token")
			if rec.Code != tt.wantStatusCode {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatusCode, rec.Body.String())
			}
			var response map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			wantError := map[int]string{
				http.StatusNotFound:            "export not found",
				http.StatusInternalServerError: "internal error",
				http.StatusServiceUnavailable:  "video metadata unavailable",
			}[tt.wantStatusCode]
			if response["error"] != wantError {
				t.Errorf("error = %q, want %q", response["error"], wantError)
			}
		})
	}
}

func TestPutExportAnalysisRejectsInvalidJSONBodies(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "vid1", 100, 100)
	writeDuration(t, dir, "vid1", 20)
	cfg, err := store.New(dir).UpsertExport("vid1", store.ExportConfig{FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}
	path := "/uploads/vid1/exports/" + cfg.ID + "/analysis"
	unknown := strings.Replace(validAnalysisJSON(), `"schema_version":1`, `"schema_version":1,"unexpected":true`, 1)
	trailing := validAnalysisJSON() + `{}`
	overflow := validAnalysisJSON() + strings.Repeat(" ", (2<<20)+1)

	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"schema_version":`},
		{name: "unknown field", body: unknown},
		{name: "trailing json", body: trailing},
		{name: "over two mebibytes", body: overflow},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := analysisReq(srv, http.MethodPut, path, tt.body, "test-internal-token")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPutExportAnalysisMapsInvalidAndConflict(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "vid1", 100, 100)
	writeDuration(t, dir, "vid1", 20)
	cfg, err := store.New(dir).UpsertExport("vid1", store.ExportConfig{FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}
	path := "/uploads/vid1/exports/" + cfg.ID + "/analysis"

	invalid := strings.Replace(validAnalysisJSON(), `"schema_version":1`, `"schema_version":2`, 1)
	if rec := analysisReq(srv, http.MethodPut, path, invalid, "test-internal-token"); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid: status = %d, want 400", rec.Code)
	}
	conflict := strings.Replace(validAnalysisJSON(), `"fps":4`, `"fps":5`, 1)
	if rec := analysisReq(srv, http.MethodPut, path, conflict, "test-internal-token"); rec.Code != http.StatusConflict {
		t.Errorf("conflict: status = %d, want 409", rec.Code)
	}

	newer := strings.Replace(validAnalysisJSON(), `"started_at":100`, `"started_at":200`, 1)
	newer = strings.Replace(newer, "7f83b47e-5f97-4b24-a5e0-2c02b8ca1527", "9a8f7266-b7ad-491d-b269-5b8e34c516c1", 1)
	if rec := analysisReq(srv, http.MethodPut, path, newer, "test-internal-token"); rec.Code != http.StatusOK {
		t.Fatalf("newer: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	older := strings.Replace(validAnalysisJSON(), "7f83b47e-5f97-4b24-a5e0-2c02b8ca1527", "00f1bd70-2cf0-4a31-a76f-e11d0ed79ecf", 1)
	if rec := analysisReq(srv, http.MethodPut, path, older, "test-internal-token"); rec.Code != http.StatusConflict {
		t.Errorf("older: status = %d, want 409", rec.Code)
	}
}

func TestVersionProxyUsesStoredSettingsAndIgnoresQuery(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "vid1", 100, 100)
	st := store.New(dir)
	cfg, err := st.UpsertExport("vid1", store.ExportConfig{FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("canonical-version-proxy")
	if err := os.WriteFile(st.ProxyPath("vid1", 4, 480, true), want, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := doReq(srv, http.MethodGet, "/uploads/vid1/exports/"+cfg.ID+"/proxy?fps=60&width=1920&gray=false", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("body = %q, want stored proxy %q", rec.Body.Bytes(), want)
	}
}

func TestVersionProxyDoesNotReuseValidatorsAfterSettingsChange(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "vid1", 100, 100)
	st := store.New(dir)
	cfg, err := st.UpsertExport("vid1", store.ExportConfig{FPS: 4, Width: 480, Gray: true})
	if err != nil {
		t.Fatal(err)
	}
	fixedTime := time.Unix(1_700_000_000, 0)
	oldPath := st.ProxyPath("vid1", 4, 480, true)
	if err := os.WriteFile(oldPath, []byte("old-version-proxy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldPath, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	url := "/uploads/vid1/exports/" + cfg.ID + "/proxy"

	first := doReq(srv, http.MethodGet, url, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("first Cache-Control = %q, want no-store", got)
	}
	validator := first.Header().Get("Last-Modified")
	if validator == "" {
		t.Fatal("first response has no Last-Modified validator")
	}

	if _, err := st.UpsertExport("vid1", store.ExportConfig{ID: cfg.ID, FPS: 8, Width: 640, Gray: false}); err != nil {
		t.Fatal(err)
	}
	newPath := st.ProxyPath("vid1", 8, 640, false)
	want := []byte("new-version-proxy")
	if err := os.WriteFile(newPath, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("If-Modified-Since", validator)
	second := httptest.NewRecorder()
	srv.ServeHTTP(second, req)
	if second.Code != http.StatusOK {
		t.Fatalf("conditional status = %d, want 200", second.Code)
	}
	if got := second.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("conditional Cache-Control = %q, want no-store", got)
	}
	if !bytes.Equal(second.Body.Bytes(), want) {
		t.Fatalf("conditional body = %q, want %q", second.Body.Bytes(), want)
	}
}

func TestFreeFormProxyUsesCanonicalNormalization(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "vid1", 100, 100)
	st := store.New(dir)
	want := []byte("normalized-free-form-proxy")
	if err := os.WriteFile(st.ProxyPath("vid1", 0.1, 64, false), want, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := doReq(srv, http.MethodGet, "/uploads/vid1/proxy?fps=0.01&width=1&gray=false", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("body = %q, want normalized proxy %q", rec.Body.Bytes(), want)
	}
}

func TestExportsUnknownUpload(t *testing.T) {
	srv, _ := newTestServer(t)
	if rec := doReq(srv, http.MethodGet, "/uploads/missing/exports", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET unknown: status = %d", rec.Code)
	}
	if rec := doReq(srv, http.MethodPost, "/uploads/missing/exports", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("POST unknown: status = %d", rec.Code)
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

func TestDownloadUsesWorkingFileAndOriginalUsesSource(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "v", 8, 8)
	st := store.New(dir)
	if err := os.WriteFile(st.DataPath("v"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.CFRPath("v"), []byte("converted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProcessing("v", store.Processing{Status: store.ProcessingReady, WorkingSource: store.WorkingConverted}); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(srv, http.MethodGet, "/uploads/v/download", ""); rec.Code != http.StatusOK || rec.Body.String() != "converted" {
		t.Fatalf("download: %d %q", rec.Code, rec.Body.String())
	}
	if rec := doReq(srv, http.MethodGet, "/uploads/v/original", ""); rec.Code != http.StatusOK || rec.Body.String() != "original" {
		t.Fatalf("original: %d %q", rec.Code, rec.Body.String())
	}
}

func TestDownloadBlocksWhileProcessing(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "v", 100, 100)
	if err := store.New(dir).SetProcessing("v", store.Processing{Status: store.ProcessingChecking}); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(srv, http.MethodGet, "/uploads/v/download", ""); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestVideoOperationsBlockWhileProcessing(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "v", 100, 100)
	if err := store.New(dir).SetProcessing("v", store.Processing{Status: store.ProcessingChecking}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/uploads/v/frame", "/uploads/v/proxy", "/uploads/v/thumbnail", "/uploads/v/exports"} {
		if rec := doReq(srv, http.MethodGet, path, ""); rec.Code != http.StatusConflict {
			t.Fatalf("%s: status=%d", path, rec.Code)
		}
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

func TestProxyGeneratesAndCaches(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	srv, dir := newTestServer(t)

	// Put a real video at the upload's data path and mark it complete.
	id := "vid"
	dataPath := filepath.Join(dir, id)
	tmp := dataPath + ".src.mp4"
	out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i",
		"testsrc=duration=2:size=640x480:rate=30", "-pix_fmt", "yuv420p", tmp).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg gen: %v\n%s", err, out)
	}
	if err := os.Rename(tmp, dataPath); err != nil {
		t.Fatal(err)
	}
	size, _ := os.Stat(dataPath)
	info := `{"ID":"` + id + `","Size":` + strconv.FormatInt(size.Size(), 10) +
		`,"Offset":0,"SizeIsDeferred":false,"MetaData":{"filename":"c.mp4","filetype":"video/mp4"}}`
	if err := os.WriteFile(filepath.Join(dir, id+".info"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/"+id+"/proxy?fps=2&width=160", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("content-type = %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("empty proxy body")
	}
	// Proxy must be cached and smaller than the original.
	proxyPath := filepath.Join(dir, id+".proxy_fps2_w160_g1.mp4")
	pi, err := os.Stat(proxyPath)
	if err != nil {
		t.Fatalf("proxy not cached: %v", err)
	}
	if pi.Size() >= size.Size() {
		t.Errorf("proxy (%d) not smaller than original (%d)", pi.Size(), size.Size())
	}
}

func TestProxyIncomplete(t *testing.T) {
	srv, dir := newTestServer(t)
	writeUpload(t, dir, "p", 100, 50) // not complete
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/p/proxy", nil))
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
