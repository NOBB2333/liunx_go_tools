package filesystem

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SnapshotManifest is embedded in snapshot.gti and is the stable metadata
// contract shared by the scanner, container reader and HTTP API.
type SnapshotManifest struct {
	SchemaVersion    int       `json:"schema_version,omitempty"`
	Root             string    `json:"root"`
	OutputDir        string    `json:"output_dir"`
	Metadata         string    `json:"metadata"`
	ScannerBackend   string    `json:"scanner_backend,omitempty"`
	AllocatedKnown   bool      `json:"allocated_bytes_known"`
	AllocationSource string    `json:"allocation_source,omitempty"`
	Workers          int       `json:"workers"`
	Files            uint64    `json:"files"`
	Directories      uint64    `json:"directories"`
	LogicalBytes     uint64    `json:"logical_bytes"`
	AllocatedBytes   uint64    `json:"allocated_bytes"`
	Errors           uint64    `json:"errors"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	Duration         int64     `json:"duration_ns"`
	EntriesPerSecond float64   `json:"entries_per_second"`
	Complete         bool      `json:"complete"`
}

func ReadManifest(snapshot string) (SnapshotManifest, error) {
	path := gtiPath(snapshot)
	if _, err := os.Stat(path); err != nil {
		return SnapshotManifest{}, errors.New("snapshot.gti is required; run filesystem scan first")
	}
	return readManifest(path)
}

func readManifest(snapshot string) (SnapshotManifest, error) {
	if strings.HasSuffix(strings.ToLower(snapshot), ".gti") {
		data, err := readGTISection(snapshot, gtiManifest)
		if err != nil {
			return SnapshotManifest{}, err
		}
		return decodeSnapshotManifest(data)
	}
	if data, err := readGTISection(filepath.Join(snapshot, "snapshot.gti"), gtiManifest); err == nil {
		return decodeSnapshotManifest(data)
	}
	data, err := os.ReadFile(filepath.Join(snapshot, "manifest.json"))
	if err != nil {
		return SnapshotManifest{}, err
	}
	return decodeSnapshotManifest(data)
}

func decodeSnapshotManifest(data []byte) (SnapshotManifest, error) {
	var manifest SnapshotManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return SnapshotManifest{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err == nil {
		if _, present := fields["allocated_bytes_known"]; !present && manifest.AllocatedBytes > 0 {
			manifest.AllocatedKnown = true
			if manifest.AllocationSource == "" {
				manifest.AllocationSource = "legacy-stat"
			}
		}
	}
	return manifest, nil
}
