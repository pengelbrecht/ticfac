package cli

import (
	"fmt"
	"os/exec"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// The code-hosting surface behind the PR + CI close-out rule (tick 0iz): the
// host's half of the seam the reconciler enforces. The reconciler checks the
// rule a target repo declares in .tick/config.md against the surface it is
// HANDED — never against something it reaches for — so this is where the
// GitHub REST surface is built, from the two facts only the host knows: the
// remote the run pushes to, and the credential the ladder resolves for it
// (forge.ResolveTokenFrom: GITHUB_TOKEN first, then gh's own auth — tick vo4,
// so the run's answer and doctor's cannot disagree).
//
// Nil is a supported answer and not a failure: most target repositories
// declare no close-out rule, and for those a run needs no surface at all.
// A repository that DOES declare the rule is refused by the reconciler at
// construction — before anything is claimed — and the refusal names the
// token, which is the one thing this builder cannot invent.
//
// Since tick hio the rule also gates the building: the credential is
// resolved when, and only when, the rule is declared, so a machine with gh
// installed and no GITHUB_TOKEN pays no `gh auth token` subprocess per run
// for a surface the reconciler would discard. The rule is read from the
// same path the reconciler reads it (reconcile.RepoConfigPath), so the
// host's decision and the reconciler's demand cannot drift apart.

// resolveForgeToken is the credential ladder the builder resolves its token
// through, a seam so a test answers with a controlled rung instead of the
// host's real gh — the same reason doctor's probes are seams. The ladder
// itself — GITHUB_TOKEN first, gh's own auth second — is forge's, and is
// proven there rung by rung (tick vo4); both surfaces here read the SAME
// ladder, so doctor's ok and the run's answer cannot disagree again.
var resolveForgeToken = forge.ResolveTokenFrom

// pullRequestsForRun builds the GitHub surface for one run: the remote the
// run's durable authority lives on, resolved to the `owner/name` the REST
// API addresses, and the bearer token the environment holds or gh answers
// (the same ladder doctor reports from, tick vo4) — but only when the
// target repository's own close-out rule needs a forge (tick hio).
//
// The rule is read first, from the same .tick/config.md the reconciler
// reads at construction, so the surface exists exactly where the
// reconciler will demand one:
//
//   - a repo that declares no rule gets no surface and no credential
//     resolution at all — the ladder's gh rung is a subprocess, and running
//     it for a surface the reconciler discards was the waste this gate
//     exists to remove;
//   - a repo that declares the rule gets the surface resolved as before,
//     fail-closed, naming the missing credential or remote;
//   - a config that cannot be read is a refusal, not a guess — the
//     reconciler refuses construction on the same read, so the command
//     still dies naming the file rather than a credential it never needed.
//
// An empty repo means the checkout the command runs in (the same default
// every other flag resolves — and the same one the reconciler's own
// RepoConfigPath resolves); an empty remote means origin.
func pullRequestsForRun(repo, remote string) (forge.PullRequests, string, error) {
	configPath := reconcile.RepoConfigPath(repo)
	rule, err := reconcile.ReadCloseoutRule(configPath)
	if err != nil {
		return nil, "", fmt.Errorf("the close-out rule could not be read: %w", err)
	}
	if !rule.Declared {
		return nil, "", nil
	}
	if repo == "" {
		return nil, "", fmt.Errorf("no checkout to resolve the remote from")
	}
	if remote == "" {
		remote = "origin"
	}
	// A rule that holds on a pull request but no remote to open one on is a
	// run that refuses to START (tick 6vp), and the refusal owes the
	// operator both ways out — point the remote at GitHub, or remove the
	// rule — named beside the config the rule lives in, rather than a bare
	// read error nothing outside the source can act on.
	url, err := exec.Command("git", "-C", repo, "remote", "get-url", remote).Output()
	if err != nil {
		return nil, "", fmt.Errorf("the %s remote could not be read: %w — the close-out rule in %s holds on "+
			"a pull request; point origin at GitHub, or remove the rule", remote, err, configPath)
	}
	slug, err := forge.ParseRepo(string(url))
	if err != nil {
		return nil, "", fmt.Errorf("the %s remote is not a GitHub repository: %v — the close-out rule in %s "+
			"holds on a pull request; point origin at GitHub, or remove the rule", remote, err, configPath)
	}
	token, source, err := resolveForgeToken()
	if err != nil {
		return nil, "", err
	}
	if token == "" {
		return nil, "", fmt.Errorf("no %s is set, so the GitHub surface has no credential", forge.TokenEnv)
	}
	// The run SAYS which rung answered (tick 9sz): a token that came from
	// gh's own auth is a fact an operator otherwise cannot see — the
	// subprocess that fetched it is the one thing hio's gate keeps out of
	// repos that need no forge, and the one thing a repo that does needs
	// named on the run's own stdout, into its run.log.
	note := "the close-out rule needs a forge: the GitHub credential is " + forge.TokenEnv + ", from the environment"
	if source == forge.TokenSourceGH {
		note = "the close-out rule needs a forge: the GitHub token was fetched from `gh auth token`"
	}
	return forge.GitHub{Token: token, Repo: slug}, note, nil
}
