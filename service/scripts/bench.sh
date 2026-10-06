#!/usr/bin/env bash
# bench.sh NAME "vlocd flags" "vlocload flags"
# Starts vlocd-core on the heads map with the given flags, runs vlocload, writes
# results/service/NAME.json, stops the server. Server logs go to results/raw/service/.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(cd .. && pwd)
NAME=$1; SFLAGS=$2; LFLAGS=$3
PORT=${PORT:-18091}
mkdir -p "$ROOT/results/service" "$ROOT/results/raw/service"
# The host is shared. Wait (up to 30 min) until no core eval or map build is running and
# the 1-minute load average is under LOAD_MAX, so a run measures the service, not a
# neighbour. The load average at each rate is stored in the results either way.
LOAD_MAX=${LOAD_MAX:-8}
for _ in $(seq ${WAIT_TRIES:-60}); do
  l1=$(sysctl -n vm.loadavg | awk '{print int($2)}')
  if ! pgrep -f 'vloc_eval|vloc_build' >/dev/null && [ "$l1" -lt "$LOAD_MAX" ]; then break; fi
  sleep 5
done
if pgrep -f 'vloc_eval|vloc_build' >/dev/null || [ "$(sysctl -n vm.loadavg | awk '{print int($2)}')" -ge "$LOAD_MAX" ]; then
  echo "host not quiet after waiting, not measuring:"; sysctl -n vm.loadavg; ps -Ao pcpu,comm -r | head -5
  exit 3
fi
VLOC_CV_THREADS=1 ./bin/vlocd-core -engine cgo -addr 127.0.0.1:$PORT -map-dir "$(mktemp -d)" \
  -map heads="$ROOT/results/maps/heads.vmap" $SFLAGS 2>"$ROOT/results/raw/service/$NAME.log" &
PID=$!
trap 'kill $PID 2>/dev/null; wait $PID 2>/dev/null || true' EXIT
until curl -sf 127.0.0.1:$PORT/readyz >/dev/null; do sleep 0.2; done
echo "== $NAME  server: $SFLAGS  load: $LFLAGS  (load avg: $(sysctl -n vm.loadavg))"
./bin/vlocload -url http://127.0.0.1:$PORT $LFLAGS -label "$NAME; vlocd $SFLAGS" -out "$ROOT/results/service/$NAME.json"
curl -s 127.0.0.1:$PORT/metrics | grep -E '^vloc_shed_total|^vloc_map_events_total' || true
