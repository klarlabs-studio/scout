package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestArtifactQuotaAvailableUsesCombinedLimits(t *testing.T) {
	if !artifactQuotaAvailable(3, maxMCPResultArtifactTotalBytes-1, 3, maxMCPResultArtifactOwnerBytes-1, 1) {
		t.Fatal("artifact at remaining process and owner byte budget was rejected")
	}
	if !artifactQuotaAvailable(maxMCPResultArtifactCount-1, maxMCPResultArtifactTotalBytes-1, maxMCPResultArtifactOwnerCount-1, maxMCPResultArtifactOwnerBytes-1, 1) {
		t.Fatal("artifact that exactly fills the final available slot and byte was rejected")
	}
	for name, args := range map[string]struct {
		count      int
		totalBytes int64
		ownerCount int
		ownerBytes int64
		size       int64
	}{
		"process count":      {maxMCPResultArtifactCount, 0, 0, 0, 1},
		"process bytes":      {0, maxMCPResultArtifactTotalBytes, 0, 0, 1},
		"owner count":        {0, 0, maxMCPResultArtifactOwnerCount, 0, 1},
		"owner bytes":        {0, 0, 0, maxMCPResultArtifactOwnerBytes, 1},
		"negative size":      {0, 0, 0, 0, -1},
		"process count over": {maxMCPResultArtifactCount, 0, 0, 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			if artifactQuotaAvailable(args.count, args.totalBytes, args.ownerCount, args.ownerBytes, args.size) {
				t.Fatal("artifact exceeding a combined registry limit was accepted")
			}
		})
	}
}

func TestDeletePrivateSnapshotReleasesQuotaAndEnforcesOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.txt")
	if err := os.WriteFile(path, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("private"))
	mu := &sync.Mutex{}
	registered := map[string][32]byte{path: digest}
	owners := map[string]string{path: "alice"}
	sizes := map[string]int64{path: 7}
	var totalBytes int64 = 7
	ownerBytes := map[string]int64{"alice": 7}
	ownerCounts := map[string]int{"alice": 1}

	if err := deletePrivateSnapshot(mu, registered, owners, sizes, &totalBytes, ownerBytes, ownerCounts, "bob", path); err == nil {
		t.Fatal("foreign owner deleted artifact")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("foreign delete removed file: %v", err)
	}
	if err := deletePrivateSnapshot(mu, registered, owners, sizes, &totalBytes, ownerBytes, ownerCounts, "alice", path); err != nil {
		t.Fatalf("owner delete failed: %v", err)
	}
	if totalBytes != 0 || len(sizes) != 0 || len(ownerBytes) != 0 || len(ownerCounts) != 0 {
		t.Fatalf("quota not released: total=%d sizes=%v ownerBytes=%v ownerCounts=%v", totalBytes, sizes, ownerBytes, ownerCounts)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("artifact still exists after delete: %v", err)
	}
}

func TestDeletePrivateSnapshotReclaimsMissingArtifactQuota(t *testing.T) {
	path := filepath.Join(t.TempDir(), "already-missing.txt")
	mu := &sync.Mutex{}
	registered := map[string][32]byte{path: sha256.Sum256(nil)}
	owners := map[string]string{path: "alice"}
	sizes := map[string]int64{path: 12}
	var totalBytes int64 = 12
	ownerBytes := map[string]int64{"alice": 12}
	ownerCounts := map[string]int{"alice": 1}
	if err := deletePrivateSnapshot(mu, registered, owners, sizes, &totalBytes, ownerBytes, ownerCounts, "alice", path); err != nil {
		t.Fatalf("delete missing artifact: %v", err)
	}
	if totalBytes != 0 || len(registered) != 0 || len(owners) != 0 || len(sizes) != 0 || len(ownerBytes) != 0 || len(ownerCounts) != 0 {
		t.Fatalf("stale quota was not reclaimed: total=%d registered=%v owners=%v sizes=%v ownerBytes=%v ownerCounts=%v", totalBytes, registered, owners, sizes, ownerBytes, ownerCounts)
	}
}

func TestCleanupPrivateArtifactsReleasesZeroByteCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mu := &sync.Mutex{}
	registered := map[string][32]byte{path: sha256.Sum256(nil)}
	owners := map[string]string{path: "alice/default"}
	sizes := map[string]int64{path: 0}
	var totalBytes int64
	ownerBytes := map[string]int64{"alice/default": 0}
	ownerCounts := map[string]int{"alice/default": 1}
	removedBytes, removedCount := cleanupPrivateArtifactsForOwner(mu, registered, owners, sizes, &totalBytes, ownerBytes, ownerCounts, "alice/default")
	if removedBytes != 0 || removedCount != 1 || len(registered) != 0 || len(owners) != 0 || len(sizes) != 0 || len(ownerCounts) != 0 || len(ownerBytes) != 0 {
		t.Fatalf("zero-byte cleanup did not release count: removed=(%d,%d) registered=%v owners=%v sizes=%v ownerCounts=%v ownerBytes=%v", removedBytes, removedCount, registered, owners, sizes, ownerCounts, ownerBytes)
	}
}

func TestLoadPrivateSnapshotChecksOwnerDigestAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("trusted content"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("trusted content"))
	mu := &sync.Mutex{}
	registered := map[string][32]byte{path: digest}
	owners := map[string]string{path: "alice"}

	if _, _, err := loadPrivateSnapshot(mu, registered, owners, "bob", path); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("foreign read error = %v, want owner denial", err)
	}
	if _, content, err := loadPrivateSnapshot(mu, registered, owners, "alice", path); err != nil || string(content) != "trusted content" {
		t.Fatalf("valid read = (%q, %v), want trusted content", string(content), err)
	}
	if err := os.WriteFile(path, []byte("changed content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPrivateSnapshot(mu, registered, owners, "alice", path); err == nil || !strings.Contains(err.Error(), "changed after creation") {
		t.Fatalf("modified artifact read error = %v, want digest rejection", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("trusted content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := loadPrivateSnapshot(mu, registered, owners, "alice", path); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink read error = %v, want regular-file rejection", err)
	}
}
