//go:build windows

package filesystem

import "fmt"

func probeFilesystem(string) (DiskProbe, error) {
	return DiskProbe{}, fmt.Errorf("disk probe is not implemented on Windows")
}
