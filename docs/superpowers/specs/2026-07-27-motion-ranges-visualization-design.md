# Motion Ranges Visualization Design

**Date:** 2026-07-27

## Goal

Show the motion analysis already owned by `camera-upload` directly in each
saved export record. A user should be able to understand where the camera was
static, inspect the individual static ranges, and preview a representative
frame without opening `camera-motion`.

## Scope

This change is limited to the `camera-upload` client UI. It does not add a new
storage format, API route, analysis operation, or video-processing service.
The existing export response remains the source of truth:

```text
GET /uploads/{uploadId}/exports
  -> exports[].analysis.duration
  -> exports[].analysis.segments[]
```

The existing frame endpoint supplies previews:

```text
GET /uploads/{uploadId}/frame?t={midpointSeconds}
```

That endpoint reads the upload's working video. This is the original upload
unless processing selected a converted CFR working copy. Adding an
original-only frame endpoint is explicitly out of scope.

## Selected UX

The selected design is a compact timeline followed by a detailed list of only
the useful static ranges.

For an export with `analysis`:

1. Keep the existing `analyzed` badge and export controls.
2. Render a full-duration timeline below the controls.
3. Draw `stable` segments in green and `transition` segments in orange.
4. Render a detailed list containing only `stable` segments.
5. Show each stable row's start, end, and duration.
6. Expand a row on click and load the frame at
   `(segment.start + segment.end) / 2`.
7. Keep at most one preview expanded within an export record.
8. Collapse the preview when the selected row is clicked again.

Exports without `analysis` retain the current `not analyzed` badge and do not
show an empty timeline or range list.

## Components and Boundaries

### Timeline renderer

The timeline is a responsive `<canvas>`. The renderer accepts only an analysis
object and canvas dimensions. It maps each segment's `[start, end]` interval
onto the authoritative analysis duration and paints the appropriate color.

Canvas avoids creating one DOM node per segment and keeps the export panel
responsive up to the server's maximum accepted segment count. It is a visual
summary only: detailed selection remains in the accessible list below it.

The canvas redraws when its displayed width changes. Its backing dimensions
account for `devicePixelRatio` so lines remain sharp on high-density displays.

### Stable range list

The list is derived with an explicit `segment.kind === "stable"` filter.
Initially it renders at most 50 rows. If more stable ranges exist, a
`Show more` control appends the next 50; the control disappears when all rows
are visible.

Each row is a real button with `aria-expanded`. It displays formatted start,
end, and duration values. Time formatting uses `M:SS` for values under one
hour and `H:MM:SS` at one hour or longer.

### Inline frame preview

Selecting a row expands content immediately beneath that row. The preview
contains:

- a loading state;
- an `<img>` whose URL uses the range midpoint;
- the midpoint timestamp and selected range boundaries;
- an inline failure message and `Retry` action if loading fails.

Selecting another row removes the previous preview before opening the new one.
The preview does not navigate away from the export panel and does not create
or mutate server state.

## Data Flow

`renderExports` already receives the complete export records. `exportRow`
passes `rec.analysis` into a focused motion-ranges renderer after creating the
existing controls. No request to `camera-motion` is made for presence or range
data.

The only additional request is the selected frame image. The URL is generated
from the upload ID and the finite midpoint of the selected validated segment.
Changing canonical export settings rotates the export ID and clears analysis;
the existing `renderExports` refresh then removes the old visualization.

## Defensive Behavior

The server validates persisted analyses, but the browser renderer must fail
closed if it receives unexpected data. It shows `Motion ranges unavailable`
instead of throwing when any of these conditions holds:

- analysis duration is absent, non-finite, or not positive;
- segments is not an array;
- a segment boundary is non-finite or outside the duration;
- a segment kind is not `stable` or `transition`.

An image failure affects only the selected preview. It does not change the
`analyzed` badge or hide the timeline and other ranges.

## Accessibility and Responsive Layout

- Stable rows use native buttons and expose expanded state with
  `aria-expanded`.
- Timeline meaning is also represented by text/list content and is not
  communicated by color alone.
- Green and orange use sufficient contrast against the panel background and
  are paired with a legend.
- On narrow layouts, the image and metadata stack vertically.
- The preview image has alternative text containing the midpoint and range.

## Testing

Automated regression coverage will verify that the embedded client:

- renders the motion-ranges component from local `rec.analysis`;
- does not restore a `/results` request to `camera-motion`;
- filters detailed rows to `stable` segments;
- computes the preview timestamp from the segment midpoint;
- constructs the existing frame endpoint URL;
- implements loading, error, retry, expand, and collapse states;
- caps the initial list at 50 rows and exposes incremental loading;
- contains the timeline canvas, legend, and accessibility attributes.

Existing server and store tests continue to cover the analysis JSON contract.
Final manual verification uses a real analyzed export on desktop and a narrow
viewport, including preview success and a forced image-error state.

## Out of Scope

- Editing or relabeling motion segments.
- Triggering or cancelling analysis from the visualization.
- Selecting ranges for floor segmentation.
- Sending representative frames to another service.
- An original-only frame extraction endpoint.
- Persisting UI expansion state across page reloads.
