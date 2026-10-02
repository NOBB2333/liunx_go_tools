//go:build darwin && !cgo

package document

import "fmt"

func (e *DocExtractor) openDocFilesWindowsAPI(_ int32) ([]string, error) {
	return nil, fmt.Errorf("Windows API 仅在 Windows 平台可用")
}

func ScanMemoryForDocs(_ int32, _ []string) ([]MemBlob, error) {
	return nil, fmt.Errorf("内存扫描需要 CGO，请用原生编译（不加 CGO_ENABLED=0）")
}
