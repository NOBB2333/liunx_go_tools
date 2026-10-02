package filesystem

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

// openInFileManager asks the host desktop to open a directory or reveal a
// file. The browser never receives direct filesystem access; the local Go
// process performs the operation with the user's existing OS permissions.
func openInFileManager(path string, isDir bool) error {
	path = filepath.Clean(path)
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
		if isDir {
			args = []string{path}
		} else {
			args = []string{"-R", path}
		}
	case "windows":
		command = "explorer.exe"
		if isDir {
			args = []string{path}
		} else {
			args = []string{"/select," + path}
		}
	default:
		command = "xdg-open"
		if isDir {
			args = []string{path}
		} else {
			args = []string{filepath.Dir(path)}
		}
	}
	cmd := exec.Command(command, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start file manager: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
