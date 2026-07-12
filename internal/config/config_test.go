package config

import (
	"strings"
	"testing"
)

func setRequiredExternalURLs(t *testing.T) {
	t.Helper()
	t.Setenv("CAMERA_MOTION_EXTERNAL_URL", "http://motion.example")
	t.Setenv("CAMERA_FISHEYE_EXTERNAL_URL", "http://fisheye.example")
}

func TestLoadRequiresCameraMotionExternalURL(t *testing.T) {
	t.Setenv("CAMERA_FISHEYE_EXTERNAL_URL", "http://fisheye.example")
	t.Setenv("CAMERA_MOTION_EXTERNAL_URL", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want missing CAMERA_MOTION_EXTERNAL_URL")
	}
	if !strings.Contains(err.Error(), "CAMERA_MOTION_EXTERNAL_URL") {
		t.Fatalf("Load() error = %q, want CAMERA_MOTION_EXTERNAL_URL", err)
	}
}

func TestLoadRequiresCameraFisheyeExternalURL(t *testing.T) {
	t.Setenv("CAMERA_MOTION_EXTERNAL_URL", "http://motion.example")
	t.Setenv("CAMERA_FISHEYE_EXTERNAL_URL", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want missing CAMERA_FISHEYE_EXTERNAL_URL")
	}
	if !strings.Contains(err.Error(), "CAMERA_FISHEYE_EXTERNAL_URL") {
		t.Fatalf("Load() error = %q, want CAMERA_FISHEYE_EXTERNAL_URL", err)
	}
}

func TestLoadReadsExternalURLs(t *testing.T) {
	setRequiredExternalURLs(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CameraMotionExternalURL != "http://motion.example" {
		t.Fatalf("CameraMotionExternalURL = %q", cfg.CameraMotionExternalURL)
	}
	if cfg.CameraFisheyeExternalURL != "http://fisheye.example" {
		t.Fatalf("CameraFisheyeExternalURL = %q", cfg.CameraFisheyeExternalURL)
	}
}
