package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DiskProbe describes the properties that determine metadata scan strategy.
type DiskProbe struct {
	Path          string    `json:"path"`
	Filesystem    string    `json:"filesystem"`
	BlockSize     uint64    `json:"block_size"`
	Blocks        uint64    `json:"blocks"`
	FreeBlocks    uint64    `json:"free_blocks"`
	Inodes        uint64    `json:"inodes"`
	FreeInodes    uint64    `json:"free_inodes"`
	Device        uint64    `json:"device"`
	DeviceName    string    `json:"device_name,omitempty"`
	MountPoint    string    `json:"mount_point,omitempty"`
	FilesystemID  string    `json:"filesystem_id,omitempty"`
	ProbedAt      time.Time `json:"probed_at"`
	RawFilesystem uint64    `json:"raw_filesystem_magic,omitempty"`
}

func ProbeDisk(path string) (DiskProbe, error) {
	if path == "" {
		return DiskProbe{}, fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return DiskProbe{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return DiskProbe{}, err
	}
	if !info.IsDir() {
		return DiskProbe{}, fmt.Errorf("path is not a directory: %s", abs)
	}

	probe, err := probeFilesystem(abs)
	if err != nil {
		return DiskProbe{}, err
	}
	probe.Path = abs
	probe.ProbedAt = time.Now()
	return probe, nil
}
