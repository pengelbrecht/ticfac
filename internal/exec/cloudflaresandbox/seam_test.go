package cloudflaresandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// THE SEAM, asserted the same three ways herdr asserts it, because a seam
// nobody tests is a seam the next refactor quietly closes:
//
//   - this Executor satisfies the reconciler's Executor interface EXACTLY as
//     the local one does — the five operations, unchanged;
//   - internal/reconcile contains no cloudflare-sandbox-specific code
//     (comments may name the executor when they explain the seam; code may
//     not);
//   - every piece of this executor's addressing a caller can see lives inside
//     JobHandle.Handle — the container name, the process id, the state
//     directory, nothing else.
//
// The compile-time assertion lives HERE and not in internal/reconcile
// precisely so that reconcile stays free of this package's code: this package
// may name that one, that one must not name this one. The five operations —
// Start, Inspect, CollectDetail, Cancel and Dispose — in the reconciler's
// own spelling, over the subprocess package's own record types, are what the
// acceptance criterion means by "satisfies the same interface as subprocess".
var _ reconcile.Executor = (*Executor)(nil)

// short: reflection over the Executor interface; no I/O.
func TestTheExecutorInterfaceIsUnchanged(t *testing.T) {
	const want = 5 // Start, Inspect, CollectDetail, Cancel, Dispose
	if got := reflect.TypeOf((*reconcile.Executor)(nil)).Elem().NumMethod(); got != want {
		t.Errorf("reconcile.Executor has %d methods, want %d: the interface is the seam, and this "+
			"package implements it exactly as it stands", got, want)
	}
}

// short: a read of this repository's own sources; no I/O.
func TestReconcileContainsNoCloudflareSandboxCode(t *testing.T) {
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "internal", "reconcile")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// No file, test or not, may import this package: an import is the
		// dependency, whatever half of the file it sits in.
		if strings.Contains(string(raw), "internal/exec/cloudflaresandbox") {
			t.Errorf("internal/reconcile/%s imports the cloudflare-sandbox side: the reconciler "+
				"reaches this executor only through the Executor interface, never around it", entry.Name())
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		code := stripComments(string(raw))
		if strings.Contains(code, "cloudflaresandbox") {
			t.Errorf("internal/reconcile/%s carries cloudflare-sandbox-specific code: the reconciler "+
				"must learn nothing about containers, doors or factories — those are this package's "+
				"business, behind the Executor interface", entry.Name())
		}
	}
}

// stripComments removes // line comments and /* */ block comments, leaving
// the code a compiler would read. It does not need to be a parser: it only
// has to be honest about which half of a Go file is prose, and no string
// literal in internal/reconcile is load-bearing for this test.
func stripComments(src string) string {
	var b strings.Builder
	inLine, inBlock := false, false
	for i := 0; i < len(src); i++ {
		switch {
		case inBlock:
			if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
			}
			continue
		case inLine:
			if src[i] == '\n' {
				inLine = false
				b.WriteByte(src[i])
			}
			continue
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '/':
			inLine = true
			continue
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
			inBlock = true
			continue
		}
		b.WriteByte(src[i])
	}
	return b.String()
}
