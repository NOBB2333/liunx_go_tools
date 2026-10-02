package cleanup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// CleanupRunOptions 表示磁盘清理执行的输入参数。
type CleanupRunOptions struct {
	HomeDir string
	Targets []string
	actions []cleanupRunAction
}

// CleanupRunReport 表示执行清理后的结果。
type CleanupRunReport struct {
	GeneratedAt         time.Time          `json:"generated_at"`
	HostOS              string             `json:"host_os"`
	HomeDir             string             `json:"home_dir"`
	TotalReclaimedBytes int64              `json:"total_reclaimed_bytes"`
	Items               []CleanupRunResult `json:"items"`
}

// CleanupRunResult 表示单个清理动作的结果。
type CleanupRunResult struct {
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	Path           string   `json:"path"`
	BeforeBytes    int64    `json:"before_bytes"`
	AfterBytes     int64    `json:"after_bytes"`
	ReclaimedBytes int64    `json:"reclaimed_bytes"`
	Notes          []string `json:"notes,omitempty"`
}

type CleanupRunner struct{}

// NewCleanupRunner 创建磁盘清理执行器。
func NewCleanupRunner() *CleanupRunner {
	return &CleanupRunner{}
}

type cleanupRunAction struct {
	Key   string
	Label string
	Path  string
	Run   func(context.Context) ([]string, error)
}

// Run 执行选定的清理动作。
func (r *CleanupRunner) Run(ctx context.Context, opt CleanupRunOptions) (CleanupRunReport, error) {
	homeDir := strings.TrimSpace(opt.HomeDir)
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return CleanupRunReport{}, err
		}
	}

	allActions := opt.actions
	if len(allActions) == 0 {
		allActions = defaultCleanupRunActions(homeDir)
	}
	actionMap := make(map[string]cleanupRunAction, len(allActions))
	for _, action := range allActions {
		actionMap[action.Key] = action
	}

	targets := normalizeCleanupTargets(opt.Targets)
	if len(targets) == 0 {
		return CleanupRunReport{}, fmt.Errorf("no cleanup targets selected")
	}

	report := CleanupRunReport{
		GeneratedAt: time.Now(),
		HostOS:      runtime.GOOS,
		HomeDir:     homeDir,
	}
	for _, key := range targets {
		action, ok := actionMap[key]
		if !ok {
			return CleanupRunReport{}, fmt.Errorf("unknown cleanup target: %s", key)
		}

		beforeBytes, err := pathUsageIfExists(ctx, action.Path)
		if err != nil {
			return CleanupRunReport{}, err
		}

		notes, err := action.Run(ctx)
		if err != nil {
			return CleanupRunReport{}, err
		}

		afterBytes, err := pathUsageIfExists(ctx, action.Path)
		if err != nil {
			return CleanupRunReport{}, err
		}

		reclaimed := beforeBytes - afterBytes
		if reclaimed < 0 {
			reclaimed = 0
		}
		report.TotalReclaimedBytes += reclaimed
		report.Items = append(report.Items, CleanupRunResult{
			Key:            action.Key,
			Label:          action.Label,
			Path:           action.Path,
			BeforeBytes:    beforeBytes,
			AfterBytes:     afterBytes,
			ReclaimedBytes: reclaimed,
			Notes:          notes,
		})
	}

	return report, nil
}

// RenderCleanupRunReport 把清理执行结果渲染成文本摘要。
func RenderCleanupRunReport(report CleanupRunReport) string {
	var b strings.Builder
	b.WriteString("磁盘清理执行结果\n")
	b.WriteString(fmt.Sprintf("时间: %s\n", report.GeneratedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("系统: %s\n", report.HostOS))
	b.WriteString(fmt.Sprintf("Home: %s\n", report.HomeDir))
	b.WriteString(fmt.Sprintf("总回收: %s\n", formatHumanBytes(report.TotalReclaimedBytes)))

	for _, item := range report.Items {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("- %s\n", item.Label))
		b.WriteString(fmt.Sprintf("  path: %s\n", item.Path))
		b.WriteString(fmt.Sprintf("  before: %s\n", formatHumanBytes(item.BeforeBytes)))
		b.WriteString(fmt.Sprintf("  after: %s\n", formatHumanBytes(item.AfterBytes)))
		b.WriteString(fmt.Sprintf("  reclaimed: %s\n", formatHumanBytes(item.ReclaimedBytes)))
		for _, note := range item.Notes {
			b.WriteString("  note: " + note + "\n")
		}
	}

	return b.String()
}

func defaultCleanupRunActions(homeDir string) []cleanupRunAction {
	return []cleanupRunAction{
		{
			Key:   "1panel-upgrade",
			Label: "1Panel upgrade cache",
			Path:  "/opt/1panel/tmp/upgrade",
			Run: func(_ context.Context) ([]string, error) {
				return nil, removeDirContents("/opt/1panel/tmp/upgrade")
			},
		},
		{
			Key:   "go-mod-cache",
			Label: "Go module cache",
			Path:  filepath.Join(homeDir, "go", "pkg", "mod"),
			Run: func(_ context.Context) ([]string, error) {
				return nil, removeDirContents(filepath.Join(homeDir, "go", "pkg", "mod"))
			},
		},
		{
			Key:   "nuget-packages",
			Label: "NuGet packages",
			Path:  filepath.Join(homeDir, ".nuget", "packages"),
			Run: func(_ context.Context) ([]string, error) {
				return nil, removeDirContents(filepath.Join(homeDir, ".nuget", "packages"))
			},
		},
		{
			Key:   "go-build-cache",
			Label: "Go build cache",
			Path:  filepath.Join(homeDir, ".cache", "go-build"),
			Run: func(_ context.Context) ([]string, error) {
				return nil, removeDirContents(filepath.Join(homeDir, ".cache", "go-build"))
			},
		},
		{
			Key:   "pip-cache",
			Label: "pip cache",
			Path:  filepath.Join(homeDir, ".cache", "pip"),
			Run: func(_ context.Context) ([]string, error) {
				return nil, removeDirContents(filepath.Join(homeDir, ".cache", "pip"))
			},
		},
		{
			Key:   "snap-disabled-revisions",
			Label: "Snap disabled revisions",
			Path:  "/var/lib/snapd",
			Run: func(ctx context.Context) ([]string, error) {
				items := loadDisabledSnapRevisions(ctx)
				if len(items) == 0 {
					return []string{"没有检测到 disabled revisions。"}, nil
				}

				notes := make([]string, 0, len(items))
				for _, item := range items {
					cmd := exec.CommandContext(ctx, "snap", "remove", item.Name, "--revision="+item.Revision)
					output, err := cmd.CombinedOutput()
					if err != nil {
						return notes, fmt.Errorf("snap remove %s@%s: %w: %s", item.Name, item.Revision, err, strings.TrimSpace(string(output)))
					}
					notes = append(notes, fmt.Sprintf("removed %s@%s", item.Name, item.Revision))
				}
				return notes, nil
			},
		},
	}
}

func normalizeCleanupTargets(values []string) []string {
	set := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			key := strings.TrimSpace(part)
			if key == "" {
				continue
			}
			if _, ok := set[key]; ok {
				continue
			}
			set[key] = struct{}{}
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func pathUsageIfExists(ctx context.Context, path string) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return diskUsageBytes(ctx, path, info)
}

func removeDirContents(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
