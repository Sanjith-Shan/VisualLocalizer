#!/bin/bash
# Per-request latency on a quiet host: one request at a time, OpenCV single-threaded,
# every 10th test frame of every scene. Waits until the 1-minute load average is below 3.
set -euo pipefail
cd "$(dirname "$0")/.."
data=${VLOC_DATA:-$HOME/Data/7scenes}
mkdir -p results/latency
for s in chess fire heads office pumpkin redkitchen stairs; do
  until [ "$(sysctl -n vm.loadavg | awk '{print ($2 < 3.0)}')" = 1 ]; do sleep 10; done
  load=$(sysctl -n vm.loadavg | awk '{print $2}')
  VLOC_CV_THREADS=1 core/build/vloc_eval --map "results/maps/$s.vmap" --scene-dir "$data/$s" --threads 1 \
    --stride 10 --csv "results/latency/${s}_1thread.csv" --summary "results/latency/${s}_1thread.json" > /dev/null
  echo "{\"scene\": \"$s\", \"load_avg_1min_at_start\": $load}" > "results/latency/${s}_load.json"
done
