package cli

import (
	"fmt"
	"os/exec"

	"github.com/pengelbrecht/ticfac/internal/forge"
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
// (the same ladder doctor reports from, tick vo4).
//
// An empty repo means the checkout the command runs in (the same default
// every other flag resolves); an empty remote means origin.
func pullRequestsForRun(repo, remote string) (forge.PullRequests, error) {
	if repo == "" {
		return nil, fmt.Errorf("no checkout to resolve the remote from")
	}
	if remote == "" {
		remote = "origin"
	}
	url, err := exec.Command("git", "-C", repo, "remote", "get-url", remote).Output()
	if err != nil {
		return nil, fmt.Errorf("read the %s remote: %w", remote, err)
	}
	slug, err := forge.ParseRepo(string(url))
	if err != nil {
		return nil, err
	}
	token, _, err := resolveForgeToken()
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, fmt.Errorf("no %s is set, so the GitHub surface has no credential", forge.TokenEnv)
	}
	return forge.GitHub{Token: token, Repo: slug}, nil
}
