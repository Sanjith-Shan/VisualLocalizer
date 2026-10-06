"""Check that vlocd returns the same pose as vloc_eval for the same frames.

vloc_eval's CSV has no raw pose, only the error against ground truth, so this script
recomputes the same two errors from the service's pose (translation error in meters and
rotation angle of R_est^T R_gt, ground truth projected onto SO(3) as vloc_eval does) and
compares them, along with the keypoint, match and inlier counts, frame by frame.

usage: python verify_e2e.py --url http://127.0.0.1:8080 --map heads \
         --scene-dir ~/Data/7scenes/heads --eval-csv eval.csv --out results/service/e2e_heads.json
"""
import argparse, csv, json, os, time, urllib.request, urllib.error
import numpy as np


def gt_pose(path):
    T = np.loadtxt(path).reshape(4, 4)
    U, _, Vt = np.linalg.svd(T[:3, :3])
    return U @ Vt, T[:3, 3]


def quat_to_R(w, x, y, z):
    return np.array([
        [1 - 2 * (y * y + z * z), 2 * (x * y - z * w), 2 * (x * z + y * w)],
        [2 * (x * y + z * w), 1 - 2 * (x * x + z * z), 2 * (y * z - x * w)],
        [2 * (x * z - y * w), 2 * (y * z + x * w), 1 - 2 * (x * x + y * y)]])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://127.0.0.1:8080")
    ap.add_argument("--map", default="heads")
    ap.add_argument("--scene-dir", required=True)
    ap.add_argument("--eval-csv", required=True)
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    scene = os.path.expanduser(a.scene_dir)
    rows = list(csv.DictReader(open(a.eval_csv)))
    url = f"{a.url}/v1/maps/{a.map}/localize?fx=525&fy=525&cx=320&cy=240"
    worst_t = worst_r = 0.0
    count_mismatch, ok_mismatch, checked = [], [], 0
    svc_ms, core_ms = [], []
    for row in rows:
        stem = os.path.join(scene, "test", row["frame"])
        body = open(stem + ".color.jpg", "rb").read()
        req = urllib.request.Request(url, data=body, method="POST",
                                     headers={"Content-Type": "image/jpeg", "X-Deadline-Ms": "30000"})
        out = json.load(urllib.request.urlopen(req))
        checked += 1
        svc_ms.append(out["timings_ms"]["service"]); core_ms.append(out["timings_ms"]["core"])
        if int(out["ok"]) != int(row["ok"]):
            ok_mismatch.append(row["frame"]); continue
        for k in ("keypoints", "matches", "inliers"):
            if out["num_" + k] != int(row[k]):
                count_mismatch.append((row["frame"], k, out["num_" + k], int(row[k])))
        if not out["ok"]:
            continue
        Rg, tg = gt_pose(stem + ".pose.txt")
        q, t = out["pose"]["q"], out["pose"]["t"]
        terr = float(np.linalg.norm(np.array([t["x"], t["y"], t["z"]]) - tg))
        D = quat_to_R(q["w"], q["x"], q["y"], q["z"]).T @ Rg
        rerr = float(np.degrees(np.arccos(np.clip((np.trace(D) - 1) / 2, -1, 1))))
        worst_t = max(worst_t, abs(terr - float(row["trans_err_m"])))
        worst_r = max(worst_r, abs(rerr - float(row["rot_err_deg"])))
    res = {
        "command": "python " + " ".join(os.sys.argv),
        "time": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "frames_checked": checked,
        "ok_flag_mismatches": ok_mismatch,
        "count_mismatches": count_mismatch,
        "max_abs_diff_trans_err_m": worst_t,
        "max_abs_diff_rot_err_deg": worst_r,
        "csv_precision": "vloc_eval writes trans_err_m to 5 decimals and rot_err_deg to 4",
        "identical": not ok_mismatch and not count_mismatch and worst_t <= 1e-5 and worst_r <= 1e-4,
        "service_ms_mean": float(np.mean(svc_ms)), "core_ms_mean": float(np.mean(core_ms)),
    }
    os.makedirs(os.path.dirname(a.out) or ".", exist_ok=True)
    json.dump(res, open(a.out, "w"), indent=2)
    print(json.dumps(res, indent=2))


if __name__ == "__main__":
    main()
