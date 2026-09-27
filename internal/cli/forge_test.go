package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// The host's half of the close-out seam: how run-epic and settle build the
// surface the reconciler is handed. The reconciler's own tests prove the
// behaviour ABOVE the seam with a fake; these prove the builder resolves
// the two facts only the host knows — the remote, the credential — and
// fails closed, naming the missing one, rather than handing the run a
// surface that cannot answer.
//
// Tick hio adds the gate in front of both: the credential is resolved only
// when the target repository's own close-out rule needs a forge, read from
// the same path the reconciler reads it, so a machine with gh and no
// GITHUB_TOKEN pays no `gh auth token` subprocess for a surface the run
// would discard.

// closeoutRuleDir makes a checkout that declares the PR + CI close-out rule
// the way a real target repository does: in `.tick/config.md`'s Rules
// section, anchored on the reader's own stable phrase.
func closeoutRuleDir(t *testing.T, rule string) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet", "-b", "main")
	mustGit(t, dir, "remote", "add", "origin", "git@github.com:example/example.git")
	writeCloseoutRule(t, dir, rule)
	return dir
}

func writeCloseoutRule(t *testing.T, dir, rule string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".tick"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "# Config\n\n## Rules\n\n" + rule + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".tick", "config.md"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
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

// recordingLadder answers with a token and records that it ran, so a test
// can prove the credential was NOT resolved — the question tick hio exists
// to ask, since a ladder that runs and is discarded looks exactly like one
// that never ran in every assertion but this one.
func recordingLadder(t *testing.T, token string) (*bool, func() (string, forge.TokenSource, error)) {
	t.Helper()
	ran := false
	ladder := func() (string, forge.TokenSource, error) {
		ran = true
		return token, forge.TokenSourceEnv, nil
	}
	return &ran, ladder
}

// A repository that declares the rule gets the surface it will be refused
// without: the remote resolved to owner/name, the credential from the env.
func TestPullRequestsForRunBuildsTheSurfaceADeclaredRuleNeeds(t *testing.T) {
	dir := closeoutRuleDir(t, "Epic integration goes through a PR + CI gate: the orchestrator opens a PR, and CI must be green.")
	t.Setenv(forge.TokenEnv, "a-token")
	ran, ladder := recordingLadder(t, "a-token")
	saveForgeTokenLadder(t, ladder)

	pulls, note, err := pullRequestsForRun(dir, "origin")
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
	if !*ran {
		t.Error("the credential ladder did not run for a repo that declares the rule")
	}
	// The note names the rung that answered (tick 9sz): the run SAYS where
	// its forge credential came from, and a repo that needs a forge must not
	// leave that a question.
	if !strings.Contains(note, forge.TokenEnv) || !strings.Contains(note, "the close-out rule needs a forge") {
		t.Errorf("the note does not name the env rung: %q", note)
	}

	// No token: no surface, and the error names the one thing missing —
	// the run is then refused by the reconciler, which this rule demands
	// a surface of.
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
	if pulls, _, err := pullRequestsForRun(dir, "origin"); err == nil || pulls != nil {
		t.Fatalf("a surface was built with no credential: %v", err)
	} else if !strings.Contains(err.Error(), forge.TokenEnv) {
		t.Errorf("the failure does not name the missing credential: %v", err)
	}

	// No remote to resolve a repository from: the same fail-closed answer.
	mustGit(t, dir, "remote", "remove", "origin")
	if pulls, _, err := pullRequestsForRun(dir, "origin"); err == nil || pulls != nil {
		t.Fatalf("a surface was built for a checkout with no remote: %v", err)
	}
}

// A gh-authed machine with no GITHUB_TOKEN is a machine the run's surface
// accepts (tick vo4): the builder resolves its credential through the same
// ladder doctor reports from, so the ok doctor prints is the answer the run
// gets — the gap this test pins was doctor accepting what the run refused.
func TestPullRequestsForRunAcceptsGhsTokenWhenTheEnvHoldsNone(t *testing.T) {
	dir := closeoutRuleDir(t, "Epic integration goes through a PR + CI gate.")
	t.Setenv(forge.TokenEnv, "")
	saveForgeTokenLadder(t, func() (string, forge.TokenSource, error) {
		return "from-gh", forge.TokenSourceGH, nil
	})

	pulls, note, err := pullRequestsForRun(dir, "origin")
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
	// The note names the rung that answered (tick 9sz): a token fetched
	// from gh's own auth is otherwise invisible — the subprocess that got
	// it is the fact the run owes its own stdout.
	if !strings.Contains(note, "gh auth token") || !strings.Contains(note, "fetched") {
		t.Errorf("the note does not say the token was fetched from gh: %q", note)
	}
}

// The regression tick hio exists for: a target repository that declares no
// close-out rule needs no surface, so the credential ladder — whose gh rung
// is a subprocess — must not run at all. Before this tick the ladder ran
// unconditionally and the surface it built was handed to a reconciler that
// threw it away; on a machine with gh installed and no GITHUB_TOKEN that was
// one `gh auth token` subprocess per run-epic, per settle, for nothing.
func TestPullRequestsForRunSkipsTheCredentialWhenNoRuleIsDeclared(t *testing.T) {
	// A checkout with no .tick/config.md at all — most target repositories.
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet", "-b", "main")
	mustGit(t, dir, "remote", "add", "origin", "git@github.com:example/example.git")
	t.Setenv(forge.TokenEnv, "")
	ran, ladder := recordingLadder(t, "a-token-nobody-should-consult")
	saveForgeTokenLadder(t, ladder)

	pulls, note, err := pullRequestsForRun(dir, "origin")
	if err != nil {
		t.Fatalf("a repo with no close-out rule is not an error: %v", err)
	}
	if pulls != nil {
		t.Fatalf("a surface was built for a repo that declares no rule: %T", pulls)
	}
	if note != "" {
		t.Errorf("a run that fetched no credential said one: %q", note)
	}
	if *ran {
		t.Error("the credential ladder ran for a repo that declares no close-out rule")
	}

	// A config that IS there but declares no rule: the same skip — a Rules
	// section saying other things is not this rule (the reader anchors on
	// the phrase, and so does the builder's decision to resolve a forge).
	writeCloseoutRule(t, dir, "Package management is pnpm only — never npm or yarn.")
	if pulls, _, err := pullRequestsForRun(dir, "origin"); err != nil || pulls != nil {
		t.Fatalf("a surface was built for a config that declares no rule: %v", err)
	}
	if *ran {
		t.Error("the credential ladder ran for a config that declares no rule")
	}

	// And a repo with no remote needs no credential resolved either: the
	// refusal the reconciler would make of a missing remote only exists
	// where a rule demands the surface.
	if pulls, _, err := pullRequestsForRun(t.TempDir(), "origin"); err != nil || pulls != nil {
		t.Fatalf("a surface was built for a checkout with no remote and no rule: %v", err)
	}
}

// TestPullRequestsForRunNamesTheFixWhenTheRuleHasNoForgeRemote (tick 6vp):
// a repository that declares the rule but has no GitHub remote is a run
// that refuses to START, and before this tick the refusal named neither
// remedy — an operator could clear it only by reading source. The refusal
// now names the config the rule lives in and both ways out: point the
// remote at GitHub, or remove the rule.
func TestPullRequestsForRunNamesTheFixWhenTheRuleHasNoForgeRemote(t *testing.T) {
	dir := closeoutRuleDir(t, "Epic integration goes through a PR + CI gate.")
	mustGit(t, dir, "remote", "remove", "origin")
	t.Setenv(forge.TokenEnv, "")
	ran, ladder := recordingLadder(t, "a-token-nobody-should-consult")
	saveForgeTokenLadder(t, ladder)

	_, _, err := pullRequestsForRun(dir, "origin")
	if err == nil {
		t.Fatal("a surface was built for a checkout with no remote")
	}
	if !strings.Contains(err.Error(), "point origin at GitHub") {
		t.Errorf("the refusal does not name the remote remedy:\n%v", err)
	}
	if !strings.Contains(err.Error(), "remove the rule") {
		t.Errorf("the refusal does not name the rule remedy:\n%v", err)
	}
	if !strings.Contains(err.Error(), reconcile.RepoConfigPath(dir)) {
		t.Errorf("the refusal does not name the config the rule lives in:\n%v", err)
	}
	if *ran {
		t.Error("the credential ladder ran although the remote could not resolve")
	}

	// A remote that is there but does not resolve as a GitHub repository is
	// the same refusal, the same remedies.
	mustGit(t, dir, "remote", "add", "origin", "https://example.com/not-aforge")
	if _, _, err := pullRequestsForRun(dir, "origin"); err == nil {
		t.Fatal("a surface was built over a remote that resolves to no repository")
	} else if !strings.Contains(err.Error(), "remove the rule") {
		t.Errorf("the refusal does not name the remedies:\n%v", err)
	}
}

// TestPullRequestsForRunRefusesARemoteOnAnotherForge (tick 4zo): a remote
// on another forge resolves to an owner/name slug, and before the host
// check that slug passed this builder, doctor's remote check and init's
// close-out guess alike — the run then constructed a GitHub surface
// addressed at api.github.com over a repository that lives on GitLab, and
// the failure surfaced only at close-out time, as 404s the operator could
// no longer act on. The refusal is HERE now, at build time, naming the host
// and both remedies — and no credential is resolved for a remote the
// surface cannot speak with.
func TestPullRequestsForRunRefusesARemoteOnAnotherForge(t *testing.T) {
	for _, remote := range []string{
		"git@gitlab.com:example/example.git",
		"https://gitlab.com/example/example.git",
		// A GitHub Enterprise host is the same refusal: forge.GitHub's API
		// field is wired by nothing a run constructs, so api.github.com is
		// the only API this surface can speak with.
		"https://ghe.example.com/example/example.git",
	} {
		dir := closeoutRuleDir(t, "Epic integration goes through a PR + CI gate: the orchestrator opens a PR, and CI must be green.")
		mustGit(t, dir, "remote", "set-url", "origin", remote)
		ran, ladder := recordingLadder(t, "a-token-nobody-should-consult")
		saveForgeTokenLadder(t, ladder)

		if pulls, note, err := pullRequestsForRun(dir, "origin"); err == nil || pulls != nil || note != "" {
			t.Fatalf("a GitHub surface was built over %q: %v", remote, err)
		} else {
			if !strings.Contains(err.Error(), "not a GitHub repository") {
				t.Errorf("the refusal does not say the remote is not GitHub's:\n%v", err)
			}
			if !strings.Contains(err.Error(), "point origin at GitHub") ||
				!strings.Contains(err.Error(), "remove the rule") {
				t.Errorf("the refusal does not name both remedies:\n%v", err)
			}
			if !strings.Contains(err.Error(), reconcile.RepoConfigPath(dir)) {
				t.Errorf("the refusal does not name the config the rule lives in:\n%v", err)
			}
		}
		if *ran {
			t.Error("the credential ladder ran although the remote is on another forge")
		}
	}
}

// The rule the builder reads is the reconciler's own, from the same path —
// so a config the reconciler cannot read is a config the builder refuses
// rather than guessing at, and the refusal names the file: the reconciler
// will refuse construction on the same read, and the command dies naming
// the config rather than a missing credential.
func TestPullRequestsForRunRefusesAConfigItCannotRead(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet", "-b", "main")
	mustGit(t, dir, "remote", "add", "origin", "git@github.com:example/example.git")
	// A directory where the config should be: a read that fails on every
	// host, root included, rather than a permission trick that depends on
	// who is running the suite.
	if err := os.MkdirAll(filepath.Join(dir, ".tick", "config.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(forge.TokenEnv, "")
	ran, ladder := recordingLadder(t, "a-token-nobody-should-consult")
	saveForgeTokenLadder(t, ladder)

	if pulls, note, err := pullRequestsForRun(dir, "origin"); err == nil || pulls != nil || note != "" {
		t.Fatalf("a surface was built over an unreadable config: %v", err)
	} else if !strings.Contains(err.Error(), reconcile.RepoConfigPath(dir)) {
		t.Errorf("the refusal does not name the config it could not read: %v", err)
	}
	if *ran {
		t.Error("the credential ladder ran although the rule could not be read")
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
