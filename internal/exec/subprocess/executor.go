package subprocess

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The executor: start, inspect, cancel, collect, and the one local operation
// the protocol does not have a verb for — dispose.

// Options configure the host. Nothing here crosses the protocol seam: the
// records are closed, so which runner is installed, where state lives and
// which remote to push to are the host's business and not the reconciler's.
type Options struct {
	// Repo is the local checkout attempts branch from. Defaults to the
	// working directory.
	Repo string

	// StateDir is the executor's private state root. Defaults to
	// $TICFAC_EXEC_STATE_DIR, else ~/.ticfac/exec/local-subprocess. It is
	// deliberately OUTSIDE the repository: an executor writing run state into
	// the tree it is running a job in would be dirtying the very worktree the
	// boundary diff reads.
	StateDir string

	// Runner is claude | codex | pi. RunnerArgv overrides the table entirely,
	// which is how the tests drive a fake runner — and an override is the
	// WHOLE invocation, so nothing below is inserted into one.
	Runner     string
	RunnerArgv []string

	// Model is the model the caller's profile resolved for this job, applied
	// through the runner's own model flag. Empty launches the runner as it is,
	// on whatever model its own configuration chooses. It is host
	// configuration and not a JobSpec field, because the protocol's records
	// are closed and a field invented here would be one the reconciler ignores.
	Model string

	// RolePrompt is the profile's prompt for this job's role: the instruction
	// that says what the role IS, which the rendered worker prompt opens with.
	// The executor owns the mechanics around it — the report path, the
	// boundary, the status vocabulary — and never the role.
	RolePrompt string

	// SupervisorArgv is how this executor re-invokes itself to supervise an
	// attempt. Defaults to the running executable plus "supervise".
	SupervisorArgv []string

	// Remote is the origin in-progress work is pushed to. Empty means no push
	// is possible, which the attempt record says out loud rather than leaving
	// a caller to infer from silence.
	Remote string

	// Attempt is the attempt number this start is for. It comes from the
	// caller because job_id is OPAQUE to an executor — the contract says the
	// reconciler owns its shape, so parsing an attempt out of it would be this
	// side deciding what the other side's identifier means.
	Attempt int

	PushInterval  time.Duration
	SalvageWindow time.Duration

	Now func() time.Time

	// guardsOff disables one named guard, for the invariants suite's negative
	// control. Names are contracts/lifecycle-invariants.json's guard names.
	guardsOff map[string]bool

	// writeFile is the state writer, so a test can inject a write that
	// silently does not land — the only way to see whether the read-back after
	// write is doing anything (Appendix A #7).
	writeFile func(path string, data []byte, perm fs.FileMode) error
}

// Executor is one host, pointed at one repository.
type Executor struct {
	opts    Options
	repo    string
	repoKey string
	root    string
	now     func() time.Time
}

// Refusal is a refusal to act, carrying the REASON as its own value. A
// refusal whose reason a caller has to recover by matching on prose is the
// failure Appendix A #9 is about.
type Refusal struct {
	Reason  string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// The reasons this executor refuses. Each is a different problem and sends the
// next repair somewhere different.
const (
	RefusedStopped      = "stopped"            // a durable stop refuses to issue a credential
	RefusedCancelled    = "cancelled"          // this handle was cancelled; reissue is refused
	RefusedLive         = "live"               // an attempt under this identity is still running
	RefusedUnknown      = "liveness_unknown"   // nobody can say whether it is running, which is not "nothing is"
	RefusedSettled      = "settled"            // this attempt already settled; a retry is a new attempt number
	RefusedNotPersisted = "not_persisted"      // disposal before the work is durable
	RefusedCredential   = "credential_live"    // teardown before the credential died
	RefusedBranchUnsafe = "branch_not_durable" // the branch holds commits no remote has
)

func refuse(reason, format string, args ...any) *Refusal {
	return &Refusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// AsRefusal reports whether err is a refusal, and which one.
func AsRefusal(err error) (*Refusal, bool) {
	var r *Refusal
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}

// New resolves the repository and the state root once, so that every
// operation afterwards names the directory it works in.
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
	if opts.Runner == "" {
		opts.Runner = "claude"
	}
	if len(opts.RunnerArgv) == 0 {
		if raw := os.Getenv(EnvRunnerArgv); raw != "" {
			opts.RunnerArgv = strings.Split(strings.TrimRight(raw, "\n"), "\n")
		}
	}
	if opts.Attempt <= 0 {
		opts.Attempt = 1
	}
	if opts.PushInterval <= 0 {
		opts.PushInterval = DefaultPushInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.writeFile == nil {
		opts.writeFile = atomicWrite
	}
	if len(opts.SupervisorArgv) == 0 {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate this executable to supervise with: %w", err)
		}
		opts.SupervisorArgv = []string{self, "supervise"}
	}
	return &Executor{opts: opts, repo: root, repoKey: key, root: opts.StateDir, now: opts.Now}, nil
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

// RepoKey identifies the repository — half of a handle's identity, and the
// half that keeps one tick id in two checkouts from colliding.
func (e *Executor) RepoKey() string { return e.repoKey }

func (e *Executor) guarded(name string) bool { return !e.opts.guardsOff[name] }

func (e *Executor) stamp() string { return e.now().UTC().Format(time.RFC3339) }

// attemptKey is the handle's identity: (repo key, job id, attempt). job_id
// carries the reconciler's run and tick; the repo key is what this executor
// adds, because two repositories on one machine can be running the same tick.
func attemptKey(repoKey, jobID string, attempt int) string {
	sum := sha256.Sum256([]byte(repoKey + "\x00" + jobID + "\x00" + strconv.Itoa(attempt)))
	return hex.EncodeToString(sum[:])[:16]
}

func (e *Executor) stateDirFor(jobID string, attempt int) string {
	return filepath.Join(e.root, e.repoKey, attemptKey(e.repoKey, jobID, attempt))
}

func (e *Executor) storeAt(dir string) *store {
	st := newStore(dir)
	st.writeFile = e.opts.writeFile
	return st
}

// ------------------------------------------------------------------ stop ---

// stopRecord is the executor-wide durable refusal to issue: written once, read
// before every boot, and never unwritten by anything in this package.
type stopRecord struct {
	SchemaVersion int    `json:"schema_version"`
	RequestedAt   string `json:"requested_at"`
	By            string `json:"by"`
	Reason        string `json:"reason,omitempty"`
}

func (e *Executor) stopPath() string { return filepath.Join(e.root, e.repoKey, "stop.json") }

// RequestStop records the durable stop. It is a refusal to ISSUE a credential,
// checked before every boot — not a revocation the next boot undoes.
func (e *Executor) RequestStop(by, reason string) error {
	record := stopRecord{SchemaVersion: stateSchemaVersion, RequestedAt: e.stamp(), By: by, Reason: reason}
	raw, err := jsonIndent(record)
	if err != nil {
		return err
	}
	return e.opts.writeFile(e.stopPath(), raw, 0o644)
}

// Stopped reports the durable stop, if there is one.
func (e *Executor) Stopped() (*stopRecord, bool) {
	raw, err := os.ReadFile(e.stopPath())
	if err != nil {
		return nil, false
	}
	var record stopRecord
	if err := jsonUnmarshal(raw, &record); err != nil {
		return nil, false
	}
	return &record, true
}

// ----------------------------------------------------------------- start ---

// Start creates the attempt worktree, issues the attempt's credential and
// launches the runner under a supervisor that outlives this call.
//
// It never redispatches a live attempt: an identity that is already running is
// ADOPTED (the same handle comes back), and one whose liveness nobody can
// answer is HELD. "Nobody can say" is not "nothing is running", and treating
// it as one is how a run pays twice for one tick.
func (e *Executor) Start(spec *JobSpec) (*JobHandle, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	attempt := e.opts.Attempt
	dir := e.stateDirFor(spec.JobID, attempt)
	st := e.storeAt(dir)

	// A1: the stop is checked before a credential is issued, not merely before
	// work is started.
	if e.guarded("stop_refuses_issue") {
		if stop, ok := e.Stopped(); ok {
			return nil, refuse(RefusedStopped, "a stop was recorded at %s by %s: no credential is issued and no attempt boots",
				stop.RequestedAt, stop.By)
		}
		if cancelled, ok := st.cancelled(); ok {
			return nil, refuse(RefusedCancelled, "this handle was cancelled at %s and reissue is %s",
				cancelled.AcceptedAt, cancelled.Reissue)
		}
	}

	// A6: adopt by stable identity; a fresh attempt is created only when the
	// previous one is proven dead.
	if existing, err := st.readAttempt(); err == nil {
		status := e.statusOf(st, existing, "")
		switch {
		case !e.guarded("never_redispatch_live"):
			// The guard is off: fall through and dispatch over whatever is
			// running, which is the bug the guard exists for.
		case status.State == StateRunning || status.State == StateStarting || status.State == StatePending:
			return e.handleFor(existing), nil
		case status.State == StateLost:
			return nil, refuse(RefusedUnknown,
				"attempt %d of %s cannot be addressed and has not settled; it is held, never redispatched",
				existing.Attempt, existing.JobID)
		case status.Terminal:
			return nil, refuse(RefusedSettled,
				"attempt %d of %s already settled as %s; a retry is a new attempt number, not this one again",
				existing.Attempt, existing.JobID, status.State)
		}
	}

	base, err := resolveCommit(e.repo, spec.Source.BaseSHA)
	if err != nil {
		return nil, fmt.Errorf("the base %q is not a commit this checkout has: %w", spec.Source.BaseSHA, err)
	}
	branch, err := branchFromWriteRef(spec)
	if err != nil {
		return nil, err
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
		Worktree:      filepath.Join(dir, dirWorktree),
		State:         dir,
		Runner:        e.opts.Runner,
		Model:         e.opts.Model,
		RolePrompt:    e.opts.RolePrompt,
		WallSeconds:   spec.Limits.WallSeconds,
		PushInterval:  int(e.opts.PushInterval / time.Second),
		PushOnTimer:   e.guarded("push_on_timer"),
		Remote:        e.remoteFor(spec),
		SourceGrade:   spec.Credentials.Source.Grade(),
		IssuedAt:      e.stamp(),
		Spec:          spec,
	}
	rel, abs, err := resultPath(record.Worktree, spec.ArtifactPrefix, record.TickID)
	if err != nil {
		return nil, err
	}
	record.ResultRel, record.ResultPath = rel, abs
	record.RunnerEnv = runnerEnv(record, spec)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	prompt := renderPrompt(record, spec)
	if err := e.opts.writeFile(st.path(filePrompt), []byte(prompt), 0o644); err != nil {
		return nil, err
	}
	// The runner's argv is rendered per ATTEMPT, because part of it is this
	// repository's git common directory: a sandboxed runner in a linked
	// worktree cannot commit without it, and it is a different path in every
	// checkout.
	common, err := gitCommonDir(e.repo)
	if err != nil {
		return nil, fmt.Errorf("resolve the repository's git common directory: %w", err)
	}
	argv, err := resolveRunner(e.opts.Runner, e.opts.RunnerArgv,
		launch{Prompt: prompt, GitCommonDir: common, Model: e.opts.Model})
	if err != nil {
		return nil, err
	}
	record.RunnerArgv = argv

	if err := e.makeWorktree(record); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}

	// The credential is issued only after everything that could refuse has
	// refused, and it is a file, because the process that revokes it and the
	// process that would spend it are not the same process.
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	if err := st.issueCredential(hex.EncodeToString(token)); err != nil {
		return nil, err
	}
	_ = st.observe(Observation{At: e.stamp(), Kind: ObsCredentialIssued,
		Detail: fmt.Sprintf("source grade %s, remote %q", record.SourceGrade, record.Remote)})

	// A7: the attempt record is read back before anything acts on it. A handle
	// for a record that did not land is a job nobody can find.
	if err := st.writeAttempt(record, e.guarded("read_back_after_write")); err != nil {
		return nil, err
	}

	pid, err := e.spawnSupervisor(record)
	if err != nil {
		return nil, err
	}
	record.SupervisorPID = pid
	if err := st.writeAttempt(record, e.guarded("read_back_after_write")); err != nil {
		return nil, err
	}
	_ = e.opts.writeFile(st.path(fileSupervisorPID), []byte(strconv.Itoa(pid)+"\n"), 0o644)

	return e.handleFor(record), nil
}

func (e *Executor) makeWorktree(record *attemptRecord) error {
	if _, err := os.Stat(record.Worktree); err == nil {
		return nil
	}
	if branchExists(e.repo, record.Branch) {
		return refuse(RefusedLive,
			"branch %s already exists in %s: this attempt would write a ref another one owns",
			record.Branch, e.repo)
	}
	if err := worktreeAdd(e.repo, record.Worktree, record.Branch, record.BaseSHA); err != nil {
		return fmt.Errorf("create the attempt worktree: %w", err)
	}
	return nil
}

func (e *Executor) spawnSupervisor(record *attemptRecord) (int, error) {
	argv := append([]string{}, e.opts.SupervisorArgv...)
	argv = append(argv, "--state", record.State)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = record.Worktree
	cmd.SysProcAttr = newProcessGroup()
	// The supervisor's own output goes nowhere: everything it has to say it
	// says in the observation log and the runner log, which survive it.
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devnull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start the supervisor: %w", err)
	}
	pid := cmd.Process.Pid

	// Reap it when it eventually exits. The supervisor is a child of THIS
	// process, and an unreaped child is a zombie — which answers kill(pid, 0)
	// and would make a finished attempt read as a live one for as long as the
	// controller stayed up. A controller embedding this executor is exactly
	// that long-lived process, so the reaping cannot be left to the shell.
	go func() { _ = cmd.Wait() }()

	return pid, nil
}

func (e *Executor) handleFor(record *attemptRecord) *JobHandle {
	local := &LocalHandle{
		PID:        record.SupervisorPID,
		Worktree:   record.Worktree,
		Branch:     record.Branch,
		Repo:       record.Repo,
		RepoKey:    record.RepoKey,
		State:      record.State,
		ResultPath: record.ResultPath,
		BaseSHA:    record.BaseSHA,
		WriteRef:   record.WriteRef,
	}
	return &JobHandle{
		SchemaVersion: SchemaVersion,
		JobID:         record.JobID,
		Attempt:       record.Attempt,
		Executor:      ExecutorName,
		Handle:        local.asMap(),
		IssuedAt:      record.IssuedAt,
	}
}

// remoteFor is the remote in-progress work is pushed to, or "" when there is
// none to push to. A read-only source grade issues no push credential at all,
// which is enforced in attemptRecord.canPush.
func (e *Executor) remoteFor(spec *JobSpec) string {
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
// this job may write, and refuses a ref outside the namespace the source grant
// bounds. That is the difference between a boundary the issuer enforces and
// one the runner is asked to respect.
func branchFromWriteRef(spec *JobSpec) (string, error) {
	ref := spec.Source.WriteRef
	if prefix := spec.Credentials.Source.WriteRefPrefix(); prefix != "" && !strings.HasPrefix(ref, prefix) {
		return "", refuse(RefusedCredential,
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

// tickOf is the tick this job is about, for the report's filename. It comes
// from the inputs, never from parsing job_id.
func tickOf(spec *JobSpec) string {
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

// resultPath is the executor's, not the worker's: absolute, inside the attempt
// worktree, under the spec's artifact prefix. A path that escapes the worktree
// is refused rather than normalised, because the escape is the interesting
// part of such a spec.
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
