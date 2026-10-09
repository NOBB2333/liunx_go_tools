//go:build linux

package filesystem

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestDirent64LayoutMatchesUnixDirent 保证 dirent64.go 里硬编码的 uapi 偏移，
// 跟 golang.org/x/sys/unix 在这个平台上给出的 unix.Dirent 布局一致。
// 这样"偏移写错"会在测试期直接失败，而不是在生产机上静默算错。
func TestDirent64LayoutMatchesUnixDirent(t *testing.T) {
	cases := []struct {
		field string
		got   uintptr
		want  uintptr
	}{
		{"Ino", unsafe.Offsetof(unix.Dirent{}.Ino), dirent64InoOffset},
		{"Reclen", unsafe.Offsetof(unix.Dirent{}.Reclen), dirent64ReclenOffset},
		{"Type", unsafe.Offsetof(unix.Dirent{}.Type), dirent64TypeOffset},
		{"Name", unsafe.Offsetof(unix.Dirent{}.Name), dirent64NameOffset},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("unix.Dirent.%s 偏移 = %d，dirent64.go 常量 = %d", c.field, c.got, c.want)
		}
	}
}
