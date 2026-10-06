# VisualLocalizer build brief

A visual relocalization service. Send a photo, get back the camera's 6-DoF pose in a prebuilt
3D map, measured on a public benchmark (Microsoft 7-Scenes) against published baselines.

Built for a backend systems internship that owns positioning services in Go and C++.
**Adjacent, not a clone.** Never name any company in code, docs or README. Credit published work
(Sattler et al. Active Search, PoseNet, hloc, DSAC*) in `docs/DESIGN.md`.

## Layout (each part has one owner, do not edit another part's files)

| Path | Owner | What |
| --- | --- | --- |
| `core/include/vloc.h` | fixed | C ABI contract between core and service |
| `core/` | core agent | C++20 library + CLI: map loading, feature extraction, 2D-3D matching, PnP + RANSAC, eval binary |
| `tools/` | core agent | Python map builder + eval scoring + baseline table |
| `service/` | service agent | Go HTTP service over cgo, ingestion, metrics, tracing, load test |
| `~/Data/7scenes/` | data agent | Dataset, outside the repo, never committed |

## Data layout (`~/Data/7scenes/<scene>/`)

- `train/seq-XX/frame-NNNNNN.{color.jpg,depth.png,pose.txt}` for every 5th training frame
- `test/seq-XX/frame-NNNNNN.{color.jpg,pose.txt}` for every test frame (no depth needed)
- `TrainSplit.txt`, `TestSplit.txt` copied from the original
- Color is the original 640x480 PNG re-encoded as JPEG quality 95 to fit disk. Depth is the
  original 16-bit PNG in millimetres, 65535 = invalid. Poses are 4x4 camera-to-world.
- Color intrinsics: fx = fy = 525, cx = 320, cy = 240 (the dataset's standard values).
- `~/Data/7scenes/READY_<scene>` is touched when a scene is complete.

## Rules

- Never fabricate a number. Every number in README/NUMBERS.md comes from a committed result file
  produced by a command written next to it.
- Log every bug in `docs/BUG_LOG.md` (symptom, cause, fix).
- Honest caveats go in the README up front.
- No em dashes anywhere in README or docs.
- Small commits with plain messages. No Claude attribution trailers in commits.
