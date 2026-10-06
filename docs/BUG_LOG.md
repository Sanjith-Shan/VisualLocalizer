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

## Service 1. Admission shed 100% of traffic and never recovered

- **Symptom.** In the first capacity sweep of the service (`results/service/sweep_shed_on_spiral.json`),
  every request from 100 req/s upward got 503, including after the load dropped, with
  zero successful requests per rate.
- **Cause.** Deadline-aware admission rejects a job when the estimated wait plus its own
  run time would overrun its deadline. The run-time estimate is an EWMA updated only by
  jobs that run. A burst on a contended host pushed the estimate (mean plus two
  deviations) above the 1 s deadline, after which every job was rejected, no job ran, and
  the estimate could never come down.
- **Fix.** A job is always admitted when a worker is free and nothing is queued, since it
  starts at once. That is also the probe that lets the estimate recover.
  `TestIdleAlwaysAdmits` in `service/internal/admit` covers it.

## Service 2. Admitted requests finished past their deadline

- **Symptom.** Client p99 above the 1 s deadline at saturation (`results/service/probe.json`,
  1580 ms at 160 req/s).
- **Cause.** Admission budgeted the job's own run time as the EWMA mean. Run time varies by
  frame and, on a hybrid CPU, by core type, so the slow half of admitted jobs overran.
- **Fix.** Budget mean plus two EWMA mean absolute deviations, and default the worker count
  to the performance cores (see Service 3).

## Service 3. Filling every core with workers made latency worse, not throughput better

- **Symptom.** At 150 req/s, 12 workers gave 59.8 ok/s with p99 1277 ms and 37 deadline
  expiries in the queue; 6 workers gave 55.1 ok/s with p99 809 ms and none
  (`results/service/workers_12.json`, `workers_6.json`).
- **Cause.** The efficiency cores run a localize call about twice as slowly (core p50 179 ms
  versus 90 ms), and 12 CPU-bound locked threads starve the HTTP front end and the load
  generator, which shows as latency the server's own clock does not see.
- **Fix.** Default workers to `hw.perflevel0.logicalcpu` on Apple silicon, CPUs minus one
  elsewhere.

## Service 4. Pose JSON had capitalized keys

- **Symptom.** `{"q":{"W":...}}` instead of `{"q":{"w":...}}`.
- **Cause.** Anonymous structs without JSON tags.
- **Fix.** Named `Quat` and `Vec3` types with lowercase tags.
