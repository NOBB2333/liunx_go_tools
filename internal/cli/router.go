package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wujunwei928/golangtools/internal/archive"
	"github.com/wujunwei928/golangtools/internal/cleanup"
	"github.com/wujunwei928/golangtools/internal/document"
	"github.com/wujunwei928/golangtools/internal/filestore"
	"github.com/wujunwei928/golangtools/internal/filesystem"
	"github.com/wujunwei928/golangtools/internal/healthserver"
	"github.com/wujunwei928/golangtools/internal/mockdata"
	"github.com/wujunwei928/golangtools/internal/networkscan"
	processutil "github.com/wujunwei928/golangtools/internal/process"
	runlog "github.com/wujunwei928/golangtools/internal/runtime"
)

var activeLogger *runlog.Logger

func Main() {
	args := os.Args[1:]
	logger, loggerErr := runlog.New(args)
	if loggerErr == nil {
		activeLogger = logger
		defer func() {
			logger.Close()
			activeLogger = nil
		}()
	}
	err := Run(args)
	if logger != nil {
		logger.Complete(err)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func Run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	cfg := filestore.LoadFromEnv()
	switch args[0] {
	case "archive":
		return runArchive(args[1:])
	case "document":
		return runDocument(args[1:])
	case "filestore":
		return runFile(args[1:], cfg)
	case "process":
		return runMonitor(args[1:])
	case "network":
		return runNetwork(args[1:])
	case "cleanup":
		return runCleanup(args[1:])
	case "mockdata":
		return runMockDataCommand(args[1:])
	case "health":
		return runHealth(args[1:])
	case "filesystem":
		return runFilesystem(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func runFilesystem(args []string) error {
	if len(args) == 0 {
		return errors.New("filesystem subcommand is required: probe, scan, index, serve, or status")
	}

	switch args[0] {
	case "probe":
		fs := flag.NewFlagSet("filesystem probe", flag.ContinueOnError)
		path := fs.String("path", "", "directory or mount path")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*path) == "" {
			return errors.New("-path is required")
		}
		probe, err := filesystem.ProbeDisk(*path)
		if err != nil {
			return err
		}
		return printJSON(probe)

	case "scan":
		fs := flag.NewFlagSet("filesystem scan", flag.ContinueOnError)
		root := fs.String("root", "", "directory or mount path")
		output := fs.String("output", "", "snapshot output directory")
		workers := fs.Int("workers", 0, "scanner worker count; 0 selects the platform default")
		metadata := fs.String("metadata", "basic", "metadata mode: basic or tree")
		backend := fs.String("backend", "auto", "scanner backend: auto, windows-mft, windows-native, or portable")
		buildIndex := fs.Bool("build-index", true, "build query.db after scanning")
		batchSize := fs.Int("batch-size", 10000, "SQLite index batch size")
		progressInterval := fs.Duration("progress-interval", 2*time.Second, "progress report interval")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*root) == "" {
			return errors.New("-root is required")
		}
		outputDir := *output
		if outputDir == "" {
			outputDir = filepath.Join("go-tool-result", "filesystem-snapshots", time.Now().Format("20060102-150405"))
		}
		if activeLogger != nil {
			_ = os.MkdirAll(outputDir, 0755)
			_ = activeLogger.Attach(filepath.Join(outputDir, "run.log"))
			activeLogger.Phase("config", "准备文件系统扫描", map[string]any{"root": *root, "output_dir": outputDir, "workers": *workers, "metadata": *metadata, "backend": *backend})
		}
		ctx, cancel := signalContext()
		defer cancel()
		summary, err := filesystem.NewFastScanner().Scan(ctx, filesystem.FastScanOptions{
			Root:             *root,
			OutputDir:        outputDir,
			Workers:          *workers,
			Metadata:         filesystem.FastMetadataMode(strings.ToLower(strings.TrimSpace(*metadata))),
			Backend:          filesystem.FastScanBackend(strings.ToLower(strings.TrimSpace(*backend))),
			ProgressInterval: *progressInterval,
			Progress: func(progress filesystem.ScanProgress) {
				if activeLogger == nil {
					return
				}
				message := fmt.Sprintf("%s files=%d dirs=%d pending=%d errors=%d rate=%.0f/s elapsed=%s", progress.Message, progress.Files, progress.Directories, progress.PendingDirs, progress.Errors, progress.EntriesPerSecond, progress.Elapsed.Round(time.Second))
				activeLogger.Progress(progress.Phase, message, map[string]any{
					"files": progress.Files, "directories": progress.Directories,
					"completed_directories": progress.CompletedDirs, "pending_directories": progress.PendingDirs,
					"errors": progress.Errors, "logical_bytes": progress.LogicalBytes,
					"allocated_bytes": progress.AllocatedBytes, "entries_per_second": progress.EntriesPerSecond,
					"elapsed_ns": progress.Elapsed.Nanoseconds(),
				})
			},
		})
		if err != nil {
			return err
		}
		if err := printJSON(summary); err != nil {
			return err
		}
		if *buildIndex {
			built, err := filesystem.NewIndexBuilder().Build(ctx, filesystem.BuildOptions{
				SnapshotDir: outputDir, BatchSize: *batchSize, ProgressInterval: *progressInterval,
				Progress: func(progress filesystem.BuildProgress) {
					if activeLogger == nil {
						return
					}
					message := progress.Message
					if progress.Indeterminate {
						message = fmt.Sprintf("%s elapsed=%s", message, progress.Elapsed.Round(time.Second))
					} else {
						message = fmt.Sprintf("%s %d/%d %.1f%% elapsed=%s", message, progress.Current, progress.Total, progress.Percent, progress.Elapsed.Round(time.Second))
					}
					activeLogger.Progress(progress.Phase, message, map[string]any{
						"current": progress.Current, "total": progress.Total, "percent": progress.Percent,
						"elapsed_ns": progress.Elapsed.Nanoseconds(), "indeterminate": progress.Indeterminate,
					})
				},
			})
			if err != nil {
				return err
			}
			fmt.Println("query index:")
			if err := printJSON(built); err != nil {
				return err
			}
		}
		return nil

	case "index":
		fs := flag.NewFlagSet("filesystem index", flag.ContinueOnError)
		snapshot := fs.String("snapshot", "", "snapshot directory")
		database := fs.String("database", "", "query database path")
		batchSize := fs.Int("batch-size", 10000, "SQLite index batch size")
		progressInterval := fs.Duration("progress-interval", 2*time.Second, "progress report interval")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*snapshot) == "" {
			return errors.New("-snapshot is required")
		}
		if activeLogger != nil {
			activeLogger.Phase("config", "准备构建查询索引", map[string]any{"snapshot_dir": *snapshot, "database": *database})
		}
		built, err := filesystem.NewIndexBuilder().Build(context.Background(), filesystem.BuildOptions{
			SnapshotDir: *snapshot, DatabasePath: *database, BatchSize: *batchSize, ProgressInterval: *progressInterval,
			Progress: func(progress filesystem.BuildProgress) {
				if activeLogger == nil {
					return
				}
				message := progress.Message
				if progress.Indeterminate {
					message = fmt.Sprintf("%s elapsed=%s", message, progress.Elapsed.Round(time.Second))
				} else {
					message = fmt.Sprintf("%s %d/%d %.1f%% elapsed=%s", message, progress.Current, progress.Total, progress.Percent, progress.Elapsed.Round(time.Second))
				}
				activeLogger.Progress(progress.Phase, message, map[string]any{
					"current": progress.Current, "total": progress.Total, "percent": progress.Percent,
					"elapsed_ns": progress.Elapsed.Nanoseconds(), "indeterminate": progress.Indeterminate,
				})
			},
		})
		if err != nil {
			return err
		}
		return printJSON(built)

	case "serve":
		fs := flag.NewFlagSet("filesystem serve", flag.ContinueOnError)
		snapshot := fs.String("snapshot", "", "snapshot directory")
		listen := fs.String("listen", "127.0.0.1:8080", "HTTP listen address")
		token := fs.String("token", "", "bearer token; required for non-loopback listen addresses")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*snapshot) == "" {
			return errors.New("-snapshot is required")
		}
		serveToken := strings.TrimSpace(*token)
		if !isLoopbackListen(*listen) && serveToken == "" {
			generated, err := randomToken()
			if err != nil {
				return err
			}
			serveToken = generated
			fmt.Println("generated token:", serveToken)
		}
		server, err := filesystem.OpenServer(filesystem.ServerOptions{SnapshotDir: *snapshot, Token: serveToken})
		if err != nil {
			return err
		}
		defer server.Close()
		viewerURL := "http://" + *listen + "/"
		if serveToken != "" && !isLoopbackListen(*listen) {
			viewerURL += "?token=" + url.QueryEscape(serveToken)
		}
		fmt.Printf("filesystem viewer: %s\n", viewerURL)
		ctx, cancel := signalContext()
		defer cancel()
		return server.Serve(ctx, *listen)

	case "status":
		fs := flag.NewFlagSet("filesystem status", flag.ContinueOnError)
		snapshot := fs.String("snapshot", "", "snapshot directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*snapshot) == "" {
			return errors.New("-snapshot is required")
		}
		manifest, err := filesystem.ReadManifest(*snapshot)
		if err != nil {
			return err
		}
		status := map[string]any{"manifest": manifest, "query_index": false}
		if _, err := os.Stat(filepath.Join(*snapshot, "query.db")); err == nil {
			status["query_index"] = true
		}
		return printJSON(status)
	default:
		return fmt.Errorf("unknown filesystem subcommand: %s", args[0])
	}
}

func printJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func randomToken() (string, error) {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", data), nil
}

func isLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return strings.HasPrefix(address, "localhost:") || strings.HasPrefix(address, "127.0.0.1:") || strings.HasPrefix(address, "[::1]:")
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func runWordRead(args []string) error {
	fs := flag.NewFlagSet("word-read", flag.ContinueOnError)
	path := fs.String("path", "", "word file path (.docx/.docm)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*path) == "" {
		return errors.New("-path is required")
	}

	ctx, cancel := signalContext()
	defer cancel()
	reader := document.NewWordReader()
	return reader.ReadToWriter(ctx, *path, os.Stdout)
}

func runBase64(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	svc := archive.NewBase64ArchiveService()
	switch args[0] {
	case "base64-encode", "b64-encode":
		fs := flag.NewFlagSet("base64-encode", flag.ContinueOnError)
		path := fs.String("path", "", "source file or directory path")
		output := fs.String("output", "", "encoded text output file")
		resultDir := fs.String("result-dir", "go-tool-result", "result directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*path) == "" {
			return errors.New("-path is required")
		}

		outputPath, err := resolveBase64EncodeOutput(*path, *resultDir, *output)
		if err != nil {
			return err
		}

		ctx, cancel := signalContext()
		defer cancel()
		meta, err := svc.EncodePath(ctx, *path, outputPath)
		if err != nil {
			return err
		}
		fmt.Printf("编码完成:\n  kind:   %s\n  source: %s\n  output: %s\n", meta.Kind, *path, outputPath)
		return nil

	case "base64-decode", "b64-decode":
		fs := flag.NewFlagSet("base64-decode", flag.ContinueOnError)
		input := fs.String("input", "", "encoded text file path")
		output := fs.String("output", "", "restore output path")
		resultDir := fs.String("result-dir", "go-tool-result", "result directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*input) == "" {
			return errors.New("-input is required")
		}

		meta, err := svc.InspectEncodedFile(*input)
		if err != nil {
			return err
		}
		outputPath, err := resolveBase64DecodeOutput(meta, *resultDir, *output)
		if err != nil {
			return err
		}

		ctx, cancel := signalContext()
		defer cancel()
		decodedMeta, err := svc.DecodeFile(ctx, *input, outputPath)
		if err != nil {
			return err
		}
		fmt.Printf("还原完成:\n  kind:   %s\n  input:  %s\n  output: %s\n", decodedMeta.Kind, *input, outputPath)
		return nil

	default:
		return fmt.Errorf("unknown base64 subcommand: %s", args[0])
	}
}

func runFile(args []string, cfg filestore.AppConfig) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	sub := args[0]
	switch sub {
	case "query":
		fs := flag.NewFlagSet("file query", flag.ContinueOnError)
		name := fs.String("name", "", "file name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("-name is required")
		}
		return withFileService(cfg, func(svc *filestore.FileService) error {
			cmd, err := svc.QueryCopyCommand(context.Background(), *name)
			if err != nil {
				return err
			}
			fmt.Println(cmd)
			return nil
		})

	case "upload-local":
		fs := flag.NewFlagSet("file upload-local", flag.ContinueOnError)
		path := fs.String("path", "", "local file path")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" {
			return errors.New("-path is required")
		}
		return withFileService(cfg, func(svc *filestore.FileService) error {
			resp, err := svc.UploadLocalFile(context.Background(), *path)
			if err != nil {
				return err
			}
			fmt.Println(resp)
			return nil
		})

	case "upload-server":
		fs := flag.NewFlagSet("file upload-server", flag.ContinueOnError)
		path := fs.String("path", "", "source file path")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" {
			return errors.New("-path is required")
		}
		return withFileService(cfg, func(svc *filestore.FileService) error {
			downloadURL, err := svc.UploadServerFile(context.Background(), *path)
			if err != nil {
				return err
			}
			fmt.Println(downloadURL)
			return nil
		})

	case "download":
		fs := flag.NewFlagSet("file download", flag.ContinueOnError)
		name := fs.String("name", "", "file name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("-name is required")
		}
		return withFileService(cfg, func(svc *filestore.FileService) error {
			url, err := svc.DownloadURL(context.Background(), *name)
			if err != nil {
				return err
			}
			fmt.Println(url)
			return nil
		})

	default:
		return fmt.Errorf("unknown file subcommand: %s", sub)
	}
}

func runAPI(args []string) error {
	fs := flag.NewFlagSet("health serve", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "HTTP listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return healthserver.Run(*listen)
}

func runMonitor(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	m := processutil.NewProcessMonitor()
	sub := args[0]
	switch sub {
	case "usage":
		fs := flag.NewFlagSet("monitor usage", flag.ContinueOnError)
		target := fs.String("target", "", "pid or process name")
		output := fs.String("output", "", "csv output path")
		resultDir := fs.String("result-dir", "go-tool-result", "result directory")
		interval := fs.Duration("interval", time.Second, "sampling interval")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *target == "" {
			return errors.New("-target is required")
		}

		pid, err := m.ResolvePID(*target)
		if err != nil {
			return err
		}
		outputPath, err := resolveResultPath(*resultDir, *output, "system_monitor.csv")
		if err != nil {
			return err
		}
		ctx, cancel := signalContext()
		defer cancel()
		return m.MonitorUsage(ctx, pid, outputPath, *interval)

	case "kill":
		fs := flag.NewFlagSet("monitor kill", flag.ContinueOnError)
		target := fs.String("target", "", "pid or process name")
		logPath := fs.String("log", "", "log output file")
		resultDir := fs.String("result-dir", "go-tool-result", "result directory")
		interval := fs.Duration("interval", 3*time.Second, "kill loop interval")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *target == "" {
			return errors.New("-target is required")
		}

		ctx, cancel := signalContext()
		defer cancel()

		resolvedLogPath, err := resolveResultPath(*resultDir, *logPath, "monitor.log")
		if err != nil {
			return err
		}
		logFile, err := os.OpenFile(resolvedLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		defer logFile.Close()

		logger := log.New(io.MultiWriter(os.Stdout, logFile), "", log.LstdFlags)
		return m.KillLoop(ctx, *target, *interval, logger)

	case "guard":
		fs := flag.NewFlagSet("monitor guard", flag.ContinueOnError)
		name := fs.String("name", "", "process name to guard")
		cmd := fs.String("cmd", "", "command used to start the process; defaults to name")
		timeFlag := fs.Duration("time", 0, "guard check interval")
		interval := fs.Duration("interval", 0, "guard check interval")
		logPath := fs.String("log", "", "log output file")
		resultDir := fs.String("result-dir", "go-tool-result", "result directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*name) == "" {
			return errors.New("-name is required")
		}

		resolvedInterval := *interval
		if *timeFlag > 0 {
			resolvedInterval = *timeFlag
		}
		if resolvedInterval <= 0 {
			resolvedInterval = 3 * time.Second
		}

		ctx, cancel := signalContext()
		defer cancel()

		resolvedLogPath, err := resolveResultPath(*resultDir, *logPath, "guard.log")
		if err != nil {
			return err
		}
		logFile, err := os.OpenFile(resolvedLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		defer logFile.Close()

		logger := log.New(io.MultiWriter(os.Stdout, logFile), "", log.LstdFlags)
		return m.GuardLoop(ctx, *name, *cmd, resolvedInterval, logger)
	default:
		return fmt.Errorf("unknown monitor subcommand: %s", sub)
	}
}

func runDocExtract(args []string) error {
	fs := flag.NewFlagSet("doc-extract", flag.ContinueOnError)
	output := fs.String("output", "go-tool-result/doc_extracted", "输出目录")
	procs := fs.String("process", "", "要扫描的进程名，逗号分隔（默认: wps,wpsoffice,et,wpp,word,winword,excel,powerpnt）")
	types := fs.String("type", "", "文件类型预设，逗号分隔: word / excel / ppt / pdf / all（默认 all）")
	ext := fs.String("ext", "", "自定义追加后缀，逗号分隔，如 .txt,.pdf")
	debug := fs.Bool("debug", false, "输出诊断信息")
	b64 := fs.Bool("b64", false, "输出为 base64 编码的 .b64.txt，还原时用 base64-decode")
	mode := fs.String("mode", "copy", "输出模式: copy=原始文件 / b64=base64文本 / pdf=伪装PDF / bin=二进制 / mem=内存扫描b64 / all=全部输出")
	if err := fs.Parse(args); err != nil {
		return err
	}

	extractor := document.NewDocExtractor()
	if strings.TrimSpace(*procs) != "" {
		extractor.ProcessNames = splitTrim(*procs)
	}
	if strings.TrimSpace(*types) != "" {
		extractor.SetTypePreset(splitTrim(*types)...)
	}
	if strings.TrimSpace(*ext) != "" {
		extractor.AddExtensions(splitTrim(*ext)...)
	}
	if *debug {
		extractor.Debug = true
	}
	// -b64 是 -mode b64 的简写，保持向后兼容
	if *b64 {
		*mode = "b64"
	}

	modes := map[string]bool{}
	if *mode == "all" {
		for _, m := range []string{"copy", "b64", "pdf", "bin", "mem"} {
			modes[m] = true
		}
	} else {
		modes[*mode] = true
	}

	outputDir := *output

	// ── 磁盘文件提取（copy / b64 / pdf / bin） ──
	diskModes := []string{}
	for _, m := range []string{"copy", "b64", "pdf", "bin"} {
		if modes[m] {
			diskModes = append(diskModes, m)
		}
	}

	if len(diskModes) > 0 {
		extractor.Base64Mode = false
		results, err := extractor.Extract(outputDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "磁盘提取失败: %v\n", err)
		} else {
			for _, r := range results {
				if r.Err != nil {
					fmt.Printf("失败: %s\n  -> %v\n", r.SourcePath, r.Err)
					continue
				}
				// 按各个模式另存
				for _, m := range diskModes {
					var dest string
					var saveErr error
					base := r.SourcePath
					switch m {
					case "copy":
						dest = r.DestPath // Extract 已经复制好了
					case "b64":
						dest = uniqueDestPkg(outputDir, filepath.Base(base)+".b64.txt")
						saveErr = document.SaveFileAsBase64(base, dest)
					case "pdf":
						dest = uniqueDestPkg(outputDir, strings.TrimSuffix(filepath.Base(base), filepath.Ext(base))+".pdf")
						saveErr = document.CopyDocFile(base, dest)
					case "bin":
						dest = uniqueDestPkg(outputDir, filepath.Base(base)+".bin")
						saveErr = document.CopyDocFile(base, dest)
					}
					if m == "copy" && saveErr == nil {
						fmt.Printf("[copy] %s\n  -> %s\n", base, dest)
					} else if saveErr != nil {
						fmt.Printf("[%s] 失败: %s -> %v\n", m, base, saveErr)
					} else {
						fmt.Printf("[%s] %s\n  -> %s\n", m, base, dest)
					}
				}
			}
		}
	}

	// ── 内存扫描（mem） ──
	if modes["mem"] {
		fmt.Println("\n[mem] 开始扫描进程内存...")
		pids, err := extractor.FindTargetPIDs()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[mem] 查找进程失败: %v\n", err)
		} else {
			memDir := filepath.Join(outputDir, "mem")
			total := 0
			for _, pid := range pids {
				blobs, err := document.ScanMemoryForDocs(pid, extractor.Extensions)
				if err != nil {
					if *debug {
						fmt.Printf("[mem] pid=%d 扫描失败: %v\n", pid, err)
					}
					continue
				}
				if len(blobs) == 0 {
					continue
				}
				if err := document.SaveMemBlobsAsB64(blobs, memDir); err != nil {
					fmt.Fprintf(os.Stderr, "[mem] 保存失败: %v\n", err)
				} else {
					fmt.Printf("[mem] pid=%d 找到 %d 个 ZIP blob -> %s\n", pid, len(blobs), memDir)
					total += len(blobs)
				}
			}
			if total == 0 {
				fmt.Println("[mem] 未在内存中找到 ZIP 结构（文件可能不在内存堆中）")
			}
		}
	}

	return nil
}

func uniqueDestPkg(dir, name string) string {
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return dest
	}
	ext2 := filepath.Ext(dest)
	base2 := strings.TrimSuffix(dest, ext2)
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s_%d%s", base2, i, ext2)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return dest
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			result = append(result, v)
		}
	}
	return result
}

func runPing(args []string) error {
	fs := flag.NewFlagSet("ping", flag.ContinueOnError)
	rangePattern := fs.String("range", "", "ip range pattern, e.g. 10.0.[1-2].[1-10]")
	configFile := fs.String("config", "", "config file containing one pattern")
	timeout := fs.Duration("timeout", time.Second, "single ping timeout")
	workers := fs.Int("workers", 256, "worker count")
	maxTargets := fs.Int("max-targets", 1000000, "maximum expanded IP targets")
	privileged := fs.Bool("privileged", true, "use privileged icmp")
	resultDir := fs.String("result-dir", "go-tool-result", "result directory")
	successFile := fs.String("success", "", "success output file")
	failFile := fs.String("fail", "", "fail output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *rangePattern == "" && *configFile == "" {
		return errors.New("-range or -config is required")
	}
	successPath, err := resolveResultPath(*resultDir, *successFile, "success.txt")
	if err != nil {
		return err
	}
	failPath, err := resolveResultPath(*resultDir, *failFile, "fail.txt")
	if err != nil {
		return err
	}

	ctx, cancel := signalContext()
	defer cancel()
	return networkscan.RunPing(ctx, networkscan.PingConfig{
		Range:       *rangePattern,
		ConfigFile:  *configFile,
		Timeout:     *timeout,
		Workers:     *workers,
		MaxTargets:  *maxTargets,
		Privileged:  *privileged,
		SuccessFile: successPath,
		FailFile:    failPath,
	})
}

func runCleanupScan(args []string) error {
	fs := flag.NewFlagSet("cleanup-scan", flag.ContinueOnError)
	format := fs.String("format", "text", "output format: text or json")
	output := fs.String("output", "", "write report to file instead of stdout")
	homeDir := fs.String("home", "", "override home directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := signalContext()
	defer cancel()

	report, err := cleanup.NewCleanupScanner().Scan(ctx, cleanup.CleanupScanOptions{
		HomeDir: *homeDir,
	})
	if err != nil {
		return err
	}

	var content []byte
	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "", "text":
		content = []byte(cleanup.RenderCleanupScanReport(report))
	case "json":
		content, err = json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported -format: %s", *format)
	}

	if strings.TrimSpace(*output) == "" {
		fmt.Print(string(content))
		if len(content) == 0 || content[len(content)-1] != '\n' {
			fmt.Println()
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(*output, content, 0644); err != nil {
		return err
	}
	fmt.Printf("报告已输出: %s\n", *output)
	return nil
}

func runCleanupRun(args []string) error {
	fs := flag.NewFlagSet("cleanup-run", flag.ContinueOnError)
	targets := fs.String("target", "", "cleanup targets, comma-separated")
	format := fs.String("format", "text", "output format: text or json")
	output := fs.String("output", "", "write report to file instead of stdout")
	homeDir := fs.String("home", "", "override home directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*targets) == "" {
		return errors.New("-target is required")
	}

	ctx, cancel := signalContext()
	defer cancel()

	report, err := cleanup.NewCleanupRunner().Run(ctx, cleanup.CleanupRunOptions{
		HomeDir: *homeDir,
		Targets: []string{*targets},
	})
	if err != nil {
		return err
	}

	var content []byte
	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "", "text":
		content = []byte(cleanup.RenderCleanupRunReport(report))
	case "json":
		content, err = json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported -format: %s", *format)
	}

	if strings.TrimSpace(*output) == "" {
		fmt.Print(string(content))
		if len(content) == 0 || content[len(content)-1] != '\n' {
			fmt.Println()
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(*output, content, 0644); err != nil {
		return err
	}
	fmt.Printf("报告已输出: %s\n", *output)
	return nil
}

func withFileService(cfg filestore.AppConfig, fn func(*filestore.FileService) error) error {
	repo, err := filestore.NewMySQLRepository(cfg.DBDSN)
	if err != nil {
		return err
	}
	defer repo.Close()

	svc := filestore.NewFileService(repo, cfg)
	return fn(svc)
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func resolveResultPath(resultDir, explicitPath, defaultName string) (string, error) {
	explicitPath = strings.TrimSpace(explicitPath)
	if explicitPath != "" {
		dir := filepath.Dir(explicitPath)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "", err
			}
		}
		return explicitPath, nil
	}

	resultDir = strings.TrimSpace(resultDir)
	if resultDir == "" {
		resultDir = "go-tool-result"
	}
	if err := os.MkdirAll(resultDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(resultDir, defaultName), nil
}

func printUsage() {
	fmt.Println("golangtools")
	fmt.Println("usage: golangtools <module> <command> [options]")
	fmt.Println("modules:")
	fmt.Println("  filesystem probe -path <mount-or-directory>")
	fmt.Println("  filesystem scan -root <source-directory> [-output snapshot-dir] [-workers N] [-metadata basic|tree] [-backend auto|windows-mft|windows-native|portable] [-build-index=false] [-progress-interval 2s]")
	fmt.Println("  filesystem index -snapshot <scan-output-snapshot-dir> [-database query.db] [-batch-size 10000] [-progress-interval 2s]")
	fmt.Println("  filesystem serve -snapshot <scan-output-snapshot-dir> [-listen 127.0.0.1:8080] [-token token]")
	fmt.Println("  filesystem status -snapshot <snapshot-dir>")
	fmt.Println("  archive encode -path <file-or-directory> [-output path]")
	fmt.Println("  archive decode -input <encoded-file> [-output path]")
	fmt.Println("  document read -path <file.docx>")
	fmt.Println("  document extract [-output directory] [-type word,excel,ppt,pdf,all] [-mode copy|b64|pdf|bin|mem|all]")
	fmt.Println("  filestore query -name <name>")
	fmt.Println("  filestore upload-local|upload-server -path <path>")
	fmt.Println("  filestore download -name <name>")
	fmt.Println("  process usage -target <pid-or-name> [-output metrics.csv]")
	fmt.Println("  process kill -target <pid-or-name> [-interval 3s]")
	fmt.Println("  process guard -name <name> [-cmd command] [-interval 3s]")
	fmt.Println("  network ping -range '<pattern>' | -config <file>")
	fmt.Println("  cleanup scan [-format text|json] [-output path] [-home path]")
	fmt.Println("  cleanup run -target <comma-separated-targets> [-format text|json]")
	fmt.Println("  mockdata generate -dsn <dsn> -tables <t1,t2> [-rows 10] [-dry-run]")
	fmt.Println("  health serve [-listen 127.0.0.1:8080]")
}

func resolveBase64EncodeOutput(sourcePath, resultDir, explicitPath string) (string, error) {
	sourceName := filepath.Base(filepath.Clean(sourcePath))
	if sourceName == "." || sourceName == string(filepath.Separator) || sourceName == "" {
		sourceName = "payload"
	}
	defaultName := sourceName + ".b64.txt"

	explicitPath = strings.TrimSpace(explicitPath)
	if explicitPath != "" {
		info, err := os.Stat(explicitPath)
		if err == nil && info.IsDir() {
			return filepath.Join(explicitPath, defaultName), nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return resolveResultPath(resultDir, explicitPath, "")
	}

	return resolveResultPath(resultDir, "", defaultName)
}

func runMockData(args []string) error {
	fs := flag.NewFlagSet("mock-data", flag.ContinueOnError)
	dsn := fs.String("dsn", "", "database DSN (e.g. root:pass@tcp(host:3306)/dbname)")
	tables := fs.String("tables", "", "comma-separated table names")
	rows := fs.Int("rows", 10, "number of rows to insert per table")
	dryRun := fs.Bool("dry-run", false, "print SQL without executing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("-dsn is required")
	}
	if strings.TrimSpace(*tables) == "" {
		return errors.New("-tables is required")
	}
	tableList := strings.Split(*tables, ",")
	for i, t := range tableList {
		tableList[i] = strings.TrimSpace(t)
	}
	return mockdata.RunMockData(*dsn, tableList, *rows, *dryRun)
}

func resolveBase64DecodeOutput(meta archive.Base64ArchiveMetadata, resultDir, explicitPath string) (string, error) {
	explicitPath = strings.TrimSpace(explicitPath)
	if explicitPath == "" {
		return resolveResultPath(resultDir, "", meta.Name)
	}

	info, err := os.Stat(explicitPath)
	if err == nil && info.IsDir() {
		return filepath.Join(explicitPath, meta.Name), nil
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}

	if meta.Kind == "dir" {
		return explicitPath, nil
	}
	return explicitPath, nil
}
