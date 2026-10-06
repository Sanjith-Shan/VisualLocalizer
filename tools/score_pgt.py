#!/usr/bin/env python3
"""Score this work and the published baselines against the pseudo ground truth files of
Brachmann, Humenberger, Rother, Sattler, "On the Limits of Pseudo Ground Truth in Visual
Camera Re-localisation", ICCV 2021 (github.com/tsattler/visloc_pseudo_gt_limitations).

Two GTs:
  dslam  their release of the original depth-SLAM GT, expressed in the RGB camera frame.
         It differs from the raw 7-Scenes pose.txt by a constant camera-side transform X
         (about 2.6 cm and 0.67 deg). Our dslam-run estimates are in the raw pose.txt frame,
         so they are mapped with T_est * X, where X is the mean of T_raw^-1 T_pgt over the
         TRAINING frames (test poses are not used to fit it).
  sfm    their SfM pseudo GT. Our sfm-run maps were built from its training poses, so the
         estimates are already in that frame.

Baselines are scored from the authors' released per-frame estimates with the same code,
so every row in the table is computed the same way: all test frames in the pGT file,
a missing or failed frame counts as infinite error.

Usage: tools/score_pgt.py [--pgt ~/Data/7scenes_pgt] > results/pgt/table.md
Needs <pgt>/{dslam,sfm}/<scene>_{train,test}.txt and <pgt>/est/{dslam,sfm}/<scene>_<method>.txt.
"""
import argparse
import csv
import glob
import json
import math
import os

import numpy as np

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCENES = ["chess", "fire", "heads", "office", "pumpkin", "redkitchen", "stairs"]
METHODS = [("as", "Active Search"), ("hloc", "hloc (SP+SG)"), ("dsac_rgb", "DSAC* (RGB)"),
           ("dsac_rgbd", "DSAC* (RGB-D)")]


def q2R(w, x, y, z):
    n = math.sqrt(w * w + x * x + y * y + z * z)
    w, x, y, z = w / n, x / n, y / n, z / n
    return np.array([[1 - 2 * (y * y + z * z), 2 * (x * y - z * w), 2 * (x * z + y * w)],
                     [2 * (x * y + z * w), 1 - 2 * (x * x + z * z), 2 * (y * z - x * w)],
                     [2 * (x * z - y * w), 2 * (y * z + x * w), 1 - 2 * (x * x + y * y)]])


def T_of(R, c):
    T = np.eye(4)
    T[:3, :3] = R
    T[:3, 3] = c
    return T


def load_w2c(path):
    """Release format: name qw qx qy qz tx ty tz [focal], world-to-camera. Returns cam-to-world."""
    out = {}
    for line in open(path):
        v = line.split()
        if len(v) < 8:
            continue
        R = q2R(*map(float, v[1:5]))
        t = np.array(list(map(float, v[5:8])))
        out[v[0].replace(".color.png", "")] = T_of(R.T, -R.T @ t)
    return out


def load_ours(path):
    out = {}
    for r in csv.DictReader(open(path)):
        if r["ok"] != "1":
            continue
        R = q2R(float(r["qw"]), float(r["qx"]), float(r["qy"]), float(r["qz"]))
        out[r["frame"]] = T_of(R, np.array([float(r["tx"]), float(r["ty"]), float(r["tz"])]))
    return out


def raw_poses(data, scene, split):
    out = {}
    for p in glob.glob(f"{data}/{scene}/{split}/seq-*/frame-*.pose.txt"):
        G = np.loadtxt(p)
        U, _, Vt = np.linalg.svd(G[:3, :3])
        G[:3, :3] = U @ Vt
        out[p.split("/")[-2] + "/" + os.path.basename(p).replace(".pose.txt", "")] = G
    return out


def fit_offset(raw, pgt):
    """Mean camera-side transform X with T_pgt = T_raw X, over frames present in both."""
    Xs = [np.linalg.inv(raw[k]) @ pgt[k] for k in raw if k in pgt]
    M = np.mean([X[:3, :3] for X in Xs], axis=0)
    U, _, Vt = np.linalg.svd(M)
    X = T_of(U @ Vt, np.mean([X[:3, 3] for X in Xs], axis=0))
    spread = float(np.max([np.linalg.norm(Xi[:3, 3] - X[:3, 3]) for Xi in Xs]))
    return X, len(Xs), spread


def score(est, gt):
    t, a = [], []
    for k, G in gt.items():
        if k not in est:
            t.append(np.inf); a.append(np.inf); continue
        E = est[k]
        t.append(np.linalg.norm(E[:3, 3] - G[:3, 3]))
        c = np.clip((np.trace(E[:3, :3].T @ G[:3, :3]) - 1) / 2, -1, 1)
        a.append(math.degrees(math.acos(c)))
    t, a = np.array(t), np.array(a)
    return {"frames": len(t), "median_cm": float(np.median(t) * 100), "median_deg": float(np.median(a)),
            "pct_5cm_5deg": float(100 * np.mean((t < 0.05) & (a < 5))),
            "pct_2cm_2deg": float(100 * np.mean((t < 0.02) & (a < 2)))}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pgt", default=os.path.expanduser("~/Data/7scenes_pgt"))
    ap.add_argument("--data", default=os.path.expanduser("~/Data/7scenes"))
    a = ap.parse_args()
    res = {"dslam": {}, "sfm": {}, "offsets": {}}
    for s in SCENES:
        for g in ["dslam", "sfm"]:
            gt = load_w2c(f"{a.pgt}/{g}/{s}_test.txt")
            row = {}
            for m, _ in METHODS:
                f = f"{a.pgt}/est/{g}/{s}_{m}.txt"
                if os.path.exists(f):
                    row[m] = score(load_w2c(f), gt)
            ours_csv = os.path.join(ROOT, "results", f"{s}.csv") if g == "dslam" else \
                os.path.join(ROOT, "results", "sfm", f"{s}.csv")
            if os.path.exists(ours_csv) and "qw" in open(ours_csv).readline():
                est = load_ours(ours_csv)
                if g == "dslam":
                    X, n, spread = fit_offset(raw_poses(a.data, s, "train"),
                                              load_w2c(f"{a.pgt}/dslam/{s}_train.txt"))
                    ang = math.degrees(math.acos(np.clip((np.trace(X[:3, :3]) - 1) / 2, -1, 1)))
                    res["offsets"][s] = {"train_frames": n, "t_cm": float(np.linalg.norm(X[:3, 3]) * 100),
                                         "r_deg": ang, "max_dev_cm": spread * 100}
                    est = {k: E @ X for k, E in est.items()}
                row["ours"] = score(est, gt)
            res[g][s] = row
    os.makedirs(os.path.join(ROOT, "results", "pgt"), exist_ok=True)
    with open(os.path.join(ROOT, "results", "pgt", "scores.json"), "w") as f:
        json.dump(res, f, indent=1)

    names = [n for _, n in METHODS] + ["This work"]
    keys = [m for m, _ in METHODS] + ["ours"]
    for g, title in [("dslam", "Original depth-SLAM GT (their RGB-frame release)"), ("sfm", "SfM pseudo GT")]:
        print(f"\n#### {title}: median cm / deg, and % within 5 cm, 5°\n")
        print("| Scene | " + " | ".join(names) + " |")
        print("|---|" + "---|" * len(names))
        for s in SCENES:
            cells = []
            for k in keys:
                r = res[g][s].get(k)
                cells.append("n/a" if r is None else
                             f"{r['median_cm']:.2f} / {r['median_deg']:.2f}, {r['pct_5cm_5deg']:.1f}%")
            print(f"| {s} | " + " | ".join(cells) + " |")
    print("\nRaw pose.txt to RGB-frame dslam pGT offset fitted on training frames:\n")
    for s, o in res["offsets"].items():
        print(f"- {s}: {o['t_cm']:.2f} cm, {o['r_deg']:.2f}° from {o['train_frames']} frames "
              f"(max deviation {o['max_dev_cm']:.3f} cm)")


if __name__ == "__main__":
    main()
