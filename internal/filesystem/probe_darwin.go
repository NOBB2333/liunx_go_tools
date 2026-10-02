//go:build darwin

package filesystem

import (
	"fmt"
	"os"
	"syscall"
)

func probeFilesystem(path string) (DiskProbe, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return DiskProbe{}, err
	}

	return DiskProbe{
		Filesystem:    darwinStatfsString(stat.Fstypename[:]),
		RawFilesystem: uint64(stat.Type),
		BlockSize:     uint64(stat.Bsize),
		Blocks:        stat.Blocks,
		FreeBlocks:    stat.Bfree,
		Inodes:        stat.Files,
		FreeInodes:    stat.Ffree,
		Device:        darwinFileDevice(path),
		DeviceName:    darwinStatfsString(stat.Mntfromname[:]),
		MountPoint:    darwinStatfsString(stat.Mntonname[:]),
		FilesystemID:  fmt.Sprintf("%d:%d", stat.Fsid.Val[0], stat.Fsid.Val[1]),
	}, nil
}

func darwinStatfsString(values []int8) string {
	end := 0
	for end < len(values) && values[end] != 0 {
		end++
	}
	data := make([]byte, end)
	for i := range data {
		data[i] = byte(values[i])
	}
	return string(data)
}

func darwinFileDevice(path string) uint64 {
	info, err := os.Stat(path)
	if err != nil || info.Sys() == nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(uint32(stat.Dev))
	}
	return 0
}
