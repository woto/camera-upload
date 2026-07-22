# CFR Video Processing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every completed upload pass a practical OpenCV seek check, create a CFR working copy only when needed, and expose the working and original versions clearly through the API and Video panel.

**Architecture:** Persist a per-upload processing sidecar in `store`, and have `process.Processor` advance it from `checking` through `converting` to `ready` or `failed`. A bundled Python/OpenCV worker reports seek mismatches as JSON; Go chooses the working source, runs FFmpeg atomically when required, then generates all downstream artifacts from the chosen file. The server gates all video operations on `ready` and the client renders the persisted processing state.

**Tech Stack:** Go 1.25, chi, tusd, FFmpeg/ffprobe, Python 3, OpenCV headless, Docker, Go tests.

---

### Task 1: Persist processing state and working-file selection

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/cleanup.go`
- Modify: `internal/store/store_test.go`
- Create: `internal/store/processing.go`
- Create: `internal/store/processing_test.go`

- [ ] **Step 1: Write failing state-persistence tests**

```go
func TestProcessingStateRoundTrip(t *testing.T) {
    st := New(t.TempDir())
    writeInfo(t, st, "vid1")
    want := Processing{Status: ProcessingChecking}
    if err := st.SetProcessing("vid1", want); err != nil { t.Fatal(err) }
    got, err := st.Processing("vid1")
    if err != nil { t.Fatal(err) }
    if got.Status != ProcessingChecking { t.Fatalf("status = %q", got.Status) }
}

func TestWorkingPathUsesConvertedFileOnlyWhenReadyAndConverted(t *testing.T) {
    st := New(t.TempDir())
    writeInfo(t, st, "vid1")
    if err := st.SetProcessing("vid1", Processing{Status: ProcessingReady, WorkingSource: WorkingConverted}); err != nil { t.Fatal(err) }
    if got := st.WorkingPath("vid1"); got != st.CFRPath("vid1") { t.Fatalf("path = %q", got) }
}
```

- [ ] **Step 2: Run the store tests and verify they fail because processing APIs do not exist**

Run: `go test ./internal/store -run 'TestProcessingStateRoundTrip|TestWorkingPathUsesConvertedFileOnlyWhenReadyAndConverted'`

Expected: compile failure for undefined `Processing`, `SetProcessing`, and `WorkingPath`.

- [ ] **Step 3: Add `internal/store/processing.go`**

```go
type ProcessingStatus string
const (
    ProcessingChecking ProcessingStatus = "checking"
    ProcessingConverting ProcessingStatus = "converting"
    ProcessingReady ProcessingStatus = "ready"
    ProcessingFailed ProcessingStatus = "failed"
)
type WorkingSource string
const ( WorkingOriginal WorkingSource = "original"; WorkingConverted WorkingSource = "converted" )
type Processing struct { Status ProcessingStatus `json:"status"`; WorkingSource WorkingSource `json:"working_source,omitempty"`; Error string `json:"error,omitempty"` }
func (s *Store) ProcessingPath(id string) string
func (s *Store) CFRPath(id string) string
func (s *Store) Processing(id string) (Processing, error)
func (s *Store) SetProcessing(id string, state Processing) error
func (s *Store) WorkingPath(id string) string
func (s *Store) IsReady(id string) (bool, Processing, error)
```

Use `{id}.processing.json` and `{id}.cfr.mp4`; absent state for an incomplete
upload reports `checking`, while a completed legacy upload without state reports
`ready` with `original` to preserve already stored files.

- [ ] **Step 4: Extend `Upload` and cleanup/delete behavior**

Add `Processing Processing` to `store.Upload`, load it in `Get`, and extend
`Store.Delete` to remove `.cfr.mp4`, `.processing.json`, conversion temporary
files, generated thumbnails, proxy cache files, metadata, user metadata, and
export sidecars. Make `CleanupIncomplete` preserve completed tus uploads even
when their processing state is non-ready.

- [ ] **Step 5: Run store tests**

Run: `go test ./internal/store`

Expected: PASS.

### Task 2: Add the OpenCV seek-check worker and Docker runtime dependency

**Files:**
- Create: `internal/process/check_seek.py`
- Create: `internal/process/check_seek_test.py`
- Modify: `Dockerfile`
- Modify: `internal/process/process.go`
- Modify: `internal/process/process_test.go`

- [ ] **Step 1: Write failing Go tests for parsing worker output**

```go
func TestSeekCheckParsesMismatchResult(t *testing.T) {
    got, err := parseSeekCheck([]byte(`{"samples":60,"mismatches":[100,200]}`))
    if err != nil { t.Fatal(err) }
    if !got.NeedsCFR() { t.Fatal("expected CFR conversion") }
}
func TestSeekCheckRejectsInvalidWorkerJSON(t *testing.T) {
    if _, err := parseSeekCheck([]byte("not-json")); err == nil { t.Fatal("expected error") }
}
```

- [ ] **Step 2: Run and verify the tests fail for missing `parseSeekCheck`**

Run: `go test ./internal/process -run 'TestSeekCheck'`

Expected: compile failure for undefined `parseSeekCheck`.

- [ ] **Step 3: Implement `check_seek.py`**

The script accepts `--video PATH --samples 60`, opens the video with OpenCV,
computes 60 evenly distributed frame numbers including the first and last,
sequentially hashes those BGR frames, seeks to each number in a fresh capture,
and prints only this JSON on stdout:

```json
{"samples":60,"mismatches":[1000,5000]}
```

It exits nonzero with a useful stderr message when opening, sequential reading,
or seeking fails. It must release captures in `finally` blocks.

- [ ] **Step 4: Implement the Go worker wrapper**

Add `SeekCheckResult`, `parseSeekCheck`, and `CheckSeek(ctx, src string)` in
`process.go` or a focused `seek_check.go`. Invoke:

```go
exec.CommandContext(ctx, "python3", "/app/check_seek.py", "--video", src, "--samples", "60")
```

Capture stderr in errors, validate the JSON, and set `Pdeathsig: SIGKILL` just
as `WriteProxy` does.

- [ ] **Step 5: Update Dockerfile**

Install `python3` and `python3-opencv` in the runtime stage alongside FFmpeg;
copy `internal/process/check_seek.py` to `/app/check_seek.py`; keep the runtime
user non-root.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/process`

Expected: PASS. Add a Python test invocation in Docker build only if the base
image test runner has Python test tooling; otherwise cover worker JSON and use
the Go integration test below.

### Task 3: Process uploads into an atomic CFR working version

**Files:**
- Modify: `internal/process/process.go`
- Modify: `internal/process/process_test.go`
- Modify: `internal/store/processing.go`

- [ ] **Step 1: Write failing lifecycle tests**

```go
func TestProcessorUsesOriginalWhenSeekCheckHasNoMismatch(t *testing.T) {
    st, id := testStoreWithVideo(t)
    p := NewWithDependencies(st, false, discardLogger(),
        func(context.Context, string) (SeekCheckResult, error) { return SeekCheckResult{}, nil },
        func(context.Context, string, string) error { t.Fatal("transcoder called"); return nil })
    p.handle(id)
    state, _ := st.Processing(id)
    if state.Status != store.ProcessingReady || state.WorkingSource != store.WorkingOriginal { t.Fatalf("state = %+v", state) }
    if _, err := os.Stat(st.CFRPath(id)); !errors.Is(err, os.ErrNotExist) { t.Fatalf("cfr file err = %v", err) }
}
func TestProcessorPromotesCFRFileWhenSeekCheckMismatches(t *testing.T) {
    st, id := testStoreWithVideo(t)
    p := NewWithDependencies(st, false, discardLogger(),
        func(context.Context, string) (SeekCheckResult, error) { return SeekCheckResult{Mismatches: []int{100}}, nil },
        func(_ context.Context, _ string, dst string) error { return os.WriteFile(dst, []byte("cfr"), 0o644) })
    p.handle(id)
    state, _ := st.Processing(id)
    if state.Status != store.ProcessingReady || state.WorkingSource != store.WorkingConverted { t.Fatalf("state = %+v", state) }
    if raw, _ := os.ReadFile(st.CFRPath(id)); string(raw) != "cfr" { t.Fatalf("cfr = %q", raw) }
}
func TestProcessorMarksFailedWhenCheckFails(t *testing.T) {
    st, id := testStoreWithVideo(t)
    p := NewWithDependencies(st, false, discardLogger(),
        func(context.Context, string) (SeekCheckResult, error) { return SeekCheckResult{}, errors.New("cannot read frame") }, nil)
    p.handle(id)
    state, _ := st.Processing(id)
    if state.Status != store.ProcessingFailed || !strings.Contains(state.Error, "cannot read frame") { t.Fatalf("state = %+v", state) }
    if _, err := os.Stat(st.DataPath(id)); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Run and verify lifecycle tests fail**

Run: `go test ./internal/process -run 'TestProcessor(UsesOriginal|PromotesCFR|MarksFailed)'`

Expected: assertion failures because the current processor probes the raw upload
immediately and has no processing state.

- [ ] **Step 3: Make processor dependencies injectable**

Give `Processor` two narrow function dependencies with production defaults:

```go
type seekChecker func(context.Context, string) (SeekCheckResult, error)
type transcoder func(context.Context, src, dst string) error
```

Use them from `handle`, setting `checking`, then `converting` only if the
checker reports mismatches. Implement `WriteCFR` with `ffmpeg -nostdin -y`,
`-map 0:v:0`, `-map 0:a?`, `-vf fps=30`, `-fps_mode cfr`, `libx264`,
`-preset medium`, `-crf 18`, `yuv420p`, AAC 192k, and `+faststart`.

- [ ] **Step 4: Make promotion and downstream processing atomic**

Write conversion to a unique `CFRPath + .<nanoseconds>.tmp`, remove it on every
failure path, rename it only after FFmpeg succeeds, set `ready/converted`, then
run `probe` and `thumbnail` using `WorkingPath(id)`. For no mismatch, set
`ready/original` before probe/thumbnail. Any failure writes `failed` with the
error string and does not delete `DataPath(id)`.

- [ ] **Step 5: Implement retry cleanup API in the processor**

Add `Processor.Retry(id string) error`: require `failed`, remove CFR/temp files
and all generated working artifacts, set `checking`, and dispatch `handle(id)`
in a goroutine. On service startup, scan `checking` and `converting` sidecars
and mark them `failed` with `processing interrupted by restart`.

- [ ] **Step 6: Run process tests**

Run: `go test ./internal/process`

Expected: PASS.

### Task 4: Gate video API operations and expose original/working downloads

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/uploads.go`
- Modify: `internal/server/server_test.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Write failing HTTP tests**

```go
func TestVideoOperationsConflictUntilProcessingReady(t *testing.T) {
    srv, st, id := readyTestServer(t)
    if err := st.SetProcessing(id, store.Processing{Status: store.ProcessingChecking}); err != nil { t.Fatal(err) }
    for _, path := range []string{"/uploads/" + id + "/download", "/uploads/" + id + "/frame", "/uploads/" + id + "/proxy", "/uploads/" + id + "/thumbnail"} {
        if rec := doReq(srv, http.MethodGet, path, ""); rec.Code != http.StatusConflict { t.Fatalf("%s: status = %d", path, rec.Code) }
    }
}
func TestDownloadUsesConvertedWorkingFileAndOriginalUsesSource(t *testing.T) {
    srv, st, id := readyTestServer(t)
    if err := os.WriteFile(st.DataPath(id), []byte("original"), 0o644); err != nil { t.Fatal(err) }
    if err := os.WriteFile(st.CFRPath(id), []byte("converted"), 0o644); err != nil { t.Fatal(err) }
    if err := st.SetProcessing(id, store.Processing{Status: store.ProcessingReady, WorkingSource: store.WorkingConverted}); err != nil { t.Fatal(err) }
    if rec := doReq(srv, http.MethodGet, "/uploads/"+id+"/download", ""); rec.Body.String() != "converted" { t.Fatalf("download = %q", rec.Body.String()) }
    if rec := doReq(srv, http.MethodGet, "/uploads/"+id+"/original", ""); rec.Body.String() != "original" { t.Fatalf("original = %q", rec.Body.String()) }
}
func TestRetryProcessingOnlyAcceptsFailedUpload(t *testing.T) {
    srv, st, id, retries := retryTestServer(t)
    _ = st.SetProcessing(id, store.Processing{Status: store.ProcessingReady, WorkingSource: store.WorkingOriginal})
    if rec := doReq(srv, http.MethodPost, "/uploads/"+id+"/retry-processing", ""); rec.Code != http.StatusConflict { t.Fatalf("ready status = %d", rec.Code) }
    _ = st.SetProcessing(id, store.Processing{Status: store.ProcessingFailed, Error: "broken"})
    if rec := doReq(srv, http.MethodPost, "/uploads/"+id+"/retry-processing", ""); rec.Code != http.StatusAccepted { t.Fatalf("failed status = %d", rec.Code) }
    if *retries != 1 { t.Fatalf("retries = %d", *retries) }
}
```

- [ ] **Step 2: Run and verify the HTTP tests fail**

Run: `go test ./internal/server -run 'Test(VideoOperationsConflictUntilProcessingReady|DownloadUsesConvertedWorkingFileAndOriginalUsesSource|RetryProcessingOnlyAcceptsFailedUpload)'`

Expected: failing assertions because routes currently use `DataPath(id)` and
only check tus completion.

- [ ] **Step 3: Add processing-aware server dependencies and routes**

Pass `*process.Processor` to `server.New` (or a narrow retry interface), mount:

```go
r.Get("/{id}/original", s.downloadOriginal)
r.Post("/{id}/retry-processing", s.retryProcessing)
```

Keep `/download` for the working file. Add a shared `requireReady` helper that
returns `409 video is processing` for `checking`/`converting` and `409
processing failed` for `failed`; call it from working download, frame, proxy,
thumbnail, thumbnail regeneration, and export CRUD. Original download checks
only that the tus upload completed.

- [ ] **Step 4: Replace raw paths with working paths**

Use `store.WorkingPath(id)` for `downloadUpload`, `frame`, `setThumbnail`, and
proxy generation. Make proxy cache paths distinguish `original` and
`converted`, so an old original proxy cannot be served after conversion.

- [ ] **Step 5: Wire restart recovery and retry in main**

After constructing the processor, call its startup interruption recovery before
starting the HTTP server. Pass the processor to `server.New`.

- [ ] **Step 6: Run server tests**

Run: `go test ./internal/server`

Expected: PASS.

### Task 5: Render processing state and version URLs in the Video panel

**Files:**
- Modify: `web/client/index.html`
- Modify: `internal/server/server_test.go`
- Modify: `README.md`

- [ ] **Step 1: Write a failing served-client assertion**

```go
func TestClientContainsProcessingAndVersionControls(t *testing.T) {
    srv, _ := newTestServer(t)
    rec := doReq(srv, http.MethodGet, "/client", "")
    body := rec.Body.String()
    for _, want := range []string{"checking video", "converting to CFR", "Working video", "Original", "retry-processing"} {
        if !strings.Contains(body, want) { t.Fatalf("client missing %q", want) }
    }
}
```

- [ ] **Step 2: Run and verify it fails**

Run: `go test ./internal/server -run TestClientContainsProcessingAndVersionControls`

Expected: assertion failure for the new labels and route.

- [ ] **Step 3: Extend row rendering and status badges**

Use `u.processing.status` to retain the existing byte-progress badge before
tus completion and render exact English post-upload labels: `checking video`,
`converting to CFR`, `ready`, and `processing failed`. Render Retry only for
`failed`; POST `/uploads/{id}/retry-processing`, then force refresh.

- [ ] **Step 4: Change the expanded Video panel**

Always render these two URL rows using the existing `copyButton` helper:

```text
Working video  [Original|Converted]  [Download] [URL input] [Copy]
Original                            [Download] [URL input] [Copy]
```

Use `/download` for working and `/original` for source. Keep camera-motion
exports below them. Disable/hide Video, Frame, and related actions until
`processing.status == "ready"`.

- [ ] **Step 5: Document external behavior**

Update the endpoints table, upload JSON example, Docker dependency note, and
the lifecycle description in `README.md` with CFR handling, original download,
retry, blocked operations, and the two Video-panel links.

- [ ] **Step 6: Run UI-serving and server tests**

Run: `go test ./internal/server`

Expected: PASS.

### Task 6: Run the complete verification suite

**Files:**
- Verify only: all files above

- [ ] **Step 1: Format Go and validate Python syntax**

Run: `gofmt -w internal/store/*.go internal/process/*.go internal/server/*.go cmd/server/main.go`

Run: `python3 -m py_compile internal/process/check_seek.py`

Expected: no output and zero exit code.

- [ ] **Step 2: Run all tests**

Run: `go test ./...`

Expected: PASS; FFmpeg-backed tests skip only if FFmpeg/ffprobe are unavailable.

- [ ] **Step 3: Build the Docker image**

Run: `docker build -t camera-upload:cfr-check .`

Expected: successful build with Python/OpenCV, FFmpeg, and the server binary.

- [ ] **Step 4: Manual smoke test**

Start the container, upload a small CFR fixture and a VFR fixture, then verify:

```text
CFR fixture: checking video -> ready, Working video = Original,
             /download and /original return the same bytes.
VFR fixture: checking video -> converting to CFR -> ready,
             Working video = Converted, /download differs from /original,
             frame/proxy/thumbnail use the converted file.
```

- [ ] **Step 5: Review diff before handoff**

Run: `git diff --check && git status --short`

Expected: no whitespace errors; only intended CFR-processing files plus the
pre-existing untracked `start-local.sh` and `.superpowers/` artifacts.
