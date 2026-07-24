# Camera SAM3 Object Search Handoff

## Goal

Keep the Camera Upload frame-selection handoff working after Camera SAM3 moved
its object-search browser client from `/client` to `/object-search`.

## Design

The `Open in camera-sam3` action will open
`${CAMERA_SAM3_EXTERNAL_URL}/object-search` in the existing `camera_sam3` named
window. The browser integration will retain the current origin validation and
message handshake:

1. Camera SAM3 sends `camera-sam3:ready` to its opener.
2. Camera Upload verifies the child window and Camera SAM3 origin.
3. Camera Upload sends `camera-upload:frames` with the selected `image_urls`.

Only the receiving page path changes. The configured Camera SAM3 base URL,
frame URL generation, selected-frame limit, message names, payload, popup name,
and origin checks remain unchanged.

## Error Handling

The existing behavior remains in place: no window is opened when the Camera
SAM3 URL or selected-frame list is empty, a blocked popup stops the handoff,
and messages from unexpected windows or origins are ignored.

## Testing

Add a focused regression assertion for the embedded client HTML that verifies
the handoff opens `/object-search` and no longer opens the Camera SAM3 landing
page at `/client`. Run the focused server test and then the full Go test suite.
