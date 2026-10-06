#!/usr/bin/env python3
"""Prints the per-scene results table (from results/<scene>_summary.json) next to the
published baselines in tools/baselines.json, as Markdown.

Usage: tools/make_tables.py > results/table.md
"""
import json
import os

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def fmt(cm, deg):
    return f"{cm:g} cm, {deg:g}°"


def main():
    base = json.load(open(os.path.join(ROOT, "tools", "baselines.json")))
    scenes = base["scenes"]
    ours = {}
    for s in scenes:
        p = os.path.join(ROOT, "results", f"{s}_summary.json")
        if os.path.exists(p):
            ours[s] = json.load(open(p))
    print("Median translation and rotation error per scene (lower is better).\n")
    hdr = ["Scene"] + [m["name"] for m in base["methods"]] + ["This work"]
    print("| " + " | ".join(hdr) + " |")
    print("|" + "---|" * len(hdr))
    for s in scenes:
        row = [s]
        for m in base["methods"]:
            cm, deg = m["median"][s]
            row.append(fmt(cm, deg))
        if s in ours:
            o = ours[s]
            row.append(f"**{o['median_trans_cm']:.1f} cm, {o['median_rot_deg']:.2f}°**")
        else:
            row.append("not run")
        print("| " + " | ".join(row) + " |")

    print("\nThis work, full detail. Latency is per frame under the stated eval concurrency.\n")
    print("| Scene | Test frames | Localized | Median | Within 5 cm, 5° | Within 2 cm, 2° | "
          "DSAC* within 5 cm, 5° | p50 ms | p99 ms | Threads | Map points |")
    print("|---|---|---|---|---|---|---|---|---|---|---|")
    dsac = next(m for m in base["methods"] if m["name"].startswith("DSAC*"))
    for s in scenes:
        if s not in ours:
            continue
        o = ours[s]
        print(f"| {s} | {o['frames']} | {100.0 * o['localized'] / o['frames']:.1f}% | "
              f"{o['median_trans_cm']:.2f} cm, {o['median_rot_deg']:.3f}° | {o['pct_within_5cm_5deg']:.1f}% | "
              f"{o['pct_within_2cm_2deg']:.1f}% | {dsac['pct_5cm_5deg'][s]}% | {o['latency_ms_p50']:.0f} | "
              f"{o['latency_ms_p99']:.0f} | {o['threads']} | {o['map_points']} |")
    print("\nSources:")
    for m in base["methods"]:
        print(f"- {m['name']}: {m['paper']}. Numbers from {m['source']}.")


if __name__ == "__main__":
    main()
