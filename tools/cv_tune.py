#!/usr/bin/env python3
"""K-fold validation on TRAINING frames only, used to pick settings.

Fold k: queries are training frames with (pos + k) % every == 0; the map leaves out those
frames and `gap` neighbours on each side (default every=10, gap=3, so a query is at least
4 subsampled steps, 20 raw frames, from the nearest map frame). Every training frame is a query exactly once across the
folds. Test frames are never touched.

Usage:
  tools/cv_tune.py --scene heads --tag base [--every 5] [--build "--mode tri"] [--eval "--ratio 0.8"]
Writes results/tuning/<scene>/<tag>/fold<k>.csv and summary.json, prints one summary line.
"""
import argparse
import csv
import json
import os
import shlex
import subprocess
import sys

import numpy as np

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUILD = os.path.join(ROOT, "core", "build")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scene", default="heads")
    ap.add_argument("--data", default=os.path.expanduser("~/Data/7scenes"))
    ap.add_argument("--tag", required=True)
    ap.add_argument("--every", type=int, default=10)
    ap.add_argument("--gap", type=int, default=3)
    ap.add_argument("--build", default="")
    ap.add_argument("--eval", default="")
    ap.add_argument("--threads", type=int, default=10)
    a = ap.parse_args()
    out = os.path.join(ROOT, "results", "tuning", a.scene, a.tag)
    os.makedirs(out, exist_ok=True)
    scene_dir = os.path.join(a.data, a.scene)
    rows, builds = [], []
    for k in range(a.every):
        vmap = os.path.join(out, f"fold{k}.vmap")
        cmd = [os.path.join(BUILD, "vloc_build"), "--scene-dir", scene_dir, "--out", vmap,
               "--val-every", str(a.every), "--val-offset", str(k), "--val-gap", str(a.gap)] + shlex.split(a.build)
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode != 0:
            sys.exit(r.stderr)
        builds.append(json.loads(r.stdout.strip().splitlines()[-1]))
        fcsv = os.path.join(out, f"fold{k}.csv")
        cmd = [os.path.join(BUILD, "vloc_eval"), "--map", vmap, "--scene-dir", scene_dir, "--split", "train",
               "--val-every", str(a.every), "--val-offset", str(k), "--threads", str(a.threads),
               "--csv", fcsv] + shlex.split(a.eval)
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode != 0:
            sys.exit(r.stderr)
        os.remove(vmap)
        with open(fcsv) as f:
            rows += list(csv.DictReader(f))
    ok = np.array([int(r["ok"]) for r in rows])
    t = np.array([float(r["trans_err_m"]) if r["ok"] == "1" else np.inf for r in rows])
    q = np.array([float(r["rot_err_deg"]) if r["ok"] == "1" else np.inf for r in rows])
    lat = np.array([float(r["ms_total"]) for r in rows])
    s = {
        "scene": a.scene, "tag": a.tag, "queries": len(rows), "localized_pct": 100 * ok.mean(),
        "median_trans_cm": 100 * float(np.median(t)), "median_rot_deg": float(np.median(q)),
        "pct_5cm_5deg": 100 * float(np.mean((t < 0.05) & (q < 5))),
        "pct_2cm_2deg": 100 * float(np.mean((t < 0.02) & (q < 2))),
        "latency_p50_ms": float(np.median(lat)),
        "map_points_mean": float(np.mean([b["points"] for b in builds])),
        "build": a.build, "eval": a.eval, "every": a.every, "gap": a.gap,
    }
    with open(os.path.join(out, "summary.json"), "w") as f:
        json.dump({"summary": s, "builds": builds}, f, indent=1)
    print(json.dumps(s))


if __name__ == "__main__":
    main()
