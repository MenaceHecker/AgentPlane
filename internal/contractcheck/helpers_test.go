// Package contractcheck holds test-only validation of the published Phase 0
// contracts (docs/gateway and docs/audit). It contains no gateway, audit
// library, signer or verifier implementation for production use: the
// reference checks live in _test.go files and exist only to prove that the
// published documents and fixtures are consistent and discriminating.
package contractcheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// repoRoot returns the repository root (the directory holding go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// readJSON decodes a JSON file into a generic value. Numbers decode as
// float64, which is exact for the integers used in the fixtures.
func readJSON(t *testing.T, path string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(readFile(t, path), &v); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return v
}
