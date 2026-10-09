package filesystem

import (
	"encoding/binary"
	"testing"
)

// mkDirent64 按内核 struct linux_dirent64 的规则合成一条记录：
// 长度 = ALIGN(19 + len(name) + 1, 8)。
// 校验过真实 getdents64 输出： "."->24、"sbin"->24、"swap.img"->32、
// "lost+found"->32、".1panel_clash"->40、"lib.usr-is-merged"->40，全部一致。
func mkDirent64(ino uint64, typ uint8, name string) []byte {
	need := dirent64NameOffset + len(name) + 1
	reclen := (need + 7) &^ 7
	buf := make([]byte, reclen)
	binary.LittleEndian.PutUint64(buf[dirent64InoOffset:dirent64InoOffset+8], ino)
	binary.LittleEndian.PutUint16(buf[dirent64ReclenOffset:dirent64ReclenOffset+2], uint16(reclen))
	buf[dirent64TypeOffset] = typ
	copy(buf[dirent64NameOffset:], name)
	return buf
}

func joinDirents(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func collect(buf []byte) []string {
	var got []string
	walkLinuxDirent64(buf, func(rec fastDirent) bool {
		got = append(got, rec.name)
		return true
	})
	return got
}

func assertNames(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("visited %d entries %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %q, want %q (full: %q)", i, got[i], want[i], got)
		}
	}
}

// TestWalkLinuxDirent64_EqualReclen 复现 /tmp/ft 上的原始故障：
// 前两条记录（". / .."）长度都是 24。旧实现 `offset = next` 把相对长度
// 当绝对偏移，游标停在第 24 字节，随后 `next <= offset`（24 <= 24）直接
// break，整棵目录只剩第一个条目。
func TestWalkLinuxDirent64_EqualReclen(t *testing.T) {
	buf := joinDirents(
		mkDirent64(1, fastDTDir, "."),
		mkDirent64(2, fastDTDir, ".."),
		mkDirent64(3, fastDTDir, "dirHIDDEN"),
	)
	assertNames(t, collect(buf), ".", "..", "dirHIDDEN")
}

// TestWalkLinuxDirent64_RootSequence 用生产机 `/` 上抓到的真实记录序列
// （长度 24 / 40 / 24 / 40 / ... 交替）做回归。旧实现在第 2 条记录之后
// 游标错位，第 3 条记录解析失败 -> 遍历提前结束，只拿到前 2 个名字。
func TestWalkLinuxDirent64_RootSequence(t *testing.T) {
	buf := joinDirents(
		mkDirent64(11, fastDTDir, "root"),
		mkDirent64(12, fastDTDir, "lib.usr-is-merged"),
		mkDirent64(13, fastDTLnk, "sbin"),
		mkDirent64(14, fastDTDir, "bin.usr-is-merged"),
		mkDirent64(15, fastDTLnk, "bin"),
		mkDirent64(16, fastDTDir, "opt"),
		mkDirent64(17, fastDTReg, "swap.img"),
		mkDirent64(18, fastDTDir, ".1panel_clash"),
	)
	assertNames(t, collect(buf),
		"root", "lib.usr-is-merged", "sbin", "bin.usr-is-merged",
		"bin", "opt", "swap.img", ".1panel_clash")
}

// TestWalkLinuxDirent64_ParsesFields 确认字段没有被读错位。
func TestWalkLinuxDirent64_ParsesFields(t *testing.T) {
	buf := joinDirents(
		mkDirent64(0xDEADBEEF, fastDTLnk, "lnk"),
		mkDirent64(0, fastDTReg, "zero-ino"),
	)
	var got []fastDirent
	walkLinuxDirent64(buf, func(rec fastDirent) bool {
		got = append(got, rec)
		return true
	})
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if got[0].ino != 0xDEADBEEF || got[0].typ != fastDTLnk || got[0].name != "lnk" {
		t.Fatalf("record0 = %+v", got[0])
	}
	// d_ino == 0 必须被保留（9p/virtiofs 会返回 0，旧逻辑曾据此丢弃整棵树）。
	if got[1].ino != 0 || got[1].typ != fastDTReg || got[1].name != "zero-ino" {
		t.Fatalf("record1 = %+v", got[1])
	}
}

// TestWalkLinuxDirent64_Truncated 表示记录被截断时必须报出来，
// 不能静默少条目。
func TestWalkLinuxDirent64_Truncated(t *testing.T) {
	full := mkDirent64(1, fastDTDir, "aaaa")
	visits := 0
	truncated := walkLinuxDirent64(joinDirents(full, mkDirent64(2, fastDTReg, "bbbb")[:10]), func(fastDirent) bool {
		visits++
		return true
	})
	if visits != 1 || !truncated {
		t.Fatalf("visits=%d truncated=%v, want visits=1 truncated=true", visits, truncated)
	}
}

// TestWalkLinuxDirent64_StopsOnFalse 确认回调返回 false 能中止遍历
// （ctx 取消路径）。
func TestWalkLinuxDirent64_StopsOnFalse(t *testing.T) {
	buf := joinDirents(
		mkDirent64(1, fastDTReg, "a.txt"),
		mkDirent64(2, fastDTReg, "b.txt"),
		mkDirent64(3, fastDTReg, "c.txt"),
	)
	visits := 0
	truncated := walkLinuxDirent64(buf, func(fastDirent) bool {
		visits++
		return visits < 2
	})
	if visits != 2 {
		t.Fatalf("visits=%d, want 2", visits)
	}
	if truncated {
		t.Fatal("truncated=true, want false (停止是回调主动要求的，不是缓冲截断)")
	}
}

// TestParseLinuxDirent64_RejectsBadReclen 覆盖 reclen 非法/越界的情况。
func TestParseLinuxDirent64_RejectsBadReclen(t *testing.T) {
	cases := map[string][]byte{
		"too-short":       make([]byte, dirent64MinSize-1),
		"reclen-zero":     func() []byte { b := mkDirent64(1, fastDTReg, "x"); binary.LittleEndian.PutUint16(b[16:18], 0); return b }(),
		"reclen-past-end": func() []byte { b := mkDirent64(1, fastDTReg, "x"); binary.LittleEndian.PutUint16(b[16:18], 4096); return b }(),
		"reclen-too-small": func() []byte {
			b := mkDirent64(1, fastDTReg, "x")
			binary.LittleEndian.PutUint16(b[16:18], 8) // < nameOffset，d_type 会落到记录外
			return b
		}(),
	}
	for name, buf := range cases {
		if _, _, ok := parseLinuxDirent64(buf); ok {
			t.Fatalf("%s: parseLinuxDirent64 returned ok=true, want false", name)
		}
	}
}

// TestWalkLinuxDirent64_EmptyBuffer 空缓冲区应当什么都不做、也不算截断。
func TestWalkLinuxDirent64_EmptyBuffer(t *testing.T) {
	visits := 0
	if truncated := walkLinuxDirent64(nil, func(fastDirent) bool { visits++; return true }); truncated {
		t.Fatal("empty buffer reported as truncated")
	}
	if visits != 0 {
		t.Fatalf("visits=%d, want 0", visits)
	}
}
