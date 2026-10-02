package document

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/process"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// 预设文件类型分组
var docExtPresets = map[string][]string{
	"word":  {".docx", ".doc", ".wps", ".docm", ".dot", ".dotx"},
	"excel": {".xlsx", ".xls", ".et", ".xlsm", ".xlt", ".xltx", ".csv"},
	"ppt":   {".pptx", ".ppt", ".dps", ".pptm", ".pot", ".potx"},
	"pdf":   {".pdf"},
	"all":   {".docx", ".doc", ".wps", ".docm", ".dot", ".dotx", ".xlsx", ".xls", ".et", ".xlsm", ".xlt", ".xltx", ".csv", ".pptx", ".ppt", ".dps", ".pptm", ".pot", ".potx", ".pdf"},
}

// DocExtractor 从目标进程中找出打开的文档文件并复制到输出目录。
type DocExtractor struct {
	// ProcessNames 要扫描的进程名（模糊匹配），默认含 wps、wpsoffice、word、WINWORD、excel、powerpnt
	ProcessNames []string
	// Extensions 要识别的文件后缀（小写）
	Extensions []string
	// Debug 开启后打印诊断信息
	Debug bool
	// Base64Mode 开启后输出 .b64.txt 而不是原始文件
	Base64Mode bool
}

// NewDocExtractor 创建默认配置的提取器（扫 word+excel+ppt 全类型）。
func NewDocExtractor() *DocExtractor {
	return &DocExtractor{
		ProcessNames: []string{"wps", "wpsoffice", "et", "wpp", "word", "winword", "excel", "powerpnt"},
		Extensions:   docExtPresets["all"],
	}
}

// SetTypePreset 按预设类型名设置扫描后缀，支持 word / excel / ppt / pdf / all，
// 可传多个，例如 SetTypePreset("word", "excel")。
func (e *DocExtractor) SetTypePreset(types ...string) {
	extSet := map[string]bool{}
	for _, t := range types {
		for _, ext := range docExtPresets[strings.ToLower(t)] {
			extSet[ext] = true
		}
	}
	e.Extensions = make([]string, 0, len(extSet))
	for ext := range extSet {
		e.Extensions = append(e.Extensions, ext)
	}
}

// AddExtensions 追加自定义后缀（如 ".pdf", ".txt"），自动补全点号。
func (e *DocExtractor) AddExtensions(exts ...string) {
	extSet := map[string]bool{}
	for _, ext := range e.Extensions {
		extSet[ext] = true
	}
	for _, ext := range exts {
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		extSet[strings.ToLower(ext)] = true
	}
	e.Extensions = make([]string, 0, len(extSet))
	for ext := range extSet {
		e.Extensions = append(e.Extensions, ext)
	}
}

// ExtractResult 单个文件的提取结果。
type ExtractResult struct {
	SourcePath string
	DestPath   string
	Err        error
}

// Extract 找到所有目标进程打开的文档，复制到 outputDir。
func (e *DocExtractor) Extract(outputDir string) ([]ExtractResult, error) {
	if outputDir == "" {
		outputDir = "doc_extracted"
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("创建输出目录失败: %w", err)
	}

	pids, err := e.findTargetPIDs()
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("未找到目标进程（%s）", strings.Join(e.ProcessNames, ", "))
	}

	seen := map[string]bool{}
	var results []ExtractResult

	for _, pid := range pids {
		paths, err := e.openDocFiles(pid)
		if err != nil {
			continue
		}
		for _, src := range paths {
			if seen[src] {
				continue
			}
			seen[src] = true

			var dest string
			var saveErr error
			if e.Base64Mode {
				dest = filepath.Join(outputDir, filepath.Base(src)+".b64.txt")
				dest = uniqueDest(dest)
				saveErr = saveAsBase64(src, dest)
			} else {
				dest = filepath.Join(outputDir, filepath.Base(src))
				dest = uniqueDest(dest)
				saveErr = copyDocFile(src, dest)
			}

			if saveErr != nil {
				results = append(results, ExtractResult{SourcePath: src, Err: saveErr})
			} else {
				results = append(results, ExtractResult{SourcePath: src, DestPath: dest})
			}
		}
	}
	return results, nil
}

// FindTargetPIDs 导出版：找到所有匹配进程名的 PID。
func (e *DocExtractor) FindTargetPIDs() ([]int32, error) {
	return e.findTargetPIDs()
}
func (e *DocExtractor) findTargetPIDs() ([]int32, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	var pids []int32
	for _, p := range procs {
		name, _ := p.Name()
		nameLower := strings.ToLower(name)
		for _, target := range e.ProcessNames {
			if strings.Contains(nameLower, strings.ToLower(target)) {
				if e.Debug {
					fmt.Printf("[debug] 匹配进程: %s (pid=%d)\n", name, p.Pid)
				}
				pids = append(pids, p.Pid)
				break
			}
		}
	}
	if e.Debug && len(pids) == 0 {
		fmt.Printf("[debug] 未匹配到任何进程，扫描列表: %v\n", e.ProcessNames)
		fmt.Println("[debug] 当前所有进程名:")
		for _, p := range procs {
			name, _ := p.Name()
			fmt.Printf("  %s (pid=%d)\n", name, p.Pid)
		}
	}
	return pids, nil
}

// openDocFiles 获取指定 PID 进程打开的文档文件路径列表。
func (e *DocExtractor) openDocFiles(pid int32) ([]string, error) {
	switch runtime.GOOS {
	case "darwin", "linux":
		return e.openDocFilesUnix(pid)
	case "windows":
		// 优先用原生 API（支持 Unicode 路径），失败再用 handle.exe
		paths, err := e.openDocFilesWindowsAPI(pid)
		if err != nil {
			if e.Debug {
				fmt.Printf("[debug] Windows API 失败，降级到 handle.exe: %v\n", err)
			}
			return e.openDocFilesWindows(pid)
		}
		return paths, nil
	default:
		return nil, fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}
}

// openDocFilesUnix 通过 lsof 获取 macOS/Linux 下进程打开的文件。
func (e *DocExtractor) openDocFilesUnix(pid int32) ([]string, error) {
	out, err := exec.Command("lsof", "-p", fmt.Sprintf("%d", pid), "-F", "n").Output()
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		// lsof -F n 输出 "n<path>" 行
		if !strings.HasPrefix(line, "n") {
			continue
		}
		path := line[1:]
		if e.isTargetExt(path) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// openDocFilesWindows 通过 handle.exe（Sysinternals）获取 Windows 下进程打开的文件。
func (e *DocExtractor) openDocFilesWindows(pid int32) ([]string, error) {
	out, err := runHandleExe(pid)
	if err != nil {
		if e.Debug {
			fmt.Printf("[debug] handle.exe 执行失败 pid=%d: %v\n", pid, err)
		}
		return nil, err
	}

	// handle.exe 在中文 Windows 上输出 GBK，转为 UTF-8
	out = decodeGBK(out)

	if e.Debug {
		fmt.Printf("[debug] handle.exe pid=%d 原始输出:\n%s\n", pid, string(out))
	}

	// handle.exe 实际输出格式：
	//   A30: File  (R--)   C:\Users\wang\Desktop\test.docx
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		// 必须包含 ": File  ("
		if !strings.Contains(line, ": File  (") {
			continue
		}
		// 找权限括号结束位置，括号后跟若干空格再接路径
		closeIdx := strings.Index(line, ")")
		if closeIdx < 0 {
			continue
		}
		path := strings.TrimSpace(line[closeIdx+1:])
		if e.Debug {
			fmt.Printf("[debug] 候选路径: %q ext=%s\n", path, filepath.Ext(path))
		}
		if e.isTargetExt(path) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// runHandleExe 依次尝试多个路径和名称，返回输出。
// 查找顺序：可执行文件同目录的 Handle 子文件夹 → 同目录 → PATH。
// ARM64 Windows 优先用 handle64a.exe，x64 用 handle64.exe，兜底 handle.exe。
func runHandleExe(pid int32) ([]byte, error) {
	pidStr := fmt.Sprintf("%d", pid)

	candidates := buildHandleCandidates()
	for _, bin := range candidates {
		out, err := exec.Command(bin, "-p", pidStr, "-nobanner", "-accepteula").Output()
		if err == nil {
			return out, nil
		}
	}
	return nil, fmt.Errorf("handle.exe 未找到，请将 Handle 目录加入 PATH 或放在可执行文件同目录（需管理员权限）")
}

// buildHandleCandidates 生成 handle 可执行文件的候选路径列表。
func buildHandleCandidates() []string {
	// 按位数优先顺序排列的文件名
	names := []string{"handle64.exe", "handle64a.exe", "handle.exe", "handle"}

	// 可执行文件所在目录
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}

	var candidates []string
	// 1. 同目录下的 Handle 子文件夹（用户解压后放在这里）
	if exeDir != "" {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(exeDir, "Handle", name))
		}
	}
	// 2. 可执行文件同目录
	if exeDir != "" {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(exeDir, name))
		}
	}
	// 3. PATH（直接用文件名，由系统查找）
	candidates = append(candidates, names...)
	return candidates
}

// isTargetExt 判断路径后缀是否在目标列表中。
func (e *DocExtractor) isTargetExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, target := range e.Extensions {
		if ext == target {
			return true
		}
	}
	return false
}

// uniqueDest 如果目标路径已存在，自动在文件名后追加序号。
func uniqueDest(dest string) string {
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return dest
	}
	ext := filepath.Ext(dest)
	base := strings.TrimSuffix(dest, ext)
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s_%d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return dest
}

// decodeGBK 将 GBK 编码的字节转为 UTF-8，失败时原样返回。
func decodeGBK(b []byte) []byte {
	out, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), b)
	if err != nil {
		return b
	}
	return out
}

// SaveFileAsBase64 导出版：读取文件并 base64 编码写入 dest。
func SaveFileAsBase64(src, dest string) error {
	return saveAsBase64(src, dest)
}

// CopyDocFile 导出版：复制文件。
func CopyDocFile(src, dest string) error {
	return copyDocFile(src, dest)
}

// saveAsBase64 读取源文件，将内容 base64 编码后写入 dest（纯文本）。
func saveAsBase64(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return os.WriteFile(dest, []byte(encoded), 0644)
}

// copyDocFile 复制文件。
func copyDocFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
