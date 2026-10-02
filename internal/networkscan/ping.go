package networkscan

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-ping/ping"
)

var rangePattern = regexp.MustCompile(`^\[(\d+)-(\d+)\]$`)

// PingConfig 表示 Ping 功能的配置。
type PingConfig struct {
	ConfigFile  string
	Range       string
	Timeout     time.Duration
	Workers     int
	MaxTargets  int
	Privileged  bool
	SuccessFile string
	FailFile    string
}

// Normalize 补齐默认配置。
func (c *PingConfig) Normalize() {
	if c.Timeout <= 0 {
		c.Timeout = time.Second
	}
	if c.Workers <= 0 {
		c.Workers = 256
	}
	if c.Workers > 4096 {
		c.Workers = 4096
	}
	if c.MaxTargets <= 0 {
		c.MaxTargets = 1000000
	}
	if c.SuccessFile == "" {
		c.SuccessFile = "Ping-success.txt"
	}
	if c.FailFile == "" {
		c.FailFile = "Ping-fail.txt"
	}
}

// RunPing 执行并发 Ping，并写入成功和失败结果。
func RunPing(ctx context.Context, cfg PingConfig) error {
	cfg.Normalize()

	ips, err := parsePingSources(cfg)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return fmt.Errorf("no ip targets generated")
	}
	if cfg.Workers > len(ips) {
		cfg.Workers = len(ips)
	}

	successFile, err := os.Create(cfg.SuccessFile)
	if err != nil {
		return err
	}
	defer successFile.Close()

	failFile, err := os.Create(cfg.FailFile)
	if err != nil {
		return err
	}
	defer failFile.Close()

	successWriter := bufio.NewWriter(successFile)
	failWriter := bufio.NewWriter(failFile)
	defer successWriter.Flush()
	defer failWriter.Flush()

	ipCh := make(chan string, cfg.Workers*2)
	var wg sync.WaitGroup
	var lock sync.Mutex
	var doneCount int64
	total := int64(len(ips))

	fmt.Printf("本次共需检测 %d 个地址，工作协程 %d 个\n", total, cfg.Workers)

	writeLine := func(writer *bufio.Writer, line string) {
		lock.Lock()
		defer lock.Unlock()
		_, _ = writer.WriteString(line + "\n")
	}

	printProgress := func() {
		completed := atomic.AddInt64(&doneCount, 1)
		currentBucket := completed * 20 / total
		previousBucket := (completed - 1) * 20 / total
		if completed == total || currentBucket > previousBucket {
			percent := completed * 100 / total
			fmt.Printf("进度 %d%% (%d/%d)\n", percent, completed, total)
		}
	}

	worker := func() {
		defer wg.Done()
		for ip := range ipCh {
			select {
			case <-ctx.Done():
				return
			default:
			}

			stats, mode, err := pingOne(ip, cfg.Timeout, cfg.Privileged)
			if err != nil {
				writeLine(failWriter, fmt.Sprintf("%s,error,%v", ip, err))
			} else if stats.PacketsRecv > 0 {
				writeLine(successWriter, fmt.Sprintf("%s,success,%v,%s", ip, stats.AvgRtt, mode))
			} else {
				writeLine(failWriter, fmt.Sprintf("%s,fail,packet_loss=%.2f,%s", ip, stats.PacketLoss, mode))
			}

			printProgress()
		}
	}

	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go worker()
	}

sendLoop:
	for _, ip := range ips {
		select {
		case <-ctx.Done():
			break sendLoop
		case ipCh <- ip:
		}
	}

	close(ipCh)
	wg.Wait()
	return nil
}

func parsePingSources(cfg PingConfig) ([]string, error) {
	var pattern string
	if strings.TrimSpace(cfg.ConfigFile) != "" {
		data, err := os.ReadFile(cfg.ConfigFile)
		if err != nil {
			return nil, err
		}
		pattern = strings.TrimSpace(string(data))
	} else {
		pattern = strings.TrimSpace(cfg.Range)
	}

	if pattern == "" {
		return nil, fmt.Errorf("ip range pattern is required")
	}

	parts := strings.Split(pattern, ".")
	if len(parts) != 4 {
		return nil, fmt.Errorf("invalid ip pattern: %s", pattern)
	}

	segments := make([][]int, 0, 4)
	for _, part := range parts {
		nums, err := parseOctetSegment(part)
		if err != nil {
			return nil, err
		}
		segments = append(segments, nums)
	}

	total := int64(1)
	for _, segment := range segments {
		total *= int64(len(segment))
		if total > int64(cfg.MaxTargets) {
			return nil, fmt.Errorf("target count %d exceeds limit %d", total, cfg.MaxTargets)
		}
	}
	ips := make([]string, 0, int(total))
	for _, a := range segments[0] {
		for _, b := range segments[1] {
			for _, c := range segments[2] {
				for _, d := range segments[3] {
					ips = append(ips, fmt.Sprintf("%d.%d.%d.%d", a, b, c, d))
				}
			}
		}
	}
	return ips, nil
}

func parseOctetSegment(part string) ([]int, error) {
	if m := rangePattern.FindStringSubmatch(part); len(m) == 3 {
		start, _ := strconv.Atoi(m[1])
		end, _ := strconv.Atoi(m[2])
		if start < 0 || end > 255 || start > end {
			return nil, fmt.Errorf("invalid range: %s", part)
		}
		result := make([]int, 0, end-start+1)
		for i := start; i <= end; i++ {
			result = append(result, i)
		}
		return result, nil
	}

	v, err := strconv.Atoi(part)
	if err != nil || v < 0 || v > 255 {
		return nil, fmt.Errorf("invalid ip segment: %s", part)
	}
	return []int{v}, nil
}

func pingOne(ip string, timeout time.Duration, preferPrivileged bool) (*ping.Statistics, string, error) {
	stats, err := runOnePing(ip, timeout, preferPrivileged)
	if err == nil {
		mode := "unprivileged"
		if preferPrivileged {
			mode = "privileged"
		}
		return stats, mode, nil
	}

	if preferPrivileged && isICMPPermissionError(err) {
		stats, retryErr := runOnePing(ip, timeout, false)
		if retryErr == nil {
			return stats, "unprivileged-fallback", nil
		}
		return nil, "", retryErr
	}

	return nil, "", err
}

func runOnePing(ip string, timeout time.Duration, privileged bool) (*ping.Statistics, error) {
	p, err := ping.NewPinger(ip)
	if err != nil {
		return nil, err
	}

	p.Count = 1
	p.Timeout = timeout
	p.SetPrivileged(privileged)
	if err := p.Run(); err != nil {
		return nil, err
	}
	return p.Statistics(), nil
}

func isICMPPermissionError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, os.ErrPermission) || strings.Contains(strings.ToLower(err.Error()), "operation not permitted")
}
