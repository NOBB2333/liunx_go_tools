//go:build !windows && !darwin

package filesystem

import (
	"os"
	"syscall"
)

func probeFilesystem(path string) (DiskProbe, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return DiskProbe{}, err
	}

	return DiskProbe{
		Filesystem:    filesystemName(uint64(stat.Type)),
		RawFilesystem: uint64(stat.Type),
		BlockSize:     uint64(stat.Bsize),
		Blocks:        uint64(stat.Blocks),
		FreeBlocks:    uint64(stat.Bfree),
		Inodes:        uint64(stat.Files),
		FreeInodes:    uint64(stat.Ffree),
		Device:        fileDevice(path),
	}, nil
}

func fileDevice(path string) uint64 {
	info, err := os.Stat(path)
	if err != nil || info.Sys() == nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Dev)
	}
	return 0
}

func filesystemName(magic uint64) string {
	switch magic {
	case 0xEF53:
		return "ext4/ext3/ext2"
	case 0x58465342:
		return "xfs"
	case 0x9123683E:
		return "btrfs"
	case 0x6969:
		return "nfs"
	case 0xFF534D42:
		return "cifs/smb"
	case 0x01021994:
		return "tmpfs"
	default:
		return "unknown"
	}
}
