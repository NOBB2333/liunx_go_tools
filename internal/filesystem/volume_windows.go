//go:build windows

package filesystem

import (
	"strings"

	"golang.org/x/sys/windows"
)

func filesystemCapacity(path string) (uint64, uint64, error) {
	// UNC share roots require a trailing backslash for GetDiskFreeSpaceEx.
	name, err := windows.UTF16PtrFromString(strings.TrimRight(path, `\/`) + `\`)
	if err != nil {
		return 0, 0, err
	}
	var available, total uint64
	if err := windows.GetDiskFreeSpaceEx(name, &available, &total, nil); err != nil {
		return 0, 0, err
	}
	return total, available, nil
}
