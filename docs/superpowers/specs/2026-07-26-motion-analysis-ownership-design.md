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
    "created_at": 1783844515,
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

Only the latest successful analysis is stored. Starting an analysis does not
clear the preceding successful result. It is replaced only after a new result
has been validated and accepted. Editing the export settings still clears it
immediately because the old result describes different proxy media.

## Delivery Contract

When camera-motion is opened from a camera-upload export record, the page
already receives `upload_id` and `export_id` and fetches that record. It will
retain a snapshot of the fetched `fps`, `width`, and `gray` values and submit
them with the job.

After motion computation succeeds, camera-motion sends an internal request:

```http
PUT /uploads/{upload_id}/exports/{export_id}/analysis
Content-Type: application/json
```

```json
{
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
uses `CAMERA_UPLOAD_INTERNAL_URL`, not the browser-facing URL.

Camera-upload validates all of the following before writing:

- the upload and export record exist;
- the submitted source snapshot exactly matches the record's current `fps`,
  `width`, and `gray` values;
- duration is finite and non-negative;
- every segment has finite timestamps, `0 <= start < end <= duration`, and a
  kind of `stable` or `transition`;
- segments are ordered, non-overlapping, and contiguous from zero through the
  duration (within a small floating-point tolerance).

A source mismatch returns `409 Conflict`; malformed analysis data returns
`400 Bad Request`; missing uploads or export records return `404 Not Found`.
The export file is not modified on rejection.

## Job Semantics and Errors

An analysis job carries `upload_id`, `export_id`, and the export source snapshot
when launched from camera-upload. Jobs created from arbitrary pasted URLs remain
supported, but have no destination and therefore remain transient.

For an associated job, completion consists of two stages:

1. compute the motion analysis;
2. deliver and persist it in camera-upload.

The job reaches `done` only after both stages succeed. A failed delivery changes
the job to `error` with a user-visible message. It does not write a local
fallback result, because that would create two owners and make the analyzed
badge ambiguous. The user can retry Analyze after resolving the conflict or
network failure.

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
- returning 404 for missing uploads or exports;
- returning 409 for source settings that no longer match;
- clearing analysis when export proxy settings change;
- retaining analysis when an update repeats identical proxy settings;
- rendering badges without a camera-motion presence request.

Camera-motion tests cover:

- submitting ownership identifiers and the source snapshot with a job;
- delivering the serialized result to the correct internal camera-upload URL;
- preserving transient behavior for jobs created from pasted URLs;
- reporting delivery rejection or network failure as a job error;
- removing the legacy result-presence API;
- leaving court-marking persistence and handoff behavior unchanged.

Cross-service verification runs both complete test suites and checks the browser
handoff contract strings used by the embedded clients.
