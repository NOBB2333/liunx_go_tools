package document

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MemBlob 内存中扫描到的一段 ZIP 数据。
type MemBlob struct {
	Data    []byte
	PidHint int32
}

// SaveMemBlobsAsB64 将内存 blobs 保存到输出目录，文件名为 mem_<pid>_<index>.b64.txt。
// 写入格式与 base64-decode 命令兼容（带 GOLANGTOOLS_BASE64_V1 头）。
func SaveMemBlobsAsB64(blobs []MemBlob, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	for i, blob := range blobs {
		name := fmt.Sprintf("mem_%d_%03d", blob.PidHint, i)
		dest := filepath.Join(outputDir, name+".b64.txt")

		meta, _ := json.Marshal(map[string]any{
			"version": 1,
			"name":    name,
			"kind":    "file",
			"mode":    "raw",
		})
		encoded := base64.StdEncoding.EncodeToString(blob.Data)
		content := "GOLANGTOOLS_BASE64_V1\n" + string(meta) + "\n" + encoded

		if err := os.WriteFile(dest, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

// findEOCD 在 data 中从末尾往前找 EOCD 签名，返回 EOCD 结束位置（从 data 起始计算）。
// 用于 data 本身就是完整 ZIP 的场景（Windows 版）。
func findEOCD(data, sig []byte) int {
	start := len(data) - 22 - 65535
	if start < 0 {
		start = 0
	}
	for i := len(data) - 22; i >= start; i-- {
		if data[i] == sig[0] && i+3 < len(data) &&
			data[i+1] == sig[1] && data[i+2] == sig[2] && data[i+3] == sig[3] {
			if i+22 <= len(data) {
				commentLen := int(data[i+20]) | int(data[i+21])<<8
				end := i + 22 + commentLen
				if end <= len(data) {
					return end
				}
				return i + 22
			}
		}
	}
	return 0
}

// findEOCDForward 从头向后扫描 data，找第一个 EOCD 签名，返回 EOCD 结束位置。
// 用于内存扫描场景：chunk[pkStart:] 后面还有大量无关数据，EOCD 不在末尾。
func findEOCDForward(data, sig []byte) int {
	for i := 0; i+22 <= len(data); i++ {
		if data[i] == sig[0] && data[i+1] == sig[1] &&
			data[i+2] == sig[2] && data[i+3] == sig[3] {
			commentLen := int(data[i+20]) | int(data[i+21])<<8
			end := i + 22 + commentLen
			if end <= len(data) {
				return end
			}
			return i + 22
		}
	}
	return 0
}

func min64(n int) int {
	if n > 64 {
		return 64
	}
	return n
}
