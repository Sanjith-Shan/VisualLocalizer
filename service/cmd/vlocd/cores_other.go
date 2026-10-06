//go:build !darwin

package main

import "runtime"

// defaultWorkers leaves one core for the HTTP front end and the runtime.
func defaultWorkers() int { return max(1, runtime.NumCPU()-1) }
