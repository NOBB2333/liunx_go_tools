package filesystem

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestSummaryDistinguishesLegacyLocalVolumeFromSnapshot(t *testing.T) {
	root := t.TempDir()
	otherRoot := t.TempDir()
	tests := []struct {
		name          string
		manifest      SnapshotManifest
		pathRoot      string
		wantLocalPath string
	}{
		{name: "legacy local root", manifest: SnapshotManifest{Root: root}, wantLocalPath: root},
		{name: "legacy mapped root", manifest: SnapshotManifest{Root: "/original/root"}, pathRoot: otherRoot, wantLocalPath: otherRoot},
		{name: "missing root", manifest: SnapshotManifest{Root: filepath.Join(root, "missing")}},
		{name: "recorded snapshot volume", manifest: SnapshotManifest{Root: root, Volume: &VolumeSpace{Path: root, TotalBytes: 100, UsedBytes: 60, AvailableBytes: 40}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &Server{manifest: tt.manifest, pathRoot: tt.pathRoot}
			response := httptest.NewRecorder()
			server.handleSummary(response)
			var payload struct {
				Data struct {
					Manifest    SnapshotManifest `json:"manifest"`
					LocalVolume *VolumeSpace     `json:"local_volume"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if tt.wantLocalPath == "" {
				if payload.Data.LocalVolume != nil {
					t.Fatalf("unexpected local volume: %+v", payload.Data.LocalVolume)
				}
			} else {
				volume := payload.Data.LocalVolume
				if volume == nil || volume.Path != tt.wantLocalPath || volume.TotalBytes == 0 || volume.AvailableBytes+volume.UsedBytes != volume.TotalBytes {
					t.Fatalf("local volume = %+v, want path %q and valid capacity", volume, tt.wantLocalPath)
				}
			}
			if (payload.Data.Manifest.Volume == nil) != (tt.manifest.Volume == nil) {
				t.Fatal("snapshot volume was changed by local probe")
			}
		})
	}
}
