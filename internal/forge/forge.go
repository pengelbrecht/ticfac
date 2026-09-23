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
// bearer token the operator's environment holds. Third-party tooling (a `gh`
// CLI, an App installation) is an optional rung a host may build on, never a
// dependency of this one.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultAPI is GitHub's REST API, the forge this implementation speaks.
const DefaultAPI = "https://api.github.com"

// TokenEnv is the one environment variable the GitHub surface reads its
// credential from. It is the name the factory's credential ladder already
// uses (internal/factory's SecretGitHubToken), so an operator who has
// provisioned a token for ticfac provisions ONE name, not one per subsystem.
const TokenEnv = "GITHUB_TOKEN"

// ResolveToken is the optional rung this package ships: the token the
// environment holds, empty when it holds none. An empty token is a
// supported state — the reconciler refuses a run whose target repository
// declares the close-out rule with no surface behind it, and that refusal
// is where an operator learns to provision one, not a startup crash.
func ResolveToken() string {
	return strings.TrimSpace(os.Getenv(TokenEnv))
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
// git remote URL. Token is required by the API for every one of the four
// operations. Client is optional (http.DefaultClient with a timeout when
// nil); API is optional (DefaultAPI).
type GitHub struct {
	Token  string
	API    string
	Repo   string
	Client *http.Client
}

// ParseRepo resolves the `owner/name` a remote URL addresses, in the forms a
// git checkout actually carries: scp-like SSH (`git@host:owner/name.git`),
// scheme URLs (`https://host/owner/name.git`), and proxied forms that
// prepend path segments, where the owner/repo pair is the LAST two segments
// — the same resolution the cloud command line performs on its own remotes.
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
		path = rest[slash+1:]
	case strings.ContainsRune(remote, ':'):
		// scp-like SSH form: [user@]host:owner/name
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

func (g GitHub) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
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
	if g.Token == "" {
		return fmt.Errorf("the GitHub surface has no token: set %s", TokenEnv)
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
	req.Header.Set("Authorization", "Bearer "+g.Token)
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

// CI answers what CI says on the PR's head, from the check runs the forge
// recorded for its SHA. The classification is closed and conservative:
// a check that has not concluded leaves the whole report `pending` (a green
// report beside a running one is not a verdict), and a concluded check fails
// the report when its conclusion names a failure — `failure` and `timed_out`
// — while `neutral` and `skipped` checks neither pass nor fail it, the way
// GitHub itself treats them.
func (g GitHub) CI(ctx context.Context, pr PullRequest) (CIReport, error) {
	if pr.HeadSHA == "" {
		return CIReport{}, fmt.Errorf("the PR #%d names no head sha to read CI from", pr.Number)
	}
	type checkRun struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		StartedAt  string `json:"started_at"`
		DetailsURL string `json:"details_url"`
	}
	var answer struct {
		TotalCount int        `json:"total_count"`
		CheckRuns  []checkRun `json:"check_runs"`
	}
	if err := g.call(ctx, http.MethodGet, "/repos/"+g.Repo+"/commits/"+pr.HeadSHA+"/check-runs", nil, &answer); err != nil {
		return CIReport{}, err
	}
	if len(answer.CheckRuns) == 0 {
		return CIReport{State: CINone}, nil
	}
	// The LATEST run of each check decides, not every run ever made. One head
	// carries several runs of the same job: CI triggers on both push and
	// pull_request for an epic branch, and a re-run adds another. Counting
	// all of them let one old failure veto a green re-run forever - an epic
	// close-out held red on 'go failed' while the same job had passed on the
	// same head (wne, 2026-09-23). started_at is RFC 3339, so it orders as a
	// string; a run not yet started sorts first and so only wins alone.
	latest := map[string]checkRun{}
	var order []string
	for _, run := range answer.CheckRuns {
		prev, seen := latest[run.Name]
		if !seen {
			order = append(order, run.Name)
		}
		if !seen || run.StartedAt > prev.StartedAt {
			latest[run.Name] = run
		}
	}
	report := CIReport{State: CIGreen}
	seenRun := map[int64]bool{}
	for _, name := range order {
		run := latest[name]
		if run.Status != "completed" {
			report.State = CIPending
			continue
		}
		switch run.Conclusion {
		case "failure", "timed_out":
			if report.State != CIPending {
				report.State = CIRed
			}
			report.Failing = append(report.Failing, run.Name)
			if id := actionsRunID(run.DetailsURL); id != 0 && !seenRun[id] {
				seenRun[id] = true
				report.FailingRuns = append(report.FailingRuns, id)
			}
		case "success", "neutral", "skipped":
			// Neither fails the report nor rescues a pending one.
		default:
			// cancelled, action_required, stale: not green, and saying the
			// report is green would be the false close this seam exists to
			// prevent. They read as pending — a state a person can fix by
			// re-running — unless something already failed outright.
			if report.State == CIGreen {
				report.State = CIPending
			}
		}
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

func splitRepo(repo string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(repo, "/")
	return owner, name, ok && owner != "" && name != ""
}
