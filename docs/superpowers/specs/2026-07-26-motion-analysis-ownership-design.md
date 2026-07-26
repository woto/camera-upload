# Camera Upload Ownership of Motion Analysis

## Goal

Make `camera-upload` the durable owner of the time ranges in which a video's
camera is stable. `camera-motion` computes those ranges and delivers them to the
specific camera-upload export record that was analyzed. A future service can
then ask camera-upload for stable ranges and request original-resolution frames
from them without depending on camera-motion storage.

This change does not move court markings. Homography configurations sent from
`camera-homography` remain owned by `camera-motion` in `data/markings.json`.

## Current State

Each camera-upload video can have multiple export records. An export record has
an `export_id` and proxy settings (`fps`, `width`, and `gray`) and is stored in
`camera-upload/data/<upload_id>.exports.json`.

Today camera-motion stores completed segment results in
`camera-motion/data/results.json`, keyed only by `export_id`. Camera-upload asks
`GET camera-motion/results?ids=...` for a presence map and uses it to display
`analyzed` or `not analyzed`. This cannot prove that a stored result matches the
export record's current proxy settings, and camera-upload cannot retrieve the
actual ranges.

## Ownership and Storage

The camera-upload export record becomes the sole durable source of truth for
the latest successful motion analysis of that version.

An export record gains an optional `analysis` field:

```json
{
  "id": "25bd2067dd1616b8",
  "fps": 4,
  "width": 480,
  "gray": true,
  "created_at": 1783844000,
  "analysis": {
    "schema_version": 1,
    "analysis_id": "7f83b47e-5f97-4b24-a5e0-2c02b8ca1527",
    "started_at": 1783844400.0,
    "created_at": 1783844515,
    "duration": 5100.0,
    "source": {"fps": 4, "width": 480, "gray": true},
    "parameters": {
      "method": "affine",
      "mask": true,
      "roi": "full",
      "enter": 2.0,
      "settle": 0.5,
      "settle_samples": 2,
      "min_segment": 30.0,
      "features": 500,
      "min_inliers": 20
    },
    "segments": [
      {"start": 0.0, "end": 280.0, "kind": "stable"},
      {"start": 280.0, "end": 310.0, "kind": "transition"},
      {"start": 310.0, "end": 5100.0, "kind": "stable"}
    ]
  }
}
```

The existing export-record list and detail endpoints expose this field, so no
additional read endpoint is required:

- `GET /uploads/{upload_id}/exports`
- `GET /uploads/{upload_id}/exports/{export_id}`

Changing `fps`, `width`, or `gray` clears `analysis` in the same write that
updates the export record. The UI derives the badge locally: `analysis != null`
means `analyzed`; an absent analysis means `not analyzed`.

Export settings are canonical data, not arbitrary proxy query parameters. One
normalization function applies the existing defaults and clamps FPS to
`0.1..60` and width to `64..1920`. Export creation, export updates, proxy URL
construction, and analysis source comparison all use those canonical stored
values.

Only the latest successful analysis is stored. Starting an analysis does not
clear the preceding successful result. It is replaced only after a new result
has been validated and accepted. Editing the export settings still clears it
immediately because the old result describes different proxy media.

## Delivery Contract

When camera-motion is opened from a camera-upload export record, the browser
submits `upload_id` and `export_id`, but it does not choose the media URL or
claim the source settings. Camera-motion fetches the export record through
`CAMERA_UPLOAD_INTERNAL_URL` and constructs the analyzed input from the trusted
record. A request must contain both ownership identifiers or neither.

Camera-upload exposes a canonical version-proxy endpoint:

```http
GET /uploads/{upload_id}/exports/{export_id}/proxy
```

It resolves the stored export settings and serves the same cached transcoded
proxy as the existing free-form `/uploads/{upload_id}/proxy?...` endpoint. An
associated camera-motion job always analyzes this canonical endpoint. A job
created from a pasted URL remains transient and cannot name an upload/export
destination.

After motion computation succeeds, camera-motion sends an internal request:

```http
PUT /uploads/{upload_id}/exports/{export_id}/analysis
Content-Type: application/json
```

```json
{
  "schema_version": 1,
  "analysis_id": "7f83b47e-5f97-4b24-a5e0-2c02b8ca1527",
  "started_at": 1783844400.0,
  "source": {"fps": 4, "width": 480, "gray": true},
  "duration": 5100.0,
  "parameters": {
    "method": "affine",
    "mask": true,
    "roi": "full",
    "enter": 2.0,
    "settle": 0.5,
    "settle_samples": 2,
    "min_segment": 30.0,
    "features": 500,
    "min_inliers": 20
  },
  "segments": [
    {"start": 0.0, "end": 280.0, "kind": "stable"}
  ]
}
```

Camera-upload supplies `created_at` when it accepts the result. The delivery
uses `CAMERA_UPLOAD_INTERNAL_URL`, not the browser-facing URL. Internal service
requests carry `Authorization: Bearer <CAMERA_INTERNAL_TOKEN>`; both services
require the same non-empty token at startup. The analysis write rejects missing
or incorrect credentials, caps the JSON body at 2 MiB, and accepts at most
100,000 segments.

Camera-upload validates all of the following before writing:

- the upload and export record exist;
- the submitted source snapshot exactly matches the record's current `fps`,
  `width`, and `gray` values;
- identifiers and `schema_version` are valid, and the analysis parameters use
  supported enums, finite values, and the same numeric bounds accepted by
  camera-motion;
- duration is finite and positive and differs from camera-upload's authoritative
  working-video duration by no more than `max(0.1 seconds, 1 / source.fps)`;
- every segment has finite timestamps, `0 <= start < end <= duration`, and a
  kind of `stable` or `transition`;
- segments are ordered, non-overlapping, and contiguous from zero through the
  duration using an absolute tolerance of `1e-6` seconds.

On acceptance, boundaries within tolerance are snapped to the preceding end,
the first start is stored as zero, and the final end and stored duration are
set to camera-upload's authoritative working-video duration. Downstream users
therefore receive an exactly contiguous timeline in original-video time.

A source mismatch or an obsolete analysis attempt returns `409 Conflict`;
malformed analysis data returns `400 Bad Request`; missing uploads or export
records return `404 Not Found`; bad credentials return `401 Unauthorized`. The
export file is not modified on rejection.

Delivery is idempotent by `analysis_id`. Retrying an already accepted attempt
returns the stored result without changing its server-supplied `created_at`.
When two valid jobs finish out of order, an attempt with an older `started_at`
cannot overwrite a result whose newer attempt has already been accepted. If the
older job is accepted first, a later-started successful job may replace it.

## Job Semantics and Errors

An associated analysis job carries its server-resolved `upload_id`, `export_id`,
canonical source snapshot, UUID `analysis_id`, and server creation time as
`started_at`. Jobs created from arbitrary pasted URLs remain supported, but
have no destination and therefore remain transient.

For an associated job, completion consists of two stages:

1. compute the motion analysis;
2. deliver and persist it in camera-upload.

The job reaches `done` only after both stages succeed. Delivery uses bounded
timeouts and retries transport failures with the same `analysis_id`. A final
delivery failure or rejection changes the job to `error` with a user-visible
message. It does not write a local fallback result, because that would create
two owners and make the analyzed badge ambiguous. The user can retry Analyze
after resolving the conflict or network failure.

## Export Persistence Guarantees

All read-modify-write operations for one upload's export records—including
create, update, delete, and analysis delivery—run under the same in-process
per-upload lock. Validation of the source snapshot and the subsequent analysis
write are one critical section, so a concurrent settings update cannot attach a
stale analysis or be lost.

Export JSON is written to a temporary file in the same directory, flushed, and
atomically renamed over the prior sidecar. Read and JSON parse failures are
returned to the caller rather than being treated as an empty export list. The
deployment runs one camera-upload writer process for a data volume; supporting
multiple writers would require an inter-process lock and is outside this scope.

## Removal of the Old Result Store

Camera-motion removes:

- `ResultStore` and `camera_motion/results.py`;
- initialization and writes to `data/results.json`;
- `GET /results?ids=...`;
- tests specific to the old result store and presence endpoint.

Existing `camera-motion/data/results.json` data is intentionally not migrated.
The runtime no longer reads or writes it. The repository's ignored local file
may be deleted manually; no application behavior depends on it.

Camera-motion retains `MarkingStore`, `data/markings.json`, and all homography
handoff endpoints unchanged. Court markings continue to be keyed by video URL
and timestamp and are outside this feature's ownership change.

## UI Behavior

Camera-upload no longer calls camera-motion while rendering export records.
Each row renders its badge directly from the record's optional `analysis`.

After a successful Analyze, an already-open camera-upload page will show the new
badge and ranges the next time its export list is refreshed or reopened. Live
cross-window refresh is not part of this change.

This feature persists and exposes the ranges but does not yet add a range
timeline or frame-selection workflow to camera-upload. Those are follow-up UI
and floor-segmentation tasks.

## Testing

Camera-upload tests cover:

- round-tripping analysis through export list and detail responses;
- accepting valid stable/transition ranges;
- rejecting malformed, overlapping, non-contiguous, or out-of-duration ranges;
- rejecting unauthorized writes, oversized bodies, excessive segment counts,
  unsupported schema/parameter values, and authoritative-duration mismatches;
- returning 404 for missing uploads or exports;
- returning 409 for source settings that no longer match;
- clearing analysis when export proxy settings change;
- retaining analysis when an update repeats identical proxy settings;
- serving the version proxy from canonical stored settings;
- serializing settings updates, deletion, and analysis delivery under one lock;
- preserving the previous sidecar on write failure and surfacing corrupt JSON;
- treating duplicate `analysis_id` delivery as a no-op and rejecting an older
  result after a newer result has been accepted;
- rendering badges without a camera-motion presence request.

Camera-motion tests cover:

- submitting ownership identifiers and the source snapshot with a job;
- resolving associated-job media from camera-upload instead of trusting a
  submitted `video_url`, and rejecting partial destination data;
- delivering the serialized result to the correct internal camera-upload URL;
- preserving transient behavior for jobs created from pasted URLs;
- retrying transport failures idempotently and reporting final delivery
  rejection or network failure as a job error;
- removing the legacy result-presence API;
- leaving court-marking persistence and handoff behavior unchanged.

Cross-service verification runs both complete test suites and checks the browser
handoff contract strings used by the embedded clients. The two services are
deployed together as one coordinated rollout: camera-upload's receiving API and
camera-motion's delivery client ship in the same orchestrator update. Temporary
mixed-version compatibility is not required, and old `results.json` badges are
intentionally discarded.
