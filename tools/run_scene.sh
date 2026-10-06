#!/bin/bash
# Build the map for one 7-Scenes scene with the default (tuned-on-heads) settings and
# evaluate every test frame. Usage: tools/run_scene.sh <scene> [threads]
set -euo pipefail
cd "$(dirname "$0")/.."
scene=$1
threads=${2:-8}
data=${VLOC_DATA:-$HOME/Data/7scenes}
mkdir -p results/maps
core/build/vloc_build --scene-dir "$data/$scene" --out "results/maps/$scene.vmap" > "results/maps/${scene}_build.json"
core/build/vloc_eval --map "results/maps/$scene.vmap" --scene-dir "$data/$scene" --threads "$threads" \
  --csv "results/$scene.csv" --summary "results/${scene}_summary.json"
