# Motion Analysis Ownership Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make camera-upload durably own validated stable-camera ranges for each export version while camera-motion becomes a transient analyzer that resolves trusted version media and delivers results idempotently.

**Architecture:** Camera-upload extends each export record with an optional motion analysis, serializes all export mutations under one per-upload lock, and writes sidecars atomically. Associated camera-motion jobs resolve input from camera-upload by `upload_id/export_id`, analyze the canonical version proxy, and deliver a token-authenticated result carrying an attempt UUID; pasted-URL jobs remain transient. Both services roll out together through camera-orchestrator, and the legacy camera-motion result store is removed without migration.

**Tech Stack:** Go 1.25 (`net/http`, chi, JSON sidecars), Python 3.12 (FastAPI, Pydantic, httpx), browser JavaScript, Docker Compose, Go tests, pytest.

---

## File map

- `camera-upload/internal/store/store.go`: shared per-upload export lock.
- `camera-upload/internal/store/exports.go`: canonical settings, analysis schema and validation, idempotency, atomic persistence.
- `camera-upload/internal/store/exports_test.go`: storage, corruption, concurrency, and idempotency tests.
- `camera-upload/internal/config/{config.go,config_test.go}`: shared internal-service token.
- `camera-upload/internal/server/{server.go,uploads.go,server_test.go}`: canonical proxy and authenticated analysis write.
- `camera-upload/web/client/index.html`: local analyzed badge.
- `camera-motion/camera_motion/upload_client.py`: trusted source resolution and result delivery.
- `camera-motion/camera_motion/{jobs.py,api.py}`: associated-job lifecycle and request contract.
- `camera-motion/tests/{test_upload_client.py,test_jobs.py,test_api.py}`: delivery and API tests.
- Delete `camera-motion/camera_motion/results.py` and its two test files.
- `camera-orchestrator/{.env.example,docker-compose.yml,README.md}`: coordinated token configuration.

### Task 1: Make camera-upload export persistence canonical and atomic

**Files:**
- Modify: `/home/woto/work/camera-upload/internal/store/store.go`
- Modify: `/home/woto/work/camera-upload/internal/store/exports.go`
- Modify: `/home/woto/work/camera-upload/internal/store/exports_test.go`

- [ ] **Step 1: Write failing schema and lifecycle tests**

Append a reusable valid input and focused tests before production changes:

```go
func validAnalysisInput() MotionAnalysisInput {
	return MotionAnalysisInput{
		SchemaVersion: 1,
		AnalysisID: "7f83b47e-5f97-4b24-a5e0-2c02b8ca1527",
		StartedAt: 100,
		Source: ExportSource{FPS: 4, Width: 480, Gray: true},
		Duration: 20,
		Parameters: MotionAnalysisParameters{
			Method: "affine", Mask: true, ROI: "full", Enter: 2,
			Settle: .5, SettleSamples: 2, MinSegment: 1,
			Features: 500, MinInliers: 20,
		},
		Segments: []MotionSegment{
			{Start: 0, End: 8, Kind: "stable"},
			{Start: 8, End: 10, Kind: "transition"},
			{Start: 10, End: 20, Kind: "stable"},
		},
	}
}

func TestNormalizeExportUsesProxyBounds(t *testing.T) {
	got := NormalizeExport(ExportConfig{FPS: .01, Width: 1, Gray: true})
	if got.FPS != .1 || got.Width != 64 || !got.Gray { t.Fatalf("got %+v", got) }
	got = NormalizeExport(ExportConfig{FPS: 99, Width: 9000})
	if got.FPS != 60 || got.Width != 1920 { t.Fatalf("upper bounds %+v", got) }
}

func TestSetExportAnalysisPersistsAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "vid1", 100, 100, "clip.mp4")
	s := New(dir)
	cfg, _ := s.UpsertExport("vid1", ExportConfig{FPS: 4, Width: 480, Gray: true})
	stored, err := s.SetExportAnalysis("vid1", cfg.ID, validAnalysisInput(), 20)
	if err != nil { t.Fatal(err) }
	if stored.Analysis == nil { t.Fatal("analysis missing") }
	reloaded, ok, err := New(dir).Export("vid1", cfg.ID)
	if err != nil || !ok || reloaded.Analysis == nil { t.Fatalf("reload: %+v %v %v", reloaded, ok, err) }
}
```

Add table cases for invalid schema/UUID/time, unsupported method/ROI, NaN/Inf and invalid parameter bounds, empty/overlapping/gapped/out-of-duration timelines, excessive segments, source mismatch, and duration mismatch. Add tests that identical settings retain analysis, changed settings clear it, duplicate `analysis_id` preserves `created_at`, and an older `started_at` returns `ErrExportConflict`.

- [ ] **Step 2: Verify RED**

```bash
cd /home/woto/work/camera-upload
go test ./internal/store -run 'TestNormalizeExport|TestSetExportAnalysis|TestValidateMotionAnalysis' -count=1
```

Expected: compilation fails because the new types and methods do not exist.

- [ ] **Step 3: Implement the data model and validation**

Add these exact public shapes to `exports.go`:

```go
const MotionAnalysisSchemaVersion = 1
const MaxMotionSegments = 100_000
const timelineTolerance = 1e-6

var ErrExportNotFound = errors.New("export not found")
var ErrExportConflict = errors.New("export conflict")
var ErrInvalidAnalysis = errors.New("invalid motion analysis")

type ExportSource struct { FPS float64 `json:"fps"`; Width int `json:"width"`; Gray bool `json:"gray"` }
type MotionSegment struct { Start float64 `json:"start"`; End float64 `json:"end"`; Kind string `json:"kind"` }
type MotionAnalysisParameters struct {
	Method string `json:"method"`; Mask bool `json:"mask"`; ROI string `json:"roi"`
	Enter float64 `json:"enter"`; Settle float64 `json:"settle"`; SettleSamples int `json:"settle_samples"`
	MinSegment float64 `json:"min_segment"`; Features int `json:"features"`; MinInliers int `json:"min_inliers"`
}
type MotionAnalysisInput struct {
	SchemaVersion int `json:"schema_version"`; AnalysisID string `json:"analysis_id"`; StartedAt float64 `json:"started_at"`
	Source ExportSource `json:"source"`; Duration float64 `json:"duration"`
	Parameters MotionAnalysisParameters `json:"parameters"`; Segments []MotionSegment `json:"segments"`
}
type MotionAnalysis struct { MotionAnalysisInput; CreatedAt int64 `json:"created_at"` }
```

Add `Analysis *MotionAnalysis `json:"analysis,omitempty"`` to `ExportConfig`. Export `NormalizeExport`, applying current defaults before clamping FPS to `0.1..60` and width to `64..1920`. Implement `SetExportAnalysis(uploadID, exportID string, input MotionAnalysisInput, authoritativeDuration float64)` as a compare-and-set that validates and canonicalizes the timeline, supplies `CreatedAt`, handles duplicate IDs idempotently, and rejects older accepted attempts.

Accepted enums are methods `affine|flow|edges|lines|ecc`, ROI `full|top|bottom`, and segment kinds `stable|transition`. Require finite positive duration/enter, finite non-negative settle/min-segment, positive integer counters, contiguous ordered coverage, and duration difference no larger than `max(.1, 1/source.FPS)`. Snap accepted boundaries and the final end to authoritative duration.

- [ ] **Step 4: Serialize mutations and write atomically**

Add a keyed lock to `Store`:

```go
type Store struct {
	dir string
	exportLocks keyedMutex
}
```

Use it in `Exports`, `Export`, `UpsertExport`, `DeleteExport`, and `SetExportAnalysis`. Make `readExports` return and propagate errors instead of silently treating corrupt JSON as empty. Make `writeExports` create a temporary file in the same directory, encode, `Sync`, close, and `Rename`, cleaning the temporary file on every error. Preserve an existing analysis only when canonical settings are unchanged; ignore caller-provided analysis on normal create/update.

- [ ] **Step 5: Add corruption and concurrency regression tests**

Test truncated JSON, preservation of the prior sidecar on write failure, concurrent settings update versus delivery, and delete versus delivery. Run:

```bash
cd /home/woto/work/camera-upload
go test -race ./internal/store -count=1
```

Expected: PASS without race reports.

- [ ] **Step 6: Commit**

```bash
cd /home/woto/work/camera-upload
git add internal/store/store.go internal/store/exports.go internal/store/exports_test.go
git commit -m "feat: persist motion analysis with export versions"
```

### Task 2: Add camera-upload ownership APIs and local badges

**Files:**
- Modify: `/home/woto/work/camera-upload/internal/config/config.go`
- Modify: `/home/woto/work/camera-upload/internal/config/config_test.go`
- Modify: `/home/woto/work/camera-upload/internal/server/server.go`
- Modify: `/home/woto/work/camera-upload/internal/server/uploads.go`
- Modify: `/home/woto/work/camera-upload/internal/server/server_test.go`
- Modify: `/home/woto/work/camera-upload/web/client/index.html`
- Modify: `/home/woto/work/camera-upload/README.md`

- [ ] **Step 1: Write failing HTTP contract tests**

Cover unauthorized and authorized analysis writes, missing upload/export, malformed and unknown JSON, 2 MiB overflow, invalid/conflicting analysis, and canonical version-proxy settings. Add this UI assertion:

```go
func TestClientReadsAnalysisBadgeLocally(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "!!rec.analysis") { t.Error("local analysis badge missing") }
	if strings.Contains(body, "'/results?ids='") { t.Error("legacy presence request remains") }
}
```

Update config tests to require `CAMERA_INTERNAL_TOKEN`.

- [ ] **Step 2: Verify RED**

```bash
cd /home/woto/work/camera-upload
go test ./internal/config ./internal/server -run 'InternalToken|ExportAnalysis|VersionProxy|AnalysisBadge' -count=1
```

Expected: FAIL because fields, routes, handlers, and local badge logic are absent.

- [ ] **Step 3: Implement configuration and handlers**

Add `InternalToken string` to `config.Config`, loaded with `requireEnv("CAMERA_INTERNAL_TOKEN")`. Register:

```go
r.Get("/{id}/exports/{exportId}/proxy", s.exportProxy)
r.Put("/{id}/exports/{exportId}/analysis", s.putExportAnalysis)
```

Authenticate analysis writes with constant-time comparison of `Authorization: Bearer ...`; cap bodies using `http.MaxBytesReader(w, r.Body, 2<<20)`; use `DisallowUnknownFields` and reject trailing JSON. Read authoritative duration through `Store.Get`, call `SetExportAnalysis`, and map errors to 400/404/409. Refactor proxy serving so the free-form route normalizes through `store.NormalizeExport` and the version route ignores query overrides and uses stored canonical settings.

- [ ] **Step 4: Replace the remote badge and document ownership**

Replace the presence fetch with:

```javascript
exports.forEach(rec => listEl.appendChild(exportRow(id, box, rec, !!rec.analysis)));
```

Document the stored analysis object, read endpoints, canonical proxy, invalidation rule, authenticated write, and single-writer sidecar assumption.

- [ ] **Step 5: Verify and commit**

```bash
cd /home/woto/work/camera-upload
go test -race ./... -count=1
git add internal/config/config.go internal/config/config_test.go internal/server/server.go internal/server/uploads.go internal/server/server_test.go web/client/index.html README.md
git commit -m "feat: receive owned motion analysis results"
```

### Task 3: Resolve trusted media and deliver completed analyses

**Files:**
- Create: `/home/woto/work/camera-motion/camera_motion/upload_client.py`
- Create: `/home/woto/work/camera-motion/tests/test_upload_client.py`
- Create: `/home/woto/work/camera-motion/tests/test_jobs.py`
- Modify: `/home/woto/work/camera-motion/camera_motion/jobs.py`
- Modify: `/home/woto/work/camera-motion/camera_motion/api.py`
- Modify: `/home/woto/work/camera-motion/tests/test_api.py`

- [ ] **Step 1: Write failing client tests**

Use `httpx.MockTransport` to define the trusted interface:

```python
def test_resolve_export_uses_internal_version_proxy():
    def handler(request):
        return httpx.Response(200, json={"id":"e1","fps":4,"width":480,"gray":True})
    client = CameraUploadClient("http://camera-upload:8000", "secret", transport=httpx.MockTransport(handler))
    got = client.resolve("upload 1", "e1")
    assert got.video_url == "http://camera-upload:8000/uploads/upload%201/exports/e1/proxy"
    assert got.source == ExportSource(fps=4, width=480, gray=True)
```

Add tests that delivery uses the bearer token and exact persisted parameter names, retries `TransportError` with the same `analysis_id`, does not retry 4xx, uses bounded timeouts, and URL-escapes identifiers.

- [ ] **Step 2: Write failing job and API tests**

Test that an associated job reaches `done` only after delivery, delivery failure produces `error`, and transient jobs do not deliver. Assert request modes:

```python
assert client.post("/jobs", json={"upload_id":"u1"}).status_code == 422
assert client.post("/jobs", json={"export_id":"e1"}).status_code == 422
assert client.post("/jobs", json={"video_url":"https://x", "upload_id":"u1", "export_id":"e1"}).status_code == 422
assert client.post("/jobs", json={"video_url":"https://x"}).status_code == 202
```

For the paired-ID success case, monkeypatch trusted resolution so the test never contacts the network. Assert embedded JS submits IDs rather than an editable associated URL.

- [ ] **Step 3: Verify RED**

```bash
cd /home/woto/work/camera-motion
uv run pytest -q tests/test_upload_client.py tests/test_jobs.py tests/test_api.py
```

Expected: FAIL because the client and new contract do not exist.

- [ ] **Step 4: Implement the focused upload client**

Create:

```python
@dataclass(frozen=True)
class ExportSource:
    fps: float
    width: int
    gray: bool

@dataclass(frozen=True)
class AnalysisDestination:
    upload_id: str
    export_id: str
    video_url: str
    source: ExportSource
    analysis_id: str
    started_at: float

class CameraUploadClient:
    def __init__(self, base_url: str, token: str, *, transport=None, timeout=10.0, attempts=3): ...
    def resolve(self, upload_id: str, export_id: str) -> AnalysisDestination: ...
    def deliver(self, destination: AnalysisDestination, result: AnalysisResult, parameters: dict) -> dict: ...
```

`resolve` performs authenticated internal export lookup and builds the canonical version-proxy URL. `deliver` maps `enter_pct/settle_pct/min_segment_s/n_features` to `enter/settle/min_segment/features`, omits transition metrics, retries only transport failures with the same UUID, and raises `AnalysisDeliveryError` with concise response detail for final failures.

- [ ] **Step 5: Make delivery part of job completion**

Replace `export_id` on `Job` with optional `destination`. `JobManager.create` takes a resolved source. In `_run`, analyze, render the chart, deliver when associated, and assign `done` only afterward. Remove callback exception swallowing so the outer handler records delivery failure.

In `api.py`, require `CAMERA_INTERNAL_TOKEN`, instantiate the client, and use a Pydantic `model_validator(mode="after")` to allow exactly one mode: `video_url`, or paired `upload_id/export_id`. Resolve associated media server-side before creating the job. Update JS so export handoff submits IDs; manual and `?url=` jobs remain transient.

- [ ] **Step 6: Verify and commit**

```bash
cd /home/woto/work/camera-motion
uv run pytest -q tests/test_upload_client.py tests/test_jobs.py tests/test_api.py tests/test_markings.py tests/test_markings_api.py
git add camera_motion/upload_client.py camera_motion/jobs.py camera_motion/api.py tests/test_upload_client.py tests/test_jobs.py tests/test_api.py
git commit -m "feat: deliver motion ranges to camera-upload"
```

### Task 4: Remove legacy result ownership

**Files:**
- Delete: `/home/woto/work/camera-motion/camera_motion/results.py`
- Delete: `/home/woto/work/camera-motion/tests/test_results.py`
- Delete: `/home/woto/work/camera-motion/tests/test_results_api.py`
- Modify: `/home/woto/work/camera-motion/camera_motion/api.py`
- Modify: `/home/woto/work/camera-motion/tests/test_api.py`
- Modify: `/home/woto/work/camera-motion/README.md`

- [ ] **Step 1: Write and observe the failing absence test**

```python
def test_legacy_results_presence_api_is_removed():
    assert TestClient(app).get("/results", params={"ids":"e1"}).status_code == 404
```

Run it and confirm current status is 200.

- [ ] **Step 2: Remove only the service result store**

Delete the legacy files and remove `ResultStore`, `_store_result`, and `GET /results` from API code. Preserve CLI `result.json`, `MarkingStore`, `data/markings.json`, and every homography route. Document camera-upload ownership and transient pasted-URL jobs.

- [ ] **Step 3: Verify and commit**

```bash
cd /home/woto/work/camera-motion
uv run pytest -q
git add -A camera_motion/results.py camera_motion/api.py tests/test_results.py tests/test_results_api.py tests/test_api.py README.md
git commit -m "refactor: remove camera-motion result ownership"
```

### Task 5: Configure coordinated deployment

**Files:**
- Modify: `/home/woto/work/camera-orchestrator/.env.example`
- Modify: `/home/woto/work/camera-orchestrator/docker-compose.yml`
- Modify: `/home/woto/work/camera-orchestrator/README.md`

- [ ] **Step 1: Verify the token is currently absent**

```bash
cd /home/woto/work/camera-orchestrator
CAMERA_INTERNAL_TOKEN=test-token docker compose config | rg 'CAMERA_INTERNAL_TOKEN'
```

Expected: no match.

- [ ] **Step 2: Add configuration without committing a secret**

Add `CAMERA_INTERNAL_TOKEN=` to `.env.example`; add `${CAMERA_INTERNAL_TOKEN:?set CAMERA_INTERNAL_TOKEN}` to the shared environment passed to both services; document secret generation and rebuilding camera-upload plus camera-motion together. Do not modify or commit the user's `.env`.

- [ ] **Step 3: Verify all suites and Compose**

```bash
cd /home/woto/work/camera-orchestrator
CAMERA_INTERNAL_TOKEN=test-token docker compose config >/tmp/camera-compose-config.yml
rg -n 'CAMERA_INTERNAL_TOKEN: test-token' /tmp/camera-compose-config.yml
cd /home/woto/work/camera-upload
go test -race ./... -count=1
cd /home/woto/work/camera-motion
CAMERA_INTERNAL_TOKEN=test-token uv run pytest -q
```

Expected: all commands pass.

- [ ] **Step 4: Commit**

```bash
cd /home/woto/work/camera-orchestrator
git add .env.example docker-compose.yml README.md
git commit -m "chore: configure motion analysis delivery"
```

### Task 6: Independent review and completion verification

**Files:** No intended production edits; accepted findings get focused regression tests.

- [ ] **Step 1: Request independent frontier review**

Give a fresh reviewer the approved design and Tasks 1–5 commits. Require findings on canonical media binding, ownership, race safety, atomic writes, authentication, idempotency, duration normalization, transient jobs, legacy removal, and unchanged homography markings.

- [ ] **Step 2: Address every accepted blocking/high finding with RED-GREEN**

For each finding: add a failing regression test, observe the expected failure, implement the smallest correction, rerun focused and complete suites, and commit with explicit files.

- [ ] **Step 3: Run clean final verification**

```bash
cd /home/woto/work/camera-upload
git diff --check
go test -race ./... -count=1
cd /home/woto/work/camera-motion
git diff --check
CAMERA_INTERNAL_TOKEN=test-token uv run pytest -q
cd /home/woto/work/camera-orchestrator
git diff --check
CAMERA_INTERNAL_TOKEN=test-token docker compose config >/tmp/camera-compose-config.yml
```

Expected: every command exits 0, no race report appears, and unrelated dirty files remain unstaged.

- [ ] **Step 4: Use verification-before-completion**

Invoke `superpowers:verification-before-completion`, report exact commands and test counts, list camera-upload's new ranges contract, and explicitly state that court markings remain in camera-motion `markings.json`.
