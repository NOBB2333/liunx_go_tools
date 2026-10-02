//go:build !windows && !darwin

package document

import "fmt"

func (e *DocExtractor) openDocFilesWindowsAPI(_ int32) ([]string, error) {
	return nil, fmt.Errorf("Windows API 仅在 Windows 平台可用")
}

func ScanMemoryForDocs(_ int32, _ []string) ([]MemBlob, error) {
	return nil, fmt.Errorf("内存扫描仅在 Windows/macOS 平台可用")
}
