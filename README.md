# VisualLocalizer

Send a photo, get back the camera's 6-DoF pose in a prebuilt 3D map. A classical
structure-based relocalizer (SIFT, 2D-3D matching, PnP + RANSAC, an Active Search style
3D-to-2D step) in C++20 behind a C ABI, served over HTTP by a Go service. Measured on
Microsoft 7-Scenes against published methods.

## Caveats first

- **One machine.** Every number was measured on one Apple Silicon Mac (6 performance and 6
  efficiency cores) that other jobs were also using during the accuracy runs. Throughput
  figures are lower bounds. Latency was measured separately on a quiet host, see
  `NUMBERS.md`.
- **Classical features.** SIFT (RootSIFT) and OpenCV's kd-forest, MAGSAC and P3P. Nothing
  learned. On the original GT this is behind learned methods (DSAC*, hloc) on five of seven
  scenes.
- **Depth at map build time.** The headline maps place 3D points with the depth images.
  Depth is never used at query time, but this puts the method in the "RGB + 3D model"
  category, and its fair peers are DSAC* trained with depth and feature pipelines with
  depth maps, not RGB-only methods.
- **Depth is not registered to color in 7-Scenes.** The builder registers it with the
  depth intrinsics and a fixed 2.5 cm offset fitted on heads training data. That is an
  approximation, not a calibration.
- **Ground truth is itself an estimate.** The original 7-Scenes poses come from depth SLAM.
  Methods that build maps from depth line up with it better. Against the SfM pseudo GT of
  Brachmann et al. (ICCV 2021) the same pipeline, with a triangulated map, scores very
  differently. Both are reported, the original GT is the headline.
- **JPEG re-encode.** To fit disk, the 7-Scenes color PNGs were re-encoded as JPEG
  quality 95 before anything ran. The published baselines used the original PNGs.
- **Tuned on one sequence.** All settings were tuned once on heads `seq-02` training
  frames with 10-fold validation, then applied unchanged to every scene. Two small test
  runs happened before tuning as pipeline checks (disclosed in `NUMBERS.md`).
- **Own RANSAC/P3P not shipped.** The pose solver is OpenCV's. A hand-written one was not
  built.

## Results (original 7-Scenes GT, all test frames)

| Scene | Median error | Within 5 cm, 5° |
|---|---|---|
| chess | 2.67 cm, 0.97° | 88.6% |
| fire | 2.24 cm, 0.96° | 90.0% |
| heads | 1.03 cm, 0.79° | 100.0% |
| office | 3.19 cm, 0.95° | 76.5% |
| pumpkin | 5.51 cm, 1.38° | 42.9% |
| redkitchen | 4.60 cm, 1.49° | 55.9% |
| stairs | 2.73 cm, 0.79° | 83.1% |

Against Active Search, hloc and DSAC* scored from their released per-frame estimates with
the same code, this work localizes more frames within 5 cm, 5° than Active Search on all
seven scenes, beats hloc on heads and stairs, and trails DSAC* on five scenes. Full tables,
the SfM pseudo GT results, latency and every command are in `NUMBERS.md`.

## Layout

| Path | What |
|---|---|
| `core/include/vloc.h` | C ABI: load a map, localize JPEG/PNG bytes, thread-safe |
| `core/src/` | map format, feature extraction, matching, pose estimation |
| `core/apps/` | `vloc_build` (map builder), `vloc_eval` (benchmark) |
| `core/tests/` | GoogleTest: synthetic PnP, map round trip, 8-thread determinism |
| `service/` | Go HTTP service over cgo (see `docs/SERVICE.md`) |
| `tools/` | tuning, per-scene runs, pseudo GT scoring, latency, tables |
| `results/` | per-frame CSVs and summaries behind every number |

## Build and run

See `docs/SETUP.md` for the toolchain (project-local conda env, OpenCV 5 headless).

```bash
E=~/.local/envs/visualloc
$E/bin/cmake -S core -B core/build -G Ninja -DCMAKE_BUILD_TYPE=Release -DCMAKE_PREFIX_PATH=$E \
  -DCMAKE_MAKE_PROGRAM=$E/bin/ninja
$E/bin/cmake --build core/build && (cd core/build && $E/bin/ctest)
tools/run_scene.sh heads        # build the heads map and evaluate all 1000 test frames
```

Design, credits and the tuning record: `docs/DESIGN.md`. Bugs found on the way:
`docs/BUG_LOG.md`.
