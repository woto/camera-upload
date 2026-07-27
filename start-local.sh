#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

HOST="${HOST:-localhost}"
export PORT="${PORT:-8200}"
export CAMERA_MOTION_EXTERNAL_URL="${CAMERA_MOTION_EXTERNAL_URL:-http://localhost:8100}"
export CAMERA_FISHEYE_EXTERNAL_URL="${CAMERA_FISHEYE_EXTERNAL_URL:-http://localhost:8400}"
export CAMERA_SAM3_EXTERNAL_URL="${CAMERA_SAM3_EXTERNAL_URL:-http://localhost:8500}"

echo "Application available at: http://${HOST}:${PORT}/client"

exec go run ./cmd/server
