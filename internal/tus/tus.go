// Package tus wires up the embedded tusd handler: it configures the filestore
// backend, enforces upload validation, and exposes the completed-uploads
// channel so the rest of the service can react to finished uploads.
package tus

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tus/tusd/v2/pkg/filestore"
	"github.com/tus/tusd/v2/pkg/handler"

	"github.com/woto/camera-upload/internal/config"
)

// Handler bundles the tusd HTTP handler with its completed-uploads channel.
type Handler struct {
	HTTP            http.Handler
	CompleteUploads chan handler.HookEvent
}

// New constructs the embedded tusd handler backed by a local filestore.
func New(cfg config.Config) (*Handler, error) {
	store := filestore.New(cfg.DataDir)
	composer := handler.NewStoreComposer()
	store.UseIn(composer)

	h, err := handler.NewHandler(handler.Config{
		BasePath:                cfg.BasePath,
		StoreComposer:           composer,
		MaxSize:                 cfg.MaxUploadSize,
		NotifyCompleteUploads:   true,
		PreUploadCreateCallback: validateVideo,
	})
	if err != nil {
		return nil, fmt.Errorf("create tusd handler: %w", err)
	}

	return &Handler{HTTP: h, CompleteUploads: h.CompleteUploads}, nil
}

// validateVideo rejects uploads whose declared content type is not a video.
// The check is intentionally lenient: a missing filetype is allowed so that
// clients which omit metadata are not blocked, but a present, non-video type
// is refused with 415.
func validateVideo(hook handler.HookEvent) (handler.HTTPResponse, handler.FileInfoChanges, error) {
	filetype := hook.Upload.MetaData["filetype"]
	if filetype != "" && !strings.HasPrefix(filetype, "video/") {
		// Returning a tusd Error carries the desired status code through to the
		// client; a plain error would surface as 500.
		err := handler.NewError(
			"ERR_UNSUPPORTED_MEDIA_TYPE",
			fmt.Sprintf("unsupported media type %q: only video/* is accepted", filetype),
			http.StatusUnsupportedMediaType,
		)
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, err
	}
	return handler.HTTPResponse{}, handler.FileInfoChanges{}, nil
}
