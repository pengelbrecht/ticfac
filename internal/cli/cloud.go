package cli

// `ticfac cloud` — the operator's closed command surface over a self-deployed
// cloud factory. Ported from ticks' cmd/tk/cmd/cloud.go (the run, stop and
// status commands, the shared client and the tracker reads; the spawn/wait/
// wave/collect family stays with the local orchestrator), with the cobra
// plumbing replaced by this package's plain flag sets and every body otherwise
// verbatim — including the messages, which name `tk` commands exactly as ticks
// spelled them: this code is a copy of ticks' until ticks tick 1ya deletes it,
// and a copy that has already edited its own strings is not a copy.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory"
	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
)

// cloudCommand is the `cloud` group's dispatcher: args[0] names the
// subcommand, the rest are its flags and operands.
func cloudCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, cloudUsage)
		return exitUsage
	}
	name, rest := args[0], args[1:]
	if len(rest) > 0 && (rest[0] == "--help" || rest[0] == "-h") {
		fmt.Fprint(stdout, cloudUsage)
		return exitSuccess
	}
	switch name {
	case "run":
		return runCloudRun(ctx, rest, stdout, stderr)
	case "stop":
		return runCloudStop(ctx, rest, stdout, stderr)
	case "status":
		return runCloudStatus(ctx, rest, stdout, stderr)
	case "logs":
		return runCloudLogs(ctx, rest, stdout, stderr)
	case "trace":
		return runCloudTrace(ctx, rest, stdout, stderr)
	case "supervisor":
		return runCloudSupervisor(ctx, rest, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, cloudUsage)
		return exitSuccess
	default:
		fmt.Fprintf(stderr, "ticfac cloud: unknown subcommand %q\n\n%s", name, cloudUsage)
		return exitUsage
	}
}

// cloudUsage is the group's help: the closed vocabulary and why the open
// half (spawn, wait, collect, wave) is deliberately not here.
const cloudUsage = `ticfac cloud — run and inspect epics in your cloud factory

usage:
  ticfac cloud run <epic> [flags]        push the current branch and start a run
  ticfac cloud stop <run> [--now]         stop a live run, cleanly or right now
  ticfac cloud status [run]               runs, leases and queue; or one run
  ticfac cloud logs <run> [-f] [--tail N] what the container printed
  ticfac cloud trace <run>                what the model said and decided
  ticfac cloud supervisor <run> [--steps N] whether the Workflow is alive

run flags:
  --notify <channel>      notification channel for this submission
  --queue                 park behind the current project lease instead of refusing
  --tick-ids <a,b,c>      dispatch these ticks as one worker container each;
                          cannot be combined with --queue
  --max-cost <usd>        cost ceiling for this run; may lower the deployment
                          budget, never raise it
  --max-wall-clock <dur>  wall-clock ceiling for this run; may lower the
                          deployment budget, never raise it

The factory is self-deployed and authenticated with the factory_url and
factory_token entries in ~/.ticfacrc. There is deliberately no cloud steering
or mutation command beyond stop: stop a run, edit the tracker in a normal
checkout, and submit it again so the new orchestrator follows the reconcile
path. logs, trace and supervisor are observation: they read records a run left
behind and cannot steer one, so the operator-to-orchestrator command vocabulary
stays run/stop/status/answer (D21).
`

// cloudHTTPClient is a package variable so command tests can exercise the
// factory protocol without binding a loopback listener. Production calls use
// the ordinary client with a bounded timeout.
var cloudHTTPClient = &http.Client{Timeout: 15 * time.Second}

type cloudClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newCloudClient() (*cloudClient, error) {
	config, err := factory.LoadCredentials()
	if err != nil {
		return nil, fmt.Errorf("cannot read factory configuration: %w", err)
	}

	baseURL := strings.TrimRight(strings.TrimSpace(config.Get(credentials.KeyURL)), "/")
	token := strings.TrimSpace(config.Get(credentials.KeyToken))
	if baseURL == "" || token == "" {
		return nil, fmt.Errorf("no factory is configured; run 'tk factory setup' first")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("factory endpoint is invalid; run 'tk factory setup' to configure it")
	}
	if cloudHTTPClient == nil {
		cloudHTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &cloudClient{baseURL: baseURL, token: token, http: cloudHTTPClient}, nil
}

func (c *cloudClient) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode factory request: %w", err)
		}
		reader = strings.NewReader(string(encoded))
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("create factory request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("factory request %s %s failed: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, fmt.Errorf("read factory response: %w", readErr)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, cloudAPIError{status: resp.StatusCode, body: data}
	}
	return data, nil
}

type cloudAPIError struct {
	status int
	body   []byte
}

func (e cloudAPIError) Error() string {
	var response struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
		Reason string `json:"reason"`
		RunID  string `json:"run_id"`
		Holder struct {
			RunID string `json:"run_id"`
		} `json:"holder"`
	}
	_ = json.Unmarshal(e.body, &response)

	holderID := response.Holder.RunID
	if holderID == "" {
		const prefix = "lease_held_by:"
		if strings.HasPrefix(response.Reason, prefix) {
			holderID = strings.TrimPrefix(response.Reason, prefix)
		}
	}
	if e.status == http.StatusConflict && holderID != "" {
		return fmt.Sprintf(
			"cloud run refused: project lease is held by %s; rerun with --queue to park this submission",
			holderID,
		)
	}
	detail := strings.TrimSpace(response.Detail)
	if detail == "" {
		detail = strings.TrimSpace(response.Error)
	}
	if detail == "" {
		detail = strings.TrimSpace(response.Reason)
	}
	if detail == "" {
		detail = strings.TrimSpace(string(e.body))
	}
	if detail == "" {
		detail = http.StatusText(e.status)
	}
	if response.RunID != "" {
		detail += " (submission " + response.RunID + ")"
	}
	return fmt.Sprintf("factory returned HTTP %d: %s", e.status, detail)
}

type cloudRunRecord struct {
	RunID     string `json:"run_id"`
	Project   string `json:"project"`
	Epic      string `json:"epic"`
	BaseSHA   string `json:"base_sha"`
	State     string `json:"state"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}

type cloudHolder struct {
	RunID string `json:"run_id"`
	Epic  string `json:"epic"`
}

type cloudQueued struct {
	RunID     string `json:"run_id"`
	Project   string `json:"project"`
	Epic      string `json:"epic"`
	BaseSHA   string `json:"base_sha"`
	BlockedBy string `json:"blocked_by"`
}

type cloudPhase struct {
	State    string `json:"state"`
	Workflow struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"workflow"`
}

type cloudProjectStatus struct {
	Project string        `json:"project"`
	Lease   *cloudHolder  `json:"lease"`
	Queued  []cloudQueued `json:"queued"`
}

// cloudEffectiveBudget is what the factory says this run will ACTUALLY be bounded by
// (tick 7zk).
//
// It is reported because a submission may only LOWER a budget: an operator who
// asked for --max-cost 40 against a deployment ceiling of 8 gets a run bounded
// at $8, and the number that governs used to appear for the first time in the
// cancellation that ended the run. That is the third time in one epic a
// deployment ceiling silently replaced an operator's number.
//
// A pointer, because a factory deployed before this existed answers without
// the field, and printing "$0.00" at an older deployment would be worse than
// printing nothing.
type cloudEffectiveBudget struct {
	MaxCostUSD     float64  `json:"max_cost_usd"`
	MaxWallClockMS int64    `json:"max_wall_clock_ms"`
	RequestedCost  *float64 `json:"requested_max_cost_usd"`
	RequestedWall  *int64   `json:"requested_max_wall_clock_ms"`
	CostClamped    bool     `json:"cost_clamped"`
	WallClamped    bool     `json:"wall_clock_clamped"`
}

type cloudSubmissionResponse struct {
	Run    cloudRunRecord        `json:"run"`
	RunID  string                `json:"run_id"`
	Queued cloudQueued           `json:"queued"`
	Holder cloudHolder           `json:"holder"`
	Budget *cloudEffectiveBudget `json:"budget"`
}

// cloudRunImage is the orchestrator container image a run booted.
//
// It is reported because a container rollout is asynchronous: the Worker
// updates promptly, the container application does not, and a run started in
// that window executes the previous image. Without this line two runs with
// identical output across a deploy read as "the fix did not work" when the fix
// was simply never running.
type cloudRunImage struct {
	Ref    string `json:"image_ref"`
	Digest string `json:"image_digest"`
}

// cloudRunProgress is what the durable layer said about a finished run:
// whether any branch on origin actually moved while it was alive.
//
// It is reported because "completed" used to mean nothing more than "the
// harness exited 0", and a run that printed a paragraph and exited was
// therefore indistinguishable from one that dispatched a wave, merged it and
// closed the epic. The state now distinguishes them; this line says why.
type cloudRunProgress struct {
	Progress string `json:"progress"`
	Detail   string `json:"detail"`
}

type cloudStatusResponse struct {
	Run      cloudRunRecord       `json:"run"`
	Phase    cloudPhase           `json:"phase"`
	Lease    *cloudHolder         `json:"lease"`
	Queued   []cloudQueued        `json:"queued"`
	Runs     []cloudRunRecord     `json:"runs"`
	Projects []cloudProjectStatus `json:"projects"`
	Image    *cloudRunImage       `json:"image"`
	Progress *cloudRunProgress    `json:"progress"`
}

func decodeCloudJSON(data []byte, into any) error {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("decode factory response: %w", err)
	}
	return nil
}

// newFlagSet builds the flag set one subcommand parses with: errors are
// written to the command's stderr, never to the process's, so a test can
// read a usage refusal back without it leaking.
func newFlagSet(name string, errOutput io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	if errOutput != nil {
		fs.SetOutput(errOutput)
	} else {
		fs.SetOutput(io.Discard)
	}
	return fs
}

// setFlagsOf reports which flags the invocation actually passed, because
// "flag given" and "flag defaulted" are different questions for --max-cost
// (a zero that lowers nothing) and --tick-ids (an empty wave is a refusal).
// It stands in for cobra's Flags().Changed.
func setFlagsOf(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

func runCloudRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := cloudRun(ctx, args, stdout, stderr)
	return reportCommand("cloud run", err, stderr)
}

func cloudRun(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("cloud run", stderr)
	notify := fs.String("notify", "", "notification channel for this submission")
	queue := fs.Bool("queue", false, "park behind the current project lease instead of refusing")
	tickIDs := fs.String("tick-ids", "", "dispatch these ticks as one worker container each, comma-separated; cannot be combined with --queue")
	maxCost := fs.Float64("max-cost", 0, "cost ceiling in USD for this run; may lower the deployment budget, never raise it")
	maxWallClock := fs.Duration("max-wall-clock", 0, "wall-clock ceiling for this run (e.g. 45m); may lower the deployment budget, never raise it")
	rest, err := parseCollectingPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return newExitError(exitUsage, "%v", err)
	}
	if len(rest) != 1 || rest[0] == "" {
		return newExitError(exitUsage, "exactly one epic id is required")
	}
	set := setFlagsOf(fs)

	client, err := newCloudClient()
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	// Parsed before anything is pushed: a budget the factory would refuse must
	// not first cost a push and a lease.
	budget, err := cloudRunBudget(set, *maxCost, *maxWallClock)
	if err != nil {
		return newExitError(exitUsage, "%v", err)
	}
	wave, err := cloudRunWave(set, strings.Split(*tickIDs, ","), *queue)
	if err != nil {
		return err
	}
	root, err := cloudRepoRoot()
	if err != nil {
		return fmt.Errorf("failed to detect repo root: %w", err)
	}
	// The wave is proven against this checkout before the push, for the same
	// reason `tk cloud spawn` proves it: a container clones at the epic's base,
	// so a tick that is not in this epic would be implemented against a base
	// its own epic never chose.
	if len(wave) > 0 {
		if err := cloudSpawnCheckWave(ctx, root, rest[0], wave); err != nil {
			return err
		}
	}

	baseSHA, project, requestedBy, err := prepareCloudSubmission(ctx, root, rest[0])
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}

	submission := struct {
		Project     string   `json:"project"`
		Epic        string   `json:"epic"`
		BaseSHA     string   `json:"base_sha"`
		RequestedBy string   `json:"requested_by"`
		Notify      string   `json:"notify,omitempty"`
		Queue       bool     `json:"queue"`
		TickIDs     []string `json:"tick_ids,omitempty"`
		MaxCostUSD  float64  `json:"max_cost_usd,omitempty"`
		MaxWallMS   int64    `json:"max_wall_clock_ms,omitempty"`
	}{
		Project: project, Epic: rest[0], BaseSHA: baseSHA, RequestedBy: requestedBy,
		Notify: strings.TrimSpace(*notify), Queue: *queue,
		TickIDs:    wave,
		MaxCostUSD: budget.maxCostUSD, MaxWallMS: budget.maxWallClockMS,
	}

	data, err := client.request(ctx, http.MethodPost, "/api/runs", submission)
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	var response cloudSubmissionResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return newExitError(exitGeneric, "%v", err)
	}

	switch {
	case response.Run.RunID != "":
		fmt.Fprintf(stdout, "Cloud run started: %s\n", response.Run.RunID)
		if response.Run.State != "" {
			fmt.Fprintf(stdout, "  state: %s\n", response.Run.State)
		}
		printCloudRunBudget(stdout, response.Budget)
		printCloudRunWave(stdout, wave)
	case response.Queued.RunID != "":
		fmt.Fprintf(stdout, "Cloud run queued: %s\n", response.Queued.RunID)
		if response.Holder.RunID != "" {
			fmt.Fprintf(stdout, "  waiting for: %s\n", response.Holder.RunID)
		}
		printCloudRunBudget(stdout, response.Budget)
	default:
		if response.RunID != "" {
			fmt.Fprintf(stdout, "Cloud run started: %s\n", response.RunID)
			printCloudRunBudget(stdout, response.Budget)
			printCloudRunWave(stdout, wave)
			break
		}
		return newExitError(exitGeneric, "factory accepted the submission but returned no run id")
	}
	return nil
}

// cloudRunBudgetOverride is what --max-cost and --max-wall-clock ask of one
// submission. Zero means "not asked for", which leaves the deployment's own
// ceiling standing — the flags bound a run downward and can never widen it,
// so an omitted flag is not the same as a flag set to the deployment value.
type cloudRunBudgetOverride struct {
	maxCostUSD     float64
	maxWallClockMS int64
}

func cloudRunBudget(set map[string]bool, maxCost float64, maxWallClock time.Duration) (cloudRunBudgetOverride, error) {
	var budget cloudRunBudgetOverride
	if set["max-cost"] {
		if maxCost <= 0 {
			return budget, fmt.Errorf("--max-cost must be a positive amount in USD, got %v", maxCost)
		}
		budget.maxCostUSD = maxCost
	}
	if set["max-wall-clock"] {
		if maxWallClock <= 0 {
			return budget, fmt.Errorf("--max-wall-clock must be a positive duration, got %s", maxWallClock)
		}
		budget.maxWallClockMS = maxWallClock.Milliseconds()
		if budget.maxWallClockMS == 0 {
			return budget, fmt.Errorf("--max-wall-clock must be at least 1ms, got %s", maxWallClock)
		}
	}
	return budget, nil
}

// cloudRunWave reads --tick-ids: the flag that makes this submission take the
// per-tick-container path (tick pjq) rather than booting one orchestrator
// sandbox that fans out harness-native subagents inside itself.
//
// Nil means no wave, which is the Phase 1 submission and stays the default.
//
// The --queue refusal is the point of the flag being read this early. The
// RunRoom's queued-submission record has no tick_ids column, so a parked
// cloud-wave submission would ignite later as a plain single-sandbox run with
// its wave silently dropped; the factory answers the pair with a 400, and
// meeting that as an HTTP error after a push is a worse way to learn it than
// being told here, before anything has been pushed or spent.
func cloudRunWave(set map[string]bool, entries []string, queue bool) ([]string, error) {
	if !set["tick-ids"] {
		return nil, nil
	}
	ids := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry)
		if id == "" {
			continue
		}
		if seen[id] {
			return nil, newExitError(exitUsage, "--tick-ids names %s more than once; one container per tick", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, newExitError(exitUsage, "--tick-ids was given no tick ids; omit it to run the epic in a single orchestrator sandbox")
	}
	if queue {
		return nil, newExitError(exitUsage,
			"--tick-ids cannot be combined with --queue: a parked submission is stored without its wave, "+
				"so it would ignite later as a single-sandbox run having dropped the fan-out; "+
				"submit the wave now, or queue the epic without --tick-ids")
	}
	return ids, nil
}

// printCloudRunWave says what a submitted wave asked for. A run that fanned
// out into containers and one that booted a single orchestrator sandbox report
// the same run id and the same state, so without this line the two are
// indistinguishable from the command that started them.
func printCloudRunWave(out io.Writer, wave []string) {
	if len(wave) == 0 {
		return
	}
	fmt.Fprintf(out, "  wave: %d tick(s), one worker container each: %s\n", len(wave), strings.Join(wave, ", "))
}

// printCloudRunBudget says what this run will ACTUALLY be bounded by, and says
// so when that is not what was asked for (tick 7zk).
//
// The clamp itself is right — a submission may lower a budget and never raise
// one — but it was silent, and silence is what let an operator run an epic
// believing it had $40 when it had $8. A ceiling that lowers a flag has to
// announce itself at the moment the flag is typed, not in the cancellation
// forty minutes later.
func printCloudRunBudget(out io.Writer, budget *cloudEffectiveBudget) {
	if budget == nil {
		return
	}
	if budget.MaxCostUSD > 0 {
		fmt.Fprintf(out, "  cost budget: $%.2f\n", budget.MaxCostUSD)
	}
	if budget.MaxWallClockMS > 0 {
		fmt.Fprintf(out, "  wall-clock budget: %s\n", time.Duration(budget.MaxWallClockMS)*time.Millisecond)
	}
	if budget.CostClamped && budget.RequestedCost != nil {
		fmt.Fprintf(out, "  note: --max-cost $%.2f was lowered to the deployment ceiling $%.2f (a submission may lower a budget, never raise it)\n",
			*budget.RequestedCost, budget.MaxCostUSD)
	}
	if budget.WallClamped && budget.RequestedWall != nil {
		fmt.Fprintf(out, "  note: --max-wall-clock %s was lowered to the deployment ceiling %s (a submission may lower a budget, never raise it)\n",
			time.Duration(*budget.RequestedWall)*time.Millisecond, time.Duration(budget.MaxWallClockMS)*time.Millisecond)
	}
}

func runCloudStop(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := cloudStop(ctx, args, stdout)
	return reportCommand("cloud stop", err, stderr)
}

func cloudStop(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("cloud stop", nil)
	now := fs.Bool("now", false, "hard stop: revoke the run's gateway credential immediately and skip closeout")
	rest, err := parseCollectingPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return newExitError(exitUsage, "%v", err)
	}
	if len(rest) != 1 || rest[0] == "" {
		return newExitError(exitUsage, "exactly one run id is required")
	}

	client, err := newCloudClient()
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	requestedBy := cloudRequestedBy()
	mode := "clean"
	if *now {
		mode = "hard"
	}
	path := "/api/runs/" + url.PathEscape(rest[0]) + "/stop"
	data, err := client.request(ctx, http.MethodPost, path, map[string]string{
		"requested_by": requestedBy,
		"mode":         mode,
	})
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	var response struct {
		Run           cloudRunRecord `json:"run"`
		Mode          string         `json:"mode"`
		TokensRevoked int            `json:"tokens_revoked"`
	}
	if err := decodeCloudJSON(data, &response); err != nil {
		return newExitError(exitGeneric, "%v", err)
	}

	state := response.Run.State
	if state == "" {
		state = "stopping"
	}
	// Which stop the factory performed, not which one was asked for: an
	// operator reaching for the kill switch must be able to read back that it
	// fired, and how many live credentials it killed.
	performed := response.Mode
	if performed == "" {
		performed = mode
	}
	if performed == "hard" {
		fmt.Fprintf(stdout, "Cloud hard stop performed: %s (%s)\n", rest[0], state)
		fmt.Fprintf(stdout, "  gateway credentials revoked: %d\n", response.TokensRevoked)
		fmt.Fprintln(stdout, "  model traffic is refused from the next request; review and closeout will not run")
		return nil
	}
	fmt.Fprintf(stdout, "Cloud clean stop requested: %s (%s)\n", rest[0], state)
	fmt.Fprintln(stdout, "  in-flight work finishes, then review and closeout run; use --now to revoke the credential immediately")
	return nil
}

func runCloudStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := cloudStatus(ctx, args, stdout, stderr)
	return reportCommand("cloud status", err, stderr)
}

func cloudStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("cloud status", nil)
	rest, err := parseCollectingPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return newExitError(exitUsage, "%v", err)
	}
	if len(rest) > 1 {
		return newExitError(exitUsage, "at most one run id is allowed")
	}

	client, err := newCloudClient()
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	path := "/api/runs"
	if len(rest) == 1 {
		// Resolved rather than looked up literally: the factory answers a
		// prefix with "no run <prefix>", which is true of the prefix and reads
		// as a verdict on the run (tick c5i).
		runID, err := cloudRunIDArg(ctx, rest[0], stderr)
		if err != nil {
			return err
		}
		path += "/" + url.PathEscape(runID)
	}
	data, err := client.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	var response cloudStatusResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return newExitError(exitGeneric, "%v", err)
	}

	if len(rest) == 1 {
		printCloudRunStatus(stdout, response)
		return nil
	}
	printCloudRunList(stdout, response)
	return nil
}

func printCloudRunStatus(out io.Writer, response cloudStatusResponse) {
	run := response.Run
	fmt.Fprintf(out, "Cloud run %s\n", run.RunID)
	if run.Project != "" {
		fmt.Fprintf(out, "  project: %s\n", run.Project)
	}
	if run.Epic != "" {
		fmt.Fprintf(out, "  epic: %s\n", run.Epic)
	}
	if run.State != "" {
		fmt.Fprintf(out, "  state: %s\n", run.State)
	}
	if response.Phase.Workflow.Status != "" {
		fmt.Fprintf(out, "  workflow: %s\n", response.Phase.Workflow.Status)
	}
	printCloudRunProgress(out, run, response.Progress)
	switch {
	case response.Image != nil && response.Image.Digest != "":
		fmt.Fprintf(out, "  image: %s\n", response.Image.Digest)
	default:
		// Said rather than omitted: "unrecorded" is what a run that started
		// before a deploy confirmed a rollout looks like, and silence there
		// reads as "same image as everything else".
		fmt.Fprintf(out, "  image: unrecorded (no deploy has confirmed a container rollout for this factory)\n")
	}
	if response.Lease != nil && response.Lease.RunID != "" {
		fmt.Fprintf(out, "  lease: %s\n", response.Lease.RunID)
	}
	for _, queued := range response.Queued {
		fmt.Fprintf(out, "  queued: %s", queued.RunID)
		if queued.BlockedBy != "" {
			fmt.Fprintf(out, " (blocked by %s)", queued.BlockedBy)
		}
		fmt.Fprintln(out)
	}
}

// printCloudRunProgress states what a finished run actually achieved.
//
// The distinction it carries is the one an operator needs before resubmitting
// an epic: `completed` means the epic moved, `stopped` with `progress: none`
// means the run ended having changed nothing at all, and `unknown` means the
// evidence itself could not be read — which is a third fact, not a quiet
// version of either of the other two.
func printCloudRunProgress(out io.Writer, run cloudRunRecord, progress *cloudRunProgress) {
	if progress == nil || strings.TrimSpace(progress.Progress) == "" {
		// Only a finished run has a verdict to report; silence on a live one is
		// accurate rather than missing.
		if isFinishedCloudRun(run.State) {
			fmt.Fprintln(out, "  progress: unrecorded (this run predates durable-evidence finalization)")
		}
		return
	}
	fmt.Fprintf(out, "  progress: %s\n", progress.Progress)
	if detail := strings.TrimSpace(progress.Detail); detail != "" {
		fmt.Fprintf(out, "    %s\n", detail)
	}
}

func isFinishedCloudRun(state string) bool {
	switch strings.TrimSpace(state) {
	case "completed", "stopped", "failed":
		return true
	default:
		return false
	}
}

func printCloudRunList(out io.Writer, response cloudStatusResponse) {
	if len(response.Runs) == 0 && len(response.Projects) == 0 {
		fmt.Fprintln(out, "No cloud runs.")
		return
	}
	for _, run := range response.Runs {
		fmt.Fprintf(out, "%s  %s  %s", run.RunID, run.State, run.Epic)
		if run.Project != "" {
			fmt.Fprintf(out, "  %s", run.Project)
		}
		fmt.Fprintln(out)
	}
	for _, project := range response.Projects {
		if project.Lease != nil && project.Lease.RunID != "" {
			fmt.Fprintf(out, "lease  %s  %s\n", project.Project, project.Lease.RunID)
		}
		for _, queued := range project.Queued {
			fmt.Fprintf(out, "queue  %s  %s", project.Project, queued.RunID)
			if queued.BlockedBy != "" {
				fmt.Fprintf(out, "  blocked by %s", queued.BlockedBy)
			}
			fmt.Fprintln(out)
		}
	}
}

func cloudRequestedBy() string {
	requestedBy, err := cloudDetectOwner()
	if err != nil || strings.TrimSpace(requestedBy) == "" {
		return "operator"
	}
	return strings.TrimSpace(requestedBy)
}

// prepareCloudSubmission makes the pushed SHA a real boundary. It pushes the
// current branch, then proves that every local tick belonging to the epic is
// tracked, clean, and present at the same SHA on origin. In particular, a
// freshly-created untracked tick can never cause a cloud run to start.
func prepareCloudSubmission(ctx context.Context, root, epicID string) (baseSHA, project, requestedBy string, err error) {
	tracker, err := cloudReadTracker(ctx, root, epicID)
	if err != nil {
		if cloudTrackerStageOf(err) == cloudTrackerStageList {
			return "", "", "", fmt.Errorf("cannot inspect ticks for epic %q: %w", epicID, err)
		}
		return "", "", "", fmt.Errorf("cannot submit epic %q: %w", epicID, err)
	}
	if !tracker.isEpic() {
		return "", "", "", fmt.Errorf("cannot submit %q: it is not an epic", epicID)
	}
	paths := tracker.epicPaths()
	if len(paths) == 0 {
		return "", "", "", fmt.Errorf("cannot submit epic %q: no tick files found", epicID)
	}

	branchOutput, err := cloudGit(ctx, root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", "", "", fmt.Errorf("cannot submit epic %q: current checkout is not on a branch: %w", epicID, err)
	}
	branch := strings.TrimSpace(branchOutput)
	if branch == "" {
		return "", "", "", fmt.Errorf("cannot submit epic %q: current checkout has no branch", epicID)
	}

	shaOutput, err := cloudGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return "", "", "", fmt.Errorf("cannot determine the commit for epic %q: %w", epicID, err)
	}
	baseSHA = strings.TrimSpace(shaOutput)
	if len(baseSHA) != 40 {
		return "", "", "", fmt.Errorf("cannot submit epic %q: HEAD is not a full commit SHA", epicID)
	}

	if _, err := cloudGit(ctx, root, "push", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return "", "", "", fmt.Errorf("could not push the current branch for epic %q: %w", epicID, err)
	}

	statusOutput, err := cloudGit(ctx, root, append([]string{"status", "--porcelain=v1", "--untracked-files=all", "--"}, paths...)...)
	if err != nil {
		return "", "", "", fmt.Errorf("could not check whether epic %q is pushed: %w", epicID, err)
	}
	if strings.TrimSpace(statusOutput) != "" {
		return "", "", "", fmt.Errorf(
			"epic %q is not pushed: its tick files have local changes; git add and commit them, then run 'tk cloud run %s' again",
			epicID, epicID,
		)
	}
	for _, path := range paths {
		if _, err := cloudGit(ctx, root, "ls-files", "--error-unmatch", "--", path); err != nil {
			return "", "", "", fmt.Errorf(
				"epic %q is not pushed: tick file %s is not committed; git add and commit it, then run 'tk cloud run %s' again",
				epicID, path, epicID,
			)
		}
	}

	remoteOutput, err := cloudGit(ctx, root, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", "", "", fmt.Errorf("could not verify the pushed branch for epic %q: %w", epicID, err)
	}
	fields := strings.Fields(remoteOutput)
	if len(fields) == 0 || fields[0] != baseSHA {
		remoteSHA := "missing"
		if len(fields) > 0 {
			remoteSHA = fields[0]
		}
		return "", "", "", fmt.Errorf(
			"epic %q is not pushed at %s: origin/%s is %s; push the current branch and retry",
			epicID, baseSHA, branch, remoteSHA,
		)
	}
	for _, path := range paths {
		if _, err := cloudGit(ctx, root, "cat-file", "-e", baseSHA+":"+path); err != nil {
			return "", "", "", fmt.Errorf(
				"epic %q is not visible on origin at %s: tick file %s is missing; commit and push it, then retry",
				epicID, baseSHA, path,
			)
		}
	}

	project, err = cloudDetectProject()
	if err != nil {
		return "", "", "", fmt.Errorf("cannot determine the GitHub project for epic %q: %w", epicID, err)
	}
	return baseSHA, project, cloudRequestedBy(), nil
}

// ------------------------------------------------- the tracker, via tk ---
//
// These cloud commands read the tracker through the `tk` CLI's JSON contract
// rather than through the Go store. That is deliberate (epic 3j4): `tk ...
// --json` is already a supported, schema-backed interface, and the sandbox
// image has always reached the tracker this way — `image/required-tk-
// commands` lists the subcommands the container must have and the image's
// last build layer install-checks each one. Having the container shell out
// while the same logic on a laptop imports Go internals would be two answers
// to one question.

// cloudTickTypeEpic is the tracker's wire value for an epic, as it appears in
// `tk show --json`. The contract here is the JSON, not a Go symbol.
const cloudTickTypeEpic = "epic"

// cloudTick is the slice of a tick these commands actually need: identity,
// kind, and the parent link the descendant walk follows. Decoding a subset
// keeps the coupling to exactly those three fields, so tk may add others.
type cloudTick struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Parent string `json:"parent"`
}

// cloudTracker is one snapshot of the tracker: the epic under consideration
// and every tick in the checkout, indexed by id.
//
// It costs exactly TWO tk invocations — one `show`, one `list` — no matter how
// many ticks the repo holds. The descendant walk needs every tick, and asking
// tk once per tick would turn a wave check into hundreds of process launches.
type cloudTracker struct {
	epic cloudTick
	byID map[string]cloudTick
}

// The two stages of a tracker read, so a caller can keep the exit code its
// users already see: a failure to read the epic is a lookup failure, a failure
// to list is not.
const (
	cloudTrackerStageEpic = "epic"
	cloudTrackerStageList = "list"
)

// cloudTrackerError says which stage of the read failed, and wraps why.
type cloudTrackerError struct {
	stage string
	err   error
}

func (e *cloudTrackerError) Error() string { return e.err.Error() }
func (e *cloudTrackerError) Unwrap() error { return e.err }

// cloudTkError reports that tk ran and refused, carrying tk's own exit code
// and whatever it printed. Distinguishing this from "tk could not be run at
// all" is what lets a missing epic keep exiting 4 while a missing or crashed
// tk says so in words instead of surfacing as a bare non-zero exit.
type cloudTkError struct {
	command string
	code    int
	message string
}

func (e *cloudTkError) Error() string { return e.message }

// cloudTrackerStageOf reports which stage produced err, or "" if err did not
// come from a tracker read.
func cloudTrackerStageOf(err error) string {
	var stageErr *cloudTrackerError
	if errors.As(err, &stageErr) {
		return stageErr.stage
	}
	return ""
}

// cloudTrackerEpicRefused reports whether err is tk itself refusing the epic
// lookup — the tick is not in this checkout — as opposed to tk being missing,
// crashing, or answering something this code cannot parse. Only the first case
// is a "not found".
func cloudTrackerEpicRefused(err error) bool {
	if cloudTrackerStageOf(err) != cloudTrackerStageEpic {
		return false
	}
	var tkErr *cloudTkError
	return errors.As(err, &tkErr)
}

// cloudReadTracker is the one helper every cloud command uses to read the
// tracker: `tk show <epic> --json` followed by a single `tk list --all --json`.
//
// `--all` is not optional. `tk list` defaults to the invoking user's own
// ticks, and an epic's descendants may be owned by anyone (a worker container
// owns what it filed), so without it the descendant walk would silently lose
// ticks the Go store used to return.
func cloudReadTracker(ctx context.Context, root, epicID string) (cloudTracker, error) {
	raw, err := cloudTkJSON(ctx, root, "show", epicID, "--json")
	if err != nil {
		return cloudTracker{}, &cloudTrackerError{stage: cloudTrackerStageEpic, err: err}
	}
	var epic cloudTick
	if err := json.Unmarshal(raw, &epic); err != nil {
		return cloudTracker{}, &cloudTrackerError{
			stage: cloudTrackerStageEpic,
			err:   fmt.Errorf("could not read the output of 'tk show %s --json': %w", epicID, err),
		}
	}

	raw, err = cloudTkJSON(ctx, root, "list", "--all", "--json")
	if err != nil {
		return cloudTracker{}, &cloudTrackerError{stage: cloudTrackerStageList, err: err}
	}
	var listed struct {
		Ticks []cloudTick `json:"ticks"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return cloudTracker{}, &cloudTrackerError{
			stage: cloudTrackerStageList,
			err:   fmt.Errorf("could not read the output of 'tk list --all --json': %w", err),
		}
	}

	byID := make(map[string]cloudTick, len(listed.Ticks))
	for _, item := range listed.Ticks {
		byID[item.ID] = item
	}
	// `tk list` is filtered output by nature; the epic itself is guaranteed
	// present because `tk show` just returned it.
	byID[epic.ID] = epic
	return cloudTracker{epic: epic, byID: byID}, nil
}

// isEpic reports whether the tick the read was asked about is one.
func (t cloudTracker) isEpic() bool { return t.epic.Type == cloudTickTypeEpic }

// lookup returns the tick with this id, if the checkout has one.
func (t cloudTracker) lookup(id string) (cloudTick, bool) {
	item, ok := t.byID[id]
	return item, ok
}

// isDescendant reports whether id sits anywhere under the epic.
func (t cloudTracker) isDescendant(id string) bool {
	item, ok := t.byID[id]
	if !ok {
		return false
	}
	return cloudIsDescendant(item, t.epic.ID, t.byID)
}

// epicPaths is every tick file belonging to the epic, epic included, as
// repo-relative slash paths.
func (t cloudTracker) epicPaths() []string {
	paths := make([]string, 0)
	for _, item := range t.byID {
		if item.ID != t.epic.ID && !cloudIsDescendant(item, t.epic.ID, t.byID) {
			continue
		}
		paths = append(paths, filepath.ToSlash(filepath.Join(".tick", "issues", item.ID+".json")))
	}
	sort.Strings(paths)
	return paths
}

func cloudIsDescendant(item cloudTick, epicID string, byID map[string]cloudTick) bool {
	seen := make(map[string]bool)
	parent := item.Parent
	for parent != "" && !seen[parent] {
		if parent == epicID {
			return true
		}
		seen[parent] = true
		ancestor, ok := byID[parent]
		if !ok {
			return false
		}
		parent = ancestor.Parent
	}
	return false
}

// cloudTkBinary resolves the tk to consult. It is a package variable for the
// same reason cloudHTTPClient is: the package's own tests stand something in
// front of it rather than depending on what happens to be installed. It
// returns the binary and any environment additions the child needs.
var cloudTkBinary = resolveCloudTkBinary

// resolveCloudTkBinary answers "which tk?" with "this one, if this is tk".
//
// os.Executable() first, PATH only as a fallback. Inside the sandbox the two
// are the same binary and the choice is invisible; on a laptop they are not —
// a released tk on PATH alongside a `go build` in the working tree is the
// normal state of a developer's machine. The JSON these commands parse is
// generated from schemas/ per version, so asking a DIFFERENT build for the
// answers would let one version's command surface read another version's
// tracker output, and the mismatch would show up as a parse error nobody can
// place. The invoked binary is the one the user chose; it is also the one
// whose behaviour they will debug.
//
// PATH is the fallback rather than the primary because it is right in exactly
// one case: this code running inside something that is not tk. That is how
// image/required-tk-commands already thinks — the container declares
// the tk subcommands it needs and install-checks them against the tk on its
// PATH, because the caller there (a shell script) is not tk either.
//
// ticfac never is tk — it does not ship the tracker — so on this side the
// answer is always the PATH lookup; the executable check stays because the
// code is shared verbatim with ticks until ticks tick 1ya deletes it.
func resolveCloudTkBinary() (string, []string, error) {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if name := filepath.Base(exe); name == "tk" || name == "tk.exe" {
			return exe, nil, nil
		}
	}
	bin, err := exec.LookPath("tk")
	if err != nil {
		return "", nil, fmt.Errorf(
			"cannot find a tk to read the tracker with: %w; install tk or put it on PATH", err)
	}
	return bin, nil, nil
}

// cloudTkJSON runs one tk subcommand in root and returns its stdout.
//
// Three outcomes, kept apart on purpose: tk answered; tk ran and refused (a
// *cloudTkError carrying tk's exit code and message, so a caller can map it to
// the code its users already see); or tk never ran, or died on a signal, in
// which case the error says which binary and which arguments, because a bare
// non-zero exit tells a human nothing.
func cloudTkJSON(ctx context.Context, root string, args ...string) ([]byte, error) {
	bin, extraEnv, err := cloudTkBinary()
	if err != nil {
		return nil, err
	}
	rendered := "tk " + strings.Join(args, " ")

	command := exec.CommandContext(ctx, bin, args...)
	command.Dir = root
	if len(extraEnv) > 0 {
		command.Env = append(os.Environ(), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = fmt.Sprintf("%q exited %d without saying why", rendered, exitErr.ExitCode())
			}
			return nil, &cloudTkError{command: rendered, code: exitErr.ExitCode(), message: message}
		}
		// Not a refusal: the binary is missing, not executable, was killed by
		// a signal, or the context expired. Name it.
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("could not run %q (%s): %w: %s", rendered, bin, err, message)
		}
		return nil, fmt.Errorf("could not run %q (%s): %w", rendered, bin, err)
	}
	return stdout.Bytes(), nil
}

func cloudGit(ctx context.Context, root string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return string(output), nil
}

// cloudSpawnCheckWave proves a named wave against this checkout before
// anything is pushed: every tick must exist here and belong to the epic,
// because a worker container clones at the epic's base and a tick outside it
// would be implemented against a base its own epic never chose. Ported from
// ticks' cmd/tk/cmd/cloud_spawn.go with the rest of the spawn family left
// behind (spawn itself stays with the local orchestrator).
func cloudSpawnCheckWave(ctx context.Context, root, epicID string, tickIDs []string) error {
	tracker, err := cloudReadTracker(ctx, root, epicID)
	if err != nil {
		// A tk that ran and refused the lookup is the missing epic users
		// already see as exit 4. A tk that could not be run at all is not a
		// "not found" — it is an environment fault, and saying so is the
		// difference between a fixable message and a bare non-zero exit.
		if cloudTrackerEpicRefused(err) {
			return newExitError(exitNotFound, "cannot dispatch epic %q: %v", epicID, err)
		}
		if cloudTrackerStageOf(err) == cloudTrackerStageEpic {
			return newExitError(exitGeneric, "cannot dispatch epic %q: %v", epicID, err)
		}
		return newExitError(exitGeneric, "cannot inspect ticks for epic %q: %v", epicID, err)
	}
	if !tracker.isEpic() {
		return newExitError(exitGeneric, "cannot dispatch %q: it is not an epic", epicID)
	}

	outside := make([]string, 0)
	for _, id := range tickIDs {
		if _, ok := tracker.lookup(id); !ok {
			return newExitError(exitNotFound, "no tick %q in this checkout", id)
		}
		if !tracker.isDescendant(id) {
			outside = append(outside, id)
		}
	}
	if len(outside) > 0 {
		sort.Strings(outside)
		return newExitError(exitGeneric,
			"%s do not belong to epic %s: a worker container clones at that epic's base and pushes tick/%s/<tick>, "+
				"so dispatching them here would implement them against a base their own epic never chose",
			strings.Join(outside, ", "), epicID, epicID)
	}
	return nil
}

// parseCollectingPositionals parses args with fs the way cobra did: flags may
// appear after the positional arguments ("cloud run epic1 --tick-ids aaa"),
// which the flag package alone would treat as more positionals. The first
// non-flag argument is pulled out and parsing resumes after it, so an
// operator's muscle memory from `tk cloud run <epic> --max-cost 2` survives
// the move unchanged.
func parseCollectingPositionals(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		trailing := fs.Args()
		if len(trailing) == 0 {
			return positionals, nil
		}
		positionals = append(positionals, trailing[0])
		rest = trailing[1:]
	}
}
