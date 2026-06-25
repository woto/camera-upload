package server

import (
	"context"
	"encoding/json"
	"errors"
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
	if !up.Completed {
		writeError(w, http.StatusConflict, "upload is not complete")
		return
	}

	at := parseTime(r.URL.Query().Get("t"))
	ctx, cancel := context.WithTimeout(r.Context(), frameTimeout)
	defer cancel()

	img, err := process.ExtractFrame(ctx, s.store.DataPath(id), at)
	if err != nil {
		s.log.Error("extract frame", "id", id, "t", at, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to extract frame")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(img)
}

// setThumbnail regenerates the thumbnail from the frame at the given timestamp.
func (s *Server) setThumbnail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	up, err := s.store.Get(id)
	if err != nil {
		s.notFoundOrError(w, err, "set thumbnail")
		return
	}
	if !up.Completed {
		writeError(w, http.StatusConflict, "upload is not complete")
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

	if err := process.WriteThumbnail(ctx, s.store.DataPath(id), s.store.ThumbPath(id), body.T); err != nil {
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
	if !up.Completed {
		writeError(w, http.StatusConflict, "upload is not complete")
		return
	}

	f, err := os.Open(s.store.DataPath(id))
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
		name = id
	}
	if up.FileType != "" {
		w.Header().Set("Content-Type", up.FileType)
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(name)))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (s *Server) thumbnail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.Get(id); err != nil {
		s.notFoundOrError(w, err, "thumbnail")
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
	s.log.Error(op, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
