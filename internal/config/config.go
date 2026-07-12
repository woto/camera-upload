// Package config loads service configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime settings for the upload service.
type Config struct {
	// Addr is the TCP address the HTTP server listens on (e.g. ":8000").
	Addr string
	// DataDir is the directory where tusd stores uploads and we store sidecars.
	DataDir string
	// BasePath is the URL prefix the tus protocol is served under. Must end with "/".
	BasePath string
	// MaxUploadSize is the maximum allowed size of a single upload in bytes.
	MaxUploadSize int64
	// Thumbnails toggles ffmpeg thumbnail generation on upload completion.
	Thumbnails bool
	// IncompleteTTL is how long an unfinished upload may live before cleanup removes it.
	IncompleteTTL time.Duration
	// CameraMotionExternalURL is the browser-facing base URL of camera-motion.
	CameraMotionExternalURL string
	// CameraFisheyeExternalURL is the browser-facing base URL of camera-fisheye.
	CameraFisheyeExternalURL string
	// CameraSAM3ExternalURL is the browser-facing base URL of camera-sam3.
	CameraSAM3ExternalURL string
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() (Config, error) {
	cfg := Config{
		Addr:          ":" + getenv("PORT", "8000"),
		DataDir:       getenv("DATA_DIR", "./data"),
		BasePath:      getenv("BASE_PATH", "/files/"),
		MaxUploadSize: 10 << 30, // 10 GiB
		Thumbnails:    getenvBool("THUMBNAILS", true),
		IncompleteTTL: 24 * time.Hour,
	}

	var err error
	if cfg.CameraMotionExternalURL, err = requireEnv("CAMERA_MOTION_EXTERNAL_URL"); err != nil {
		return Config{}, err
	}
	if cfg.CameraFisheyeExternalURL, err = requireEnv("CAMERA_FISHEYE_EXTERNAL_URL"); err != nil {
		return Config{}, err
	}
	if cfg.CameraSAM3ExternalURL, err = requireEnv("CAMERA_SAM3_EXTERNAL_URL"); err != nil {
		return Config{}, err
	}

	if v := os.Getenv("MAX_UPLOAD_SIZE"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("invalid MAX_UPLOAD_SIZE %q: %w", v, err)
		}
		cfg.MaxUploadSize = n
	}

	if v := os.Getenv("INCOMPLETE_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid INCOMPLETE_TTL %q: %w", v, err)
		}
		cfg.IncompleteTTL = d
	}

	// tusd requires the base path to end with a slash.
	if cfg.BasePath == "" || cfg.BasePath[len(cfg.BasePath)-1] != '/' {
		cfg.BasePath += "/"
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func requireEnv(key string) (string, error) {
	if v := os.Getenv(key); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s is required", key)
}

func getenvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
