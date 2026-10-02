package processutil

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/process"
)

// ProcessMonitor 封装进程监控相关能力。
type ProcessMonitor struct {
	findPIDsByName func(string) ([]int32, error)
	startCommand   func(string) error
}

// NewProcessMonitor 创建进程监控器。
func NewProcessMonitor() *ProcessMonitor {
	return &ProcessMonitor{
		findPIDsByName: findPIDsByName,
		startCommand:   startCommand,
	}
}

// ResolvePID 优先按 PID 解析，失败后再按进程名模糊查找。
func (m *ProcessMonitor) ResolvePID(target string) (int32, error) {
	if pid, err := strconv.Atoi(target); err == nil {
		return int32(pid), nil
	}

	pids, err := m.findPIDsByName(target)
	if err != nil {
		return 0, err
	}
	if len(pids) == 0 {
		return 0, fmt.Errorf("no process found for target: %s", target)
	}
	return pids[0], nil
}

// MonitorUsage 按固定间隔采集系统和进程占用，并写入 CSV。
func (m *ProcessMonitor) MonitorUsage(ctx context.Context, pid int32, outputFile string, interval time.Duration) error {
	if outputFile == "" {
		outputFile = "system_monitor.csv"
	}
	if interval <= 0 {
		interval = time.Second
	}

	file, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"timestamp", "sys_cpu_percent", "sys_mem_percent", "proc_name", "proc_cpu_percent", "proc_mem_percent"}); err != nil {
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			writer.Flush()
			return writer.Error()
		case <-ticker.C:
			timestamp := time.Now().Format("2006-01-02 15:04:05")
			sysCPU, sysMem := readSystemUsage()
			name, procCPU, procMem := readProcessUsage(pid)

			if err := writer.Write([]string{
				timestamp,
				fmt.Sprintf("%.2f", sysCPU),
				fmt.Sprintf("%.2f", sysMem),
				name,
				fmt.Sprintf("%.2f", procCPU),
				fmt.Sprintf("%.2f", procMem),
			}); err != nil {
				return err
			}
			writer.Flush()
			if err := writer.Error(); err != nil {
				return err
			}
		}
	}
}

// KillLoop 持续发现并结束指定进程，直到上下文取消。
func (m *ProcessMonitor) KillLoop(ctx context.Context, target string, interval time.Duration, logger *log.Logger) error {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	if logger == nil {
		logger = log.New(os.Stdout, "", log.LstdFlags)
	}

	killOnce := func() {
		var pids []int32
		if pid, err := strconv.Atoi(target); err == nil {
			pids = append(pids, int32(pid))
		} else {
			matched, err := m.findPIDsByName(target)
			if err != nil {
				logger.Printf("查找进程失败: %v", err)
				return
			}
			pids = matched
		}

		for _, pid := range pids {
			p, err := process.NewProcess(pid)
			if err != nil {
				continue
			}
			name, _ := p.Name()
			if err := p.Kill(); err != nil {
				logger.Printf("结束进程失败 pid=%d name=%s err=%v", pid, name, err)
				continue
			}
			logger.Printf("已结束进程 pid=%d name=%s", pid, name)
		}
	}

	killOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			killOnce()
		}
	}
}

// GuardLoop 持续检查指定进程是否存活；如果不存在则按命令拉起。
func (m *ProcessMonitor) GuardLoop(ctx context.Context, name, command string, interval time.Duration, logger *log.Logger) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("process name is required")
	}

	command = strings.TrimSpace(command)
	if command == "" {
		command = name
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if logger == nil {
		logger = log.New(io.Discard, "", log.LstdFlags)
	}

	ensureRunning := func() {
		pids, err := m.findPIDsByName(name)
		if err != nil {
			logger.Printf("查找进程失败 name=%s err=%v", name, err)
			return
		}
		if len(pids) > 0 {
			return
		}

		if err := m.startCommand(command); err != nil {
			logger.Printf("拉起进程失败 name=%s cmd=%q err=%v", name, command, err)
			return
		}
		logger.Printf("进程不存在，已拉起 name=%s cmd=%q", name, command)
	}

	ensureRunning()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			ensureRunning()
		}
	}
}

func readSystemUsage() (float64, float64) {
	cpuPct, _ := cpu.Percent(0, false)
	memInfo, _ := mem.VirtualMemory()
	if len(cpuPct) == 0 {
		return 0, memInfo.UsedPercent
	}
	return cpuPct[0], memInfo.UsedPercent
}

func readProcessUsage(pid int32) (string, float64, float64) {
	p, err := process.NewProcess(pid)
	if err != nil {
		return "", -1, -1
	}

	name, _ := p.Name()
	cpuPct, _ := p.CPUPercent()
	memPct, _ := p.MemoryPercent()
	return name, cpuPct, float64(memPct)
}

func findPIDsByName(target string) ([]int32, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}

	target = strings.ToLower(strings.TrimSpace(target))
	var pids []int32
	for _, p := range procs {
		name, _ := p.Name()
		if strings.Contains(strings.ToLower(name), target) {
			pids = append(pids, p.Pid)
		}
	}
	return pids, nil
}

func startCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("start command is empty")
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}
