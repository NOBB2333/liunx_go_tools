//go:build !darwin

package filesystem

import "runtime"

func defaultFastScanWorkers() int {
	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	if workers > 32 {
		workers = 32
	}
	return workers
}
