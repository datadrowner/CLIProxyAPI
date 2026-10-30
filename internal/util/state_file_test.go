package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateStateFile(t *testing.T) {
	dir := t.TempDir()
	legacy, path := filepath.Join(dir, "x.json"), filepath.Join(dir, "x.state")

	MigrateStateFile(legacy, path) // nothing to move
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatal("state file created from nothing")
	}

	if errWrite := os.WriteFile(legacy, []byte("old"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	MigrateStateFile(legacy, path)
	if data, _ := os.ReadFile(path); string(data) != "old" {
		t.Fatalf("migrated content = %q", data)
	}
	if _, errStat := os.Stat(legacy); !os.IsNotExist(errStat) {
		t.Fatal("legacy file still present")
	}

	// An existing new file is never overwritten.
	if errWrite := os.WriteFile(legacy, []byte("stale"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	MigrateStateFile(legacy, path)
	if data, _ := os.ReadFile(path); string(data) != "old" {
		t.Fatalf("new file overwritten with %q", data)
	}
}
