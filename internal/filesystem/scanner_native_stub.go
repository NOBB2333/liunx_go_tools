//go:build !linux

package filesystem

import (
	"context"
	"sync"
	"sync/atomic"
)

func scanDirectoryNative(
	ctx context.Context,
	task fastDirTask,
	opt FastScanOptions,
	nextID *atomic.Uint64,
	pending *sync.WaitGroup,
	jobs chan<- fastDirTask,
	fileRecords chan<- fastFileRecord,
	errorRecords chan<- fastErrorRecord,
	dirsSeen, filesSeen, logicalBytes, allocatedBytes, errorCount *atomic.Uint64,
	aggregator *fastAggregator,
) (uint64, uint64, uint64) {
	return scanDirectoryPortable(ctx, task, opt, nextID, pending, jobs, fileRecords, errorRecords, dirsSeen, filesSeen, logicalBytes, allocatedBytes, errorCount, aggregator)
}
