package herdr

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// THE SEAM, asserted three ways, because a seam nobody tests is a seam the
// next refactor quietly closes:
//
//   - this Executor satisfies the reconciler's Executor interface EXACTLY as
//     the local one does — the five operations, unchanged;
//   - internal/reconcile contains no herdr-specific code (comments may name
//     herdr when they explain the seam; code may not);
//   - every piece of herdr addressing a caller can see lives inside
//     JobHandle.Handle.

// The compile-time assertion. It lives HERE and not in internal/reconcile
// precisely so that reconcile stays free of herdr code: this package may
// name that one, that one must not name this one. Start, Inspect,
// CollectDetail, Cancel and Dispose — the five operations, in the
// reconciler's own spelling, over the subprocess package's own record types.
var _ reconcile.Executor = (*Executor)(nil)

// TestTheExecutorInterfaceIsUnchanged pins the METHOD COUNT of the seam the
// reconciler defines: five operations, no more. A sixth operation added to
// the interface is a change to the protocol this package has not followed —
// and a fifth quietly dropped is one this executor stopped implementing.
func TestTheExecutorInterfaceIsUnchanged(t *testing.T) {
	const want = 5 // Start, Inspect, CollectDetail, Cancel, Dispose
	if got := reflect.TypeOf((*reconcile.Executor)(nil)).Elem().NumMethod(); got != want {
		t.Errorf("reconcile.Executor has %d methods, want %d: the interface is the seam, and this "+
			"package implements it exactly as it stands", got, want)
	}
}

// TestReconcileContainsNoHerdrCode is the acceptance criterion's grep, as a
// test: every NON-TEST .go file under internal/reconcile, with comments
// stripped, must carry no occurrence of herdr — no import of this package or
// of internal/herd, no herdr identifier, no herdr constant. Two honest
// exclusions, both deliberate:
//
//   - comments may name herdr, because a comment that explains why a profile
//     naming `herdr` is refused is the seam's own documentation, not herdr
//     code;
//   - the reconciler's own tests may use herdr as a fixture VALUE, because
//     asserting "a profile naming herdr is refused for now" is a test of the
//     seam, exactly the way this package asserts the seam from its side.
//     What neither side's tests may do is import the other's packages, and
//     the import half of this check runs over the tests too.
func TestReconcileContainsNoHerdrCode(t *testing.T) {
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
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// No file, test or not, may import this package or internal/herd:
		// an import is the dependency, whatever half of the file it sits in.
		if strings.Contains(string(raw), "internal/herd") || strings.Contains(string(raw), "internal/exec/herdr") {
			t.Errorf("internal/reconcile/%s imports the herdr side: the reconciler reaches herdr only "+
				"through the Executor interface, never around it", entry.Name())
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		code := stripComments(string(raw))
		if strings.Contains(strings.ToLower(code), "herdr") {
			t.Errorf("internal/reconcile/%s carries herdr-specific code: the reconciler must learn "+
				"nothing about panes, workspaces or protocols — panes, workspaces and protocols are "+
				"this package's business, behind the Executor interface", entry.Name())
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
		if inLine {
			if src[i] == '\n' {
				inLine = false
				b.WriteByte(src[i])
			}
			continue
		}
		if inBlock {
			if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if src[i] == '/' && i+1 < len(src) && src[i+1] == '/' {
			inLine = true
			i++
			continue
		}
		if src[i] == '/' && i+1 < len(src) && src[i+1] == '*' {
			inBlock = true
			i++
			continue
		}
		b.WriteByte(src[i])
	}
	return b.String()
}

// TestHerdrAddressingLivesInsideTheHandle is the acceptance criterion's
// third clause: the herdr-specific facts a handle carries — workspace, pane,
// agent name, herdr's own worktree — exist in the ONE open object the
// contract leaves open, and nowhere else on the JobHandle.
func TestHerdrAddressingLivesInsideTheHandle(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	if handle.Executor != ExecutorName {
		t.Errorf("handle.Executor = %q, want %q", handle.Executor, ExecutorName)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ field, got, why string }{
		{"workspace_id", local.WorkspaceID, "the workspace herdr opened"},
		{"pane_id", local.PaneID, "the pane the agent occupies"},
		{"agent_name", local.AgentName, "the herdr agent name"},
		{"worktree", local.Worktree, "the worktree herdr created"},
	} {
		if check.got == "" {
			t.Errorf("JobHandle.handle carries no %s: %s has no place in the closed fields, "+
				"so the handle IS its only home", check.field, check.why)
		}
	}
	// And the closed half of the handle carries nothing herdr-shaped.
	record, err := (&store{dir: local.State}).readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if record.WorkspaceID != local.WorkspaceID || record.AgentName != local.AgentName ||
		record.PaneID != local.PaneID || record.Worktree != local.Worktree {
		t.Errorf("the handle's addressing and the attempt record's disagree: the handle is the frozen copy")
	}
}
