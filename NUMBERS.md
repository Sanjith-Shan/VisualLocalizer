# Numbers

Every number below comes from a committed result file, produced by the command written
next to it. Tools: `core/build/vloc_build`, `core/build/vloc_eval`, `tools/run_scene.sh`,
`tools/score_pgt.py`, `tools/make_tables.py`, `tools/cv_tune.py`, `tools/latency.sh`.

## What this method is, for comparison purposes

Classical SIFT features, a 3D point map, PnP + RANSAC. **Depth images are used at map
build time** (to place 3D points) and never at query time. Query input is one RGB image.
The fair peers are therefore methods in the "RGB + 3D model" category: DSAC* trained with
depth or a 3D model, and feature-matching pipelines whose map uses depth. PoseNet is an
RGB-only regression baseline and is included for scale.

## Tuning protocol and test-set exposure

- Settings were tuned once on one training sequence (heads `seq-02`, the only heads
  training sequence) with 10-fold validation on training frames
  (`tools/cv_tune.py`, results in `results/tuning/heads/*/summary.json`). The same settings
  ran on every scene. Details in `docs/DESIGN.md`.
- Two test-set runs happened before tuning, and neither was used to choose anything:
  1. A pipeline sanity check of 100 heads test frames (`--stride 10`) on the first,
     untuned triangulated map, to confirm the code ran end to end. Not committed.
  2. `results/service/e2e_heads_eval_summary.json`: 50 heads test frames on the same
     pre-tuning map, run by the service side as a parity check that the Go service and
     `vloc_eval` return the same poses.

## Main result: original 7-Scenes GT

Command per scene (`<scene>` in chess fire heads office pumpkin redkitchen stairs):

```bash
tools/run_scene.sh <scene> 10
```

which runs

```bash
core/build/vloc_build --scene-dir ~/Data/7scenes/<scene> --out results/maps/<scene>.vmap --provenance "git <sha>"
core/build/vloc_eval --map results/maps/<scene>.vmap --scene-dir ~/Data/7scenes/<scene> --threads 10 \
  --csv results/<scene>.csv --summary results/<scene>_summary.json
```

Map build commands, git SHA and build statistics are in `results/maps/<scene>_build.json`.
The core sources are identical across the three SHAs recorded there (997488b, 5593c70,
1fbf887); only service files changed between them.

Scored against the raw `pose.txt` of every test frame. Failed frames count as infinite
error. Medians and percentages are over all test frames.

| Scene | Test frames | Localized | Median error | Within 5 cm, 5° | Within 2 cm, 2° | Map points |
|---|---|---|---|---|---|---|
| chess | 2000 | 2000 | 2.67 cm, 0.968° | 88.6% | 33.2% | 488,339 |
| fire | 2000 | 2000 | 2.24 cm, 0.964° | 90.0% | 43.4% | 564,586 |
| heads | 1000 | 1000 | 1.03 cm, 0.787° | 100.0% | 87.5% | 116,008 |
| office | 4000 | 4000 | 3.19 cm, 0.952° | 76.5% | 24.0% | 736,953 |
| pumpkin | 2000 | 1995 | 5.51 cm, 1.375° | 42.9% | 6.5% | 708,929 |
| redkitchen | 5000 | 5000 | 4.60 cm, 1.493° | 55.9% | 11.2% | 1,542,391 |
| stairs | 1000 | 1000 | 2.73 cm, 0.790° | 83.1% | 37.0% | 298,514 |

Sources: `results/<scene>_summary.json`, per-frame rows in `results/<scene>.csv`.

### Against published methods on the same GT

`tools/score_pgt.py` scores Active Search, hloc (SuperPoint + SuperGlue) and DSAC* from the
per-frame estimates the authors released with Brachmann, Humenberger, Rother, Sattler,
"On the Limits of Pseudo Ground Truth in Visual Camera Re-localisation", ICCV 2021
(github.com/tsattler/visloc_pseudo_gt_limitations, `estimates/dslam/7scenes`), against
their release of the original depth-SLAM GT. That release is in the RGB camera frame and
differs from the raw `pose.txt` by a constant camera-side transform (2.56 to 2.57 cm and
0.67°, fitted on training frames, at most 0.085 cm deviation across frames). This work's
estimates are mapped through that transform and scored with the same code, so every cell
is computed the same way.

The baseline percentages reproduce Table 2 ("orig." columns) of that paper: chess, fire,
heads and office match to the decimal (86.4, 86.3, 95.7, 65.6 for Active Search), and
pumpkin, redkitchen and stairs differ by at most 0.3 points.

Median cm / median deg, % within 5 cm, 5°:

| Scene | Active Search | hloc (SP+SG) | DSAC* (RGB) | DSAC* (RGB-D) | This work |
|---|---|---|---|---|---|
| chess | 2.68 / 0.87, 86.4% | 2.40 / 0.77, 94.2% | 1.83 / 0.59, 97.8% | 0.85 / 0.38, 99.4% | 2.66 / 0.97, 88.5% |
| fire | 2.38 / 1.01, 86.3% | 1.81 / 0.75, 93.7% | 1.71 / 0.77, 94.5% | 0.94 / 0.53, 98.9% | 2.24 / 0.96, 89.8% |
| heads | 1.15 / 0.82, 95.7% | 0.93 / 0.59, 99.7% | 1.04 / 0.66, 98.8% | 0.81 / 0.63, 99.9% | 1.03 / 0.79, 100.0% |
| office | 3.85 / 1.15, 65.6% | 2.61 / 0.77, 83.2% | 2.70 / 0.79, 83.9% | 0.96 / 0.47, 98.9% | 3.19 / 0.95, 76.3% |
| pumpkin | 6.57 / 1.69, 34.2% | 4.44 / 1.15, 55.1% | 3.86 / 1.05, 62.0% | 1.66 / 0.58, 80.8% | 5.53 / 1.38, 42.7% |
| redkitchen | 5.37 / 1.72, 45.2% | 4.03 / 1.38, 61.9% | 3.89 / 1.24, 65.5% | 1.88 / 0.80, 92.4% | 4.60 / 1.49, 55.8% |
| stairs | 3.75 / 1.01, 68.1% | 5.05 / 1.46, 49.4% | 3.52 / 0.93, 78.0% | 2.11 / 0.70, 92.6% | 2.72 / 0.79, 83.0% |

Command: `tools/score_pgt.py > results/pgt/table.md`. Data: `results/pgt/scores.json`.
DSAC* (RGB) here is the release's RGB-only DSAC*. DSAC* (RGB-D) uses depth at query time,
which this work does not.

Published numbers from papers (not recomputable from released estimates):

| Scene | PoseNet | DSAC* (RGB + 3D model), median | DSAC* (RGB + 3D model), within 5 cm, 5° |
|---|---|---|---|
| chess | 32 cm, 8.12° | 1.8 cm, 1.10° | 97.5% |
| fire | 47 cm, 14.4° | 1.9 cm, 1.24° | 93.5% |
| heads | 29 cm, 12.0° | 1.1 cm, 1.82° | 99.8% |
| office | 48 cm, 7.68° | 2.5 cm, 1.15° | 90.0% |
| pumpkin | 47 cm, 8.42° | 3.9 cm, 1.34° | 62.4% |
| redkitchen | 59 cm, 8.64° | 3.8 cm, 1.68° | 65.3% |
| stairs | 47 cm, 13.8° | 2.9 cm, 1.16° | 87.5% |

Sources: PoseNet, Kendall, Grimes, Cipolla, ICCV 2015, arXiv:1505.07427, Figure 6 table,
"PoseNet" column. DSAC*, Brachmann and Rother, TPAMI 2021, arXiv:2002.12324, Figure 6,
"RGB + 3D model" row. Kept in `tools/baselines.json`, table from `tools/make_tables.py`
(`results/table.md`).

Reading it plainly. Against the original GT this work localizes more frames within 5 cm, 5°
than Active Search on every scene (same feature family), though its median rotation error is
higher on chess. It beats hloc on heads and stairs,
loses on chess, fire, office, pumpkin and redkitchen. DSAC* (RGB and RGB + 3D model) is
more accurate on chess, fire, office, pumpkin and redkitchen. DSAC* (RGB-D), which uses
depth at query time, is better everywhere except heads.

## Second result: SfM pseudo GT

The same paper releases an SfM pseudo GT for 7-Scenes, with its own per-scene focal
length. Under it, the map has to live in the SfM frame, so maps are built by triangulating
SIFT matches between training frames with the SfM training poses (`--mode tri`, no depth),
and test frames are scored against the SfM test poses. All localization settings are the
heads-tuned defaults; nothing was retuned for this GT.

```bash
tools/run_scene.sh <scene> 10 sfm
# = vloc_build --mode tri --pose-file ~/Data/7scenes_pgt/sfm/<scene>_train.txt ...
#   vloc_eval --pose-file ~/Data/7scenes_pgt/sfm/<scene>_test.txt ...
```

Median cm / median deg, % within 5 cm, 5°, all from `tools/score_pgt.py` over the
released estimates in `estimates/sfm/7scenes`:

| Scene | Active Search | hloc (SP+SG) | DSAC* (RGB) | DSAC* (RGB-D) | This work |
|---|---|---|---|---|---|
| chess | 0.43 / 0.14, 99.9% | 0.78 / 0.11, 100.0% | 0.50 / 0.17, 99.9% | 1.42 / 0.45, 99.6% | 0.49 / 0.13, 100.0% |
| fire | 0.55 / 0.23, 99.8% | 0.87 / 0.24, 99.4% | 0.78 / 0.28, 98.9% | 1.87 / 0.66, 96.9% | 0.47 / 0.21, 99.9% |
| heads | 0.46 / 0.28, 100.0% | 0.60 / 0.25, 100.0% | 0.50 / 0.34, 99.8% | 1.05 / 0.70, 99.5% | 0.43 / 0.29, 100.0% |
| office | 0.82 / 0.24, 98.6% | 1.24 / 0.20, 100.0% | 1.16 / 0.34, 98.1% | 1.76 / 0.51, 95.3% | 1.11 / 0.27, 99.0% |
| pumpkin | 0.90 / 0.20, 99.6% | 1.41 / 0.15, 100.0% | 1.17 / 0.28, 99.0% | 2.02 / 0.59, 90.9% | 1.15 / 0.24, 96.9% |
| redkitchen | 0.55 / 0.16, 99.8% | 1.10 / 0.14, 98.6% | 0.74 / 0.21, 97.0% | 1.58 / 0.44, 96.4% | 0.77 / 0.18, 100.0% |
| stairs | 1.43 / 0.44, 91.9% | 2.89 / 0.80, 72.0% | 2.65 / 0.78, 92.0% | 2.83 / 0.85, 88.4% | 1.57 / 0.48, 92.2% |

Per-scene files: `results/sfm/<scene>_summary.json`, `results/sfm/<scene>.csv`,
`results/sfm/<scene>_build.json`. The `vloc_eval` summaries agree with this table
(for example heads 0.43 cm, 0.287°, 100%).

The ranking flips between GTs, which is the finding of that paper: a method scores best
against the GT whose construction it resembles. Under SfM GT, with a triangulated map,
this work's median translation is within 3 mm of Active Search (which reuses the SfM
model's own features) on every scene, lower than hloc's on every scene, and lower than
RGB DSAC*'s on six of seven. hloc keeps lower median rotation on five scenes. Under the depth-SLAM GT,
with a depth map, it is behind the learned methods. Neither table alone is an absolute
accuracy measurement.

## Side by side

Median cm / deg and % within 5 cm, 5° for this work under each GT:

| Scene | Original GT (depth map) | SfM pseudo GT (triangulated map) |
|---|---|---|
| chess | 2.67 / 0.97, 88.6% | 0.49 / 0.13, 100.0% |
| fire | 2.24 / 0.96, 90.0% | 0.47 / 0.21, 99.9% |
| heads | 1.03 / 0.79, 100.0% | 0.43 / 0.29, 100.0% |
| office | 3.19 / 0.95, 76.5% | 1.11 / 0.27, 99.0% |
| pumpkin | 5.51 / 1.38, 42.9% | 1.15 / 0.24, 96.9% |
| redkitchen | 4.60 / 1.49, 55.9% | 0.77 / 0.18, 100.0% |
| stairs | 2.73 / 0.79, 83.1% | 1.57 / 0.48, 92.2% |

## Throughput

From the main runs above: 10 eval threads on this Mac (6 performance + 6 efficiency
cores), `cv::setNumThreads(1)` inside the evaluator, other agents' work running on the
machine at the same time, so these are lower bounds.

| Scene | chess | fire | heads | office | pumpkin | redkitchen | stairs |
|---|---|---|---|---|---|---|---|
| Frames per second | 48.6 | 25.8 | 51.0 | 52.9 | 44.9 | 42.6 | 49.6 |

Source: `throughput_fps` in `results/<scene>_summary.json`.

## Latency

Per-request latency: one request at a time (`--threads 1`, `VLOC_CV_THREADS=1`), every
10th test frame of each scene, timed from encoded JPEG bytes to pose. Same settings on all
scenes. Run after the service benchmark finished, each scene starting only once the
1-minute load average was below 3 (chess, fire) or 4.5 (the rest). The threshold was
raised because this Mac sits at a load of 2 to 4 with nothing of ours running (a system
Bluetooth daemon holds one core at 100%). Load at the start of each scene is in
`results/latency/<scene>_load.json`.

```bash
tools/latency.sh                                   # chess, fire
VLOC_MAX_LOAD=4.5 VLOC_SCENES="heads office pumpkin redkitchen stairs" tools/latency.sh
```

| Scene | Frames | p50 ms | p90 ms | p99 ms | Extract p50 | Match p50 | Pose p50 | Map points |
|---|---|---|---|---|---|---|---|---|
| chess | 200 | 131.7 | 164.7 | 177.5 | 64.6 | 55.6 | 10.9 | 488,339 |
| fire | 200 | 299.7 | 334.6 | 1020.2 | 106.8 | 173.7 | 17.3 | 564,586 |
| heads | 100 | 95.6 | 123.9 | 150.8 | 53.1 | 29.6 | 10.5 | 116,008 |
| office | 400 | 139.2 | 222.3 | 291.1 | 63.8 | 62.3 | 11.4 | 736,953 |
| pumpkin | 200 | 179.3 | 256.0 | 437.5 | 74.0 | 87.9 | 15.0 | 708,929 |
| redkitchen | 500 | 220.6 | 295.4 | 331.7 | 82.5 | 120.3 | 15.6 | 1,542,391 |
| stairs | 100 | 170.4 | 241.5 | 267.2 | 79.5 | 76.9 | 13.7 | 298,514 |

Sources: `results/latency/<scene>_1thread.json` and per-frame `_1thread.csv`. Decode is
about 1 ms everywhere. SIFT extraction and kd-forest matching dominate, and matching grows
with map size and with how textured the scene is (fire yields the most keypoints per
frame). With 100 to 500 frames per scene, p99 is close to the maximum and should be read
that way. The tail is not a property of particular frames: fire `seq-04/frame-000950`
spent 60.7 s in matching in this run (nothing is excluded; it is the slowest of 200, and
the 1020 ms p99 is interpolated between the next two, 1019 and 1136 ms), but took 198 ms of
matching in the main run. Redkitchen `seq-04/frame-000880` took 1494 ms of matching here,
and 143, 153 and 517 ms when rerun alone three times. The likely cause is the host (other
agents' jobs, and a disk at 99% full under memory pressure) rather than the algorithm, but
that is not proven, so the numbers stand as measured.

## Validation folds (heads training data)

See `docs/DESIGN.md` for the full tuning table. Final settings on the 10 heads training
folds: 100% localized, median 1.10 cm and 0.79°, 99.0% within 5 cm, 5°
(`results/tuning/heads/final_defaults/summary.json`, command
`tools/cv_tune.py --scene heads --tag final_defaults`).
