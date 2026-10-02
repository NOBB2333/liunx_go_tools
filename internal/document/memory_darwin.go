//go:build darwin && cgo

package document

/*
#include <mach/mach.h>
#include <mach/mach_vm.h>
#include <stdlib.h>

kern_return_t get_task_for_pid(int pid, task_t *task) {
    return task_for_pid(mach_task_self(), pid, task);
}

// 用 mach_vm_read_overwrite 直接写入预分配 buffer：
// 页不可访问时返回错误，不会触发 SIGBUS。
kern_return_t read_mem_safe(task_t task, mach_vm_address_t addr, mach_vm_size_t size,
                            void *buf, mach_vm_size_t *out_size) {
    return mach_vm_read_overwrite(task, addr, size, (mach_vm_address_t)buf, out_size);
}

void release_task_port(mach_port_t port) {
    mach_port_deallocate(mach_task_self(), port);
}
*/
import "C"

import (
	"encoding/base64"
	"fmt"
	"unsafe"
)

func (e *DocExtractor) openDocFilesWindowsAPI(_ int32) ([]string, error) {
	return nil, fmt.Errorf("Windows API 仅在 Windows 平台可用")
}

// ScanMemoryForDocs 扫描指定进程内存（macOS Mach VM 实现），需要 sudo 运行。
func ScanMemoryForDocs(pid int32, _ []string) ([]MemBlob, error) {
	var task C.task_t
	kr := C.get_task_for_pid(C.int(pid), &task)
	if kr != C.KERN_SUCCESS {
		return nil, fmt.Errorf("task_for_pid pid=%d 失败 (kr=%d)，请用 sudo 运行", pid, kr)
	}
	defer C.release_task_port(C.mach_port_t(task))

	zipMagic := []byte{0x50, 0x4B, 0x03, 0x04}
	eocdMagic := []byte{0x50, 0x4B, 0x05, 0x06}

	const maxCarry = 50 * 1024 * 1024 // 跨区域拼接上限 50MB

	var addr C.mach_vm_address_t
	var blobs []MemBlob
	seen := map[string]bool{}

	// carry：上一个区域末尾未完成的 ZIP 片段
	var carry []byte
	var carryEndAddr C.mach_vm_address_t

	var statRegions, statReadOK, statPKHits int64

	flush := func(data []byte) {
		for i := 0; i+4 < len(data); i++ {
			if data[i] == zipMagic[0] && data[i+1] == zipMagic[1] &&
				data[i+2] == zipMagic[2] && data[i+3] == zipMagic[3] {
				statPKHits++
				remaining := len(data) - i
				end := findEOCDForward(data[i:], eocdMagic)
				if end <= 0 {
					tail := 16
					if tail > remaining {
						tail = remaining
					}
					fmt.Printf("[mem-pk] pid=%d PK@+%d 可用字节=%d 无EOCD 前16字节=%X\n",
						pid, i, remaining, data[i:i+tail])
					continue
				}
				zipData := make([]byte, end)
				copy(zipData, data[i:i+end])
				key := base64.StdEncoding.EncodeToString(zipData[:min64(len(zipData))])
				if !seen[key] {
					seen[key] = true
					blobs = append(blobs, MemBlob{Data: zipData, PidHint: pid})
				}
			}
		}
	}

	for {
		var size C.mach_vm_size_t
		var info C.vm_region_basic_info_data_64_t
		var count C.mach_msg_type_number_t = C.VM_REGION_BASIC_INFO_COUNT_64
		var objectName C.mach_port_t

		kr = C.mach_vm_region(
			task,
			&addr,
			&size,
			C.VM_REGION_BASIC_INFO_64,
			(C.vm_region_info_t)(unsafe.Pointer(&info)),
			&count,
			&objectName,
		)
		if kr != C.KERN_SUCCESS {
			break
		}

		readable := (info.protection & C.VM_PROT_READ) != 0
		if readable && size > 0 && size <= 100*1024*1024 {
			statRegions++
			buf := C.malloc(C.size_t(size))
			if buf != nil {
				var outSize C.mach_vm_size_t
				kr2 := C.read_mem_safe(task, addr, size, buf, &outSize)
				if kr2 == C.KERN_SUCCESS && outSize > 4 {
					statReadOK++
					chunk := C.GoBytes(buf, C.int(outSize))

					// 与上个区域连续 → 拼接；否则先处理 carry 再重置
					if len(carry) > 0 && addr == carryEndAddr {
						carry = append(carry, chunk...)
					} else {
						if len(carry) > 0 {
							flush(carry)
						}
						carry = make([]byte, len(chunk))
						copy(carry, chunk)
					}
					carryEndAddr = addr + C.mach_vm_address_t(outSize)

					// carry 超限时先 flush 再重置
					if len(carry) > maxCarry {
						flush(carry)
						carry = nil
					}
				}
				C.free(buf)
			}
		} else if len(carry) > 0 {
			// 当前区域不可读，中断连续性
			flush(carry)
			carry = nil
		}

		addr += size
	}
	if len(carry) > 0 {
		flush(carry)
	}

	fmt.Printf("[mem-diag] pid=%d 可读区域=%d 成功读取=%d PK魔数命中=%d ZIP完整=%d\n",
		pid, statRegions, statReadOK, statPKHits, len(blobs))
	return blobs, nil
}
