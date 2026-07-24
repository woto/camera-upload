# Camera SAM3 Object Search Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Camera Upload frame handoff open Camera SAM3's `/object-search` page instead of its `/client` landing page.

**Architecture:** Keep the existing browser-to-browser `postMessage` handshake and origin checks intact. Change only the child-window route and protect that contract with a server-level regression test against the rendered embedded client.

**Tech Stack:** Go, `net/http/httptest`, embedded HTML/JavaScript

---

### Task 1: Update the Camera SAM3 handoff route

**Files:**
- Modify: `internal/server/server_test.go`
- Modify: `web/client/index.html`

- [ ] **Step 1: Write the failing regression test**

Add this test next to `TestClientInjectsCameraSAM3URL` in `internal/server/server_test.go`:

```go
func TestClientOpensCameraSAM3ObjectSearch(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, BasePath: "/files/", CameraSAM3ExternalURL: "http://sam3.example:8500"}
	st := store.New(dir)
	tusStub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, tusStub, log)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "CAMERA_SAM3_EXTERNAL_URL + '/object-search'") {
		t.Errorf("served client does not open the camera-sam3 object-search page")
	}
	if strings.Contains(body, "CAMERA_SAM3_EXTERNAL_URL + '/client'") {
		t.Errorf("served client still opens the camera-sam3 landing page")
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/server -run TestClientOpensCameraSAM3ObjectSearch -count=1`

Expected: FAIL with `served client does not open the camera-sam3 object-search page` and `served client still opens the camera-sam3 landing page`.

- [ ] **Step 3: Implement the minimal route change**

In `openSAM3` in `web/client/index.html`, replace:

```javascript
const child = window.open(CAMERA_SAM3_EXTERNAL_URL + '/client', 'camera_sam3');
```

with:

```javascript
const child = window.open(CAMERA_SAM3_EXTERNAL_URL + '/object-search', 'camera_sam3');
```

- [ ] **Step 4: Run the focused test and verify GREEN**

Run: `go test ./internal/server -run TestClientOpensCameraSAM3ObjectSearch -count=1`

Expected: PASS.

- [ ] **Step 5: Run full verification**

Run: `go test ./... -count=1`

Expected: all packages pass with no failures.

- [ ] **Step 6: Review the final diff**

Run: `git diff --check` and `git diff -- internal/server/server_test.go web/client/index.html`

Expected: no whitespace errors; the diff contains only the regression test and the `/client` to `/object-search` route replacement.
