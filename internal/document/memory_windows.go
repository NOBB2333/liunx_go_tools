//go:build windows

package document

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modNtdll    = windows.NewLazySystemDLL("ntdll.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procNtQuerySystemInformation = modNtdll.NewProc("NtQuerySystemInformation")
	procNtQueryObject            = modNtdll.NewProc("NtQueryObject")
	procGetFinalPathNameByHandle = modKernel32.NewProc("GetFinalPathNameByHandleW")
	procVirtualQueryEx           = modKernel32.NewProc("VirtualQueryEx")
)

const (
	systemHandleInformation = 16
	objectNameInformation   = 1
	fileTypeDisk            = 0x0001
	volumeNameNt            = 0x02
	volumeNameDos           = 0x00

	memCommit    = 0x1000
	pageNoAccess = 0x01
	pageGuard    = 0x100
)

// SYSTEM_HANDLE_TABLE_ENTRY_INFO
type sysHandleEntry struct {
	UniqueProcessID       uint16
	CreatorBackTraceIndex uint16
	ObjectTypeIndex       uint8
	HandleAttributes      uint8
	HandleValue           uint16
	Object                uintptr
	GrantedAccess         uint32
}

type sysHandleInfo struct {
	NumberOfHandles uint32
	Handles         [1]sysHandleEntry
}

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type objectNameInfo struct {
	Name unicodeString
}

// memoryBasicInformation - MEMORY_BASIC_INFORMATION
type memoryBasicInformation struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
}

// openDocFilesWindowsAPI 通过 Windows 原生 API 枚举进程打开的文件，正确支持 Unicode 路径。
func (e *DocExtractor) openDocFilesWindowsAPI(pid int32) ([]string, error) {
	buf := make([]byte, 1<<20)
	var retLen uint32
	for {
		r, _, _ := procNtQuerySystemInformation.Call(
			systemHandleInformation,
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
			uintptr(unsafe.Pointer(&retLen)),
		)
		if r == 0 {
			break
		}
		if r == 0x80000005 || r == 0xC0000004 {
			buf = make([]byte, len(buf)*2)
			continue
		}
		return nil, fmt.Errorf("NtQuerySystemInformation failed: 0x%X", r)
	}

	info := (*sysHandleInfo)(unsafe.Pointer(&buf[0]))
	count := int(info.NumberOfHandles)
	entrySize := unsafe.Sizeof(sysHandleEntry{})
	basePtr := uintptr(unsafe.Pointer(&info.Handles[0]))

	hProc, err := windows.OpenProcess(windows.PROCESS_DUP_HANDLE, false, uint32(pid))
	if err != nil {
		return nil, fmt.Errorf("OpenProcess pid=%d: %w", pid, err)
	}
	defer windows.CloseHandle(hProc)

	self := windows.CurrentProcess()
	var paths []string
	seen := map[string]bool{}

	for i := 0; i < count; i++ {
		entry := (*sysHandleEntry)(unsafe.Pointer(basePtr + uintptr(i)*entrySize))
		if int(entry.UniqueProcessID) != int(pid) {
			continue
		}
		var dupHandle windows.Handle
		err := windows.DuplicateHandle(hProc, windows.Handle(entry.HandleValue), self, &dupHandle, 0, false, windows.DUPLICATE_SAME_ACCESS)
		if err != nil {
			continue
		}
		ft, err := windows.GetFileType(dupHandle)
		if err != nil || ft != fileTypeDisk {
			windows.CloseHandle(dupHandle)
			continue
		}
		path := getFinalPath(dupHandle)
		windows.CloseHandle(dupHandle)
		if path == "" || seen[path] {
			continue
		}
		if e.isTargetExt(path) {
			if e.Debug {
				fmt.Printf("[debug] Windows API 候选路径: %q ext=%s\n", path, filepath.Ext(path))
			}
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// getFinalPath 获取文件的完整 Unicode DOS 路径。
func getFinalPath(h windows.Handle) string {
	buf := make([]uint16, 512)
	r, _, _ := procGetFinalPathNameByHandle.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		volumeNameDos,
	)
	if r == 0 || r >= uintptr(len(buf)) {
		return ""
	}
	path := windows.UTF16ToString(buf[:r])
	if len(path) > 4 && path[:4] == `\\?\` {
		path = path[4:]
		if len(path) > 4 && path[:4] == `UNC\` {
			path = `\\` + path[4:]
		}
	}
	return path
}

// MemBlob / SaveMemBlobsAsB64 / findEOCD / min64 定义在 文档提取_mem_common.go

// ScanMemoryForDocs 扫描指定进程内存，寻找 ZIP/DOCX/XLSX 文件结构（PK 魔数），
// 返回每个找到的完整 ZIP blob（base64编码）及建议的文件名。
func ScanMemoryForDocs(pid int32, exts []string) ([]MemBlob, error) {
	// 需要 VM_READ + QUERY_INFORMATION 权限
	const access = windows.PROCESS_VM_READ | windows.PROCESS_QUERY_INFORMATION
	hProc, err := windows.OpenProcess(access, false, uint32(pid))
	if err != nil {
		return nil, fmt.Errorf("OpenProcess(VM_READ) pid=%d: %w", pid, err)
	}
	defer windows.CloseHandle(hProc)

	// ZIP 魔数：PK\x03\x04（本地文件头）
	zipMagic := []byte{0x50, 0x4B, 0x03, 0x04}
	// ZIP EOCD 魔数：PK\x05\x06
	eocdMagic := []byte{0x50, 0x4B, 0x05, 0x06}

	var addr uintptr
	var blobs []MemBlob
	seen := map[string]bool{}

	for {
		var mbi memoryBasicInformation
		r, _, _ := procVirtualQueryEx.Call(
			uintptr(hProc),
			addr,
			uintptr(unsafe.Pointer(&mbi)),
			unsafe.Sizeof(mbi),
		)
		if r == 0 {
			break
		}

		// 只读已提交、可读、非保护页
		if mbi.State == memCommit &&
			(mbi.Protect&pageNoAccess) == 0 &&
			(mbi.Protect&pageGuard) == 0 &&
			mbi.RegionSize > 0 &&
			mbi.RegionSize <= 100*1024*1024 { // 跳过超大页（>100MB）

			chunk := make([]byte, mbi.RegionSize)
			var nRead uintptr
			err := windows.ReadProcessMemory(hProc, mbi.BaseAddress, &chunk[0], uintptr(len(chunk)), &nRead)
			if err == nil && nRead > 4 {
				chunk = chunk[:nRead]
				// 在这块内存里搜索 ZIP 魔数
				for i := 0; i+4 < len(chunk); i++ {
					if chunk[i] == zipMagic[0] && chunk[i+1] == zipMagic[1] &&
						chunk[i+2] == zipMagic[2] && chunk[i+3] == zipMagic[3] {

						// 找 EOCD 结束记录
						end := findEOCD(chunk[i:], eocdMagic)
						if end <= 0 {
							continue
						}
						zipData := chunk[i : i+end]
						// 简单去重：用前64字节的b64作key
						key := base64.StdEncoding.EncodeToString(zipData[:min64(len(zipData))])
						if seen[key] {
							continue
						}
						seen[key] = true
						blobs = append(blobs, MemBlob{
							Data:    zipData,
							PidHint: pid,
						})
					}
				}
			}
		}

		next := mbi.BaseAddress + mbi.RegionSize
		if next <= addr { // 防止溢出
			break
		}
		addr = next
	}
	return blobs, nil
}
