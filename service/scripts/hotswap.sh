#!/usr/bin/env bash
# Hot swap on the real core: replay heads at a steady rate while the heads map is
# re-uploaded SWAPS times through PUT /v1/maps/heads. Every request must succeed, and
# every replaced map must be freed. Writes results/service/hotswap_core.json.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(cd .. && pwd)
PORT=${PORT:-18092}; SWAPS=${SWAPS:-10}; RATE=${RATE:-20}
VLOC_CV_THREADS=1 ./bin/vlocd-core -engine cgo -addr 127.0.0.1:$PORT -map-dir "$(mktemp -d)" \
  -map heads="$ROOT/results/maps/heads.vmap" -log-level warn &
PID=$!
trap 'kill $PID 2>/dev/null || true' EXIT
until curl -sf 127.0.0.1:$PORT/readyz >/dev/null; do sleep 0.2; done
./bin/vlocload -url http://127.0.0.1:$PORT -rates $RATE -duration 25s -warmup 1s -deadline-ms 5000 \
  -label "hot swap: $SWAPS PUTs during the run" -out "$ROOT/results/service/hotswap_core_load.json" &
LP=$!
sleep 3
codes=()
for i in $(seq $SWAPS); do
  codes+=($(curl -s -o /dev/null -w '%{http_code}' -XPUT --data-binary @"$ROOT/results/maps/heads.vmap" 127.0.0.1:$PORT/v1/maps/heads))
  sleep 1.5
done
wait $LP
sleep 1
M=$(curl -s 127.0.0.1:$PORT/metrics)
ev() { echo "$M" | awk -v e="$1" '$0 ~ "^vloc_map_events_total\\{event=\""e"\"" {print $2}'; }
python3 - "$ROOT/results/service/hotswap_core.json" "${codes[*]}" "$(ev loaded)" "$(ev swapped)" "$(ev freed)" "$ROOT/results/service/hotswap_core_load.json" <<'PY'
import json, sys
out, codes, loaded, swapped, freed, load = sys.argv[1:]
run = json.load(open(load))["runs"][0]
res = {"command": "service/scripts/hotswap.sh", "put_status_codes": codes.split(),
       "map_events": {"loaded": loaded, "swapped": swapped, "freed": freed},
       "requests_sent": run["sent"], "ok": run["ok"], "shed": run["shed_503"], "timeouts": run["timeout_504"],
       "other_errors": run["other_error"], "p50_ms": run["p50_ms"], "p99_ms": run["p99_ms"]}
json.dump(res, open(out, "w"), indent=2); print(json.dumps(res, indent=2))
PY
