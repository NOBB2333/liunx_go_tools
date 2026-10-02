//go:build windows

package filesystem

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

// scanDirectoryNative uses the Win32 directory enumeration API directly. It
// avoids one os.FileInfo/stat call per entry; FindFirstFileW already returns
// the logical size and timestamps for every item in the directory buffer.
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
	pattern, err := windows.UTF16PtrFromString(filepath.Join(task.path, "*"))
	if err != nil {
		errorCount.Add(1)
		sendFastError(ctx, errorRecords, task.path, err)
		return 0, 0, 0
	}
	var data windows.Win32finddata
	handle, err := windows.FindFirstFile(pattern, &data)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return 0, 0, 0
		}
		errorCount.Add(1)
		sendFastError(ctx, errorRecords, task.path, err)
		return 0, 0, 0
	}
	defer windows.FindClose(handle)

	var localLogical, localAllocated, localFiles uint64
	for {
		if ctx.Err() != nil {
			return localLogical, localAllocated, localFiles
		}
		name := windows.UTF16ToString(data.FileName[:])
		if name != "." && name != ".." {
			attributes := data.FileAttributes
			isDir := attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
			// Junctions and reparse-point directories are not traversed unless
			// link following is explicitly implemented by a future backend.
			if isDir && attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
				isDir = false
			}
			childPath := filepath.Join(task.path, name)
			if isDir {
				id := nextID.Add(1)
				pending.Add(1)
				aggregator.addDirectory(id, task.id, name, task.depth+1)
				dirsSeen.Add(1)
				select {
				case jobs <- fastDirTask{id: id, parentID: task.id, name: name, path: childPath, depth: task.depth + 1}:
				case <-ctx.Done():
					pending.Done()
				}
			} else {
				record := fastFileRecord{parentID: task.id, name: name, mode: attributes}
				if opt.Metadata == FastMetadataBasic {
					record.size = int64(uint64(data.FileSizeHigh)<<32 | uint64(data.FileSizeLow))
					record.mtimeNS = data.LastWriteTime.Nanoseconds()
					record.btimeNS = data.CreationTime.Nanoseconds()
					// Windows FindFirstFile does not expose allocation size.
					// Keep it unknown rather than issuing a per-file handle query.
					if record.size > 0 {
						localLogical += uint64(record.size)
						logicalBytes.Add(uint64(record.size))
					}
				}
				localFiles++
				filesSeen.Add(1)
				select {
				case fileRecords <- record:
				case <-ctx.Done():
					return localLogical, localAllocated, localFiles
				}
			}
		}
		if err := windows.FindNextFile(handle, &data); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			errorCount.Add(1)
			sendFastError(ctx, errorRecords, task.path, err)
			break
		}
	}
	return localLogical, localAllocated, localFiles
}
