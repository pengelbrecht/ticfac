// Package forge is the code-hosting surface behind the PR + CI close-out
// rule a target repository may declare in its `.tick/config.md`: the four
// questions and one answer the reconciler puts to the forge the code lives
// on — is there an open PR for the epic branch, open one if not, what does
// CI say on its head, and carry this body onto the PR so the person merging
// reads the run's record beside its CI status.
//
// It exists as a package of its own for the same reason the executor seam
// does: the reconciler names the QUESTIONS (the interface) and the host
// supplies the answers (this implementation), so a run against a repository
// whose code is hosted somewhere else is a new implementation of the seam
// rather than a fork of the reconciler. The interface is deliberately four
// methods wide — everything a close-out needs and nothing a future caller
// could abuse into a general GitHub client: this package never merges, never
// comments, and never writes anything but the epic pull request the run's
// own configuration demands — its existence, and the body that carries the
// review's verdict and the run's findings to the person the merge belongs
// to.
//
// The GitHub implementation is stdlib-only: net/http, encoding/json, and a
// bearer token resolved from ONE ladder (ResolveTokenFrom): GITHUB_TOKEN
// first, then gh's own `auth token`. The gh rung ships INSIDE the ladder
// rather than being left to each caller, because two surfaces that each
// answered for a credential were the gap this tick closes — doctor accepted
// gh while the run's surface read the environment only. Third-party tooling
// (a `gh` CLI, an App installation) remains an optional rung, never a
// dependency of this one: the environment answers first, and a host with no
// gh at all loses nothing it ever had.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/httpnet"
	// A test binary that reads the run's factory token door sheds a live
	// run's control plane first (runenv's init).
	_ "github.com/pengelbrecht/ticfac/internal/runenv"
)

// DefaultAPI is GitHub's REST API, the forge this implementation speaks.
const DefaultAPI = "https://api.github.com"

// TokenEnv is the first rung of the credential ladder: the environment
// variable the GitHub surface reads its credential from. It is the name
// the factory's credential ladder already uses (internal/factory's
// SecretGitHubToken), so an operator who has provisioned a token for ticfac
// provisions ONE name, not one per subsystem.
const TokenEnv = "GITHUB_TOKEN"

// TokenSource names which rung of the credential ladder answered — the
// detail doctor reports and the caller's record carries, because a
// credential answered for is only half the answer until it says where it
// came from (tick vo4).
type TokenSource string

const (
	// TokenSourceEnv is the GITHUB_TOKEN rung, the one an operator
	// provisions deliberately, and the one that answers first.
	TokenSourceEnv TokenSource = TokenEnv
	// TokenSourceGH is gh's own `auth token` — the credential a person who
	// logged in through gh already holds, and the rung that made doctor's ok
	// optimistic while the run's surface read the environment only (tick
	// vo4).
	TokenSourceGH TokenSource = "gh auth token"
)

// ghAuthToken is the gh rung of the ladder, a seam for the same reason
// doctor's probes are: the answer is the environment's, and a test that
// consulted the host's real gh would report it present once and absent
// another time — not a test. The production value runs `gh auth token`;
// the LADDER, not this seam, decides what its answer means.
var ghAuthToken = func() ([]byte, error) {
	return exec.Command("gh", "auth", "token").Output()
}

// ResolveTokenFrom answers the credential the GitHub surface speaks with:
// the token, and which rung of the ladder it came from — GITHUB_TOKEN
// first, then gh's own auth (tick vo4). ONE ladder, because two surfaces
// that each answered for a credential were the gap: doctor accepted `gh
// auth token` while run-epic's surface read the environment only, so a
// machine with gh authed and no GITHUB_TOKEN passed doctor and was refused
// by the run it had been checked for. Both ask this ladder now, so one
// answer is one answer.
//
// The gh rung is still the optional rung this package always shipped:
// GITHUB_TOKEN answers first, gh is consulted only when the environment
// holds nothing, and a gh that does not answer is a refusal naming BOTH
// rungs — the two fixes there are. An empty token remains a supported state
// — the reconciler refuses a run whose target repository declares the
// close-out rule with no surface behind it, and that refusal is where an
// operator learns to provision one, not a startup crash.
func ResolveTokenFrom() (token string, source TokenSource, err error) {
	if token := strings.TrimSpace(os.Getenv(TokenEnv)); token != "" {
		return token, TokenSourceEnv, nil
	}
	out, err := ghAuthToken()
	if err != nil {
		return "", "", fmt.Errorf("no %s is set, and gh auth token did not answer: %v", TokenEnv, err)
	}
	if token := strings.TrimSpace(string(out)); token != "" {
		return token, TokenSourceGH, nil
	}
	return "", "", fmt.Errorf("no %s is set, and gh auth token printed no token", TokenEnv)
}

// PullRequest is the epic integration PR: the head ref this run integrates
// on, the base ref it asks to merge into, and where the forge says it lives.
// Body is the body the PR carries as the forge READS it back (tick aqm) — the
// field Find and Open decode, never a copy of what this process last wrote,
// because the close-out's carried check is a round trip: it asks the forge
// what the PR says now, and a PR a person (or a lying surface) stripped a
// finding from must be read as it stands, not as the run remembers it.
type PullRequest struct {
	Number  int
	URL     string
	HeadRef string
	HeadSHA string
	BaseRef string
	Body    string
}

// CIState is what CI says on the PR's head, closed. The vocabulary is closed
// because each member sends the next repair somewhere different: `green`
// admits the close-out, `red` is a failing job to name, `pending` is a wait
// the run bounds, and `none` is a workflow that never ran on the PR at all —
// the failure mode the rule exists to catch, because a workflow that does
// not trigger on pull_request makes the close-out's precondition
// unsatisfiable rather than merely unmet.
type CIState string

const (
	CIGreen   CIState = "green"
	CIRed     CIState = "red"
	CIPending CIState = "pending"
	CINone    CIState = "none"
)

// CIReport is CI's answer on one PR head: its state, and — the whole point
// of the typed refusal that consumes it — the NAMES of the jobs that failed,
// so the refusal a person reads names the job and not "CI".
type CIReport struct {
	State   CIState
	Failing []string
	// FailingRuns are the Actions workflow runs behind the failing checks,
	// for a caller that may re-run them (CIRerunner). Empty when the forge
	// could not tell which run a check belongs to.
	FailingRuns []int64
	// CancelledRuns are the Actions workflow runs behind checks whose latest
	// run concluded cancelled or stale — the checks a later push superseded
	// (ci.yml's cancel-in-progress). Nothing ever re-runs them by itself, so
	// the commit they were about has NO verdict until something does: a
	// caller that needs one may restart them (CIRestarter).
	CancelledRuns []int64
}

// CIRerunner is the optional half of the CI seam: re-run the failed jobs of
// workflow runs, ONCE each. A forge that cannot is simply not one, and the
// close-out then refuses red CI exactly as it always did.
//
// Once is durable without any state of ours: GitHub numbers a workflow run's
// attempts (run_attempt), so a run already past its first attempt is left
// alone. A restarted reconciler therefore cannot retry a second time, and a
// genuinely red job fails the close-out on its second red, as it should.
type CIRerunner interface {
	RerunFailedOnce(ctx context.Context, runIDs []int64) (rerun []int64, err error)
}

// CIRestarter is the other optional half of the CI seam: restart workflow
// runs that were CANCELLED before they concluded, ONCE each (epic-6in's
// close-out, 2026-09-28). A run superseded by a later push is cancelled by
// the workflow's own concurrency rule, and when the later push changed only
// paths the workflow ignores, no run replaces it: the last commit that
// changed code then has no executed verdict at all, and waiting cannot give
// it one. Restarting is how the verdict is made to exist.
//
// Once, by GitHub's own run_attempt, for the same reason CIRerunner is: a
// restarted reconciler cannot restart a run twice.
type CIRestarter interface {
	RestartCancelledOnce(ctx context.Context, runIDs []int64) (restarted []int64, err error)
}

// CIDispatcher is the third optional half of the CI seam: start the CI
// workflow on a branch when the code it carries has NO run at all — not a
// cancelled one to restart, nothing (a push whose run was cancelled while
// still queued leaves no check run behind, and the pushes after it changed
// only ignored paths). The workflow must declare workflow_dispatch.
type CIDispatcher interface {
	DispatchWorkflow(ctx context.Context, workflow, ref string) error
}

// PullRequests is the seam the close-out rule needs: find the PR for the
// epic branch, open one if it does not exist, ask what CI says on it, and
// write the body the PR carries (tick 4sb).
//
// Find answers nil (and no error) when no open PR exists for headRef: "no PR
// yet" is the state the rule is about, not a failure. Open is idempotent
// against Find in the way the forge enforces it (GitHub allows one open PR
// per head branch), so a resumed run that was cut between the Find and the
// Open finds the one the previous incarnation opened.
//
// UpdateBody is the write half, and it is an EDIT of the body rather than a
// comment on purpose (tick 4sb). The durable record is the run's own state —
// the decisions and the findings drafts — and the PR's body is a VIEW of
// that record, recomposed and overwritten every time the close-out writes
// it. A comment would APPEND, and appending is a shape no resume can survive
// honestly: a run cut after one write and resumed into a second would post
// the findings twice, and the second copy would be as authoritative-looking
// as the first while being pure noise. An overwrite makes the write
// idempotent by construction — the same record composes the same body, and
// writing it twice leaves the PR carrying each fact exactly once — so the
// seam needs no "did I already post this" bookkeeping the durable record
// would then have to agree with.
type PullRequests interface {
	Find(ctx context.Context, headRef, baseRef string) (*PullRequest, error)
	Open(ctx context.Context, headRef, baseRef, title, body string) (*PullRequest, error)
	UpdateBody(ctx context.Context, pr PullRequest, body string) error
	CI(ctx context.Context, pr PullRequest) (CIReport, error)
}

// GitHub speaks the seam against GitHub's REST API.
//
// Repo is the `owner/name` the API addresses; ParseRepo resolves it from a
// GitHub remote URL (and refuses a remote on a host api.github.com cannot
// speak with). Token is required by the API for every one of the four
// operations. Client is optional (http.DefaultClient with a timeout when
// nil); API is optional (DefaultAPI) — and nothing a run constructs ever
// sets it, which is why ParseRepo checks the host.
type GitHub struct {
	Token  string
	API    string
	Repo   string
	Client *http.Client
	// Refresh, when set, answers the token each call speaks with, falling
	// back to Token when it cannot (epic dm6). A cloud run on the factory's
	// GitHub App rung boots with an installation token that dies an hour
	// later while the run lives up to six, so its forge asks the factory for
	// the current one instead of holding the first — see FactoryTokenSource.
	Refresh func(ctx context.Context) (string, error)
}

// githubHosts are the hosts GitHub's own remotes live on: github.com
// itself, the www form a checkout may carry, and ssh.github.com — the host
// ssh-over-443 addresses. Repositories on all of them are addressed by
// api.github.com, the API the surface below speaks; no run ever wires that
// surface to another API, so no OTHER host is a repository this surface can
// do anything with.
var githubHosts = map[string]bool{
	"github.com":     true,
	"www.github.com": true,
	"ssh.github.com": true,
}

// ParseRepo resolves the `owner/name` a GitHub remote addresses, in the
// forms a git checkout actually carries: scp-like SSH
// (`git@github.com:owner/name.git`), scheme URLs
// (`https://github.com/owner/name.git`), and proxied forms that prepend
// path segments, where the owner/repo pair is the LAST two segments — the
// same resolution the cloud command line performs on its own remotes.
//
// The HOST is checked, not just parsed (tick 4zo): the GitHub surface this
// package builds always addresses api.github.com — its API field is
// optional, but nothing a run constructs ever sets it — so a remote on
// another host (gitlab.com, bitbucket.org, a GitHub Enterprise host) is a
// remote the surface cannot speak with. Before the check, such a remote
// resolved to an owner/name slug that passed every reader that means
// "GitHub remote" — init's close-out guess, doctor's remote check, the
// run's own surface — and the failure surfaced only at close-out time, as
// 404s against api.github.com the operator could no longer act on. The
// check lives HERE, in the one reader all of those share, so they cannot
// disagree about what counts as a GitHub remote. A GitHub Enterprise host
// is refused with the same words until the surface's API field is wired to
// a host of its own.
func ParseRepo(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimSuffix(remote, "/")
	var path string
	switch {
	case remote == "":
		return "", fmt.Errorf("no remote to resolve a repository from")
	case strings.Contains(remote, "://"):
		rest := remote[strings.Index(remote, "://")+len("://"):]
		slash := strings.IndexByte(rest, '/')
		if slash == -1 {
			return "", fmt.Errorf("unsupported remote format: %s", remote)
		}
		if err := requireGitHubHost(authorityHost(rest[:slash]), remote); err != nil {
			return "", err
		}
		path = rest[slash+1:]
	case strings.ContainsRune(remote, ':'):
		// scp-like SSH form: [user@]host:owner/name
		if err := requireGitHubHost(authorityHost(remote[:strings.IndexRune(remote, ':')]), remote); err != nil {
			return "", err
		}
		path = remote[strings.IndexRune(remote, ':')+1:]
	default:
		return "", fmt.Errorf("unsupported remote format: %s", remote)
	}
	path = strings.TrimSuffix(strings.TrimSpace(path), ".git")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-1] == "" || parts[len(parts)-2] == "" {
		return "", fmt.Errorf("no owner/name in the remote %s", remote)
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}

// authorityHost reads the host a remote's authority names: the [user@]host
// before the path of a scheme URL, or the [user@]host before the colon of
// the scp-like SSH form. A trailing :port (a scheme URL may carry one)
// names the same host and is trimmed; the scp-like form never reaches here
// with a port, because its first colon is the path separator.
func authorityHost(authority string) string {
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	if colon := strings.LastIndexByte(authority, ':'); colon >= 0 &&
		authority[colon+1:] != "" && strings.Trim(authority[colon+1:], "0123456789") == "" {
		authority = authority[:colon]
	}
	return authority
}

// requireGitHubHost refuses a remote on a host the GitHub surface cannot
// speak with, naming the host it found and github.com it needed — the one
// refusal every reader that means "GitHub remote" shares, so an operator
// meets it where a fix is still cheap (tick 4zo).
func requireGitHubHost(host, remote string) error {
	if host == "" {
		return fmt.Errorf("unsupported remote format: %s", remote)
	}
	if !githubHosts[strings.ToLower(host)] {
		return fmt.Errorf("the remote %s is hosted on %s, not github.com: the GitHub surface "+
			"speaks api.github.com only, and no run wires it to another API", remote, host)
	}
	return nil
}

func (g GitHub) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return httpnet.Client(30 * time.Second)
}

func (g GitHub) api() string {
	if g.API != "" {
		return strings.TrimSuffix(g.API, "/")
	}
	return DefaultAPI
}

// call performs one REST call, decoding a successful body into out. A
// non-2xx answer is an error carrying the status and the API's own message,
// bounded: an error a person reads names what the forge refused, and the
// refusal a typed close-out refusal carries forward names its cause.
func (g GitHub) call(ctx context.Context, method, path string, body any, out any) error {
	token := g.Token
	if g.Refresh != nil {
		// A refresh that fails is not the call failing: the token the run
		// booted with may well still be live, and if it is not, GitHub's own
		// 401 below says so with the call it refused.
		if fresh, err := g.Refresh(ctx); err == nil && fresh != "" {
			token = fresh
		}
	}
	if token == "" {
		return fmt.Errorf("the GitHub surface has no token: set %s, or gh auth login", TokenEnv)
	}
	if g.Repo == "" {
		return fmt.Errorf("the GitHub surface addresses no repository: owner/name is empty")
	}
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.api()+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ticfac")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var message struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &message)
		return fmt.Errorf("GitHub answered %d for %s %s: %s", resp.StatusCode, method, path,
			firstLine(message.Message))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode GitHub's answer for %s %s: %w", method, path, err)
	}
	return nil
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}

// Find answers the open PR for headRef, nil when none exists. The baseRef is
// the one the run would open into; it is carried for the caller's record, and
// matched only where the forge allows more than one open PR per head —
// GitHub does not, which is why Find matches the HEAD alone: the one open PR
// for a branch is the PR for that branch, whatever this run would have asked.
func (g GitHub) Find(ctx context.Context, headRef, baseRef string) (*PullRequest, error) {
	owner, _, ok := splitRepo(g.Repo)
	if !ok {
		return nil, fmt.Errorf("the GitHub surface addresses no repository: %q", g.Repo)
	}
	var found []struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := g.call(ctx, http.MethodGet,
		"/repos/"+g.Repo+"/pulls?head="+owner+":"+headRef+"&state=open", nil, &found); err != nil {
		return nil, err
	}
	for i := range found {
		pr := found[i]
		if pr.Head.Ref != headRef {
			continue
		}
		return &PullRequest{
			Number: pr.Number, URL: pr.HTMLURL, HeadRef: pr.Head.Ref, HeadSHA: pr.Head.SHA,
			BaseRef: pr.Base.Ref, Body: pr.Body,
		}, nil
	}
	return nil, nil
}

// Open creates the PR for headRef into baseRef.
func (g GitHub) Open(ctx context.Context, headRef, baseRef, title, body string) (*PullRequest, error) {
	var created struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := g.call(ctx, http.MethodPost, "/repos/"+g.Repo+"/pulls",
		map[string]string{"title": title, "head": headRef, "base": baseRef, "body": body}, &created); err != nil {
		return nil, err
	}
	return &PullRequest{
		Number: created.Number, URL: created.HTMLURL, HeadRef: created.Head.Ref, HeadSHA: created.Head.SHA,
		BaseRef: created.Base.Ref, Body: created.Body,
	}, nil
}

// UpdateBody rewrites the PR's body — the write half of the close-out rule
// (tick 4sb), and the one thing this seam could not do until now: put the
// run's record where the person merging reads it. The body is a VIEW of the
// run's durable state, recomposed by the close-out on every admission and
// again at its close, so this method is a pure overwrite: see the interface
// for why it is an edit and not a comment.
func (g GitHub) UpdateBody(ctx context.Context, pr PullRequest, body string) error {
	if pr.Number == 0 {
		return fmt.Errorf("the PR to rewrite names no number to address")
	}
	return g.call(ctx, http.MethodPatch, "/repos/"+g.Repo+"/pulls/"+strconv.Itoa(pr.Number),
		map[string]string{"body": body}, nil)
}

// checkRun is one check run as the GitHub API answers it.
type checkRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	StartedAt  string `json:"started_at"`
	DetailsURL string `json:"details_url"`
}

// CheckRun is one check's latest run on a head — the per-check facts a
// status surface carries beside the classification [GitHub.CI] derives
// from the same reduction: the check's name, whether it has finished, and
// what it concluded.
type CheckRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	StartedAt  string `json:"started_at"`
}

// fetchCheckRuns asks the forge for every check run it recorded for the
// PR's head sha.
func (g GitHub) fetchCheckRuns(ctx context.Context, pr PullRequest) ([]checkRun, error) {
	if pr.HeadSHA == "" {
		return nil, fmt.Errorf("the PR #%d names no head sha to read CI from", pr.Number)
	}
	var answer struct {
		TotalCount int        `json:"total_count"`
		CheckRuns  []checkRun `json:"check_runs"`
	}
	if err := g.call(ctx, http.MethodGet, "/repos/"+g.Repo+"/commits/"+pr.HeadSHA+"/check-runs", nil, &answer); err != nil {
		return nil, err
	}
	return answer.CheckRuns, nil
}

// reduceCheckRuns keeps the LATEST run of each check, in first-seen order —
// the reduction both [GitHub.CI] and [GitHub.Checks] read, so the
// classification and the per-check facts it classifies can never disagree
// about which run won.
//
// The LATEST run of each check decides, not every run ever made. One head
// carries several runs of the same job: CI triggers on both push and
// pull_request for an epic branch, and a re-run adds another. Counting
// all of them let one old failure veto a green re-run forever - an epic
// close-out held red on 'go failed' while the same job had passed on the
// same head (wne, 2026-09-23). started_at is RFC 3339, so it orders as a
// string; a run not yet started sorts first and so only wins alone.
//
// A SKIPPED run never supersedes one that actually ran. CI skips its
// pull_request run for an epic branch (the push run on the same head
// already carries the checks), and a later-started skip would otherwise
// shadow a red push run and read as not-failing: a false green. A run that
// executed replaces a skip whatever the order.
func reduceCheckRuns(runs []checkRun) (latest map[string]checkRun, order []string) {
	latest = map[string]checkRun{}
	for _, run := range runs {
		prev, seen := latest[run.Name]
		if !seen {
			order = append(order, run.Name)
		}
		switch {
		case !seen:
			latest[run.Name] = run
		case run.Conclusion == "skipped" && prev.Conclusion != "skipped":
		case prev.Conclusion == "skipped" && run.Conclusion != "skipped":
			latest[run.Name] = run
		case run.StartedAt > prev.StartedAt:
			latest[run.Name] = run
		}
	}
	return latest, order
}

// Checks answers the latest run of every check the forge recorded for the
// PR's head, per check. Empty when the forge recorded none — the same
// `none` the classification names, as the fact it is.
func (g GitHub) Checks(ctx context.Context, pr PullRequest) ([]CheckRun, error) {
	runs, err := g.fetchCheckRuns(ctx, pr)
	if err != nil {
		return nil, err
	}
	latest, order := reduceCheckRuns(runs)
	out := make([]CheckRun, 0, len(order))
	for _, name := range order {
		run := latest[name]
		out = append(out, CheckRun{
			Name:       run.Name,
			Status:     run.Status,
			Conclusion: run.Conclusion,
			StartedAt:  run.StartedAt,
		})
	}
	return out, nil
}

// CI answers what CI says on the PR's head, from the check runs the forge
// recorded for its SHA. The classification is closed and conservative:
// a check that has not concluded leaves the whole report `pending` (a green
// report beside a running one is not a verdict), and a concluded check fails
// the report when its conclusion names a failure — `failure` and `timed_out`
// — while `neutral` and `skipped` checks neither pass nor fail it, the way
// GitHub itself treats them.
//
// And GREEN NEEDS A CHECK THAT RAN (epic-6in, 2026-09-28). A head whose every
// latest check was skipped or neutral is not a head CI passed, it is a head
// CI never looked at: ci.yml skips its pull_request jobs for an epic branch
// (the push run carries them), and a push that changed only .ticfac/ starts
// no push run at all — so 6in's checkpoint head carried five skipped checks
// and nothing else, read green, and admitted a close-out over a red go job
// on the code one commit back. Such a head answers `none`: no CI for this
// code yet, which is exactly the state the caller's walk to the commit that
// changed code (and its wait) exists for.
func (g GitHub) CI(ctx context.Context, pr PullRequest) (CIReport, error) {
	runs, err := g.fetchCheckRuns(ctx, pr)
	if err != nil {
		return CIReport{}, err
	}
	if len(runs) == 0 {
		return CIReport{State: CINone}, nil
	}
	latest, order := reduceCheckRuns(runs)
	// The verdict is a function of the SET of latest runs, never of the order
	// the API lists them in (tick 89g: a property test found a failure beside
	// a cancelled run read red in one order and pending in the other). In
	// precedence: any check still running makes the report pending (a green
	// beside a running check is not a verdict); otherwise any failure makes it
	// red; otherwise any cancelled/stale/action_required run makes it pending
	// (re-runnable, and never a false green); otherwise green.
	report := CIReport{State: CIGreen}
	var running, failed, unsettled, executed bool
	seenRun := map[int64]bool{}
	seenCancelled := map[int64]bool{}
	for _, name := range order {
		run := latest[name]
		if run.Status != "completed" {
			running = true
			continue
		}
		switch run.Conclusion {
		case "failure", "timed_out":
			failed = true
			report.Failing = append(report.Failing, run.Name)
			if id := actionsRunID(run.DetailsURL); id != 0 && !seenRun[id] {
				seenRun[id] = true
				report.FailingRuns = append(report.FailingRuns, id)
			}
		case "success":
			executed = true
		case "neutral", "skipped":
			// Neither fails the report nor rescues a pending one — and
			// neither is a check that RAN, so neither can make it green.
		default:
			// cancelled, action_required, stale: not green, and saying the
			// report is green would be the false close this seam exists to
			// prevent. They are fixed by running them again (CIRestarter).
			unsettled = true
			if run.Conclusion == "cancelled" || run.Conclusion == "stale" {
				if id := actionsRunID(run.DetailsURL); id != 0 && !seenCancelled[id] {
					seenCancelled[id] = true
					report.CancelledRuns = append(report.CancelledRuns, id)
				}
			}
		}
	}
	switch {
	case running:
		report.State = CIPending
	case failed:
		report.State = CIRed
	case unsettled:
		report.State = CIPending
	case !executed:
		// Every latest check was skipped or neutral: nothing ran on this
		// code, so there is no verdict to call green.
		report.State = CINone
	}
	return report, nil
}

// actionsRunIDPattern finds the workflow run in a check run's details URL:
// https://github.com/<owner>/<repo>/actions/runs/<run>/job/<job>.
var actionsRunIDPattern = regexp.MustCompile(`/actions/runs/([0-9]+)`)

func actionsRunID(detailsURL string) int64 {
	m := actionsRunIDPattern.FindStringSubmatch(detailsURL)
	if m == nil {
		return 0
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// RerunFailedOnce re-runs the failed jobs of each workflow run still on its
// FIRST attempt, and answers which it re-ran. A run already re-run is left
// alone: once, by GitHub's own count.
func (g GitHub) RerunFailedOnce(ctx context.Context, runIDs []int64) ([]int64, error) {
	var rerun []int64
	for _, id := range runIDs {
		var run struct {
			RunAttempt int `json:"run_attempt"`
		}
		path := "/repos/" + g.Repo + "/actions/runs/" + strconv.FormatInt(id, 10)
		if err := g.call(ctx, http.MethodGet, path, nil, &run); err != nil {
			return rerun, err
		}
		if run.RunAttempt > 1 {
			continue
		}
		if err := g.call(ctx, http.MethodPost, path+"/rerun-failed-jobs", nil, nil); err != nil {
			return rerun, err
		}
		rerun = append(rerun, id)
	}
	return rerun, nil
}

// RestartCancelledOnce re-runs each CANCELLED workflow run still on its
// first attempt — the whole run, since a cancelled run has no failed jobs to
// re-run — and answers which it restarted. A run already past its first
// attempt is left alone: once, by GitHub's own count.
func (g GitHub) RestartCancelledOnce(ctx context.Context, runIDs []int64) ([]int64, error) {
	var restarted []int64
	for _, id := range runIDs {
		var run struct {
			RunAttempt int `json:"run_attempt"`
		}
		path := "/repos/" + g.Repo + "/actions/runs/" + strconv.FormatInt(id, 10)
		if err := g.call(ctx, http.MethodGet, path, nil, &run); err != nil {
			return restarted, err
		}
		if run.RunAttempt > 1 {
			continue
		}
		if err := g.call(ctx, http.MethodPost, path+"/rerun", nil, nil); err != nil {
			return restarted, err
		}
		restarted = append(restarted, id)
	}
	return restarted, nil
}

// DispatchWorkflow starts the workflow (a path like .github/workflows/ci.yml,
// or its file name) on ref through GitHub's workflow_dispatch event. The run
// attaches its check runs to the ref's head commit.
func (g GitHub) DispatchWorkflow(ctx context.Context, workflow, ref string) error {
	name := workflow
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || ref == "" {
		return fmt.Errorf("a workflow dispatch names a workflow and a ref: %q on %q", workflow, ref)
	}
	return g.call(ctx, http.MethodPost, "/repos/"+g.Repo+"/actions/workflows/"+name+"/dispatches",
		map[string]string{"ref": ref}, nil)
}

func splitRepo(repo string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(repo, "/")
	return owner, name, ok && owner != "" && name != ""
}
