package cleanup

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CleanupScanOptions 表示磁盘清理分析的输入参数。
type CleanupScanOptions struct {
	HomeDir string
	targets []cleanupTargetSpec
}

// CleanupScanReport 表示扫描后的结构化结果。
type CleanupScanReport struct {
	GeneratedAt                 time.Time               `json:"generated_at"`
	HostOS                      string                  `json:"host_os"`
	HomeDir                     string                  `json:"home_dir"`
	Notes                       []string                `json:"notes,omitempty"`
	TotalObservedBytes          int64                   `json:"total_observed_bytes"`
	TotalLikelyReclaimableBytes int64                   `json:"total_likely_reclaimable_bytes"`
	Categories                  []CleanupCategoryReport `json:"categories"`
}

// CleanupCategoryReport 表示某一类清理候选。
type CleanupCategoryReport struct {
	Key                    string             `json:"key"`
	Label                  string             `json:"label"`
	ObservedBytes          int64              `json:"observed_bytes"`
	LikelyReclaimableBytes int64              `json:"likely_reclaimable_bytes"`
	Candidates             []CleanupCandidate `json:"candidates"`
}

// CleanupCandidate 表示具体路径上的分析结果。
type CleanupCandidate struct {
	Key                    string   `json:"key"`
	Label                  string   `json:"label"`
	Path                   string   `json:"path"`
	ObservedBytes          int64    `json:"observed_bytes"`
	LikelyReclaimableBytes int64    `json:"likely_reclaimable_bytes"`
	Risk                   string   `json:"risk"`
	AutoCleanable          bool     `json:"auto_cleanable"`
	Reason                 string   `json:"reason"`
	Suggestion             string   `json:"suggestion"`
	Notes                  []string `json:"notes,omitempty"`
}

type CleanupScanner struct{}

// NewCleanupScanner 创建磁盘清理扫描器。
func NewCleanupScanner() *CleanupScanner {
	return &CleanupScanner{}
}

type cleanupTargetSpec struct {
	Key                    string
	Category               string
	Label                  string
	Path                   string
	Risk                   string
	AutoCleanable          bool
	Reason                 string
	Suggestion             string
	IncludeInObservedTotal bool
	Analyze                func(context.Context, string) cleanupAnalysis
}

type cleanupAnalysis struct {
	LikelyReclaimableBytes int64
	Notes                  []string
}

type snapRevision struct {
	Name     string
	Revision string
}

var cleanupCategoryLabels = map[string]string{
	"docker":   "Docker",
	"snap":     "Snap",
	"logs":     "Logs",
	"1panel":   "1Panel",
	"boot":     "Boot",
	"devcache": "Dev Cache",
}

// Scan 扫描当前 Linux 主机上的典型清理目标。
func (s *CleanupScanner) Scan(ctx context.Context, opt CleanupScanOptions) (CleanupScanReport, error) {
	homeDir := strings.TrimSpace(opt.HomeDir)
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return CleanupScanReport{}, err
		}
	}

	report := CleanupScanReport{
		GeneratedAt: time.Now(),
		HostOS:      runtime.GOOS,
		HomeDir:     homeDir,
	}
	if runtime.GOOS != "linux" {
		report.Notes = append(report.Notes, "当前预设规则按 Linux 主机设计；其他系统只会扫描实际存在的同名路径。")
	}

	targets := opt.targets
	if len(targets) == 0 {
		targets = defaultCleanupTargets(ctx, homeDir)
	}

	categories := make(map[string]*CleanupCategoryReport, len(cleanupCategoryLabels))
	for _, target := range targets {
		if strings.TrimSpace(target.Path) == "" {
			continue
		}

		info, err := os.Lstat(target.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return CleanupScanReport{}, err
		}

		sizeBytes, err := diskUsageBytes(ctx, target.Path, info)
		if err != nil {
			return CleanupScanReport{}, err
		}

		analysis := cleanupAnalysis{}
		if target.Analyze != nil {
			analysis = target.Analyze(ctx, target.Path)
		}
		if analysis.LikelyReclaimableBytes < 0 {
			analysis.LikelyReclaimableBytes = 0
		}
		if analysis.LikelyReclaimableBytes > sizeBytes {
			analysis.LikelyReclaimableBytes = sizeBytes
		}

		category := categories[target.Category]
		if category == nil {
			category = &CleanupCategoryReport{
				Key:   target.Category,
				Label: cleanupCategoryLabel(target.Category),
			}
			categories[target.Category] = category
		}

		candidate := CleanupCandidate{
			Key:                    target.Key,
			Label:                  target.Label,
			Path:                   target.Path,
			ObservedBytes:          sizeBytes,
			LikelyReclaimableBytes: analysis.LikelyReclaimableBytes,
			Risk:                   target.Risk,
			AutoCleanable:          target.AutoCleanable,
			Reason:                 target.Reason,
			Suggestion:             target.Suggestion,
			Notes:                  analysis.Notes,
		}
		category.Candidates = append(category.Candidates, candidate)
		if target.IncludeInObservedTotal {
			category.ObservedBytes += sizeBytes
			category.LikelyReclaimableBytes += analysis.LikelyReclaimableBytes
		}
	}

	for _, category := range categories {
		sort.Slice(category.Candidates, func(i, j int) bool {
			return category.Candidates[i].ObservedBytes > category.Candidates[j].ObservedBytes
		})
		report.TotalObservedBytes += category.ObservedBytes
		report.TotalLikelyReclaimableBytes += category.LikelyReclaimableBytes
		report.Categories = append(report.Categories, *category)
	}

	sort.Slice(report.Categories, func(i, j int) bool {
		return report.Categories[i].ObservedBytes > report.Categories[j].ObservedBytes
	})
	if len(report.Categories) == 0 {
		report.Notes = append(report.Notes, "没有发现预设的清理目标路径。")
	}

	return report, nil
}

// RenderCleanupScanReport 把结构化结果渲染成文本摘要。
func RenderCleanupScanReport(report CleanupScanReport) string {
	var b strings.Builder
	b.WriteString("磁盘清理分析\n")
	b.WriteString(fmt.Sprintf("时间: %s\n", report.GeneratedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("系统: %s\n", report.HostOS))
	b.WriteString(fmt.Sprintf("Home: %s\n", report.HomeDir))
	b.WriteString(fmt.Sprintf("已观测总量: %s\n", formatHumanBytes(report.TotalObservedBytes)))
	b.WriteString(fmt.Sprintf("预计可回收: %s\n", formatHumanBytes(report.TotalLikelyReclaimableBytes)))
	if len(report.Notes) > 0 {
		b.WriteString("\n说明:\n")
		for _, note := range report.Notes {
			b.WriteString("- " + note + "\n")
		}
	}

	for _, category := range report.Categories {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("[%s] %s / 可回收 %s\n", category.Label, formatHumanBytes(category.ObservedBytes), formatHumanBytes(category.LikelyReclaimableBytes)))
		for _, item := range category.Candidates {
			b.WriteString(fmt.Sprintf("- %s\n", item.Label))
			b.WriteString(fmt.Sprintf("  path: %s\n", item.Path))
			b.WriteString(fmt.Sprintf("  observed: %s", formatHumanBytes(item.ObservedBytes)))
			if item.LikelyReclaimableBytes > 0 {
				b.WriteString(fmt.Sprintf(" / reclaimable: %s", formatHumanBytes(item.LikelyReclaimableBytes)))
			}
			b.WriteString("\n")
			b.WriteString(fmt.Sprintf("  risk: %s / auto: %t\n", item.Risk, item.AutoCleanable))
			b.WriteString("  why: " + item.Reason + "\n")
			b.WriteString("  suggest: " + item.Suggestion + "\n")
			for _, note := range item.Notes {
				b.WriteString("  note: " + note + "\n")
			}
		}
	}

	return b.String()
}

func cleanupCategoryLabel(key string) string {
	if label, ok := cleanupCategoryLabels[key]; ok {
		return label
	}
	return strings.Title(key)
}

func defaultCleanupTargets(ctx context.Context, homeDir string) []cleanupTargetSpec {
	disabledSnaps := loadDisabledSnapRevisions(ctx)
	currentKernel := loadCurrentKernelRelease(ctx)

	return []cleanupTargetSpec{
		{
			Key:                    "docker-overlay2",
			Category:               "docker",
			Label:                  "Docker overlay2",
			Path:                   "/var/lib/docker/overlay2",
			Risk:                   "review",
			Reason:                 "这里通常是镜像层和容器可写层的大头；体积大不代表都能直接删。",
			Suggestion:             "先用 `docker system df`、`docker ps -a --size`、`docker image ls` 定位未使用镜像和容器，再决定是否 prune。",
			IncludeInObservedTotal: true,
		},
		{
			Key:                    "docker-containers",
			Category:               "docker",
			Label:                  "Docker containers",
			Path:                   "/var/lib/docker/containers",
			Risk:                   "review",
			Reason:                 "这里除了容器元数据，最常见的大头是 `*-json.log` 容器日志。",
			Suggestion:             "先确认哪些容器日志异常大，再做日志截断或给 Docker 配置 log rotation。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				size, files := sumMatchingFiles(path, func(filePath string, entry fs.DirEntry) bool {
					return strings.HasSuffix(filePath, "-json.log")
				})
				notes := []string{}
				if len(files) > 0 {
					notes = append(notes, fmt.Sprintf("检测到 %d 个容器日志文件。", len(files)))
				}
				return cleanupAnalysis{
					LikelyReclaimableBytes: size,
					Notes:                  notes,
				}
			},
		},
		{
			Key:                    "docker-volumes",
			Category:               "docker",
			Label:                  "Docker volumes",
			Path:                   "/var/lib/docker/volumes",
			Risk:                   "review",
			Reason:                 "命名卷可能包含数据库和持久化业务数据，不应直接删。",
			Suggestion:             "先比对 `docker volume ls` 和正在运行的容器引用关系，只清理未使用卷。",
			IncludeInObservedTotal: true,
		},
		{
			Key:                    "docker-buildkit",
			Category:               "docker",
			Label:                  "Docker build cache",
			Path:                   "/var/lib/docker/buildkit",
			Risk:                   "review",
			Reason:                 "BuildKit 缓存通常可以回收，但需要确认最近是否还会重复构建。",
			Suggestion:             "可先评估 `docker builder prune`，再决定是否清理。",
			IncludeInObservedTotal: true,
		},
		{
			Key:                    "snapd-snaps",
			Category:               "snap",
			Label:                  "snapd snaps",
			Path:                   "/var/lib/snapd/snaps",
			Risk:                   "review",
			Reason:                 "这里是 Snap 包实体文件本体；旧 revision 常常仍然保留用于回滚。",
			Suggestion:             "优先识别 disabled revision，再执行 `snap remove <name> --revision=<rev>`。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				var total int64
				var notes []string
				if len(disabledSnaps) == 0 {
					return cleanupAnalysis{
						Notes: []string{"未解析到 disabled revision，可能机器上未安装 snap 或当前没有旧 revision。"},
					}
				}
				refs := make([]string, 0, len(disabledSnaps))
				for _, item := range disabledSnaps {
					filePath := filepath.Join(path, item.Name+"_"+item.Revision+".snap")
					info, err := os.Stat(filePath)
					if err != nil {
						continue
					}
					total += info.Size()
					refs = append(refs, item.Name+"@"+item.Revision)
				}
				if len(refs) > 0 {
					notes = append(notes, "检测到 disabled revisions: "+strings.Join(refs, ", "))
				}
				return cleanupAnalysis{
					LikelyReclaimableBytes: total,
					Notes:                  notes,
				}
			},
		},
		{
			Key:                    "snapd-cache",
			Category:               "snap",
			Label:                  "snapd cache",
			Path:                   "/var/lib/snapd/cache",
			Risk:                   "safe",
			AutoCleanable:          true,
			Reason:                 "这里通常是 Snap 下载缓存，不是运行时必须数据。",
			Suggestion:             "适合先作为安全清理项；清掉后后续需要时会重新下载。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				size, _ := dirUsageByWalk(path)
				return cleanupAnalysis{LikelyReclaimableBytes: size}
			},
		},
		{
			Key:                    "snapd-seed",
			Category:               "snap",
			Label:                  "snapd seed",
			Path:                   "/var/lib/snapd/seed/snaps",
			Risk:                   "manual",
			Reason:                 "seed 通常是系统初始化保留包，不建议作为默认自动清理项。",
			Suggestion:             "只有在确认系统恢复/初始化场景不依赖时再手工处理。",
			IncludeInObservedTotal: true,
		},
		{
			Key:                    "snap-mounted-view",
			Category:               "snap",
			Label:                  "/snap mounted view",
			Path:                   "/snap",
			Risk:                   "info",
			Reason:                 "`/snap` 是挂载后的可见视图，不是另一份独立包文件，和 `/var/lib/snapd/snaps` 存在重复观察。",
			Suggestion:             "不要把 `/snap` 和 `/var/lib/snapd/snaps` 简单相加。",
			IncludeInObservedTotal: false,
		},
		{
			Key:                    "snap-user-data",
			Category:               "snap",
			Label:                  "user snap data",
			Path:                   filepath.Join(homeDir, "snap"),
			Risk:                   "review",
			Reason:                 "这里多半是应用配置、缓存和个人数据，不等于程序本体。",
			Suggestion:             "只能按具体应用逐个确认，不建议一刀切删除整个目录。",
			IncludeInObservedTotal: true,
		},
		{
			Key:                    "system-logs",
			Category:               "logs",
			Label:                  "/var/log",
			Path:                   "/var/log",
			Risk:                   "review",
			Reason:                 "系统日志通常不大，真正值得优先看的往往是已轮转的旧日志和异常增长的单个日志。",
			Suggestion:             "先保留近期日志，重点处理 `.gz`、`.1`、`.old` 这类已轮转历史日志。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				size, files := sumMatchingFiles(path, func(filePath string, entry fs.DirEntry) bool {
					base := filepath.Base(filePath)
					return strings.HasSuffix(base, ".gz") || strings.HasSuffix(base, ".1") || strings.HasSuffix(base, ".old")
				})
				notes := []string{}
				if len(files) > 0 {
					notes = append(notes, fmt.Sprintf("检测到 %d 个轮转日志文件。", len(files)))
				}
				return cleanupAnalysis{
					LikelyReclaimableBytes: size,
					Notes:                  notes,
				}
			},
		},
		{
			Key:                    "1panel-upgrade",
			Category:               "1panel",
			Label:                  "1Panel upgrade cache",
			Path:                   "/opt/1panel/tmp/upgrade",
			Risk:                   "review",
			AutoCleanable:          true,
			Reason:                 "这里通常是 1Panel 升级包和临时解压产物，长期积累后很容易留下历史残留。",
			Suggestion:             "确认当前版本稳定且不需要回滚后，可优先清理旧升级目录。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				size, _ := dirUsageByWalk(path)
				return cleanupAnalysis{LikelyReclaimableBytes: size}
			},
		},
		{
			Key:                    "1panel-backups",
			Category:               "1panel",
			Label:                  "1Panel backups",
			Path:                   "/opt/1panel-v1-backups",
			Risk:                   "manual",
			Reason:                 "这类目录通常是历史迁移或回滚备份，可能是最后的兜底恢复资料。",
			Suggestion:             "先确认是否已经离线备份或不再需要回滚，再考虑手动清理。",
			IncludeInObservedTotal: true,
		},
		{
			Key:                    "boot-files",
			Category:               "boot",
			Label:                  "/boot",
			Path:                   "/boot",
			Risk:                   "manual",
			Reason:                 "旧内核文件可以释放一点空间，但这是系统级变更，不能手删文件。",
			Suggestion:             "应通过包管理器清理旧内核，例如 Ubuntu 上优先走 `apt autoremove`。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				if currentKernel == "" {
					return cleanupAnalysis{}
				}
				versions, total := oldKernelFootprint(path, currentKernel)
				notes := []string{}
				if len(versions) > 0 {
					notes = append(notes, "当前内核: "+currentKernel)
					notes = append(notes, "检测到旧内核版本: "+strings.Join(versions, ", "))
				}
				return cleanupAnalysis{
					LikelyReclaimableBytes: total,
					Notes:                  notes,
				}
			},
		},
		{
			Key:                    "go-mod-cache",
			Category:               "devcache",
			Label:                  "Go module cache",
			Path:                   filepath.Join(homeDir, "go", "pkg", "mod"),
			Risk:                   "safe",
			AutoCleanable:          true,
			Reason:                 "Go 模块缓存可以随时重新下载，属于标准可回收开发缓存。",
			Suggestion:             "可直接清理，或优先用 `go clean -modcache`。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				size, _ := dirUsageByWalk(path)
				return cleanupAnalysis{LikelyReclaimableBytes: size}
			},
		},
		{
			Key:                    "go-build-cache",
			Category:               "devcache",
			Label:                  "Go build cache",
			Path:                   filepath.Join(homeDir, ".cache", "go-build"),
			Risk:                   "safe",
			AutoCleanable:          true,
			Reason:                 "Go 编译缓存是纯临时数据。",
			Suggestion:             "可直接清理，或优先用 `go clean -cache`。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				size, _ := dirUsageByWalk(path)
				return cleanupAnalysis{LikelyReclaimableBytes: size}
			},
		},
		{
			Key:                    "pip-cache",
			Category:               "devcache",
			Label:                  "pip cache",
			Path:                   filepath.Join(homeDir, ".cache", "pip"),
			Risk:                   "safe",
			AutoCleanable:          true,
			Reason:                 "pip 下载缓存和 wheel 缓存都可以重新生成。",
			Suggestion:             "可直接清理，或优先用 `pip cache purge`。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				size, _ := dirUsageByWalk(path)
				return cleanupAnalysis{LikelyReclaimableBytes: size}
			},
		},
		{
			Key:                    "nuget-packages",
			Category:               "devcache",
			Label:                  "NuGet packages",
			Path:                   filepath.Join(homeDir, ".nuget", "packages"),
			Risk:                   "safe",
			AutoCleanable:          true,
			Reason:                 "NuGet 包缓存可在 `dotnet restore` 时重新下载。",
			Suggestion:             "适合作为开发缓存清理项；清完后首次 restore 会慢一些。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				size, _ := dirUsageByWalk(path)
				return cleanupAnalysis{LikelyReclaimableBytes: size}
			},
		},
		{
			Key:                    "cargo-registry",
			Category:               "devcache",
			Label:                  "Cargo registry",
			Path:                   filepath.Join(homeDir, ".cargo", "registry"),
			Risk:                   "safe",
			AutoCleanable:          true,
			Reason:                 "Rust registry 缓存可以重新拉取。",
			Suggestion:             "确认近期不会大量离线构建后，可直接清理。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				size, _ := dirUsageByWalk(path)
				return cleanupAnalysis{LikelyReclaimableBytes: size}
			},
		},
		{
			Key:                    "npm-cache",
			Category:               "devcache",
			Label:                  "npm cache",
			Path:                   filepath.Join(homeDir, ".npm"),
			Risk:                   "safe",
			AutoCleanable:          true,
			Reason:                 "npm 缓存属于典型可再生成数据。",
			Suggestion:             "可直接清理，或使用 `npm cache clean --force`。",
			IncludeInObservedTotal: true,
			Analyze: func(_ context.Context, path string) cleanupAnalysis {
				size, _ := dirUsageByWalk(path)
				return cleanupAnalysis{LikelyReclaimableBytes: size}
			},
		},
	}
}

func loadDisabledSnapRevisions(ctx context.Context) []snapRevision {
	out, err := exec.CommandContext(ctx, "snap", "list", "--all").Output()
	if err != nil {
		return nil
	}
	return parseDisabledSnapRevisions(string(out))
}

func parseDisabledSnapRevisions(output string) []snapRevision {
	lines := strings.Split(output, "\n")
	seen := make(map[string]struct{})
	result := make([]snapRevision, 0, 8)

	for idx, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if idx == 0 && strings.HasPrefix(strings.ToLower(line), "name ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		notes := fields[len(fields)-1]
		if !strings.Contains(notes, "disabled") {
			continue
		}
		item := snapRevision{
			Name:     fields[0],
			Revision: fields[2],
		}
		key := item.Name + "@" + item.Revision
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Revision < result[j].Revision
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func loadCurrentKernelRelease(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "uname", "-r").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func oldKernelFootprint(bootPath, currentKernel string) ([]string, int64) {
	entries, err := os.ReadDir(bootPath)
	if err != nil {
		return nil, 0
	}

	versionSizes := make(map[string]int64)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		version := kernelVersionFromBootFile(entry.Name())
		if version == "" || version == currentKernel {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		versionSizes[version] += info.Size()
	}

	versions := make([]string, 0, len(versionSizes))
	var total int64
	for version, size := range versionSizes {
		versions = append(versions, version)
		total += size
	}
	sort.Strings(versions)
	return versions, total
}

func kernelVersionFromBootFile(name string) string {
	prefixes := []string{"vmlinuz-", "initrd.img-", "System.map-", "config-"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return strings.TrimPrefix(name, prefix)
		}
	}
	return ""
}

func diskUsageBytes(ctx context.Context, path string, info os.FileInfo) (int64, error) {
	if !info.IsDir() {
		return info.Size(), nil
	}

	if runtime.GOOS == "linux" {
		if out, err := exec.CommandContext(ctx, "du", "-s", "-B1", path).Output(); err == nil {
			fields := strings.Fields(string(out))
			if len(fields) > 0 {
				size, parseErr := strconv.ParseInt(fields[0], 10, 64)
				if parseErr == nil {
					return size, nil
				}
			}
		}
	}

	return dirUsageByWalk(path)
}

func dirUsageByWalk(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			total += info.Size()
			return nil
		}
		if !entry.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func sumMatchingFiles(root string, match func(string, fs.DirEntry) bool) (int64, []string) {
	var total int64
	paths := make([]string, 0, 8)
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() || !match(path, entry) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		paths = append(paths, path)
		return nil
	})
	return total, paths
}
