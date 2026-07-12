// Package server exposes the management API (upload listing, status, download,
// thumbnails) and serves the embedded client, mounting the embedded tusd
// handler under the configured base path.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/woto/camera-upload/internal/config"
	"github.com/woto/camera-upload/internal/store"
	"github.com/woto/camera-upload/web"
)

// Server holds the dependencies shared by the HTTP handlers.
type Server struct {
	cfg        config.Config
	store      *store.Store
	log        *slog.Logger
	proxyLocks keyedMutex
}

// keyedMutex serializes work per key (used so concurrent requests for the same
// proxy don't transcode it more than once).
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*sync.Mutex{}
	}
	l, ok := k.m[key]
	if !ok {
		l = &sync.Mutex{}
		k.m[key] = l
	}
	k.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// New builds the chi router wiring together the management API, the embedded
// client and the tusd protocol handler mounted at cfg.BasePath.
func New(cfg config.Config, st *store.Store, tusHandler http.Handler, log *slog.Logger) http.Handler {
	s := &Server{cfg: cfg, store: st, log: log}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(requestLogger(log))
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	r.Get("/health", s.health)
	r.Get("/client", s.client)
	r.Get("/tags", s.listTags)

	r.Route("/uploads", func(r chi.Router) {
		r.Get("/", s.listUploads)
		r.Get("/{id}", s.getUpload)
		r.Patch("/{id}", s.updateUpload)
		r.Delete("/{id}", s.deleteUpload)
		r.Get("/{id}/download", s.downloadUpload)
		r.Get("/{id}/frame", s.frame)
		r.Get("/{id}/proxy", s.proxy)
		r.Get("/{id}/thumbnail", s.thumbnail)
		r.Post("/{id}/thumbnail", s.setThumbnail)
		r.Get("/{id}/exports", s.listExports)
		r.Get("/{id}/exports/{exportId}", s.getExport)
		r.Post("/{id}/exports", s.createExport)
		r.Put("/{id}/exports/{exportId}", s.updateExport)
		r.Delete("/{id}/exports/{exportId}", s.deleteExport)
	})

	// Mount the tusd handler at the configured base path. tusd strips the base
	// path itself, so we hand it the request unchanged.
	base := strings.TrimSuffix(cfg.BasePath, "/")
	r.Mount(base, http.StripPrefix(base, tusHandler))

	return r
}

func (s *Server) client(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := strings.Replace(string(web.ClientHTML), "__CAMERA_MOTION_EXTERNAL_URL__", s.cfg.CameraMotionExternalURL, 1)
	html = strings.Replace(html, "__CAMERA_FISHEYE_EXTERNAL_URL__", s.cfg.CameraFisheyeExternalURL, 1)
	html = strings.Replace(html, "__CAMERA_SAM3_EXTERNAL_URL__", s.cfg.CameraSAM3ExternalURL, 1)
	_, _ = w.Write([]byte(html))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeJSON encodes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError sends a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// requestLogger logs each request with method, path, status and duration.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
			)
		})
	}
}

// corsMiddleware allows the Uppy client to call the API from another origin.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, HEAD, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers",
			"Authorization, Content-Type, Location, Tus-Resumable, Upload-Length, Upload-Metadata, Upload-Offset, Upload-Checksum, X-Requested-With, X-HTTP-Method-Override")
		h.Set("Access-Control-Expose-Headers",
			"Location, Tus-Resumable, Tus-Version, Tus-Extension, Tus-Max-Size, Upload-Offset, Upload-Length, Upload-Metadata")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
