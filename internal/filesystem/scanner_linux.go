//go:build linux

package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// fastDirentBufferSize 是一次 getdents64 读取用的缓冲区大小。
// d_type 取值与 linux_dirent64 的解析见 dirent64.go。
const fastDirentBufferSize = 128 << 10

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
			// 遍历交给 walkLinuxDirent64（见 dirent64.go）：记录长度是**相对**长度，
			// 必须按 `offset += reclen` 推进。旧实现写 `offset = reclen`，
			// 把相对长度当绝对偏移用，在典型 ext4 目录（". / .." 的 reclen 都是 24）
			// 上第 2 条记录就越界，整棵树只剩第一个条目——也就是之前那个
			// "files=0 / dirs=2" 的静默丢数据故障。
			aborted := false
			truncated := walkLinuxDirent64(buf[:n], func(record fastDirent) bool {
				// 只按名字跳过 . / ..；不要因为 d_ino == 0 就跳过条目——
				// 9p/virtiofs 等文件系统（以及部分被安全软件接管的进程）会返回 d_ino = 0，
				// 旧逻辑会把整棵树静默丢空，表现成 "files=0 dirs=0" 而完全看不出原因。
				if record.name == "." || record.name == ".." {
					return true
				}

				var st unix.Statx_t
				needStat := record.typ == fastDTUnknown || (opt.Metadata == FastMetadataBasic && record.typ != fastDTDir)
				if needStat {
					statErr := statxFast(fd, record.name, &st)
					if statErr != nil {
						errorCount.Add(1)
						sendFastError(ctx, errorRecords, filepath.Join(task.path, record.name), statErr)
						return true
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
					return true
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
					return true
				case <-ctx.Done():
					aborted = true
					return false
				}
			})
			if truncated {
				// 内核只会返回完整记录，走到这里说明缓冲区被截断或记录错位。
				// 显式记一条错误，避免再次退化成"目录里凭空少条目却不报错"。
				errorCount.Add(1)
				sendFastError(ctx, errorRecords, task.path, fmt.Errorf("malformed dirent in %d-byte buffer", n))
			}
			if aborted {
				return localLogical, localAllocated, localFiles
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
	if err == nil {
		return nil
	}
	// statx 任何失败都退回 fstatat：老内核返回 ENOSYS/EINVAL，而 seccomp、
	// 容器安全策略或终端安全软件常常直接回 EPERM/EACCES。旧逻辑只对
	// ENOSYS/EINVAL/ENOTSUP 回退，其余情况会把每个条目都记成错误并跳过，
	// 表现成 "files=0 errors=N"。
	var legacy unix.Stat_t
	if ferr := unix.Fstatat(fd, name, &legacy, unix.AT_SYMLINK_NOFOLLOW); ferr != nil {
		return fmt.Errorf("statx: %v; fstatat: %w", err, ferr)
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
