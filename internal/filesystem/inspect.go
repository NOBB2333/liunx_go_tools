package filesystem

import (
	"bufio"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// InspectOptions controls a bounded, read-only query against an existing GTI.
// Names are matched as case-insensitive substrings of the individual entry
// name; directory IDs are always interpreted as directory IDs, not file IDs.
type InspectOptions struct {
	Snapshot    string
	View        string
	Kind        string
	Query       string
	DirectoryID uint64
	MinBytes    uint64
	Limit       int
	Offset      int
}

type inspectPage[T any] struct {
	Items  []T    `json:"items"`
	Total  uint64 `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

type inspectOverview struct {
	TopDirectories inspectPage[listItem]           `json:"top_directories"`
	LargestFiles   inspectPage[listItem]           `json:"largest_files"`
	Extensions     inspectPage[fastIndexExtension] `json:"extensions"`
	Errors         inspectPage[fastErrorRecord]    `json:"errors"`
}

type inspectScope struct {
	DirectoryID uint64 `json:"directory_id"`
	Path        string `json:"path"`
}

// InspectResult is the versioned JSON contract for assistants and other tools.
type InspectResult struct {
	Schema       string           `json:"schema"`
	SnapshotFile string           `json:"snapshot_file"`
	Manifest     SnapshotManifest `json:"manifest"`
	View         string           `json:"view"`
	SizeMetric   string           `json:"size_metric"`
	Scope        inspectScope     `json:"scope"`
	Kind         string           `json:"kind,omitempty"`
	Query        string           `json:"query,omitempty"`
	MinBytes     uint64           `json:"min_bytes,omitempty"`
	Data         any              `json:"data"`
}

// InspectSnapshot reads snapshot metadata only. It does not read file contents,
// walk the source filesystem, start a server, or change the snapshot.
func InspectSnapshot(ctx context.Context, opt InspectOptions) (InspectResult, error) {
	if strings.TrimSpace(opt.Snapshot) == "" {
		return InspectResult{}, errors.New("snapshot is required")
	}
	if opt.View == "" {
		opt.View = "overview"
	}
	if opt.Kind == "" {
		opt.Kind = "all"
	}
	if opt.DirectoryID == 0 {
		opt.DirectoryID = 1
	}
	if opt.Limit == 0 {
		opt.Limit = 20
	}
	if opt.Limit < 1 || opt.Limit > 500 || opt.Offset < 0 || opt.Offset > 10000 || opt.Offset+opt.Limit > 10500 {
		return InspectResult{}, errors.New("limit must be 1..500 and offset must be 0..10000")
	}
	switch opt.View {
	case "overview", "children", "largest", "extensions", "errors":
	default:
		return InspectResult{}, fmt.Errorf("unsupported view: %s", opt.View)
	}
	switch opt.Kind {
	case "all", "files", "directories":
	default:
		return InspectResult{}, fmt.Errorf("unsupported kind: %s", opt.Kind)
	}
	if opt.View != "children" && opt.View != "largest" && (opt.Kind != "all" || strings.TrimSpace(opt.Query) != "" || opt.MinBytes != 0 || opt.DirectoryID != 1) {
		return InspectResult{}, errors.New("kind, query, min-bytes, and directory-id filters are only supported for children and largest")
	}
	if opt.View == "overview" && opt.Offset != 0 {
		return InspectResult{}, errors.New("overview does not support offset")
	}

	path, err := filepath.Abs(SnapshotPath(opt.Snapshot))
	if err != nil {
		return InspectResult{}, err
	}
	manifest, err := ReadManifest(path)
	if err != nil {
		return InspectResult{}, err
	}
	if manifest.Metadata == string(FastMetadataTree) && !manifest.AllocatedKnown && opt.View != "errors" {
		return InspectResult{}, errors.New("snapshot was scanned with tree-only metadata; rescan with -metadata basic for size analysis")
	}
	f, err := openFastIndex(path)
	if err != nil {
		return InspectResult{}, err
	}
	defer f.Close()
	dirIndex, ok := f.directoryIndex(opt.DirectoryID)
	if !ok {
		return InspectResult{}, fmt.Errorf("directory id %d: %w", opt.DirectoryID, os.ErrNotExist)
	}
	scopeItem, err := f.nodeItem(dirIndex)
	if err != nil {
		return InspectResult{}, err
	}
	if err := f.decoratePath(&scopeItem, manifest.Root); err != nil {
		return InspectResult{}, err
	}
	metric := "size_bytes"
	if manifest.AllocatedKnown {
		metric = "allocated_bytes"
	}
	result := InspectResult{
		Schema: "golangtools.filesystem.inspect.v1", SnapshotFile: path,
		Manifest: manifest, View: opt.View, SizeMetric: metric,
		Scope: inspectScope{DirectoryID: opt.DirectoryID, Path: scopeItem.Path},
	}
	switch opt.View {
	case "overview":
		dirOpt := opt
		dirOpt.Kind = "directories"
		directories, err := inspectItems(ctx, f, manifest, dirIndex, dirOpt, true)
		if err != nil {
			return InspectResult{}, err
		}
		fileOpt := opt
		fileOpt.Kind = "files"
		files, err := inspectItems(ctx, f, manifest, dirIndex, fileOpt, false)
		if err != nil {
			return InspectResult{}, err
		}
		exts := inspectExtensions(f, manifest.AllocatedKnown, opt.Limit, 0)
		errorsPage, err := inspectErrors(f, manifest.Errors, opt.Limit, 0)
		if err != nil {
			return InspectResult{}, err
		}
		result.Data = inspectOverview{TopDirectories: directories, LargestFiles: files, Extensions: exts, Errors: errorsPage}
	case "children", "largest":
		result.Kind, result.Query, result.MinBytes = opt.Kind, strings.TrimSpace(opt.Query), opt.MinBytes
		page, err := inspectItems(ctx, f, manifest, dirIndex, opt, opt.View == "children")
		if err != nil {
			return InspectResult{}, err
		}
		result.Data = page
	case "extensions":
		result.Data = inspectExtensions(f, manifest.AllocatedKnown, opt.Limit, opt.Offset)
	case "errors":
		page, err := inspectErrors(f, manifest.Errors, opt.Limit, opt.Offset)
		if err != nil {
			return InspectResult{}, err
		}
		result.Data = page
	}
	return result, nil
}

type inspectCandidate struct {
	index uint32
	node  fastIndexNode
}

// betterInspectCandidate orders results by size, directories before files on
// ties, and ID for determinism. The heap keeps the worst retained item first.
func betterInspectCandidate(a, b inspectCandidate, allocatedKnown bool) bool {
	left, right := a.node.Size, b.node.Size
	if allocatedKnown {
		left, right = a.node.Allocated, b.node.Allocated
	}
	if left != right {
		return left > right
	}
	aDir, bDir := a.node.Flags&fastFlagDirectory != 0, b.node.Flags&fastFlagDirectory != 0
	if aDir != bDir {
		return aDir
	}
	return a.node.ID < b.node.ID
}

type inspectHeap struct {
	items          []inspectCandidate
	allocatedKnown bool
}

func (h inspectHeap) Len() int { return len(h.items) }
func (h inspectHeap) Less(i, j int) bool {
	return betterInspectCandidate(h.items[j], h.items[i], h.allocatedKnown)
}
func (h inspectHeap) Swap(i, j int)   { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *inspectHeap) Push(value any) { h.items = append(h.items, value.(inspectCandidate)) }
func (h *inspectHeap) Pop() any {
	last := h.items[len(h.items)-1]
	h.items = h.items[:len(h.items)-1]
	return last
}

func inspectItems(ctx context.Context, f *fastIndex, manifest SnapshotManifest, dirIndex uint32, opt InspectOptions, childrenOnly bool) (inspectPage[listItem], error) {
	page := inspectPage[listItem]{Items: []listItem{}, Limit: opt.Limit, Offset: opt.Offset}
	var scope map[uint32]struct{}
	if !childrenOnly && opt.DirectoryID != 1 {
		var err error
		scope, err = inspectSubtree(ctx, f, dirIndex)
		if err != nil {
			return page, err
		}
	}
	query := strings.ToLower(strings.TrimSpace(opt.Query))
	h := &inspectHeap{allocatedKnown: manifest.AllocatedKnown}
	keep := opt.Limit + opt.Offset
	consider := func(index uint32) error {
		node, err := f.readNode(index)
		if err != nil {
			return err
		}
		isDir := node.Flags&fastFlagDirectory != 0
		if (isDir && opt.Kind == "files") || (!isDir && opt.Kind == "directories") {
			return nil
		}
		if !childrenOnly {
			if index == dirIndex {
				return nil
			}
			if scope != nil {
				if _, ok := scope[node.Parent]; !ok {
					return nil
				}
			}
		}
		size := node.Size
		if manifest.AllocatedKnown {
			size = node.Allocated
		}
		if size < opt.MinBytes {
			return nil
		}
		if query != "" {
			name, err := f.readName(node.NameOff, node.NameLen)
			if err != nil {
				return err
			}
			if !strings.Contains(strings.ToLower(name), query) {
				return nil
			}
		}
		page.Total++
		candidate := inspectCandidate{index: index, node: node}
		if h.Len() < keep {
			heap.Push(h, candidate)
		} else if betterInspectCandidate(candidate, h.items[0], manifest.AllocatedKnown) {
			h.items[0] = candidate
			heap.Fix(h, 0)
		}
		return nil
	}
	if childrenOnly {
		children, err := f.readChildren(dirIndex)
		if err != nil {
			return page, err
		}
		for n, index := range children {
			if n%8192 == 0 && ctx.Err() != nil {
				return page, ctx.Err()
			}
			if err := consider(index); err != nil {
				return page, err
			}
		}
	} else {
		start, end := uint64(0), f.manifest.DirectoryCount+f.manifest.FileCount
		if opt.Kind == "files" {
			start = f.manifest.DirectoryCount
		} else if opt.Kind == "directories" {
			end = f.manifest.DirectoryCount
		}
		for index := start; index < end; index++ {
			if index%8192 == 0 && ctx.Err() != nil {
				return page, ctx.Err()
			}
			if err := consider(uint32(index)); err != nil {
				return page, err
			}
		}
	}
	sort.Slice(h.items, func(i, j int) bool { return betterInspectCandidate(h.items[i], h.items[j], manifest.AllocatedKnown) })
	start := opt.Offset
	if start > len(h.items) {
		start = len(h.items)
	}
	for _, candidate := range h.items[start:] {
		item, err := f.nodeItem(candidate.index)
		if err != nil {
			return page, err
		}
		if err := f.decoratePath(&item, manifest.Root); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func inspectSubtree(ctx context.Context, f *fastIndex, dirIndex uint32) (map[uint32]struct{}, error) {
	dirs := map[uint32]struct{}{dirIndex: {}}
	queue := []uint32{dirIndex}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[0]
		queue = queue[1:]
		children, err := f.readChildren(current)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if uint64(child) >= f.manifest.DirectoryCount {
				continue
			}
			if _, exists := dirs[child]; !exists {
				dirs[child] = struct{}{}
				queue = append(queue, child)
			}
		}
	}
	return dirs, nil
}

func inspectExtensions(f *fastIndex, allocatedKnown bool, limit, offset int) inspectPage[fastIndexExtension] {
	items := append([]fastIndexExtension(nil), f.extensions...)
	sort.Slice(items, func(i, j int) bool {
		left, right := items[i].SizeBytes, items[j].SizeBytes
		if allocatedKnown {
			left, right = items[i].AllocatedBytes, items[j].AllocatedBytes
		}
		if left != right {
			return left > right
		}
		return items[i].Extension < items[j].Extension
	})
	start := offset
	if start > len(items) {
		start = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	result := append([]fastIndexExtension{}, items[start:end]...)
	return inspectPage[fastIndexExtension]{Items: result, Total: uint64(len(items)), Limit: limit, Offset: offset}
}

func inspectErrors(f *fastIndex, total uint64, limit, offset int) (inspectPage[fastErrorRecord], error) {
	page := inspectPage[fastErrorRecord]{Items: []fastErrorRecord{}, Total: total, Limit: limit, Offset: offset}
	section, ok := f.sections[gtiErrors]
	if !ok || section.Length == 0 {
		return page, nil
	}
	scanner := bufio.NewScanner(io.NewSectionReader(f.container, int64(section.Offset), int64(section.Length)))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	line := 0
	for scanner.Scan() {
		if line >= offset {
			var item fastErrorRecord
			if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
				return page, err
			}
			page.Items = append(page.Items, item)
			if len(page.Items) == limit {
				break
			}
		}
		line++
	}
	return page, scanner.Err()
}
