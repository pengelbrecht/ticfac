package cli

import (
	"fmt"
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
	//
	// The ladder behind the seam is forge's own (it is proven there, rung by
	// rung); here it is answered with the failure shape its gh rung and the
	// env rung together produce, so what is under test is the BUILDER: it
	// fails closed, forwarding the ladder's refusal rather than inventing
	// an optimism of its own.
	saveForgeTokenLadder(t, func() (string, forge.TokenSource, error) {
		return "", "", fmt.Errorf("no %s is set, and gh auth token did not answer: gh is not installed",
			forge.TokenEnv)
	})
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

// A gh-authed machine with no GITHUB_TOKEN is a machine the run's surface
// accepts (tick vo4): the builder resolves its credential through the same
// ladder doctor reports from, so the ok doctor prints is the answer the run
// gets — the gap this test pins was doctor accepting what the run refused.
func TestPullRequestsForRunAcceptsGhsTokenWhenTheEnvHoldsNone(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet", "-b", "main")
	mustGit(t, dir, "remote", "add", "origin", "git@github.com:example/example.git")
	t.Setenv(forge.TokenEnv, "")
	saveForgeTokenLadder(t, func() (string, forge.TokenSource, error) {
		return "from-gh", forge.TokenSourceGH, nil
	})

	pulls, err := pullRequestsForRun(dir, "origin")
	if err != nil {
		t.Fatalf("the surface was not built on gh's answer: %v", err)
	}
	github, ok := pulls.(forge.GitHub)
	if !ok {
		t.Fatalf("the surface is %T, want the GitHub one", pulls)
	}
	if github.Repo != "example/example" || github.Token != "from-gh" {
		t.Errorf("the surface addresses %q with token %q, want example/example on gh's token",
			github.Repo, github.Token)
	}
}

// saveForgeTokenLadder replaces the ladder the builder resolves its
// credential through, restoring the production one on cleanup — the ladder's
// own rungs are proven in internal/forge; here the seam exists so the builder
// is tested against a controlled answer, never the host's real gh.
func saveForgeTokenLadder(t *testing.T, ladder func() (string, forge.TokenSource, error)) {
	t.Helper()
	saved := resolveForgeToken
	resolveForgeToken = ladder
	t.Cleanup(func() { resolveForgeToken = saved })
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
