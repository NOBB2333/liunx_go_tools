package filesystem

import (
	"fmt"
	"time"
)

// VolumeSpace is a point-in-time view of the filesystem containing Path.
// AvailableBytes is what the scanning user can use; UsedBytes is the
// remainder of TotalBytes, including any space reserved from that user.
type VolumeSpace struct {
	Path           string    `json:"path"`
	TotalBytes     uint64    `json:"total_bytes"`
	AvailableBytes uint64    `json:"available_bytes"`
	UsedBytes      uint64    `json:"used_bytes"`
	CapturedAt     time.Time `json:"captured_at"`
}

func readVolumeSpace(path string) (*VolumeSpace, error) {
	total, available, err := filesystemCapacity(path)
	if err != nil {
		return nil, err
	}
	if total == 0 || available > total {
		return nil, fmt.Errorf("invalid filesystem capacity for %s", path)
	}
	return &VolumeSpace{
		Path:           path,
		TotalBytes:     total,
		AvailableBytes: available,
		UsedBytes:      total - available,
		CapturedAt:     time.Now(),
	}, nil
}
