#!/usr/bin/env bash
# End-to-end: start vlocd on the real core, compare its poses with vloc_eval's.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(cd .. && pwd)
PY=${PY:-$HOME/.local/envs/visualloc/bin/python}
OUT=$ROOT/results/service
PORT=${PORT:-18090}
mkdir -p "$OUT"
"$ROOT/core/build/vloc_eval" --map "$ROOT/results/maps/heads.vmap" --scene-dir ~/Data/7scenes/heads \
  --split test --stride 20 --c-abi --threads 4 --csv "$OUT/e2e_heads_eval.csv" \
  --summary "$OUT/e2e_heads_eval_summary.json" >/dev/null
VLOC_CV_THREADS=1 ./bin/vlocd-core -engine cgo -addr 127.0.0.1:$PORT -map-dir "$(mktemp -d)" \
  -map heads="$ROOT/results/maps/heads.vmap" -log-level warn &
PID=$!
trap 'kill $PID' EXIT
until curl -sf 127.0.0.1:$PORT/readyz >/dev/null; do sleep 0.2; done
"$PY" scripts/verify_e2e.py --url http://127.0.0.1:$PORT --map heads --scene-dir ~/Data/7scenes/heads \
  --eval-csv "$OUT/e2e_heads_eval.csv" --out "$OUT/e2e_heads.json"
