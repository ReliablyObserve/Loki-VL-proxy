package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReliablyObserve/Loki-VL-proxy/internal/cache"
)

func TestPatternsPersistenceLoop_StartStopAndPersist(t *testing.T) {
	persistPath := filepath.Join(t.TempDir(), "patterns-snapshot.json")
	patternsEnabled := true

	p, err := New(Config{
		BackendURL:              "http://unused",
		Cache:                   cache.New(60*time.Second, 1000),
		LogLevel:                "error",
		PatternsEnabled:         &patternsEnabled,
		PatternsPersistPath:     persistPath,
		PatternsPersistInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	key := `patterns:tenant-a:query={app="api"}`
	payload := []byte(`{"status":"success","data":[{"pattern":"GET /health","samples":[[1710000000,3]]}]}`)
	p.recordPatternSnapshotEntry(key, payload, time.Now().UTC())

	p.startPatternsPersistenceLoop()
	p.startPatternsPersistenceLoop()

	time.Sleep(70 * time.Millisecond)
	p.stopPatternsPersistenceLoop(context.Background())

	raw, err := os.ReadFile(persistPath)
	if err != nil {
		t.Fatalf("expected persisted patterns snapshot file, read error: %v", err)
	}
	var snapshot patternsSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decode persisted patterns snapshot: %v", err)
	}
	if len(snapshot.EntriesByKey) == 0 {
		t.Fatalf("expected persisted patterns snapshot to contain entries")
	}
	if entry, ok := snapshot.EntriesByKey[key]; !ok || len(entry.Value) == 0 {
		t.Fatalf("expected persisted snapshot entry for key %q", key)
	}
}

func TestPatternsPersistenceLoop_SkipsUnchangedPeriodicWrites(t *testing.T) {
	persistPath := filepath.Join(t.TempDir(), "patterns-snapshot.json")
	patternsEnabled := true

	p, err := New(Config{
		BackendURL:              "http://unused",
		Cache:                   cache.New(60*time.Second, 1000),
		LogLevel:                "error",
		PatternsEnabled:         &patternsEnabled,
		PatternsPersistPath:     persistPath,
		PatternsPersistInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	p.recordPatternSnapshotEntry("patterns:tenant-a:query={app=\"api\"}", []byte(`{"status":"success","data":[]}`), time.Now().UTC())
	p.startPatternsPersistenceLoop()
	defer p.stopPatternsPersistenceLoop(context.Background())

	var first []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		first, err = os.ReadFile(persistPath)
		if err == nil && len(first) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("expected first persisted patterns snapshot file, read error: %v", err)
	}

	// Wait across multiple persistence ticks without mutating the snapshot.
	time.Sleep(120 * time.Millisecond)

	second, err := os.ReadFile(persistPath)
	if err != nil {
		t.Fatalf("expected persisted patterns snapshot file on second read: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("expected unchanged patterns snapshot to skip periodic rewrite")
	}
}

func TestPatternsSnapshotApply_IsAppendOnlyAndLongLivedInCache(t *testing.T) {
	p := newTestProxy(t, "http://unused")

	baseTS := time.Now().Add(-1 * time.Minute).UnixNano()
	newTS := time.Now().UTC().UnixNano()
	key := "patterns:tenant-a:query={app=\"api\"}"
	secondKey := "patterns:tenant-a:query={app=\"worker\"}"

	applied := p.applyPatternsSnapshot(patternsSnapshot{
		Version:         1,
		SavedAtUnixNano: baseTS,
		EntriesByKey: map[string]patternSnapshotEntry{
			key: {Value: []byte(`{"status":"success","data":[{"pattern":"v1"}]}`), UpdatedAtUnixNano: baseTS},
		},
	})
	if applied != 1 {
		t.Fatalf("expected first snapshot apply to add one entry, got %d", applied)
	}

	// Older updates for an existing key must be ignored.
	applied = p.applyPatternsSnapshot(patternsSnapshot{
		Version:         1,
		SavedAtUnixNano: baseTS,
		EntriesByKey: map[string]patternSnapshotEntry{
			key: {Value: []byte(`{"status":"success","data":[{"pattern":"older"}]}`), UpdatedAtUnixNano: baseTS - 1},
		},
	})
	if applied != 0 {
		t.Fatalf("expected older snapshot update to be ignored, got %d", applied)
	}

	// A newer update for an existing key must replace previous value.
	applied = p.applyPatternsSnapshot(patternsSnapshot{
		Version:         1,
		SavedAtUnixNano: newTS,
		EntriesByKey: map[string]patternSnapshotEntry{
			key: {Value: []byte(`{"status":"success","data":[{"pattern":"v2"}]}`), UpdatedAtUnixNano: newTS},
		},
	})
	if applied != 1 {
		t.Fatalf("expected newer snapshot update to apply, got %d", applied)
	}

	// Append-only behavior for other keys: existing keys are retained even if absent from incoming snapshot.
	applied = p.applyPatternsSnapshot(patternsSnapshot{
		Version:         1,
		SavedAtUnixNano: newTS + 1,
		EntriesByKey: map[string]patternSnapshotEntry{
			secondKey: {Value: []byte(`{"status":"success","data":[{"pattern":"worker"}]}`), UpdatedAtUnixNano: newTS + 1},
		},
	})
	if applied != 1 {
		t.Fatalf("expected second key to be appended, got %d", applied)
	}

	p.patternsSnapshotMu.RLock()
	defer p.patternsSnapshotMu.RUnlock()

	if len(p.patternsSnapshotEntries) != 2 {
		t.Fatalf("expected append-only snapshot map to keep both keys, got %d", len(p.patternsSnapshotEntries))
	}
	if got := string(p.patternsSnapshotEntries[key].Value); !bytes.Contains([]byte(got), []byte(`"v2"`)) {
		t.Fatalf("expected latest value for %q, got %s", key, got)
	}

	_, ttl, ok := p.cache.GetWithTTL(key)
	if !ok {
		t.Fatalf("expected patterns key %q in cache", key)
	}
	if ttl < (5 * 365 * 24 * time.Hour) {
		t.Fatalf("expected long-lived patterns TTL, got %s", ttl)
	}
}
