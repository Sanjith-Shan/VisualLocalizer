# Bug log

Each entry: symptom, cause, fix.

## 1. Every frame showed about 1.3 degrees of rotation error

- **Symptom.** On the training validation folds, translation error was 0.8 cm median but
  rotation error sat between 1.27 and 1.87 degrees on 90% of frames, with no low tail. A
  sub-centimetre position with a constant 1.5 degree rotation error is not a real pose
  error pattern.
- **Cause.** The 7-Scenes pose files store rotations that are not exactly orthonormal.
  For `heads/train/seq-02/frame-000000`, the diagonal of R^T R is about 0.9997 and
  det(R) is 0.99954. The error metric `acos((trace(R_est^T R_gt) - 1) / 2)` is very
  sensitive near zero, so a 0.03% shrink in the trace reads as about 1.3 degrees.
- **Fix.** `read_pose` in `core/src/dataset.h` projects every GT rotation onto SO(3) with
  an SVD (U V^T) before use, in both the map builder and the evaluator. Median rotation
  error on the same folds dropped from 1.52 to 0.60 degrees, translation unchanged.

## 2. Map builder summary was not valid JSON

- **Symptom.** `tools/cv_tune.py` crashed parsing the builder's summary line.
- **Cause.** In `--mode tri` no depth statistics exist, and the builder printed `nan`,
  which JSON does not allow.
- **Fix.** Unmeasured statistics print as `-1`.

## 3. Shell argument parsing in the tuning driver

- **Symptom.** `cv_tune.py --eval "--no-active"` failed with "expected one argument".
- **Cause.** argparse treats a value starting with `--` as a new option.
- **Fix.** Pass these as `--eval=--no-active` and `--build=--mode depth`.
