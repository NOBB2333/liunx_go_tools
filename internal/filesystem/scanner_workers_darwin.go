//go:build darwin

package filesystem

import "runtime"

// APFS directory metadata benefits from more outstanding reads than the
// number of CPU cores. The cap is based on full-volume measurements on Apple
// silicon and keeps descriptor/memory usage bounded.
func defaultFastScanWorkers() int {
	workers := runtime.NumCPU() * 8
	if workers < 8 {
		workers = 8
	}
	if workers > 64 {
		workers = 64
	}
	return workers
}
