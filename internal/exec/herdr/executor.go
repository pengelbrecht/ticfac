package herdr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// The executor: start, inspect, cancel, collect, and dispose — the same five
// operations internal/reconcile names, over a herdr workspace per attempt.
//
// Nothing here crosses the protocol seam that is not already crossed by the
// records internal/exec/subprocess owns: which agent KIND to launch, its
// args, the model and role prompt the caller's profile resolved, where state
// lives and which remote the work is durable on are host configuration
// (Options), because the protocol records are closed and a field invented
// here would be one the reconciler ignores.

// Options configure the host.
type Options struct {
	// Repo is the local checkout herdr creates the attempt worktree
	// against — the reconciler's own checkout. Defaults to the working
	// directory.
	Repo string

	// StateDir is the executor's private state root, one directory per
	// attempt under it. It is the directory the reconciler's Dispatch
	// assigns, so a restarted run finds the attempt the previous
	// incarnation started without guessing at this executor's naming.
	// Defaults to $TICFAC_EXEC_STATE_DIR, else ~/.ticfac/exec/herdr. It is
	// deliberately OUTSIDE the repository.
	StateDir string

	// SocketPath is the herdr API socket. Empty resolves per the runners
	// configuration's order: explicit, $HERDR_SOCKET_PATH, then
	// ~/.config/herdr/herdr.sock.
	SocketPath string

	// Kind is the herdr agent kind ("claude", "codex", "pi", …) — the
	// harness dimension, the herdr form of the profile's runner field. The
	// caller resolves it from its role profile; empty defaults to claude,
	// the same default the local executor's runner falls back to.
	Kind string

	// Args is the argv appended after the kind's own launch template, one
	// element per argv entry. It is host configuration, resolved from the
	// runners configuration by the caller — this executor compiles nothing.
	Args []string

	// Model and RolePrompt are what the caller's profile resolved for this
	// job. Model is recorded in provenance and the prompt; RolePrompt opens
	// the worker prompt.
	Model      string
	RolePrompt string

	// Remote is the origin in-progress work is durable on, for disposal's
	// branch-safety question. Empty means "origin", and a repository without
	// that remote records no remote rather than inventing one.
	Remote string

	// Attempt is the attempt number this start is for. It comes from the
	// caller because job_id is OPAQUE to an executor.
	Attempt int

	// StartupTimeout bounds the two measured startup races Start absorbs —
	// the agent_pane_busy retry and the interactive_ready poll — with the
	// caller's patience budget rather than a fixed attempt count. Zero is
	// DefaultStartupTimeout.
	StartupTimeout time.Duration

	// ConfirmTimeout bounds the one wait Start attaches to the prompt
	// submission: the wait for the agent to reach `working`. Its elapsing is
	// an observation, never a failure. Zero is DefaultConfirmTimeout.
	ConfirmTimeout time.Duration

	Now func() time.Time

	// writeFile is the state writer, so a test can inject a write that
	// silently does not land — the only way to see whether the read-back
	// after write is doing anything (Appendix A #7).
	writeFile func(path string, data []byte, perm fs.FileMode) error
}

// DefaultStartupTimeout is the patience budget for launching an agent. It is
// herdr's own ceiling (agent.start accepts at most 300s) taken well under,
// because a dispatch that cannot launch in a minute is one an operator should
// hear about, not one a tick should wait ten minutes on.
const DefaultStartupTimeout = 60 * time.Second

// DefaultConfirmTimeout is the budget for confirming the agent entered
// `working` after the prompt. The spawn machinery this lesson comes from
// charged about a second and did not serialize a wave.
const DefaultConfirmTimeout = 15 * time.Second

// The retry cadence for the two startup races. Sub-second, because both races
// are measured in the first few hundred milliseconds of a pane's life.
const paneBusyRetryInterval = 250 * time.Millisecond
const readinessPollInterval = 250 * time.Millisecond

// Executor is one host, pointed at one repository and one herdr session.
type Executor struct {
	opts    Options
	repo    string
	repoKey string
	root    string
	client  *client.Client
	now     func() time.Time
}

// New resolves the repository, the state root and a herdr connection once, so
// that every operation afterwards names the directory it works in and the
// session it talks to. The ping handshake is here and not in Start: a herdr
// that is not running is a dispatch that fails before the tick is claimed
// rather than a run that discovers it three ticks in.
func New(opts Options) (*Executor, error) {
	if opts.Repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		opts.Repo = wd
	}
	root, err := repoRoot(opts.Repo)
	if err != nil {
		return nil, err
	}
	key, err := repoKey(root)
	if err != nil {
		return nil, err
	}
	if opts.StateDir == "" {
		opts.StateDir = DefaultStateDir()
	}
	if opts.Kind == "" {
		opts.Kind = "claude"
	}
	if opts.Attempt <= 0 {
		opts.Attempt = 1
	}
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = DefaultStartupTimeout
	}
	if opts.ConfirmTimeout <= 0 {
		opts.ConfirmTimeout = DefaultConfirmTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := client.New(ctx, client.Options{SocketPath: opts.SocketPath})
	if err != nil {
		return nil, fmt.Errorf("herdr executor: the herdr session is not usable: %w", err)
	}
	return &Executor{opts: opts, repo: root, repoKey: key, root: opts.StateDir, client: c, now: opts.Now}, nil
}

// DefaultStateDir is where attempts are recorded when nothing says otherwise.
func DefaultStateDir() string {
	if dir := os.Getenv("TICFAC_EXEC_STATE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "ticfac", "exec", ExecutorName)
	}
	return filepath.Join(home, ".ticfac", "exec", ExecutorName)
}

// Repo is the checkout this executor was pointed at.
func (e *Executor) Repo() string { return e.repo }

// stamp is one record timestamp.
func (e *Executor) stamp() string { return stamp(e.now) }

// refuse is a refusal to act, carrying the REASON as its own value, so a
// caller recovers it without matching on prose. The reason vocabulary is the
// one the reconciler already reads — subprocess.Refused* — so the
// keep-the-branch retry on a teardown reads this executor's refusals exactly
// as it reads the local one's.
func refuse(reason, format string, args ...any) *subprocess.Refusal {
	return &subprocess.Refusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// ----------------------------------------------------------------- start ---

// Start creates the attempt's worktree and workspace through herdr, launches
// the agent in the workspace's root pane, and submits the worker prompt.
//
// It never redispatches a live attempt: an identity that is already running
// is ADOPTED (the same handle comes back), and one whose liveness nobody can
// answer is refused for a person to hold. "Nobody can say" is not "nothing is
// running", and treating it as one is how a run pays twice for one tick.
//
// A substrate failure on the way cleans up NOTHING: the failed pane and the
// record of the failed launch stay as diagnostic state, exactly as ticks'
// spawn machinery learned to leave them — the next attempt takes the next
// attempt number, whose branch and workspace are its own.
func (e *Executor) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	attempt := e.opts.Attempt
	dir := e.stateDirFor(spec.JobID, attempt)
	st := e.storeAt(dir)

	// A1, in the herdr executor's terms: a cancelled attempt's dispatch is
	// its credential, and the durable refusal to reissue is checked before a
	// new one is issued — not merely before work is started.
	if cancelled, ok := st.cancelled(); ok {
		return nil, refuse(subprocess.RefusedCancelled, "this handle was cancelled at %s and reissue is %s",
			cancelled.AcceptedAt, cancelled.Reissue)
	}

	// A6: adopt by stable identity. A fresh attempt is created only when the
	// previous one is proven settled or never started.
	if existing, err := st.readAttempt(); err == nil {
		status := e.statusOf(existing)
		switch {
		case status.State == subprocess.StateRunning || status.State == subprocess.StateStarting ||
			status.State == subprocess.StatePending:
			return handleFor(existing), nil
		case status.State == subprocess.StateLost:
			return nil, refuse(subprocess.RefusedUnknown,
				"attempt %d of %s cannot be addressed and has not settled; it is held, never redispatched",
				existing.Attempt, existing.JobID)
		case status.Terminal:
			return nil, refuse(subprocess.RefusedSettled,
				"attempt %d of %s already settled as %s; a retry is a new attempt number, not this one again",
				existing.Attempt, existing.JobID, status.State)
		}
	}

	// The wall clock this spec carries is a promise THIS executor makes at
	// dispatch — herdr owns the agent's process, so nothing inherited stops
	// it. A herdr too old for the interrupt surface is one the bound cannot
	// be enforced through, and an unenforceable bound is refused here,
	// naming the bound and the herdr version, rather than issued as a
	// promise nothing would keep (wall.go). Adoption is exempt: an attempt
	// being adopted was issued its bound by an incarnation that could
	// enforce it, and refusing the adoption would strand a live worker.
	if err := e.enforceableWall(spec); err != nil {
		return nil, err
	}

	base, err := resolveCommit(e.repo, spec.Source.BaseSHA)
	if err != nil {
		return nil, fmt.Errorf("the base %q is not a commit this checkout has: %w", spec.Source.BaseSHA, err)
	}
	branch, err := branchFromWriteRef(spec)
	if err != nil {
		return nil, err
	}
	if branchExists(e.repo, branch) {
		return nil, refuse(subprocess.RefusedLive,
			"branch %s already exists in %s: this attempt would write a ref another one owns",
			branch, e.repo)
	}

	ctx := context.Background()

	// The worktree AND the workspace come from herdr in one call; the
	// returned root pane is where the agent goes.
	info := e.client.ServerInfo()
	created, err := e.client.WorktreeCreate(ctx, client.WorktreeCreateParams{
		Cwd:    client.Ptr(e.repo),
		Branch: client.Ptr(branch),
		Base:   client.Ptr(base),
		Label:  client.Ptr(tickOf(spec)),
		Focus:  false,
	})
	if err != nil {
		return nil, fmt.Errorf("herdr worktree.create for %s failed: %w", spec.JobID, err)
	}
	worktree := created.Worktree.Path
	if worktree == "" || created.RootPane.PaneID == "" || created.Workspace.WorkspaceID == "" {
		return nil, fmt.Errorf("herdr worktree.create for %s answered without a worktree, workspace or root pane: %w",
			spec.JobID, errNoShape)
	}

	record := &attemptRecord{
		SchemaVersion: stateSchemaVersion,
		Key:           attemptKey(e.repoKey, spec.JobID, attempt),
		RepoKey:       e.repoKey,
		Repo:          e.repo,
		JobID:         spec.JobID,
		Attempt:       attempt,
		TickID:        tickOf(spec),
		Branch:        branch,
		WriteRef:      spec.Source.WriteRef,
		BaseSHA:       base,
		WorkspaceID:   created.Workspace.WorkspaceID,
		PaneID:        created.RootPane.PaneID,
		AgentName:     agentName(tickOf(spec), attempt),
		Worktree:      worktree,
		State:         dir,
		Kind:          e.opts.Kind,
		AgentArgs:     e.opts.Args,
		Model:         e.opts.Model,
		RolePrompt:    e.opts.RolePrompt,
		WallSeconds:   spec.Limits.WallSeconds,
		Remote:        e.remoteFor(spec),
		SourceGrade:   spec.Credentials.Source.Grade(),
		ServerVersion: info.Version,
		Protocol:      info.Protocol,
		IssuedAt:      e.stamp(),
		Spec:          spec,
	}
	rel, abs, err := resultPath(record.Worktree, spec.ArtifactPrefix, record.TickID)
	if err != nil {
		return nil, err
	}
	record.ResultRel, record.ResultPath = rel, abs

	prompt := renderWorkerPrompt(record, spec)
	if err := st.writeFile(st.path(filePrompt), []byte(prompt), 0o644); err != nil {
		return nil, fmt.Errorf("record the worker prompt: %w", err)
	}

	// A7: the attempt record is read back before anything acts on it. A
	// handle for a record that did not land is a job nobody can find.
	if err := st.writeAttempt(record); err != nil {
		return nil, err
	}

	if _, err := e.startAgent(ctx, st, record); err != nil {
		// Never clean up on a substrate failure: the pane, the workspace and
		// the record stay as diagnostic state. The attempt is terminal —
		// never launched — so a re-Start of this identity is refused as
		// settled rather than redispatching over the failure.
		record.LaunchConfirmed = false
		if writeErr := st.writeAttempt(record); writeErr != nil {
			return nil, fmt.Errorf("record the failed launch (%v): %w", err, writeErr)
		}
		return nil, err
	}
	record.LaunchConfirmed = true
	if err := st.writeAttempt(record); err != nil {
		return nil, err
	}
	if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsStarted,
		Detail: fmt.Sprintf("agent %s (%s) started in pane %s, workspace %s, worktree %s",
			record.AgentName, record.Kind, record.PaneID, record.WorkspaceID, record.Worktree)}); err != nil {
		return nil, fmt.Errorf("record the launch: %w", err)
	}

	e.submit(ctx, st, record)

	return handleFor(record), nil
}

// errNoShape reports a reply that answered success without the identity the
// next call needs.
var errNoShape = errors.New("the reply carries no usable shape")

// startAgent launches the agent in the workspace's root pane, absorbing the
// two startup races ticks measured live against herdr:
//
//   - agent.start → agent_pane_busy: the root pane is not an interactive
//     shell yet. Retried on that code alone, bounded by the caller's
//     StartupTimeout — the operator's own patience budget — rather than a
//     fixed attempt count that ignored it.
//   - a launch that answers launch_pending (or not yet interactive_ready):
//     herdr typed the command but has not detected the agent. Sampled by
//     agent.get until readiness flips, because the STATUS reaches idle about
//     a second before readiness does and waiting on lifecycle status alone
//     does not help.
//
// Any other failure is returned: never cleaned up, never retried, because a
// repeated substrate error is a diagnosis for a person and not something a
// second identical call is likelier to fix.
func (e *Executor) startAgent(ctx context.Context, st *store, record *attemptRecord) (*client.AgentStarted, error) {
	params := client.AgentStartParams{
		Name:   record.AgentName,
		Kind:   record.Kind,
		PaneID: record.PaneID,
		Args:   record.AgentArgs,
	}
	deadline := e.now().Add(e.opts.StartupTimeout)
	for {
		started, err := e.client.AgentStart(ctx, params)
		if err == nil {
			if started.Agent.InteractiveReady {
				return started, nil
			}
			// The launch is accepted but pending: herdr has typed the
			// command and not yet detected the agent. Poll readiness.
			return e.waitInteractiveReady(ctx, st, record)
		}
		if !client.IsCode(err, client.CodeAgentPaneBusy) {
			return nil, fmt.Errorf("herdr agent.start for %s failed: %w", record.AgentName, err)
		}
		if !e.now().Before(deadline) {
			return nil, fmt.Errorf("herdr agent.start for %s kept answering agent_pane_busy past the startup budget of %s: %w",
				record.AgentName, e.opts.StartupTimeout, err)
		}
		if obsErr := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "the root pane is not an interactive shell yet (agent_pane_busy); retrying"}); obsErr != nil {
			return nil, fmt.Errorf("record the pane-busy retry: %w", obsErr)
		}
		e.sleepUntil(paneBusyRetryInterval)
	}
}

// waitInteractiveReady samples the agent by name until herdr reports it ready
// for input. agent_not_ready on a PROMPT is what this poll exists to outlast.
func (e *Executor) waitInteractiveReady(ctx context.Context, st *store, record *attemptRecord) (*client.AgentStarted, error) {
	deadline := e.now().Add(e.opts.StartupTimeout)
	for {
		agent, err := e.client.AgentGet(ctx, record.AgentName)
		if err != nil {
			return nil, fmt.Errorf("herdr agent.get for %s after a pending launch: %w", record.AgentName, err)
		}
		if agent.InteractiveReady {
			return &client.AgentStarted{Agent: *agent}, nil
		}
		if !e.now().Before(deadline) {
			return nil, fmt.Errorf("the agent %s never reported interactive_ready within the startup budget of %s",
				record.AgentName, e.opts.StartupTimeout)
		}
		e.sleepUntil(readinessPollInterval)
	}
}

// submit delivers the worker prompt and confirms the dispatch.
//
// The confirmation is the spawn lesson that is NOT fire-and-forget: returning
// the instant the submission is accepted leaves the worker in the settled
// state the launch left it in, and a wait moments later resolves it as
// already settled — a wave that fans in before any work starts. So Start
// waits once for the agent to reach `working`. A confirmation that times out
// is reported through the observation log and DispatchConfirmed=false rather
// than failing the spawn: a trivial tick can finish before `working` is ever
// rendered, and an unconfirmed dispatch is a fact, not a verdict.
//
// agent_prompt_stalled decides nothing here. herdr saw no state change after
// submitting — which a worker that answered in under a second also produces —
// and there is deliberately no content gate reading the pane back: a
// rendering or truncation change must never turn into "the agent cannot work"
// (that classification is tick x6j's). The stall is recorded; the wait below
// is the one thing that observes the agent's answer.
func (e *Executor) submit(ctx context.Context, st *store, record *attemptRecord) {
	prompt := renderWorkerPrompt(record, record.Spec)
	_, err := e.client.AgentPrompt(ctx, client.AgentPromptParams{
		Target: record.AgentName,
		Text:   prompt,
	})
	if err != nil {
		if client.IsCode(err, client.CodeAgentPromptStalled) {
			_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
				Detail: "herdr saw no state change after submitting the prompt (agent_prompt_stalled); " +
					"the submission is left to the wait to judge"})
		} else {
			_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
				Detail: "prompt submission answered " + err.Error() + "; the attempt is left for the wait to judge"})
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, e.opts.ConfirmTimeout)
	defer cancel()
	if _, waitErr := e.client.AgentWait(waitCtx, client.AgentWaitParams{
		Target:  record.AgentName,
		Until:   []client.AgentStatus{client.StatusWorking},
		Timeout: e.opts.ConfirmTimeout,
	}); waitErr == nil {
		record.DispatchConfirmed = true
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "the agent entered working: the dispatch is confirmed"})
	} else if client.IsTimeout(waitErr) {
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "the agent did not visibly enter working within " + e.opts.ConfirmTimeout.String() +
				"; the dispatch is recorded unconfirmed — a trivial tick can finish before working is rendered"})
	} else {
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "the confirmation wait answered " + waitErr.Error() + "; the dispatch is recorded unconfirmed"})
	}
	if err := st.writeAttempt(record); err != nil {
		// The launch is already durable; the confirmation is a recorded
		// observation and its absence is a fact, not a failure. A write
		// failure here is still reported: a record that did not land is a
		// job nobody can find.
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: "the dispatch confirmation could not be recorded: " + err.Error()})
	}
}

// sleepUntil waits one retry interval between the startup races' attempts.
func (e *Executor) sleepUntil(d time.Duration) {
	time.Sleep(d)
}

// stateDirFor is the attempt's state directory: (repo key, job id, attempt)
// beneath the state root. job_id carries the reconciler's run and tick; the
// repo key is what this executor adds, because two repositories on one
// machine can be running the same tick.
func (e *Executor) stateDirFor(jobID string, attempt int) string {
	return filepath.Join(e.root, e.repoKey, attemptKey(e.repoKey, jobID, attempt))
}

func (e *Executor) storeAt(dir string) *store {
	st := newStore(dir)
	if e.opts.writeFile != nil {
		st.writeFile = e.opts.writeFile
	}
	return st
}

func attemptKey(repoKey, jobID string, attempt int) string {
	sum := sha256.Sum256([]byte(repoKey + "\x00" + jobID + "\x00" + fmt.Sprint(attempt)))
	return hex.EncodeToString(sum[:])[:16]
}

// remoteFor is the remote the work is durable on, or "" when there is none.
func (e *Executor) remoteFor(spec *subprocess.JobSpec) string {
	remote := e.opts.Remote
	if remote == "" {
		remote = "origin"
	}
	if !hasRemote(e.repo, remote) {
		return ""
	}
	return remote
}

// branchFromWriteRef takes the branch from the ONE field that says which ref
// this job may write, and refuses a ref outside the namespace the source
// grant bounds. Same rule as the local executor's, because it is the
// issuer-enforced boundary, not executor mechanics.
func branchFromWriteRef(spec *subprocess.JobSpec) (string, error) {
	ref := spec.Source.WriteRef
	if prefix := spec.Credentials.Source.WriteRefPrefix(); prefix != "" && !strings.HasPrefix(ref, prefix) {
		return "", refuse(subprocess.RefusedCredential,
			"write_ref %s is outside the namespace this grant may advance (%s)", ref, prefix)
	}
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if branch == "" || strings.HasPrefix(branch, "refs/") {
		return "", fmt.Errorf("write_ref %q is not a branch this executor can write", ref)
	}
	if strings.ContainsAny(branch, " \t~^:?*[\\") || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
		return "", fmt.Errorf("write_ref %q is not a well-formed branch name", ref)
	}
	return branch, nil
}

// tickOf is the tick this job is about, for the report's filename and the
// agent's name. It comes from the inputs, never from parsing job_id.
func tickOf(spec *subprocess.JobSpec) string {
	for _, in := range spec.Inputs {
		if in.Kind == "tick" {
			return in.ID
		}
	}
	for _, in := range spec.Inputs {
		return in.ID
	}
	return "job"
}

// resultPath is the executor's, not the worker's: absolute, inside the
// attempt worktree, under the spec's artifact prefix. A path that escapes
// the worktree is refused rather than normalised.
func resultPath(worktree, prefix, tick string) (rel, abs string, err error) {
	if strings.HasPrefix(prefix, "/") {
		return "", "", fmt.Errorf("artifact_prefix %q is absolute: it names a destination outside the attempt, "+
			"and quietly reading it as relative would put the report somewhere nobody asked for", prefix)
	}
	rel = filepath.Join(filepath.FromSlash(prefix), "RESULT-"+tick+".md")
	abs = filepath.Join(worktree, rel)
	clean := filepath.Clean(abs)
	if !strings.HasPrefix(clean, filepath.Clean(worktree)+string(filepath.Separator)) {
		return "", "", fmt.Errorf("artifact_prefix %q puts the report outside the attempt worktree", prefix)
	}
	return filepath.ToSlash(rel), clean, nil
}
