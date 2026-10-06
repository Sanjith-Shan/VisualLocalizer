#!/bin/bash
# Build the map for one 7-Scenes scene with the default (tuned-on-heads) settings and
# evaluate every test frame.
#   tools/run_scene.sh <scene> [threads] [gt]
# gt = dslam (default): raw 7-Scenes poses, depth-based map, results in results/
# gt = sfm: SfM pseudo GT of Brachmann et al. ICCV 2021 (train poses for the map, test
#           poses for scoring, its focal length), triangulated map, results in results/sfm/
set -euo pipefail
cd "$(dirname "$0")/.."
scene=$1
threads=${2:-10}
gt=${3:-dslam}
data=${VLOC_DATA:-$HOME/Data/7scenes}
pgt=${VLOC_PGT:-$HOME/Data/7scenes_pgt}
sha=$(git rev-parse --short HEAD)$(git diff --quiet -- core || echo -dirty)
mkdir -p results/maps
if [ "$gt" = sfm ]; then
  mkdir -p results/sfm
  core/build/vloc_build --scene-dir "$data/$scene" --out "results/maps/${scene}_sfm.vmap" --mode tri \
    --pose-file "$pgt/sfm/${scene}_train.txt" --provenance "git $sha" > "results/sfm/${scene}_build.json"
  core/build/vloc_eval --map "results/maps/${scene}_sfm.vmap" --scene-dir "$data/$scene" --threads "$threads" \
    --pose-file "$pgt/sfm/${scene}_test.txt" --csv "results/sfm/$scene.csv" --summary "results/sfm/${scene}_summary.json"
else
  core/build/vloc_build --scene-dir "$data/$scene" --out "results/maps/$scene.vmap" --provenance "git $sha" \
    > "results/maps/${scene}_build.json"
  core/build/vloc_eval --map "results/maps/$scene.vmap" --scene-dir "$data/$scene" --threads "$threads" \
    --csv "results/$scene.csv" --summary "results/${scene}_summary.json"
fi
