package reconcile

import (
	"path/filepath"
	"strings"
	"testing"
)

// A remote that cannot be ASKED is not a remote that answered "no such
// branch". On epic-yoh (2026-09-24) a DNS flap made ls-remote fail, the
// reconciler read that as absence and pushed the base over the live epic
// branch; only the fast-forward check refused it, and the refusal then
// stopped the run unclassified. Here the fetch URL is unreachable while the
// push URL works, which is exactly the case where the old code silently
// CREATED the branch on the strength of an error.
//
// short: two local repositories and three git commands, no harness
func TestEnsureRemoteBranchDoesNotReadAnUnaskableRemoteAsAbsent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", bare},
		{"init", "--quiet", "-b", "main", work},
		{"-C", work, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "base"},
		{"-C", work, "remote", "add", "origin", filepath.Join(root, "unreachable.git")},
		{"-C", work, "remote", "set-url", "--push", "origin", bare},
	} {
		if out, err := harnessCommand("git", args...).command(root).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	g := &repoGit{dir: work, name: "ticfac test", email: "test@example.com", remote: "origin"}

	if _, err := g.ensureRemoteBranch("epic/x", "main"); err == nil {
		t.Fatal("ensureRemoteBranch succeeded against a remote it could not read")
	}
	out, err := harnessCommand("git", "-C", bare, "for-each-ref", "refs/heads/").output(root)
	if err != nil {
		t.Fatalf("read the bare remote: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("a branch was pushed on the strength of a failed read: %s", out)
	}
}
