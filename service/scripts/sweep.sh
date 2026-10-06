#!/usr/bin/env bash
# The service measurements in docs/SERVICE.md. Each step writes results/service/<name>.json.
set -euo pipefail
cd "$(dirname "$0")"
L="-duration 15s -warmup 3s"
# 1. Low-load overhead: HTTP + validation + cgo + JSON on top of the core's own time.
[ -n "${SKIP_OVERHEAD:-}" ] || ./bench.sh overhead_5rps "-log-level warn" "-rates 5 -duration 30s -warmup 3s -deadline-ms 1000"
# 2. Capacity sweep with deadline-aware admission (the default), 1 s client deadline.
./bench.sh sweep_shed_on "-log-level warn" "-rates 10,20,30,40,50,60,70,90,120 $L -deadline-ms 1000"
# 3. Past capacity with admission off and an unbounded queue: what happens without
#    shedding (30 s client deadline so the server never drops work).
./bench.sh sweep_shed_off "-log-level warn -no-deadline-shed -queue 100000" "-rates 50,60,90,120 $L -deadline-ms 30000"
