package cli

// `ticfac factory wait-deployed <sha>` — block until the factory runs a fix.
//
// Agents waiting for "my fix is live" hand-rolled loops over `gh run list
// --workflow deploy-factory.yml` that matched their own sha's run and waited
// for it to complete. Those loops never ended when that run was skipped or
// cancelled because a NEWER main commit superseded it — which is normal:
// deploy-factory deploys the newest green main head, and a pending deploy is
// replaced by the next one (one pending job per concurrency group). On
// 2026-10-01 several such pollers ran for one to two hours and had to be
// killed by hand.
//
// This command asks the question the way the workflow itself does. Done is
// read from the factory's own answer (GET /api/deployment, the same read
// `factory status` makes): the factory runs a commit that CONTAINS the sha,
// or a commit whose shipped tree the sha did not change (the workflow's own
// path check would skip it). Failure is read from GitHub: the newest
// deploy-factory run that could carry the sha failed, or CI on main failed
// for the newest commit carrying it, with nothing still in flight that could
// carry it instead. Anything else is waiting, until --timeout.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/factory"
	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

// factoryShippedPaths is what the factory ships, as deploy-factory.yml's path
// check names it (its `paths=(` block). A commit that changes none of these
// changes nothing the factory runs. TestFactoryShippedPathsMatchTheWorkflow
// keeps this list and the workflow's the same.
var factoryShippedPaths = []string{
	".github/workflows/deploy-factory.yml",
	"cloudflare", "image",
	// The harness package the Worker bundle links (epic 43y, tick xd3).
	"harness/src", "harness/package.json", "harness/pnpm-lock.yaml",
	"harness/pnpm-workspace.yaml", "harness/tsconfig.json",
	"embedded.go", "go.mod", "go.sum", "factory.pin.json", "contracts.pin.json", "contracts",
	"profiles", "profiles-cloudflare-sandbox", "profiles-herdr",
	"cmd/ticfac", "cmd/ticfac-exec-subprocess", "internal",
	":(exclude,glob)**/*_test.go",
	":(exclude,glob)**/testdata/**",
}

// errCommitNotFound is a sha that resolves neither locally (after a fetch)
// nor on GitHub.
var errCommitNotFound = errors.New("commit not found")

// workflowRun is one GitHub Actions run as `gh run list --json` reports it.
type workflowRun struct {
	ID         int64     `json:"databaseId"`
	HeadSHA    string    `json:"headSha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	CreatedAt  time.Time `json:"createdAt"`
	URL        string    `json:"url"`
	Event      string    `json:"event"`
}

// deployWaitSource is everything wait-deployed reads: the factory, the
// repository's history, and the two workflows. The real one shells out to
// git and gh and asks the factory over HTTP; tests substitute a fake.
type deployWaitSource interface {
	// ResolveCommit turns a ref into a full sha, or errCommitNotFound.
	ResolveCommit(ctx context.Context, ref string) (string, error)
	// Deployed is the factory's own answer to what it runs.
	Deployed(ctx context.Context) (*factory.DeployedFacts, error)
	// Contains reports whether descendant's history includes ancestor.
	Contains(ctx context.Context, ancestor, descendant string) (bool, error)
	// ShippedChanges lists the shipped files that differ between two commits.
	ShippedChanges(ctx context.Context, base, head string) ([]string, error)
	// DeployRuns lists recent deploy-factory.yml runs.
	DeployRuns(ctx context.Context) ([]workflowRun, error)
	// MainCIRuns lists recent ci.yml runs on main.
	MainCIRuns(ctx context.Context) ([]workflowRun, error)
}

// newDeployWaitSource builds the real source; tests replace it.
var newDeployWaitSource = func(repo string) deployWaitSource {
	return &ghDeployWaitSource{repo: repo}
}

// Default --timeout and poll interval.
const (
	defaultWaitDeployedTimeout  = 90 * time.Minute
	defaultWaitDeployedInterval = 30 * time.Second
)

const factoryWaitDeployedLong = `Block until the factory runs a commit that contains <sha> — the usual
argument is your merge commit on main — and exit by the outcome:

  0  done: the factory reports (GET /api/deployment) a commit that contains
     <sha>, or one whose shipped tree <sha> does not change
  1  failed: the deploy-factory run that would carry <sha> failed (the run is
     named with its conclusion), CI on main failed for the newest commit that
     carries it, or no factory is configured
  2  usage: a malformed invocation
  4  missing: <sha> resolves neither locally nor on GitHub
  5  running: --timeout passed with the deploy still not live

A deploy-factory run that is skipped or cancelled because a newer main commit
superseded it is normal and is not a failure: the newer commit's deploy
carries <sha> too, and this command keeps waiting for it. Use this, never a
hand-rolled gh polling loop: a loop keyed to one run's sha never ends when
that run is superseded.`

// newFactoryWaitDeployedCommand builds `factory wait-deployed`'s cobra command.
func newFactoryWaitDeployedCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait-deployed <sha>",
		Short: "block until the factory runs a commit containing <sha>",
		Long:  factoryWaitDeployedLong,
	}
	fs := newFlagSet("factory wait-deployed", nil)
	timeout := fs.Duration("timeout", defaultWaitDeployedTimeout, "give up (exit 5) after this long")
	interval := fs.Duration("interval", defaultWaitDeployedInterval, "how often to re-read the factory and GitHub")
	repo := fs.String("repo", "", "GitHub repository as owner/name (default: the one gh infers from this checkout)")
	asJSON := fs.Bool("json", false, "print one versioned document (ticfac.factory-wait-deployed.v1) with the outcome")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(runFactoryWaitDeployed(c.Context(), args, *timeout, *interval, *repo, *asJSON, stdout, stderr))
	}
	return cmd
}

// factoryWaitDeployedJSON is `factory wait-deployed --json`'s answer.
type factoryWaitDeployedJSON struct {
	agentDoc
	SHA             string       `json:"sha"`
	Reason          string       `json:"reason"`
	DeployedVersion string       `json:"deployed_version,omitempty"`
	DeployedCommit  string       `json:"deployed_commit,omitempty"`
	Run             *workflowRun `json:"run,omitempty"`
}

// deployVerdict is one observation: done, failed, or still waiting (running).
type deployVerdict struct {
	state  string // agentStateDone, agentStateFailed, agentStateRunning
	reason string
	facts  *factory.DeployedFacts
	run    *workflowRun
}

func runFactoryWaitDeployed(ctx context.Context, args []string, timeout, interval time.Duration, repo string, asJSON bool, stdout, stderr io.Writer) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return reportCommand("factory wait-deployed", newExitError(exitUsage, "factory wait-deployed takes exactly one argument: the commit sha to wait for"), stderr)
	}
	if timeout <= 0 || interval <= 0 {
		return reportCommand("factory wait-deployed", newExitError(exitUsage, "--timeout and --interval must be positive durations"), stderr)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	src := newDeployWaitSource(repo)
	sha, err := src.ResolveCommit(ctx, strings.TrimSpace(args[0]))
	if errors.Is(err, errCommitNotFound) {
		return reportCommand("factory wait-deployed", newExitError(exitNotFound, "%s resolves to no commit, here or on GitHub", args[0]), stderr)
	}
	if err != nil {
		return reportCommand("factory wait-deployed", newExitError(exitGeneric, "cannot resolve %s: %v", args[0], err), stderr)
	}

	verdict := waitDeployed(ctx, src, sha, timeout, interval, stderr)

	if asJSON {
		doc := factoryWaitDeployedJSON{
			agentDoc: agentDoc{Schema: agentSchemaID("factory-wait-deployed"), State: verdict.state},
			SHA:      sha,
			Reason:   verdict.reason,
			Run:      verdict.run,
		}
		if verdict.facts != nil {
			doc.DeployedVersion = verdict.facts.Version
			doc.DeployedCommit = verdict.facts.Commit()
		}
		if err := emitAgentJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "ticfac factory wait-deployed: %v\n", err)
			return exitGeneric
		}
	} else {
		switch verdict.state {
		case agentStateDone:
			fmt.Fprintf(stdout, "deployed: %s — %s\n", shortSHA(sha), verdict.reason)
		case agentStateFailed:
			fmt.Fprintf(stderr, "ticfac factory wait-deployed: %s is not deployed — %s\n", shortSHA(sha), verdict.reason)
		default:
			fmt.Fprintf(stderr, "ticfac factory wait-deployed: timed out after %s waiting for %s — %s\n", timeout, shortSHA(sha), verdict.reason)
		}
	}
	return stateExitClass(verdict.state)
}

// waitDeployed polls until a verdict is done or failed, or until timeout,
// printing each new waiting reason to stderr once.
func waitDeployed(ctx context.Context, src deployWaitSource, sha string, timeout, interval time.Duration, stderr io.Writer) deployVerdict {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	obs := &deployObserver{src: src, sha: sha, contains: map[string]bool{}}
	last := ""
	var prev *deployVerdict
	for {
		v := obs.observe(ctx)
		if ctx.Err() != nil && prev != nil {
			// The deadline cut this observation short (a killed gh call):
			// the last whole observation is the honest answer.
			return *prev
		}
		if v.state != agentStateRunning {
			return v
		}
		prev = &v
		if v.reason != last {
			fmt.Fprintf(stderr, "waiting for %s: %s\n", shortSHA(sha), v.reason)
			last = v.reason
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return v
		case <-timer.C:
		}
	}
}

// deployObserver makes one observation at a time, caching ancestry answers
// (history does not change under a commit).
type deployObserver struct {
	src      deployWaitSource
	sha      string
	contains map[string]bool
}

func (o *deployObserver) containsSHA(ctx context.Context, ancestor, descendant string) (bool, error) {
	key := ancestor + ".." + descendant
	if v, ok := o.contains[key]; ok {
		return v, nil
	}
	v, err := o.src.Contains(ctx, ancestor, descendant)
	if err != nil {
		return false, err
	}
	o.contains[key] = v
	return v, nil
}

// failedConclusion is a completed run that went red. Skipped and cancelled
// are supersession or a CI that did not pass — not the deploy failing.
func failedConclusion(conclusion string) bool {
	switch conclusion {
	case "success", "skipped", "cancelled", "neutral", "":
		return false
	}
	return true
}

func (o *deployObserver) observe(ctx context.Context) deployVerdict {
	sha := o.sha
	v := deployVerdict{state: agentStateRunning}

	// 1. What the factory runs, by its own answer.
	facts, err := o.src.Deployed(ctx)
	switch {
	case errors.Is(err, factory.ErrNoFactory):
		return deployVerdict{state: agentStateFailed, reason: "no factory is configured in ~/.ticfacrc (ticfac factory setup)"}
	case err != nil:
		v.reason = "cannot read what the factory runs yet: " + err.Error()
	default:
		v.facts = facts
		deployed := facts.Commit()
		if deployed == "" {
			v.reason = fmt.Sprintf("the factory's version %q names no commit", facts.Version)
			break
		}
		if ok, err := o.containsSHA(ctx, sha, deployed); err == nil && ok {
			v.state = agentStateDone
			v.reason = fmt.Sprintf("the factory runs %s, which contains it", facts.Version)
			return v
		}
		if older, err := o.containsSHA(ctx, deployed, sha); err == nil && older {
			if changed, err := o.src.ShippedChanges(ctx, deployed, sha); err == nil && len(changed) == 0 {
				v.state = agentStateDone
				v.reason = fmt.Sprintf("the factory runs %s, and nothing it ships changed between %s and %s", facts.Version, shortSHA(deployed), shortSHA(sha))
				return v
			}
		}
		v.reason = fmt.Sprintf("the factory still runs %s", facts.Version)
	}

	// 2. Is anything that could carry the sha still going, or did it fail?
	deploys, err := o.src.DeployRuns(ctx)
	if err != nil {
		v.reason += "; cannot list deploy-factory runs: " + err.Error()
		return v
	}
	cis, err := o.src.MainCIRuns(ctx)
	if err != nil {
		v.reason += "; cannot list CI runs on main: " + err.Error()
		return v
	}
	carryingDeploys := o.carrying(ctx, deploys)
	carryingCI := o.carrying(ctx, cis)

	var inFlight *workflowRun
	for _, runs := range [][]workflowRun{carryingDeploys, carryingCI} {
		for i := range runs {
			if runs[i].Status != "completed" && inFlight == nil {
				r := runs[i]
				inFlight = &r
			}
		}
	}
	newestDeploy := newestDecided(carryingDeploys)
	newestCI := newestDecided(carryingCI)

	if inFlight == nil {
		if newestDeploy != nil && failedConclusion(newestDeploy.Conclusion) {
			return deployVerdict{
				state: agentStateFailed, facts: v.facts, run: newestDeploy,
				reason: fmt.Sprintf("deploy-factory run %d (main at %s) concluded %s, and nothing newer is in flight to carry it: %s — retry with `gh workflow run deploy-factory.yml`",
					newestDeploy.ID, shortSHA(newestDeploy.HeadSHA), newestDeploy.Conclusion, newestDeploy.URL),
			}
		}
		if newestCI != nil && failedConclusion(newestCI.Conclusion) &&
			(newestDeploy == nil || newestDeploy.CreatedAt.Before(newestCI.CreatedAt)) {
			return deployVerdict{
				state: agentStateFailed, facts: v.facts, run: newestCI,
				reason: fmt.Sprintf("CI on main for %s (run %d) concluded %s, so no deploy will carry it until a green commit lands: %s",
					shortSHA(newestCI.HeadSHA), newestCI.ID, newestCI.Conclusion, newestCI.URL),
			}
		}
	}

	switch {
	case inFlight != nil:
		v.run = inFlight
		v.reason += fmt.Sprintf("; run %d (main at %s) is %s", inFlight.ID, shortSHA(inFlight.HeadSHA), inFlight.Status)
	case newestDeploy != nil && newestDeploy.Conclusion == "success":
		v.run = newestDeploy
		v.reason += fmt.Sprintf("; deploy-factory run %d (main at %s) succeeded, waiting for the factory to report it", newestDeploy.ID, shortSHA(newestDeploy.HeadSHA))
	default:
		v.reason += "; no deploy-factory run carries it yet (a skipped or superseded deploy is normal)"
	}
	return v
}

// carrying keeps the runs whose head commit contains the sha, newest first.
// A run whose ancestry cannot be read is left out rather than guessed.
func (o *deployObserver) carrying(ctx context.Context, runs []workflowRun) []workflowRun {
	var out []workflowRun
	for _, r := range runs {
		if r.HeadSHA == "" {
			continue
		}
		if ok, err := o.containsSHA(ctx, o.sha, r.HeadSHA); err == nil && ok {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// newestDecided is the newest completed run that was not skipped or
// cancelled — the one whose verdict counts. runs is newest first.
func newestDecided(runs []workflowRun) *workflowRun {
	for i := range runs {
		r := runs[i]
		if r.Status == "completed" && r.Conclusion != "skipped" && r.Conclusion != "cancelled" {
			return &r
		}
	}
	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// ghDeployWaitSource is the real source: git in the current checkout first,
// the gh CLI (GitHub's API) when git cannot answer, and the factory's own
// GET /api/deployment.
type ghDeployWaitSource struct {
	repo    string
	fetched bool
}

func (s *ghDeployWaitSource) git(ctx context.Context, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, gitbin.Path(), args...)
	// Bounded transport: the one fetch this source makes must not hang on a
	// remote gone silent.
	cmd.Env = append(os.Environ(), gitbin.TransportEnv()...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return strings.TrimSpace(out.String()), exitErr.ExitCode(), fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(errOut.String()))
	}
	if err != nil {
		return "", -1, err
	}
	return strings.TrimSpace(out.String()), 0, nil
}

// fetchOnce brings origin's main in the first time a commit is missing. It
// writes only a private ref (never FETCH_HEAD or refs/remotes/origin/*, which
// a run or an operator in the same checkout may be fetching: tick wdb).
func (s *ghDeployWaitSource) fetchOnce(ctx context.Context) {
	if s.fetched {
		return
	}
	s.fetched = true
	_, _, _ = s.git(ctx, "fetch", "--no-write-fetch-head", "--refmap=", "--quiet", "origin", "+refs/heads/main:refs/ticfac/wait-deployed/main")
}

func (s *ghDeployWaitSource) gh(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if s.repo != "" {
		cmd.Env = append(os.Environ(), "GH_REPO="+s.repo)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}

func (s *ghDeployWaitSource) ResolveCommit(ctx context.Context, ref string) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if out, code, _ := s.git(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); code == 0 && out != "" {
			return out, nil
		}
		s.fetchOnce(ctx)
	}
	out, err := s.gh(ctx, "api", "repos/{owner}/{repo}/commits/"+ref, "--jq", ".sha")
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "422") || strings.Contains(err.Error(), "No commit found") {
			return "", errCommitNotFound
		}
		return "", err
	}
	if sha := strings.TrimSpace(string(out)); sha != "" {
		return sha, nil
	}
	return "", errCommitNotFound
}

func (s *ghDeployWaitSource) Deployed(ctx context.Context) (*factory.DeployedFacts, error) {
	facts, _, err := factory.ReadDeployed(ctx, "", nil)
	return facts, err
}

func (s *ghDeployWaitSource) Contains(ctx context.Context, ancestor, descendant string) (bool, error) {
	for attempt := 0; attempt < 2; attempt++ {
		_, code, _ := s.git(ctx, "merge-base", "--is-ancestor", ancestor, descendant)
		switch code {
		case 0:
			return true, nil
		case 1:
			return false, nil
		}
		s.fetchOnce(ctx)
	}
	// git cannot answer (not a checkout, or the commits are not here): ask
	// GitHub. compare A...B is "ahead" or "identical" exactly when B
	// contains A.
	out, err := s.gh(ctx, "api", "repos/{owner}/{repo}/compare/"+ancestor+"..."+descendant, "--jq", ".status")
	if err != nil {
		return false, err
	}
	switch status := strings.TrimSpace(string(out)); status {
	case "ahead", "identical":
		return true, nil
	case "behind", "diverged":
		return false, nil
	default:
		return false, fmt.Errorf("GitHub compare answered %q", status)
	}
}

func (s *ghDeployWaitSource) ShippedChanges(ctx context.Context, base, head string) ([]string, error) {
	args := append([]string{"diff", "--name-only", base, head, "--"}, factoryShippedPaths...)
	out, code, err := s.git(ctx, args...)
	if code != 0 {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

const runListFields = "databaseId,headSha,status,conclusion,createdAt,url,event"

func (s *ghDeployWaitSource) runs(ctx context.Context, args ...string) ([]workflowRun, error) {
	out, err := s.gh(ctx, append([]string{"run", "list", "--limit", "40", "--json", runListFields}, args...)...)
	if err != nil {
		return nil, err
	}
	var runs []workflowRun
	if err := json.Unmarshal(out, &runs); err != nil {
		return nil, fmt.Errorf("gh run list answered something that is not a run list: %w", err)
	}
	return runs, nil
}

func (s *ghDeployWaitSource) DeployRuns(ctx context.Context) ([]workflowRun, error) {
	return s.runs(ctx, "--workflow", "deploy-factory.yml")
}

func (s *ghDeployWaitSource) MainCIRuns(ctx context.Context) ([]workflowRun, error) {
	return s.runs(ctx, "--workflow", "ci.yml", "--branch", "main")
}
