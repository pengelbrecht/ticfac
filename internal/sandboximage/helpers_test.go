//go:build !windows

package sandboximage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Small file helpers the script tests share. They came from ticks'
// internal/sandbox setup_test.go with the rest of the suite (tick r6w); the
// tests of the Go verbs that file held stay with the verbs (tick 46x).

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
