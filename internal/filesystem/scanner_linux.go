//go:build linux

package filesystem

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	fastDirentBufferSize = 128 << 10
	fastDTUnknown        = 0
	fastDTDir            = 4
	fastDTReg            = 8
	fastDTLnk            = 10
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
	fd, err := unix.Open(task.path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		errorCount.Add(1)
		sendFastError(ctx, errorRecords, task.path, err)
		return 0, 0, 0
	}
	defer unix.Close(fd)

	buf := make([]byte, fastDirentBufferSize)
	var localLogical, localAllocated, localFiles uint64
	for {
		n, readErr := unix.ReadDirent(fd, buf)
		if n > 0 {
			for offset := 0; offset < n; {
				record, next, ok := parseFastDirent(buf[offset:n])
				if !ok || next <= offset {
					break
				}
				offset = next
				if record.name == "." || record.name == ".." || record.ino == 0 {
					continue
				}

				var st unix.Statx_t
				needStat := record.typ == fastDTUnknown || (opt.Metadata == FastMetadataBasic && record.typ != fastDTDir)
				if needStat {
					statErr := statxFast(fd, record.name, &st)
					if statErr != nil {
						errorCount.Add(1)
						sendFastError(ctx, errorRecords, filepath.Join(task.path, record.name), statErr)
						continue
					}
				}

				isDir := record.typ == fastDTDir
				if needStat {
					mode := st.Mode & unix.S_IFMT
					isDir = mode == unix.S_IFDIR
				}
				if isDir {
					childPath := filepath.Join(task.path, record.name)
					id := nextID.Add(1)
					pending.Add(1)
					aggregator.addDirectory(id, task.id, record.name, task.depth+1)
					dirsSeen.Add(1)
					select {
					case jobs <- fastDirTask{id: id, parentID: task.id, name: record.name, path: childPath, depth: task.depth + 1}:
					case <-ctx.Done():
						pending.Done()
					}
					continue
				}
				rec := fastFileRecord{parentID: task.id, name: record.name, inode: record.ino}
				if opt.Metadata == FastMetadataBasic {
					// statx is already relative to the open directory fd, so no
					// path lookup or os.FileInfo allocation is needed.
					rec.size = int64(st.Size)
					rec.blocks = int64(st.Blocks)
					rec.mtimeNS = st.Mtime.Sec*1e9 + int64(st.Mtime.Nsec)
					rec.ctimeNS = st.Ctime.Sec*1e9 + int64(st.Ctime.Nsec)
					rec.btimeNS = st.Btime.Sec*1e9 + int64(st.Btime.Nsec)
					rec.mode = uint32(st.Mode)
					rec.device = uint64(st.Dev_major)<<32 | uint64(st.Dev_minor)
					if rec.size > 0 {
						localLogical += uint64(rec.size)
						logicalBytes.Add(uint64(rec.size))
					}
					if rec.blocks > 0 {
						localAllocated += uint64(rec.blocks) * 512
						allocatedBytes.Add(uint64(rec.blocks) * 512)
					}
				}
				localFiles++
				filesSeen.Add(1)
				select {
				case fileRecords <- rec:
				case <-ctx.Done():
					return localLogical, localAllocated, localFiles
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				errorCount.Add(1)
				sendFastError(ctx, errorRecords, task.path, readErr)
			}
			break
		}
		if n == 0 {
			break
		}
	}
	return localLogical, localAllocated, localFiles
}

func statxFast(fd int, name string, st *unix.Statx_t) error {
	err := unix.Statx(fd, name, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT, unix.STATX_BASIC_STATS|unix.STATX_BTIME, st)
	if err == nil || (err != unix.ENOSYS && err != unix.EINVAL && err != unix.ENOTSUP) {
		return err
	}
	var legacy unix.Stat_t
	if err := unix.Fstatat(fd, name, &legacy, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	st.Mode = uint16(legacy.Mode)
	st.Ino = legacy.Ino
	st.Size = uint64(maxInt64(legacy.Size))
	st.Blocks = uint64(maxInt64(legacy.Blocks))
	st.Mtime.Sec = legacy.Mtim.Sec
	st.Mtime.Nsec = uint32(legacy.Mtim.Nsec)
	st.Ctime.Sec = legacy.Ctim.Sec
	st.Ctime.Nsec = uint32(legacy.Ctim.Nsec)
	st.Dev_major = uint32(legacy.Dev >> 32)
	st.Dev_minor = uint32(legacy.Dev)
	return nil
}

func maxInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

type fastDirent struct {
	ino  uint64
	typ  uint8
	name string
}

func parseFastDirent(buf []byte) (fastDirent, int, bool) {
	if len(buf) < int(unsafe.Offsetof(unix.Dirent{}.Name))+1 {
		return fastDirent{}, 0, false
	}
	reclenOffset := int(unsafe.Offsetof(unix.Dirent{}.Reclen))
	inoOffset := int(unsafe.Offsetof(unix.Dirent{}.Ino))
	typeOffset := int(unsafe.Offsetof(unix.Dirent{}.Type))
	nameOffset := int(unsafe.Offsetof(unix.Dirent{}.Name))
	if reclenOffset+2 > len(buf) || inoOffset+8 > len(buf) {
		return fastDirent{}, 0, false
	}
	reclen := int(binary.LittleEndian.Uint16(buf[reclenOffset : reclenOffset+2]))
	if reclen < nameOffset || reclen > len(buf) || typeOffset >= reclen {
		return fastDirent{}, 0, false
	}
	nameBytes := buf[nameOffset:reclen]
	for i, b := range nameBytes {
		if b == 0 {
			nameBytes = nameBytes[:i]
			break
		}
	}
	return fastDirent{
		ino:  binary.LittleEndian.Uint64(buf[inoOffset : inoOffset+8]),
		typ:  buf[typeOffset],
		name: string(nameBytes),
	}, reclen, true
}
