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
	// A READ-ONLY grade is always written empty: its runner is pinned so that
	// every push fails at the transport (grade.go), so a non-empty remote would
	// install checkpoints whose every push the pins refuse — one failed
	// checkpoint a tool round — and a finish phase that salvages onto a ref the
	// grade granted no write to. The harness keys the checkpoints, the restore
	// and the finish phase on exactly this field (worker-host.ts), so empty is
	// the whole difference for a read-only local run.
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
	// Metering is the local gateway metering join (tick m1w), on a dispatch
	// whose resolver produced one AND a Workers AI model: the facts the
	// harness composes its provider override from — the gateway route, the
	// run id, the attribution metadata and the credential pipeline. Nil for
	// an unmetered dispatch, and for any model the join does not apply to:
	// the config the tests' faux worker reads carries none, the same rule
	// the herdr executor's extension launch applies.
	Metering *workerMetering `json:"metering,omitempty"`
}

// workerMetering is the metering join as worker.json carries it — the facts
// the harness composes its provider override from (harness/src/local/
// gateway-metering.ts, tick m1w). The metadata is the COMPOSED header value,
// not the raw facts: one writer (MetadataValue) for both artifacts the join
// reaches — the pi CLI's generated extension and this file — so the rows a
// local durable worker tags can never disagree with the rows the same join
// tags through the CLI. The credential command is the pipeline WITHOUT pi's
// `!` config-value prefix: the harness executes it itself, at request time,
// and the token is never written to disk.
type workerMetering struct {
	// GatewayURL is the operator's AI Gateway base URL; the harness composes
	// the workers-ai route under it.
	GatewayURL string `json:"gatewayUrl"`
	// RunID is the run the spend is attributed to — the metadata's key, and
	// the affinity header's value.
	RunID string `json:"runId"`
	// Metadata is the cf-aig-metadata header value, composed once.
	Metadata string `json:"metadata"`
	// CredentialCommand is the POSIX shell pipeline that prints the
	// operator's Cloudflare API token as its Bearer value.
	CredentialCommand string `json:"credentialCommand"`
}

// meteringFor is the join a durable launch carries, or nil: the dispatch's
// resolver output only on a model the join applies to, and validated to the
// same absolute-URL bar WriteExtension enforces — a join that cannot be
// composed is a launch refusal, never a silently unmetered run.
func meteringFor(record *attemptRecord, opts *Options) (*workerMetering, error) {
	if opts == nil || opts.Metering == nil || !opts.Metering.Applies(record.Model) {
		return nil, nil
	}
	if _, err := opts.Metering.gatewayBase(); err != nil {
		return nil, err
	}
	return &workerMetering{
		GatewayURL:        opts.Metering.GatewayURL,
		RunID:             opts.Metering.RunID,
		Metadata:          opts.Metering.MetadataValue(),
		CredentialCommand: gatewayCredentialPipeline,
	}, nil
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
		Branch:    record.Branch,
		Base:      record.BaseSHA,
		Report:    record.ResultPath,
		Checker:   opts.SupervisorArgv[0],
		Tick:      record.TickID,
		Model:     record.Model,
	}
	// The remote is a WRITE grade's alone (tick x8e): a read-only attempt's
	// runner cannot push anywhere — the grade pins every push to refusal — so
	// its config carries none and the harness runs it without the workspace
	// checkpoints. The grade fails CLOSED, the same sentence grade.go states:
	// a grade this build does not recognise is not a write grant.
	if !record.readOnly() {
		config.Remote = record.Remote
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
	join, err := meteringFor(record, opts)
	if err != nil {
		return err
	}
	config.Metering = join
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return write(filepath.Join(dir, fileWorkerConfig), raw, 0o644)
}
