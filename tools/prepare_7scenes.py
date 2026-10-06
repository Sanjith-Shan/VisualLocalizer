#!/usr/bin/env python3
"""Download Microsoft 7-Scenes and write a compact copy of each scene.

Output per scene (one tar per scene, rooted at <scene>/):
  train/seq-XX/frame-NNNNNN.{color.jpg,depth.png,pose.txt}  every 5th training frame
  test/seq-XX/frame-NNNNNN.{color.jpg,pose.txt}             every test frame
  TrainSplit.txt, TestSplit.txt                              copied from the original

Color is the original 640x480 PNG re-encoded as JPEG quality 95. Depth PNGs and poses are
copied byte for byte. Sequence zips are read in place, never extracted, so the only large
file on disk at a time is the scene zip itself. The next scene downloads while the current
one is compacted.

Usage:
  python prepare_7scenes.py --work DIR [--stride 5] [--quality 95] scene [scene ...]

For each scene this writes DIR/out/<scene>.tar and then DIR/out/<scene>.json with counts.
The json appears last, so a consumer can treat it as the "tar is complete" marker.
"""
import argparse
import io
import json
import math
import os
import re
import shutil
import subprocess
import sys
import tarfile
import threading
import zipfile
from concurrent.futures import ThreadPoolExecutor

from PIL import Image

URL = "https://download.microsoft.com/download/2/8/5/28564B23-0828-408F-8631-23B1EFF1DAC8/{}.zip"
FRAME_RE = re.compile(r"frame-(\d{6})\.(color\.png|depth\.png|pose\.txt)$")


def log(*a):
    print(*a, flush=True)


def download(scene, dest):
    if os.path.exists(dest) and zipfile.is_zipfile(dest):
        log(f"[{scene}] zip already present")
        return
    tmp = dest + ".part"
    for attempt in range(3):
        log(f"[{scene}] downloading (attempt {attempt + 1})")
        r = subprocess.run(["curl.exe" if os.name == "nt" else "curl", "-sSL", "--retry", "5",
                            "-C", "-", "-o", tmp, URL.format(scene)])
        if r.returncode == 0 and zipfile.is_zipfile(tmp):
            os.replace(tmp, dest)
            log(f"[{scene}] downloaded {os.path.getsize(dest) / 1e9:.2f} GB")
            return
    raise RuntimeError(f"download failed for {scene}")


def split_ids(text):
    ids = []
    for line in text.splitlines():
        m = re.search(r"(\d+)", line)
        if m:
            ids.append(int(m.group(1)))
    return ids


def pose_ok(data):
    vals = data.split()
    if len(vals) != 16:
        return False
    try:
        return all(math.isfinite(float(v)) for v in vals)
    except ValueError:
        return False


def to_jpeg(png_bytes, quality):
    im = Image.open(io.BytesIO(png_bytes)).convert("RGB")
    buf = io.BytesIO()
    im.save(buf, "JPEG", quality=quality)
    return buf.getvalue()


def add_bytes(tar, lock, name, data):
    info = tarfile.TarInfo(name)
    info.size = len(data)
    info.mtime = 0
    with lock:
        tar.addfile(info, io.BytesIO(data))


def compact(scene, zpath, out_dir, stride, quality, pool):
    stats = {"scene": scene, "train_frames": 0, "test_frames": 0, "bad_poses": [],
             "missing": [], "sequences": {}}
    tar_tmp = os.path.join(out_dir, scene + ".tar.part")
    lock = threading.Lock()
    with zipfile.ZipFile(zpath) as outer, tarfile.open(tar_tmp, "w", format=tarfile.GNU_FORMAT) as tar:
        names = outer.namelist()

        def find(base):
            hits = [n for n in names if n.rsplit("/", 1)[-1] == base]
            return hits[0] if hits else None

        splits = {}
        for split, fname in (("train", "TrainSplit.txt"), ("test", "TestSplit.txt")):
            n = find(fname)
            if n is None:
                raise RuntimeError(f"{scene}: {fname} missing from zip")
            data = outer.read(n)
            add_bytes(tar, lock, f"{scene}/{fname}", data)
            for i in split_ids(data.decode()):
                splits[i] = split
        log(f"[{scene}] splits: {sorted((v, k) for k, v in splits.items())}")

        for seq_id in sorted(splits):
            split = splits[seq_id]
            seq = f"seq-{seq_id:02d}"
            inner_name = find(seq + ".zip")
            if inner_name is None:
                raise RuntimeError(f"{scene}: {seq}.zip missing")
            # Extract the inner zip to a temp file next to the outer zip (random access is
            # far faster from disk than through a nested zip stream).
            inner_path = os.path.join(os.path.dirname(zpath), f"{scene}_{seq}.zip")
            with outer.open(inner_name) as src, open(inner_path, "wb") as dst:
                shutil.copyfileobj(src, dst, 1 << 22)
            frames = {}
            with zipfile.ZipFile(inner_path) as inner:
                for n in inner.namelist():
                    m = FRAME_RE.search(n)
                    if m:
                        frames.setdefault(int(m.group(1)), {})[m.group(2)] = n
                keep = sorted(f for f in frames if split == "test" or f % stride == 0)
                need = ["color.png", "pose.txt"] + (["depth.png"] if split == "train" else [])
                prefix = f"{scene}/{split}/{seq}/"
                futures = []
                kept = 0
                for f in keep:
                    parts = frames[f]
                    miss = [k for k in need if k not in parts]
                    if miss:
                        stats["missing"].append(f"{split}/{seq}/frame-{f:06d}:{','.join(miss)}")
                        continue
                    stem = f"{prefix}frame-{f:06d}"
                    pose = inner.read(parts["pose.txt"])
                    if not pose_ok(pose.decode(errors="replace")):
                        stats["bad_poses"].append(f"{split}/{seq}/frame-{f:06d}")
                    add_bytes(tar, lock, stem + ".pose.txt", pose)
                    if split == "train":
                        add_bytes(tar, lock, stem + ".depth.png", inner.read(parts["depth.png"]))
                    png = inner.read(parts["color.png"])
                    futures.append(pool.submit(
                        lambda p=png, s=stem: add_bytes(tar, lock, s + ".color.jpg", to_jpeg(p, quality))))
                    kept += 1
                for fu in futures:
                    fu.result()
            os.remove(inner_path)
            stats["sequences"][seq] = {"split": split, "frames_in_seq": len(frames), "kept": kept}
            stats[f"{split}_frames"] += kept
            log(f"[{scene}] {split}/{seq}: {len(frames)} frames, kept {kept}")
    tar_path = os.path.join(out_dir, scene + ".tar")
    os.replace(tar_tmp, tar_path)
    stats["tar_bytes"] = os.path.getsize(tar_path)
    stats["files_in_tar"] = 2 + stats["train_frames"] * 3 + stats["test_frames"] * 2
    with open(os.path.join(out_dir, scene + ".json"), "w") as fh:
        json.dump(stats, fh, indent=1)
    log(f"[{scene}] done: train {stats['train_frames']} test {stats['test_frames']} "
        f"tar {stats['tar_bytes'] / 1e9:.2f} GB bad_poses {len(stats['bad_poses'])} "
        f"missing {len(stats['missing'])}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--work", required=True)
    ap.add_argument("--stride", type=int, default=5)
    ap.add_argument("--quality", type=int, default=95)
    ap.add_argument("--threads", type=int, default=4)
    ap.add_argument("scenes", nargs="+")
    a = ap.parse_args()
    raw_dir = os.path.join(a.work, "raw")
    out_dir = os.path.join(a.work, "out")
    os.makedirs(raw_dir, exist_ok=True)
    os.makedirs(out_dir, exist_ok=True)

    def zp(s):
        return os.path.join(raw_dir, s + ".zip")

    dl = ThreadPoolExecutor(1)
    pending = {a.scenes[0]: dl.submit(download, a.scenes[0], zp(a.scenes[0]))}
    with ThreadPoolExecutor(a.threads) as pool:
        for i, scene in enumerate(a.scenes):
            pending.pop(scene).result()
            if i + 1 < len(a.scenes):
                nxt = a.scenes[i + 1]
                pending[nxt] = dl.submit(download, nxt, zp(nxt))
            if os.path.exists(os.path.join(out_dir, scene + ".json")):
                log(f"[{scene}] already compacted, skipping")
            else:
                compact(scene, zp(scene), out_dir, a.stride, a.quality, pool)
            os.remove(zp(scene))
    dl.shutdown()
    log("all done")


if __name__ == "__main__":
    sys.exit(main())
