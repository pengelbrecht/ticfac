package subprocess

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The pi-durable runner's per-attempt interface (epic 43y step 7, tick hpk):
// the worker.json this executor writes beside the attempt record, and the
// Node host (harness/src/local/main.ts) reads. The runner argv carries only
// this file's path, the model and the message — everything else about one
// attempt's worker is here, spelled once, in the shape the harness reads.
//
// The field names are the harness package's LocalWorkerConfig (it defines
// the interface; this is the writer), and the file is durable state like the
// attempt record beside it: a relaunched runner (the supervisor's nudge,
// report pushback or stuck re-prompt) reads the same file the first process
// did, reopens the same storage, and continues the same conversation.

// fileWorkerConfig is worker.json's name in the attempt's state directory.
const fileWorkerConfig = "worker.json"

// fileWorkerStorage is the conversation's local SQLite storage, beside the
// config that names it: one database per attempt, owned by whichever runner
// process is live (pi-durable's own single-owner rule).
const fileWorkerStorage = "worker.sqlite"

// steerSockPath is the Unix domain socket a live durable runner listens on
// for steers — the door the supervisor's stuck watch speaks through, named
// once here for the attempt record, worker.json and the supervisor's client
// alike. The socket is a TRANSIENT path, not state-directory content: a
// Unix domain socket's path is bounded by the kernel (104 bytes on darwin),
// and an attempt's state directory sits deeper than that bound in a real
// run's tree — so the socket lives under the system temp directory, at a
// short path HASHED from the state directory. The hash is the whole state
// path, so it is stable across supervisor restarts (the record names the
// socket, not a scan of temp) and unique per attempt. $TICFAC_STEER_SOCK_DIR
// moves the sockets somewhere else — a host whose temp directory is deeper
// than the socket-path bound, the test harness's per-suite TMPDIR being the
// one this repository actually meets.
func steerSockPath(stateDir string) string {
	dir := os.Getenv("TICFAC_STEER_SOCK_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	sum := sha256.Sum256([]byte(stateDir))
	return filepath.Join(dir, fmt.Sprintf("ticfac-steer-%s.sock", hex.EncodeToString(sum[:])[:16]))
}

// workerConfig is worker.json: the whole per-attempt interface with the
// pi-durable harness. Written at Start, before any runner process exists.
type workerConfig struct {
	// Storage is the conversation's local SQLite file.
	Storage string `json:"storage"`
	// SteerSock is the Unix socket the supervisor's stuck watch steers a
	// live runner through — a short hashed path (see steerSockPath).
	SteerSock string `json:"steerSock"`
	// Worktree is the attempt worktree: the tools' cwd, the report's home.
	Worktree string `json:"worktree"`
	// Remote is the remote the attempt branch pushes to (and the wip
	// checkpoints after every tool round); empty disables the checkpoints.
	Remote string `json:"remote"`
	// Branch is the attempt branch — the run's write ref.
	Branch string `json:"branch"`
	// Base is the commit the attempt was cut from.
	Base string `json:"base,omitempty"`
	// Report is the report's absolute path inside the worktree.
	Report string `json:"report"`
	// Checker is the report checker binary, absolute: the supervisor's own
	// executable, the same one record.LintCommand spells.
	Checker string `json:"checker"`
	// Tick is the tick this attempt implements.
	Tick string `json:"tick"`
	// Role is the role the report is checked as, "" for role-neutral.
	Role string `json:"role,omitempty"`
	// Model is the routed model, `provider/id` — what --model passes.
	Model string `json:"model"`
	// WallDeadlineMS is the wall as an ABSOLUTE epoch-ms deadline, so a
	// relaunched runner arms the wall the attempt already runs under rather
	// than a fresh one. Zero means none.
	WallDeadlineMS int64 `json:"wallDeadlineMs,omitempty"`
	// FauxTranscript is a TEST-ONLY seam: a scripted model transcript the
	// harness's own suite and this package's end-to-end test drive the
	// worker with, because a test that needs a model credential cannot run
	// anywhere. Empty on every production attempt.
	FauxTranscript string `json:"fauxTranscript,omitempty"`
}

// writeWorkerConfig renders and durably writes one attempt's worker.json,
// and is called before any runner process exists. The wall deadline is
// absolute: the attempt's issued-at stamp plus its wall seconds, so the
// clock a relaunched runner arms is the same one the supervisor enforces.
func writeWorkerConfig(write func(path string, data []byte, perm fs.FileMode) error, dir string, record *attemptRecord, opts *Options) error {
	config := workerConfig{
		Storage:   filepath.Join(dir, fileWorkerStorage),
		SteerSock: steerSockPath(dir),
		Worktree:  record.Worktree,
		Remote:    record.Remote,
		Branch:    record.Branch,
		Base:      record.BaseSHA,
		Report:    record.ResultPath,
		Checker:   opts.SupervisorArgv[0],
		Tick:      record.TickID,
		Model:     record.Model,
	}
	if record.Spec != nil {
		config.Role = record.Spec.Role
	}
	if record.WallSeconds > 0 {
		if issued, err := time.Parse(time.RFC3339, record.IssuedAt); err == nil {
			config.WallDeadlineMS = issued.Add(time.Duration(record.WallSeconds) * time.Second).UnixMilli()
		} else {
			return fmt.Errorf("parse the attempt's issued-at stamp %q for the worker's wall deadline: %w", record.IssuedAt, err)
		}
	}
	if opts != nil && opts.HarnessFauxTranscript != "" {
		config.FauxTranscript = opts.HarnessFauxTranscript
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return write(filepath.Join(dir, fileWorkerConfig), raw, 0o644)
}
