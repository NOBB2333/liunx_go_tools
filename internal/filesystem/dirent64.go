package filesystem

import "encoding/binary"

// 本文件刻意**不加** build tag。
//
// getdents64 返回的 struct linux_dirent64 是稳定内核 ABI，布局在所有 Linux
// 平台上完全一致：
//
//	struct linux_dirent64 {
//	    u64  d_ino;    // 偏移 0
//	    s64  d_off;    // 偏移 8   （本工具不使用）
//	    u16  d_reclen; // 偏移 16  —— 记录总长，**相对当前缓冲区开头**
//	    u8   d_type;   // 偏移 18
//	    char d_name[]; // 偏移 19  NUL 结尾，整条记录按 8 字节对齐
//	};
//
// 把这些偏移和遍历逻辑放在无 tag 的文件里，是为了让"按记录边界推进游标"
// 这件最容易出错的事，能在**任意平台**（包括开发用的 Windows）上用合成
// buffer 做单元测试。此前的实现在 scanner_linux.go（//go:build linux）里把
// d_reclen 这个相对长度当绝对偏移用，Windows 上根本跑不到，直到上生产机才
// 暴露成 "files=0" 这种静默丢数据的故障。
const (
	dirent64InoOffset    = 0
	dirent64ReclenOffset = 16
	dirent64TypeOffset   = 18
	dirent64NameOffset   = 19
	// dirent64MinSize 是合法记录的长度下界：d_name 至少要有 1 个字节的空间
	// （即使真实名字为空，内核也会留出对齐所需的字节）。
	dirent64MinSize = dirent64NameOffset + 1
)

// d_type 取值，来自内核的 DT_* 枚举（linux_dirent64.d_type）。
// DT_UNKNOWN 表示该文件系统不提供类型，必须再 statx 一次。
const (
	fastDTUnknown = 0
	fastDTDir     = 4
	fastDTReg     = 8
	fastDTLnk     = 10
)

// fastDirent 是一条目录记录中本工具关心的字段。
type fastDirent struct {
	ino  uint64
	typ  uint8
	name string
}

// parseLinuxDirent64 解析 buf 开头的第一条记录。
//
// 返回值 reclen 是这条记录的长度，**相对于 buf 的起始位置**（即
// buf[0]），不是绝对偏移。调用方必须用 `offset += reclen` 推进游标；
// 写成 `offset = reclen` 会把相对长度当绝对偏移，通常在第 2 条记录上就
// 越界并提前结束遍历。
func parseLinuxDirent64(buf []byte) (rec fastDirent, reclen int, ok bool) {
	if len(buf) < dirent64MinSize {
		return fastDirent{}, 0, false
	}
	reclen = int(binary.LittleEndian.Uint16(buf[dirent64ReclenOffset : dirent64ReclenOffset+2]))
	// d_type 必须落在记录内部，否则说明这条记录本身是错位的。
	if reclen < dirent64MinSize || reclen > len(buf) || dirent64TypeOffset >= reclen {
		return fastDirent{}, 0, false
	}
	name := buf[dirent64NameOffset:reclen]
	for i, b := range name {
		if b == 0 {
			name = name[:i]
			break
		}
	}
	return fastDirent{
		ino:  binary.LittleEndian.Uint64(buf[dirent64InoOffset : dirent64InoOffset+8]),
		typ:  buf[dirent64TypeOffset],
		name: string(name),
	}, reclen, true
}

// walkLinuxDirent64 按记录边界遍历一段 getdents64 返回的缓冲区，对每条记录
// 调用 fn。fn 返回 false 表示中止遍历（例如 ctx 被取消）。
//
// truncated 为 true 表示缓冲区在记录中间就解析失败了——正常情况下不会发生
// （内核只会返回完整记录），一旦发生必须由调用方记成错误，否则又会退化成
// "目录里凭空少条目却不报错"。
func walkLinuxDirent64(buf []byte, fn func(fastDirent) bool) (truncated bool) {
	for offset := 0; offset < len(buf); {
		rec, reclen, ok := parseLinuxDirent64(buf[offset:])
		if !ok || reclen <= 0 {
			return true
		}
		offset += reclen // 相对长度必须累加，见 parseLinuxDirent64 注释
		if !fn(rec) {
			return false
		}
	}
	return false
}
