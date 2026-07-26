package config

import (
	"strings"
	"testing"
)

func setRequiredExternalURLs(t *testing.T) {
	t.Helper()
	t.Setenv("CAMERA_MOTION_EXTERNAL_URL", "http://motion.example")
	t.Setenv("CAMERA_FISHEYE_EXTERNAL_URL", "http://fisheye.example")
	t.Setenv("CAMERA_SAM3_EXTERNAL_URL", "http://sam3.example")
	t.Setenv("CAMERA_INTERNAL_TOKEN", "test-internal-token")
}

func TestLoadRequiresCameraMotionExternalURL(t *testing.T) {
	t.Setenv("CAMERA_FISHEYE_EXTERNAL_URL", "http://fisheye.example")
	t.Setenv("CAMERA_SAM3_EXTERNAL_URL", "http://sam3.example")
	t.Setenv("CAMERA_INTERNAL_TOKEN", "test-internal-token")
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
	t.Setenv("CAMERA_SAM3_EXTERNAL_URL", "http://sam3.example")
	t.Setenv("CAMERA_INTERNAL_TOKEN", "test-internal-token")
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
	if cfg.CameraSAM3ExternalURL != "http://sam3.example" {
		t.Fatalf("CameraSAM3ExternalURL = %q", cfg.CameraSAM3ExternalURL)
	}
	if cfg.InternalToken != "test-internal-token" {
		t.Fatalf("InternalToken = %q", cfg.InternalToken)
	}
}

func TestLoadRequiresCameraSAM3ExternalURL(t *testing.T) {
	t.Setenv("CAMERA_MOTION_EXTERNAL_URL", "http://motion.example")
	t.Setenv("CAMERA_FISHEYE_EXTERNAL_URL", "http://fisheye.example")
	t.Setenv("CAMERA_INTERNAL_TOKEN", "test-internal-token")
	t.Setenv("CAMERA_SAM3_EXTERNAL_URL", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "CAMERA_SAM3_EXTERNAL_URL") {
		t.Fatalf("Load() error = %v, want missing CAMERA_SAM3_EXTERNAL_URL", err)
	}
}

func TestLoadRequiresInternalToken(t *testing.T) {
	t.Setenv("CAMERA_MOTION_EXTERNAL_URL", "http://motion.example")
	t.Setenv("CAMERA_FISHEYE_EXTERNAL_URL", "http://fisheye.example")
	t.Setenv("CAMERA_SAM3_EXTERNAL_URL", "http://sam3.example")
	t.Setenv("CAMERA_INTERNAL_TOKEN", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "CAMERA_INTERNAL_TOKEN") {
		t.Fatalf("Load() error = %v, want missing CAMERA_INTERNAL_TOKEN", err)
	}
}
