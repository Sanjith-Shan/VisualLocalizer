# Positioning service (`service/`)

A Go service in front of the C++ localization core. It takes a photo and camera
intrinsics, runs the core through cgo, and returns the camera's 6-DoF pose in a loaded
map. Maps can be uploaded and swapped while traffic is running. Load is bounded by a
fixed worker pool with deadline-aware admission control.

## Caveats first

- Every number below was measured on one shared Mac (Apple silicon, 6 performance plus
  6 efficiency cores) with the load generator on the same machine. Other work ran on the
  host at the same time, including the core's own evaluations. Each result file stores
  the host load average at the start and end of every rate so a noisy run is visible.
  Treat absolute capacity as this host's, not a general claim.
- Capacity is about the heads map. A bigger map has a slower match stage.
- The service adds no accuracy. Poses are the core's, bit for bit (checked below).

## Measured (heads map, real core)

### Pose parity with the core's evaluator

The service returns the same pose as `vloc_eval --c-abi` for the same frame. 50 frames
(every 20th heads test frame): identical ok flags, identical keypoint, match and inlier
counts, and translation and rotation errors equal within the CSV's printed precision (max
difference 4.9e-6 m and 4.9e-5 deg).
Command: `make -C service e2e` (or `service/scripts/e2e.sh`). Result: `results/service/e2e_heads.json`.

### Service overhead

At 5 req/s (no queueing, host load average 3.8), 150 requests, 6 workers:

| | p50 ms |
| --- | --- |
| core's own stage sum (decode+extract+match+pose) | 98.9 |
| server handler wall time | 99.0 |
| server minus core (validation, admission, cgo, JSON) | 0.11 |
| client latency minus core (adds HTTP, loopback, client) | 1.5 |

Client p50 / p95 / p99: 100.3 / 128.0 / 147.1 ms.
Command: `service/scripts/bench.sh overhead_5rps "-log-level warn" "-rates 5 -duration 30s -warmup 3s -deadline-ms 1000"`.
Result: `results/service/overhead_5rps.json`.

### Capacity sweep: INTERIM

The full sweep (`SKIP_OVERHEAD=1 service/scripts/sweep.sh`, shedding on vs off up to 2x
capacity) has not produced a clean run yet: the host was saturated by other work (map
builds and evaluations at 700 to 950% CPU, load average 40 to 97) each time, and the
bench script now refuses to measure on a busy host. The only clean points so far are the
low rates of a run whose upper rates were contaminated and which also exposed the
admission bug fixed since (`results/service/sweep_shed_on_spiral.json`, BUG_LOG Service 1):
10 req/s gave p50 101 ms, p99 187 ms, no shedding; 30 req/s gave p50 98 ms, p99 256 ms,
no shedding (host load 3.8 to 6.8). With a 99 ms median core call on 6 workers the
expected ceiling is about 60 req/s; that is not yet measured.


## API

The full contract is `service/api/openapi.yaml` (OpenAPI 3.0). A gRPC face with the same
behaviour is `service/api/proto/vloc.proto` (`-grpc-addr` to enable).

| Route | What |
| --- | --- |
| `POST /v1/maps/{name}/localize` | Body is JPEG or PNG bytes. Intrinsics as `?fx=&fy=&cx=&cy=` or header `X-Intrinsics: fx,fy,cx,cy`. Deadline as `X-Deadline-Ms` (default 2000, max 30000). Returns pose, inlier counts, per-stage timings, request id. |
| `PUT /v1/maps/{name}` | Upload a `.vmap`. Header and size are validated, the map is loaded from a temp file, then swapped in atomically. 201 new, 200 replaced. |
| `DELETE /v1/maps/{name}` | Unload (freed after its in-flight requests). |
| `GET /v1/maps` | Loaded maps with version, size, load time, requests holding each one. |
| `GET /healthz` | Liveness. |
| `GET /readyz` | 200 once a map is loaded and the server is not draining. |
| `GET /metrics` | Prometheus. |

Example:

```bash
curl -s -XPOST --data-binary @frame-000000.color.jpg -H 'X-Deadline-Ms: 1000' \
  'http://127.0.0.1:8080/v1/maps/heads/localize?fx=525&fy=525&cx=320&cy=240'
```

```json
{"request_id":"108ca999b549621d","map":"heads","map_version":1,"ok":true,
 "pose":{"q":{"w":0.9754,"x":-0.1255,"y":-0.1804,"z":0.0168},"t":{"x":-0.1548,"y":-0.1269,"z":0.1765}},
 "num_keypoints":949,"num_matches":435,"num_inliers":365,"image_width":640,"image_height":480,
 "timings_ms":{"queue":0.02,"decode":4.9,"extract":162.8,"match":209.7,"pose":20.4,
               "core":397.8,"worker":397.9,"service":398.7,"overhead":0.87}}
```

(That is the first request after start, with cold caches. Steady state is in the tables.)

A pose is camera-to-world, the same convention as the 7-Scenes `pose.txt` files. `ok:false`
with a 200 means the core ran and found no pose; `reason` says why.

### Errors

One JSON shape, `{"error":{"code","message"},"request_id"}`, with stable codes:

| Status | Code | When |
| --- | --- | --- |
| 400 | `BAD_MAP_NAME` | name not `^[a-z0-9][a-z0-9_-]{0,63}$` |
| 404 | `MAP_NOT_FOUND` | no such map loaded |
| 400 | `EMPTY_BODY` | no bytes |
| 413 | `IMAGE_TOO_LARGE` / `MAP_TOO_LARGE` | over 8 MiB image / 2 GiB map (flags) |
| 415 | `UNSUPPORTED_MEDIA_TYPE` | bytes do not sniff as JPEG or PNG (Content-Type is not trusted) |
| 400 | `BAD_IMAGE` | header unreadable (truncated, corrupt) |
| 422 | `BAD_IMAGE_SIZE` | a side outside 32..4096 px |
| 400 | `MISSING_INTRINSICS` | any of fx, fy, cx, cy absent |
| 400/422 | `BAD_INTRINSICS` | not a number, not finite, focal not positive or absurd, principal point outside the image |
| 400 | `BAD_DEADLINE` | not a positive number |
| 422 | `BAD_MAP_HEADER` | wrong magic or version, bad counts, file shorter than its header declares |
| 422 | `MAP_LOAD_FAILED` | the core refused the file (reason from `vloc_map_load`) |
| 503 | `OVERLOADED` | shed by admission control, with `Retry-After` |
| 504 | `DEADLINE_EXCEEDED` | deadline passed while queued; the core never ran it |
| 503 | `SHUTTING_DOWN` | draining |
| 500 | `CORE_ERROR` | the core call itself failed |

Image validation reads only the image header (`image.DecodeConfig`), so a rejected
upload costs microseconds, not a decode.

## Architecture

```
HTTP / gRPC ──> instrument (request id, root span, latency histogram, one JSON log line)
            ──> validate (size, sniff, dimensions, intrinsics, deadline)
            ──> registry.Acquire(map)            refcount +1
            ──> admit.Pool.Do(ctx, localize)     admission, FIFO queue, N locked OS threads
                   └─ cgo: vloc_localize(map, image, K)
            ──> stage metrics + spans from vloc_result timings
            ──> handle.Release()                 refcount -1, last one frees the old map
```

| Package | Role |
| --- | --- |
| `internal/vloc` | Go view of `vloc.h`. `Engine` loads maps, `Map` localizes. Two engines: `cgo` (build tag `vloc`, links `core/build/libvloc.dylib`) and `fake` (deterministic pose from a hash of the image, burns a set CPU time, panics on use after free). |
| `internal/maps` | Name to handle registry with atomic hot swap and reference counting. |
| `internal/admit` | Fixed worker pool and admission control. |
| `internal/api` | HTTP and gRPC handlers, validation, error codes, logging. |
| `internal/obs` | Prometheus metrics and OpenTelemetry tracer setup. |
| `cmd/vlocd` | The server. |
| `cmd/vlocload` | Open-loop load generator. |

### Hot swap

Each loaded map sits in a handle whose count starts at 1 (the registry's reference).
A request takes a reference with a compare-and-swap that refuses once the count has hit
zero, so it can never resurrect a map being freed; if it loses that race it reloads the
slot and gets the replacement. A swap is one atomic pointer store followed by dropping the
registry's reference on the old handle. Requests already running finish on the old map,
new requests see the new one, and whichever release brings the old count to zero calls
`vloc_map_free`. The response's `map_version` says which map served it. An upload that
fails validation or loading never touches the serving map, on disk or in memory.

### Worker pool and admission

`vloc_localize` is CPU bound and blocks its OS thread for the whole call, so the real
concurrency limit is a fixed set of worker goroutines, each locked to an OS thread. The
default is the number of performance cores on Apple silicon (`hw.perflevel0.logicalcpu`,
6 here) and CPUs minus one elsewhere. Run the server with `VLOC_CV_THREADS=1` so the
core does not add its own threads on top.

Jobs wait in a bounded FIFO queue (default 4 x workers). A job is rejected before it is
queued when:

1. the queue is full (`queue_full`), or
2. the estimated wait plus its own run time would overrun its deadline
   (`would_miss_deadline`). The wait estimate is the work ahead of it divided across the
   workers, in units of the EWMA run time. Its own run time is budgeted as EWMA mean plus
   two EWMA mean absolute deviations, because run time varies by frame and by core type.

Both return 503 with `Retry-After` (the estimated wait, at least 1 s). A job whose
deadline passes while it waits is dropped by the worker without calling the core (504).
Refusing at the door costs well under a millisecond; running a request that will be
too late costs a full core call and delays everything behind it.

### Observability

Metrics (own registry, plus Go and process collectors):

| Metric | Labels |
| --- | --- |
| `vloc_http_request_duration_seconds` histogram | `route` (mux pattern), `outcome` (ok, no_pose, shed, timeout, client_error, error) |
| `vloc_stage_duration_seconds` histogram | `stage`: queue, decode, extract, match, pose (from `vloc_result`), core, overhead |
| `vloc_localize_inliers` histogram | |
| `vloc_queue_depth`, `vloc_inflight`, `vloc_maps_loaded` gauges | |
| `vloc_service_time_estimate_seconds` gauge | the admission EWMA |
| `vloc_shed_total` counter | `reason` |
| `vloc_map_events_total` counter | `map`, `event`: loaded, swapped, freed, deleted, load_failed, rejected |

Logs are `log/slog` JSON on stderr, one line per request with `request_id`, route,
status, outcome, duration, map, inliers, queue and core time, and the shed reason.

Tracing is OpenTelemetry. `-trace auto` (default) exports over OTLP/HTTP when
`OTEL_EXPORTER_OTLP_ENDPOINT` is set and is a no-op otherwise; `-trace stdout` prints spans
for a demo. Each localize request has a root span with children `validate`, `queue`,
`core.localize`, and under it `decode`, `extract`, `match`, `pose`. The core reports
durations, not timestamps, so the four stage spans are laid end to end from the moment a
worker picked the job up.

## Build, test, run

```bash
cd service
make race          # go test -race ./... (fake engine, no C toolchain needed)
make build-core    # links core/build/libvloc.dylib, needs the core built (docs/CORE_READY.md)
make run-core      # serves heads on 127.0.0.1:8080
make run-fake      # same service, fake engine
make e2e           # pose parity with vloc_eval
make sweep         # the measurements below
scripts/hotswap.sh # swap the real map under load
```

Server flags worth knowing: `-workers`, `-queue`, `-no-deadline-shed`,
`-default-deadline`, `-max-image-bytes`, `-max-map-bytes`, `-map name=path`, `-map-dir`,
`-grpc-addr`, `-trace`, `-log-level`.

### What the tests cover

`go test -race ./...` (all pass):

- Happy path, determinism (same frame, same pose; header and query intrinsics agree),
  PNG and JPEG.
- 19 validation cases, each checking status, error code and request id.
- Ingestion: bad magic, wrong version, short, empty, bad names; a bad upload never
  replaces the serving map; replace returns the previous version; delete frees.
- Hot swap under load: 16 goroutines localizing while the map is replaced 40 times.
  Every request succeeds, every replaced map is freed exactly once, none while in use
  (the fake map panics otherwise). A registry-level stress test swaps 2000 times.
- Shedding: 40 concurrent requests on one 20 ms worker with a 100 ms deadline; the
  excess gets 503 with `Retry-After` and every admitted request finishes in time.
- Admission unit tests: queue full, would-miss, expired in queue.
- Metrics exposition, gRPC parity with HTTP, core `.vmap` header and size checks.

## Load generator

`service/cmd/vlocload` replays real 7-Scenes test frames (spread across all sequences,
read once into memory) at a constant arrival rate. It is open loop: requests go out on
schedule whether or not earlier ones have returned, so an overloaded server cannot slow
the offered load down. Latency is measured from each request's scheduled send time, so
any lag in the generator itself counts against the result instead of hiding it. Latency
percentiles are over 200 responses; shed (503) and timeout (504) are counted separately.
Each rate has an unmeasured warmup.
