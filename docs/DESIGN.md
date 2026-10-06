# Design

A classical structure-based visual relocalizer in the Active Search family. A map is a
set of 3D points with SIFT descriptors. A query image is localized by matching its SIFT
features to the map's 3D points, then solving for the camera pose with PnP inside RANSAC.
No learned components.

## Credits

- **Active Search.** T. Sattler, B. Leibe, L. Kobbelt. Efficient & Effective Prioritized
  Matching for Large-Scale Image-Based Localization. TPAMI 2017. The overall structure
  (direct 2D-to-3D matching, a 3D-to-2D search around the first pose, prioritised
  early termination) follows this line of work.
- **SIFT and the ratio test.** D. Lowe. Distinctive Image Features from Scale-Invariant
  Keypoints. IJCV 2004.
- **RootSIFT.** R. Arandjelovic, A. Zisserman. Three things everyone should know to
  improve object retrieval. CVPR 2012.
- **P3P.** L. Kneip, D. Scaramuzza, R. Siegwart. A Novel Parametrization of the
  Perspective-Three-Point Problem. CVPR 2011, and M. Persson, K. Nordberg. Lambda Twist.
  ECCV 2018, as the minimal-solver line of work. The code does not reimplement either: it
  calls OpenCV's P3P inside its USAC framework (and AP3P, T. Ke and S. Roumeliotis,
  CVPR 2017, in the classic RANSAC path). See "RANSAC and P3P" below.
- **MAGSAC.** D. Barath, J. Matas, J. Noskova. MAGSAC: marginalizing sample consensus.
  CVPR 2019 (through OpenCV's USAC framework).
- **hloc.** P.-E. Sarlin, C. Cadena, R. Siegwart, M. Dymczyk. From Coarse to Fine.
  CVPR 2019. Used as a published baseline, and its evaluation convention (median errors,
  percent within thresholds) is the one reported here.
- **Kd-forest.** M. Muja, D. Lowe. FLANN. VISAPP 2009 (through OpenCV).
- **Dataset.** J. Shotton et al. Scene Coordinate Regression Forests. CVPR 2013
  (Microsoft 7-Scenes).

## Pipeline

### Map building (`core/apps/vloc_build.cpp`)

1. SIFT on every training frame (every 5th frame of the training sequences), RootSIFT
   descriptors quantised to uint8.
2. Each frame is matched to the next three frames of its sequence (brute force, Lowe
   ratio 0.8), and each match must lie within 3 px (Sampson distance) of the epipolar
   line implied by the ground-truth relative pose. One match per target keypoint.
3. Matches are joined into tracks with union-find. A track that hits one frame twice
   keeps the first observation.
4. Each track gets one 3D point. Three ways were implemented and measured (below):
   - `tri`: linear multi-view triangulation from the GT poses, observations above 4 px
     reprojection error dropped and the point re-solved once, at least 2 degrees of
     parallax.
   - `depth`: the 16-bit depth image registered into the color camera, then each
     observation back-projected, and the per-coordinate median taken. Rejected if more
     than half of the observations reproject worse than 8 px. Single-view keypoints with
     valid depth are kept as points too ("singletons").
   - `hybrid`: `tri` where it succeeds, otherwise `depth`.
5. Descriptor per point: the mean of its observations' descriptors (one descriptor per
   point, as in Active Search).
6. Written as a `.vmap` file (`core/src/vmap.h`): magic, version, name, feature type,
   points, descriptors, descriptor-to-point ids, per-point observing frames, frame poses.
   The reader validates sizes and indices, and a truncated file is rejected.

### Depth registration

7-Scenes depth comes from a separate camera (fx = fy = 585, cx = 320, cy = 240) that is
not registered to the color camera (fx = fy = 525). The builder back-projects each depth
pixel with the depth intrinsics, shifts it by a fixed offset into the color camera frame,
projects it with the color intrinsics and keeps the nearest surface per pixel (z-buffer).
A keypoint reads the registered depth at its pixel, or the nearest surface in a 3x3
window.

The offset was fitted on heads training data only, by the median reprojection error of
depth-derived track points into all frames that observe them (lower is better). A grid
over x in [-5, 0] cm and y in [-1, 1] cm gave a shallow minimum near x = -2 to -3 cm
(1.85 px against 1.96 px at zero offset). The builder uses x = -2.5 cm.

### Triangulation vs depth: measured

On the heads training folds (validation method below), with otherwise identical settings
(SIFT contrast 0.04, ratio 0.8, RANSAC + AP3P at 8 px):

| Map points from | Points per map | Median error | Within 5 cm, 5° |
|---|---|---|---|
| triangulation | 3.6k | 1.53 cm, 1.06° | 83.0% |
| depth, tracked points only | 4.4k | 1.61 cm, 1.07° | 91.0% |
| hybrid | 4.8k | 1.65 cm, 1.04° | 86.5% |
| depth with singletons | 14.8k | 1.64 cm, 0.97° | 93.5% |
| depth with singletons, offset -2.5 cm | 14.6k | 1.53 cm, 0.98° | 93.0% |

Triangulation is not more accurate per point here. Neighbouring training frames are only
5 to 15 raw frames apart, so baselines are short, and the triangulated and depth points of
the same track differ by 3.1 to 4.1 cm median (builder output `tri_vs_depth_median_m`).
Depth also covers single-view features, which triangulation cannot. The map builder
defaults to depth with singletons. Result files: `results/tuning/heads/*/summary.json`.

### Localization (`core/src/localize.cpp`)

1. Decode the JPEG or PNG to grayscale (`cv::imdecode`).
2. SIFT with the same settings as the map (the map records `sift` or `rootsift`, and the
   query follows it).
3. 2D-to-3D matching: every query descriptor searches a randomized kd-forest (4 trees,
   64 leaf checks) built over the map descriptors at load time. The ratio test compares the
   nearest descriptor with the nearest descriptor of a different 3D point. If several query
   features hit one point, the closest wins.
4. Pose: OpenCV `solvePnPRansac` with the USAC framework (MAGSAC scoring, sigma local
   optimisation, OpenCV's P3P minimal solver) at a 12 px threshold, then Levenberg-Marquardt
   refinement on the inliers and re-scoring of all matches. At least 12 inliers.
5. Active Search style 3D-to-2D step: the 10 map frames that see the most inlier points
   are taken as the co-visible neighbourhood. Every point they see is projected into the
   query with the first pose, and matched against query features within 6 px (ratio 0.8).
   The first pose's inliers plus these new matches go through RANSAC again, and the second
   pose is kept if it has at least as many inliers.
6. Pose returned as camera-to-world quaternion and camera centre.

### RANSAC and P3P: why OpenCV's

The brief allowed a hand-written P3P and RANSAC if it matched or beat OpenCV. There was no
time to write one and show that it does, so the shipped code uses OpenCV's solvers. The
choice between the two OpenCV variants was measured on the heads folds: classic RANSAC +
AP3P at 12 px gave 95.5% within 5 cm, 5° (median 1.47 cm), USAC MAGSAC at 12 px gave 96.5%
(median 1.36 cm), and the gap held with denser features (99.0% vs 97.5%, 1.10 cm vs
1.23 cm).

### Thread safety

A loaded map is immutable. `vloc_localize` allocates everything per call. The kd-forest
search through `cv::flann::Index::knnSearch` is not const-qualified in OpenCV, but its
search state lives on the stack, and the test suite runs 40 concurrent calls from 8 threads
on one map and requires bit-identical results. RANSAC randomness is seeded per call, so a
frame gives the same pose regardless of which thread runs it. `VLOC_CV_THREADS` caps
OpenCV's internal pool for servers that already run one call per core.

## Tuning protocol

All settings were chosen on **heads training data only**. Heads has one training sequence
of 200 subsampled frames. `tools/cv_tune.py` runs 10 folds: in fold k, frames with
(position + k) mod 10 == 0 are queries, and the map leaves out those frames and three
neighbours on each side, so the nearest map frame is at least 4 subsampled frames (20 raw
frames) away. Across the folds every training frame is a query once. Test frames were not
used for any choice. The same settings were then run on every scene.

What tuning changed, each measured on those folds:

| Change | Within 5 cm, 5° | Median |
|---|---|---|
| start: triangulated map, SIFT, contrast 0.04, ratio 0.8, RANSAC 8 px | 83.0% | 1.53 cm, 1.06° |
| 3D-to-2D search off (for reference) | 82.0% | 1.84 cm, 1.18° |
| plain SIFT instead of RootSIFT (for reference) | 84.5% | 1.75 cm, 1.14° |
| depth map with singletons, offset -2.5 cm | 93.0% | 1.53 cm, 0.98° |
| ratio 0.9, RANSAC 12 px | 95.5% | 1.47 cm, 0.98° |
| SIFT contrast threshold 0.01 (about 3x the keypoints) | 97.5% | 1.23 cm, 0.85° |
| USAC MAGSAC instead of RANSAC + AP3P (final) | 99.0% | 1.10 cm, 0.79° |

## Evaluation

`vloc_eval` reads each test JPEG from disk, times the full `localize` call from encoded
bytes, and compares with ground truth: translation error is the distance between camera
centres, rotation error is the angle of R_est^T R_gt with the GT rotation first projected
onto SO(3) (see `docs/BUG_LOG.md`, entry 1). A frame that fails to localize counts as
infinite error, so medians and percentages are over all test frames.
