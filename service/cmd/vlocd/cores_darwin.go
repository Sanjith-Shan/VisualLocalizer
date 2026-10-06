package main

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// defaultWorkers is the number of performance cores. On a hybrid Apple CPU the
// efficiency cores run a localize call about twice as slow, and filling every core with
// CPU-bound workers starves the HTTP front end and the Go runtime, which shows up as
// latency the server's own clock never sees (results/service/workers_*.json).
func defaultWorkers() int {
	if n, err := unix.SysctlUint32("hw.perflevel0.logicalcpu"); err == nil && n > 0 {
		return int(n)
	}
	return runtime.NumCPU()
}
