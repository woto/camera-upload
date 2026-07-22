# CFR Video Processing Design

## Goal

After a tus upload completes, verify that OpenCV frame seeking is reliable. Keep
the uploaded source unchanged, create a constant-frame-rate (CFR) working copy
only when the verification detects mismatches, and use the working file for all
normal video operations.

## Processing lifecycle

The per-upload processing sidecar has one of four states:

- `checking`: the OpenCV worker is comparing sequentially decoded frames with
  frame-number seeks at 60 evenly distributed locations.
- `converting`: a CFR copy is being created because at least one checked frame
  mismatched.
- `ready`: a working video is available. Its source is `original` when no
  conversion was required, or `converted` when the CFR copy was created.
- `failed`: checking or conversion did not finish successfully.

The source upload `{id}` is immutable. A conversion writes to a temporary file
and atomically promotes it to `{id}.cfr.mp4` only after FFmpeg exits
successfully. Metadata, thumbnails, frames, proxy clips, and exports are then
generated from the selected working file.

If the service restarts while a job is checking or converting, that upload is
marked `failed`. It is never resumed automatically.

## OpenCV worker

The Docker runtime includes Python and OpenCV. Go invokes a small Python worker
after upload completion. The worker opens the uploaded file, sequentially
decodes through the last sample, hashes the sample frames, seeks to the same 60
frame numbers in a new `VideoCapture`, and compares hashes. It returns a JSON
result to Go. Any mismatch requires CFR conversion.

## API behavior

- `GET /uploads/{id}` and `GET /uploads` expose the processing state and the
  selected working source.
- `GET /uploads/{id}/download` returns the working video.
- `GET /uploads/{id}/original` always returns the uploaded source video.
- `POST /uploads/{id}/retry-processing` is available only for `failed` uploads.
  It removes partial conversions and stale generated artifacts, preserves the
  source upload, and starts the lifecycle again.
- Until state is `ready`, `/download`, `/frame`, `/proxy`, `/thumbnail`, and
  export operations return a processing error. In `failed`, they return a
  processing-failed error.

## User interface

The existing upload status cell keeps its upload percentage and adds these
English states after bytes are uploaded:

- `checking video`
- `converting to CFR`
- `ready`
- `processing failed` with a Retry button

The expanded Video panel always shows two sections. Both contain a download
button, a URL field, and a Copy button:

- `Working video` is labelled `Original` when the original is the working file
  and `Converted` when a CFR copy is the working file.
- `Original` always links to the source upload.

## Error handling and cleanup

Processing errors persist their message in the sidecar, block all normal video
operations, and leave the source upload intact. Retry removes only temporary
conversion files and generated artifacts that may no longer match the future
working file. Deleting an upload removes the source, CFR copy, processing
sidecar, and all generated artifacts.

## Verification

Tests cover processing-state persistence and recovery, worker-result handling,
CFR command construction and atomic promotion, API access gating and retry,
download/original routing, and UI status/panel rendering. Integration tests use
small FFmpeg-generated videos and skip only when the required binaries are
unavailable.
