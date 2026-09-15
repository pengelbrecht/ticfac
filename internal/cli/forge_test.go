package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/forge"
)

// The host's half of the close-out seam: how run-epic builds the surface the
// reconciler is handed. The reconciler's own tests prove the behaviour ABOVE
// the seam with a fake; these prove the builder resolves the two facts only
// the host knows — the remote, the credential — and fails closed, naming the
// missing one, rather than handing the run a surface that cannot answer.

func TestPullRequestsForRun(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet", "-b", "main")
	mustGit(t, dir, "remote", "add", "origin", "git@github.com:example/example.git")

	t.Setenv(forge.TokenEnv, "a-token")
	pulls, err := pullRequestsForRun(dir, "origin")
	if err != nil {
		t.Fatal(err)
	}
	github, ok := pulls.(forge.GitHub)
	if !ok {
		t.Fatalf("the surface is %T, want the GitHub one", pulls)
	}
	if github.Repo != "example/example" || github.Token != "a-token" {
		t.Errorf("the surface addresses %q with token %q", github.Repo, github.Token)
	}

	// No token: no surface, and the error names the one thing missing —
	// the run is then refused by the reconciler only where the target
	// repo's own rule demands a surface.
	t.Setenv(forge.TokenEnv, "")
	if pulls, err := pullRequestsForRun(dir, "origin"); err == nil || pulls != nil {
		t.Fatalf("a surface was built with no credential: %v", err)
	} else if !strings.Contains(err.Error(), forge.TokenEnv) {
		t.Errorf("the failure does not name the missing credential: %v", err)
	}

	// No remote to resolve a repository from: the same fail-closed answer.
	if pulls, err := pullRequestsForRun(t.TempDir(), "origin"); err == nil || pulls != nil {
		t.Fatalf("a surface was built for a checkout with no remote: %v", err)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
