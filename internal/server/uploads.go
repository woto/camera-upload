package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/woto/camera-upload/internal/process"
	"github.com/woto/camera-upload/internal/store"
)

const frameTimeout = 30 * time.Second

const defaultPageSize = 20

const maxAnalysisBodyBytes = 2 << 20

// listUploads supports filtering by name (?q=) and tags (?tag=, repeatable, AND
// semantics) plus pagination (?page=, ?page_size=). Results stay newest-first.
func (s *Server) listUploads(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.List()
	if err != nil {
		s.log.Error("list uploads", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list uploads")
		return
	}

	query := r.URL.Query()
	q := strings.ToLower(strings.TrimSpace(query.Get("q")))
	wantTags := normalizeQueryTags(query["tag"])

	filtered := make([]store.Upload, 0, len(all))
	for _, up := range all {
		if q != "" && !matchesName(up, q) {
			continue
		}
		if !hasAllTags(up, wantTags) {
			continue
		}
		filtered = append(filtered, up)
	}

	page := atoiDefault(query.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := atoiDefault(query.Get("page_size"), defaultPageSize)
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > 100 {
		pageSize = 100
	}

	total := len(filtered)
	pages := (total + pageSize - 1) / pageSize

	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	pageItems := filtered[start:end]
	if pageItems == nil {
		pageItems = []store.Upload{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"uploads":   pageItems,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"pages":     pages,
	})
}

// matchesName reports whether the upload's title or filename contains q (q is
// already lower-cased).
func matchesName(up store.Upload, q string) bool {
	return strings.Contains(strings.ToLower(up.Title), q) ||
		strings.Contains(strings.ToLower(up.Filename), q)
}

// hasAllTags reports whether the upload carries every requested tag.
func hasAllTags(up store.Upload, want []string) bool {
	for _, t := range want {
		found := false
		for _, ut := range up.Tags {
			if ut == t {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func normalizeQueryTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	up, err := s.store.Get(id)
	if err != nil {
		s.notFoundOrError(w, err, "get upload")
		return
	}
	writeJSON(w, http.StatusOK, up)
}

// updateUpload sets the title and tags of an upload.
func (s *Server) updateUpload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Title string   `json:"title"`
		Tags  []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := s.store.SetUserMeta(id, body.Title, body.Tags); err != nil {
		s.notFoundOrError(w, err, "update upload")
		return
	}
	up, err := s.store.Get(id)
	if err != nil {
		s.notFoundOrError(w, err, "update upload")
		return
	}
	writeJSON(w, http.StatusOK, up)
}

type exportBody struct {
	FPS   float64 `json:"fps"`
	Width int     `json:"width"`
	Gray  bool    `json:"gray"`
}

// listExports returns an upload's saved export records.
func (s *Server) listExports(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireReadyID(w, chi.URLParam(r, "id")); !ok {
		return
	}
	list, err := s.store.Exports(chi.URLParam(r, "id"))
	if err != nil {
		s.notFoundOrError(w, err, "list exports")
		return
	}
	if list == nil {
		list = []store.ExportConfig{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"exports": list})
}

// getExport returns a single export record.
func (s *Server) getExport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireReadyID(w, chi.URLParam(r, "id")); !ok {
		return
	}
	cfg, ok, err := s.store.Export(chi.URLParam(r, "id"), chi.URLParam(r, "exportId"))
	if err != nil {
		s.notFoundOrError(w, err, "get export")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "export not found")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// putExportAnalysis accepts the completed motion timeline for one saved export.
// camera-motion is the only writer and authenticates with the shared internal token.
func (s *Server) putExportAnalysis(w http.ResponseWriter, r *http.Request) {
	if !s.authorizedInternalRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAnalysisBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input store.MotionAnalysisInput
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	uploadID := chi.URLParam(r, "id")
	upload, err := s.store.Get(uploadID)
	if err != nil {
		s.notFoundOrError(w, err, "get upload for analysis")
		return
	}
	exportID := chi.URLParam(r, "exportId")
	_, exportExists, err := s.store.Export(uploadID, exportID)
	if err != nil {
		s.notFoundOrError(w, err, "get export for analysis")
		return
	}
	if !exportExists {
		writeError(w, http.StatusNotFound, "export not found")
		return
	}
	if upload.Duration <= 0 || math.IsNaN(upload.Duration) || math.IsInf(upload.Duration, 0) {
		writeError(w, http.StatusServiceUnavailable, "video metadata unavailable")
		return
	}
	// SetExportAnalysis rechecks existence and source settings under the export
	// lock, so deletion or mutation after the preflight retains its 404/409 map.
	cfg, err := s.store.SetExportAnalysis(uploadID, exportID, input, upload.Duration)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, cfg)
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrExportNotFound):
		writeError(w, http.StatusNotFound, "upload or export not found")
	case errors.Is(err, store.ErrExportConflict):
		writeError(w, http.StatusConflict, "analysis conflicts with export")
	case errors.Is(err, store.ErrInvalidAnalysis):
		writeError(w, http.StatusBadRequest, "invalid motion analysis")
	default:
		s.log.Error("store export analysis", "upload_id", uploadID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (s *Server) authorizedInternalRequest(r *http.Request) bool {
	want := sha256.Sum256([]byte("Bearer " + s.cfg.InternalToken))
	got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// createExport adds a new export record (params default when omitted).
func (s *Server) createExport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireReadyID(w, chi.URLParam(r, "id")); !ok {
		return
	}
	var body exportBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	cfg, err := s.store.UpsertExport(chi.URLParam(r, "id"),
		store.ExportConfig{FPS: body.FPS, Width: body.Width, Gray: body.Gray})
	if err != nil {
		s.notFoundOrError(w, err, "create export")
		return
	}
	writeJSON(w, http.StatusCreated, cfg)
}

// updateExport updates an existing export record's params.
func (s *Server) updateExport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireReadyID(w, chi.URLParam(r, "id")); !ok {
		return
	}
	var body exportBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	cfg, err := s.store.UpsertExport(chi.URLParam(r, "id"),
		store.ExportConfig{ID: chi.URLParam(r, "exportId"), FPS: body.FPS, Width: body.Width, Gray: body.Gray})
	if err != nil {
		s.notFoundOrError(w, err, "update export")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// deleteExport removes an export record.
func (s *Server) deleteExport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireReadyID(w, chi.URLParam(r, "id")); !ok {
		return
	}
	ok, err := s.store.DeleteExport(chi.URLParam(r, "id"), chi.URLParam(r, "exportId"))
	if err != nil {
		s.notFoundOrError(w, err, "delete export")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": ok})
}

// listTags returns the union of all tags across uploads, for autocomplete.
func (s *Server) listTags(w http.ResponseWriter, _ *http.Request) {
	tags, err := s.store.AllTags()
	if err != nil {
		s.log.Error("list tags", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list tags")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// frame streams a full-resolution JPEG of the frame at ?t=<seconds>. The same
// URL is what gets handed to camera-homography for calibration.
func (s *Server) frame(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	up, err := s.store.Get(id)
	if err != nil {
		s.notFoundOrError(w, err, "frame")
		return
	}
	if !s.requireReady(w, up) {
		return
	}

	at := parseTime(r.URL.Query().Get("t"))
	ctx, cancel := context.WithTimeout(r.Context(), frameTimeout)
	defer cancel()

	img, err := process.ExtractFrame(ctx, s.store.WorkingPath(id), at)
	if err != nil {
		s.log.Error("extract frame", "id", id, "t", at, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to extract frame")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	// Suggest a meaningful filename (with extension) when the frame is saved,
	// while still displaying inline as an <img>/preview.
	name := frameFilename(up, at)
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(name))
	_, _ = w.Write(img)
}

// frameFilename builds a download name like "match-1_12.5s.jpg" from the upload's
// title (or original filename), the timestamp and a .jpg extension.
func frameFilename(up store.Upload, at float64) string {
	base := up.Title
	if base == "" {
		base = up.Filename
	}
	if base == "" {
		base = up.ID
	}
	base = filepath.Base(base)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = sanitizeFilename(base)
	if base == "" {
		base = "frame"
	}
	return fmt.Sprintf("%s_%ss.jpg", base, strconv.FormatFloat(at, 'f', -1, 64))
}

// sanitizeFilename keeps a safe subset of characters for a download filename,
// collapsing anything else to underscores.
func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('_')
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "._")
}

// setThumbnail regenerates the thumbnail from the frame at the given timestamp.
func (s *Server) setThumbnail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	up, err := s.store.Get(id)
	if err != nil {
		s.notFoundOrError(w, err, "set thumbnail")
		return
	}
	if !s.requireReady(w, up) {
		return
	}

	var body struct {
		T float64 `json:"t"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), frameTimeout)
	defer cancel()

	if err := process.WriteThumbnail(ctx, s.store.WorkingPath(id), s.store.ThumbPath(id), body.T); err != nil {
		s.log.Error("set thumbnail", "id", id, "t", body.T, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to set thumbnail")
		return
	}
	// Remember the timestamp so the frame picker can reopen at the same spot.
	if err := s.store.SetThumbnailT(id, body.T); err != nil {
		s.log.Error("persist thumbnail timestamp", "id", id, "t", body.T, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "t": body.T})
}

const proxyTimeout = 30 * time.Minute

// proxy serves a compact transcoded clip (decimated fps, downscaled, optional
// grayscale) for analysis services so they don't pull the multi-GB original.
// The proxy is generated on first request and cached per parameter set.
func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	up, err := s.store.Get(id)
	if err != nil {
		s.notFoundOrError(w, err, "proxy")
		return
	}
	if !s.requireReady(w, up) {
		return
	}

	q := r.URL.Query()
	cfg := store.NormalizeExport(store.ExportConfig{
		FPS:   parseFloatDefault(q.Get("fps"), 4),
		Width: atoiDefault(q.Get("width"), 480),
		Gray:  q.Get("gray") != "0" && q.Get("gray") != "false", // grayscale by default
	})
	s.serveProxy(w, r, id, cfg)
}

// exportProxy serves the canonical proxy for a saved export version. Query
// overrides are intentionally ignored: stored settings define the media.
func (s *Server) exportProxy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := chi.URLParam(r, "id")
	if _, ok := s.requireReadyID(w, id); !ok {
		return
	}
	cfg, ok, err := s.store.Export(id, chi.URLParam(r, "exportId"))
	if err != nil {
		s.notFoundOrError(w, err, "get export proxy")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "export not found")
		return
	}
	// The same version URL can resolve to a new cache file after its settings
	// change. Old validators must therefore never turn the new representation
	// into a 304 response.
	request := r.Clone(r.Context())
	request.Header = r.Header.Clone()
	for _, header := range []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "If-Range"} {
		request.Header.Del(header)
	}
	s.serveProxy(w, request, id, cfg)
}

func (s *Server) serveProxy(w http.ResponseWriter, r *http.Request, id string, cfg store.ExportConfig) {
	cfg = store.NormalizeExport(cfg)
	fps, width, gray := cfg.FPS, cfg.Width, cfg.Gray

	dst := s.store.ProxyPath(id, fps, width, gray)

	if _, err := os.Stat(dst); err != nil {
		// Cache miss: generate (serialized per cache key so we transcode once).
		unlock := s.proxyLocks.lock(dst)
		defer unlock()
		if _, err := os.Stat(dst); err != nil { // re-check after acquiring the lock
			ctx, cancel := context.WithTimeout(context.Background(), proxyTimeout)
			defer cancel()
			// Unique temp name per generation: a leftover ffmpeg orphaned by a
			// restart can never write to the same temp as a new run (which would
			// corrupt the cached file). Cleaned up on every exit path.
			tmp := fmt.Sprintf("%s.%d.tmp", dst, time.Now().UnixNano())
			defer os.Remove(tmp)
			if err := process.WriteProxy(ctx, s.store.WorkingPath(id), tmp, fps, width, gray); err != nil {
				s.log.Error("build proxy", "id", id, "err", err)
				writeError(w, http.StatusInternalServerError, "failed to build proxy")
				return
			}
			if err := os.Rename(tmp, dst); err != nil {
				s.log.Error("finalize proxy", "id", id, "err", err)
				writeError(w, http.StatusInternalServerError, "failed to finalize proxy")
				return
			}
		}
	}

	f, err := os.Open(dst)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy unavailable")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stat failed")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, filepath.Base(dst), fi.ModTime(), f)
}

func parseFloatDefault(s string, def float64) float64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

func parseTime(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func (s *Server) deleteUpload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.Delete(id); err != nil {
		s.notFoundOrError(w, err, "delete upload")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) downloadUpload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	up, err := s.store.Get(id)
	if err != nil {
		s.notFoundOrError(w, err, "download upload")
		return
	}
	if !s.requireReady(w, up) {
		return
	}
	s.serveVideo(w, r, up, s.store.WorkingPath(id))
}

func (s *Server) downloadOriginal(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	up, err := s.store.Get(id)
	if err != nil {
		s.notFoundOrError(w, err, "download original")
		return
	}
	if !s.requireReady(w, up) {
		return
	}
	s.serveVideo(w, r, up, s.store.DataPath(id))
}

func (s *Server) retryProcessing(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	up, err := s.store.Get(id)
	if err != nil {
		s.notFoundOrError(w, err, "retry processing")
		return
	}
	if up.Processing.Status != store.ProcessingFailed {
		writeError(w, http.StatusConflict, "processing is not failed")
		return
	}
	if s.retry == nil {
		writeError(w, http.StatusServiceUnavailable, "retry unavailable")
		return
	}
	if err := s.retry(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retry processing")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) requireReady(w http.ResponseWriter, up store.Upload) bool {
	if !up.Completed {
		writeError(w, http.StatusConflict, "upload is not complete")
		return false
	}
	if up.Processing.Status == store.ProcessingReady {
		return true
	}
	if up.Processing.Status == store.ProcessingFailed {
		writeError(w, http.StatusConflict, "processing failed")
	} else {
		writeError(w, http.StatusConflict, "video is processing")
	}
	return false
}

func (s *Server) requireReadyID(w http.ResponseWriter, id string) (store.Upload, bool) {
	up, err := s.store.Get(id)
	if err != nil {
		s.notFoundOrError(w, err, "get upload")
		return store.Upload{}, false
	}
	return up, s.requireReady(w, up)
}

func (s *Server) serveVideo(w http.ResponseWriter, r *http.Request, up store.Upload, path string) {
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "data not found")
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stat failed")
		return
	}

	name := up.Filename
	if name == "" {
		name = up.ID
	}
	if up.FileType != "" {
		w.Header().Set("Content-Type", up.FileType)
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(name)))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (s *Server) thumbnail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, ok := s.requireReadyID(w, id); !ok {
		return
	}
	path := s.store.ThumbPath(id)
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusNotFound, "thumbnail not available")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, path)
}

// notFoundOrError maps a store error to the appropriate HTTP response.
func (s *Server) notFoundOrError(w http.ResponseWriter, err error, op string) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "upload not found")
		return
	}
	if errors.Is(err, store.ErrExportNotFound) {
		writeError(w, http.StatusNotFound, "export not found")
		return
	}
	s.log.Error(op, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
