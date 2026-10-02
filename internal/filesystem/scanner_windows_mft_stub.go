//go:build !windows

package filesystem

import "context"

// scanWindowsMFT is compiled away on non-Windows platforms. Keeping the
// platform decision in scanner.go lets the public scanner remain portable.
func scanWindowsMFT(ctx context.Context, opt FastScanOptions, root string) (FastScanSummary, bool, error) {
	return FastScanSummary{}, false, nil
}
