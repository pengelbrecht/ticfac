package herdr

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The executor's durable state for one attempt.
//
// Everything a restarted controller needs is here or in git, because the
// handle it holds may carry nothing but this directory's path — and the
// reconciler's adoption and teardown paths construct exactly that minimal
// handle. The rule the layout is built around is Appendix A #7: a write is
// confirmed by RE-READING it before anything acts on it. Start does not
// return a handle until the attempt record it just wrote has been read back.

const stateSchemaVersion = 1

// The files one attempt owns. attempt.json is the name the RECONCILER's
// findAttemptState walks for — changing it would strand every herdr attempt
// the moment a restart went looking for one.
const (
	fileAttempt      = "attempt.json"
	fileCancel       = "cancel.json"
	fileObservations = "observations.jsonl"
	filePrompt       = "prompt.md"
	fileResult       = "result.json"
	// fileAgentGone is this executor's own settlement record: written once,
	// by the inspect that POSITIVELY observed herdr answer that the agent is
	// no longer there. It is the herdr-free evidence a later collect reads to
	// say `failed` rather than `lost` — the "exit evidence the executor
	// itself recorded" half of the completion contract.
	fileAgentGone = "agent-gone"
	// fileReportArchive is where disposal moves the attempt's own untracked
	// report out of the worktree, so worktree.remove can proceed without
	// Force while the report survives somewhere a person can still read it.
	fileReportArchive = "report.md"
)

// attemptRecord is the durable description of one attempt: the herdr
// addressing a restarted controller needs to re-address it, and the
// provenance facts a collected record cites.
type attemptRecord struct {
	SchemaVersion int    `json:"schema_version"`
	Key           string `json:"key"`
	RepoKey       string `json:"repo_key"`
	Repo          string `json:"repo"`
	JobID         string `json:"job_id"`
	Attempt       int    `json:"attempt"`
	TickID        string `json:"tick_id"`

	Branch   string `json:"branch"`
	WriteRef string `json:"write_ref"`
	BaseSHA  string `json:"base_sha"`

	// The herdr addressing, in full: the workspace the attempt lives in, the
	// pane the agent occupies, the agent's name, and the worktree herdr
	// created. The handle carries a copy; this record is the one a minimal
	// {"state": …} handle resolves against.
	WorkspaceID string `json:"workspace_id"`
	PaneID      string `json:"pane_id"`
	AgentName   string `json:"agent_name"`
	Worktree    string `json:"worktree"`
	State       string `json:"state"`

	// ResultPath is ABSOLUTE and inside the attempt worktree; ResultRel is
	// the same path relative to the worktree, so a report the worker
	// committed can still be read out of the branch after the worktree is
	// gone.
	ResultPath string `json:"result_path"`
	ResultRel  string `json:"result_rel"`

	Kind       string   `json:"kind"` // the herdr agent kind
	AgentArgs  []string `json:"agent_args,omitempty"`
	Model      string   `json:"model,omitempty"`
	RolePrompt string   `json:"role_prompt,omitempty"`

	WallSeconds int    `json:"wall_seconds"`
	Remote      string `json:"remote"`
	SourceGrade string `json:"source_grade"`

	// ServerVersion and Protocol are what herdr said it was when this
	// attempt was started — the substrate provenance av8 asks for, so a run
	// that spans an upgrade can be diagnosed rather than guessed at.
	ServerVersion string `json:"server_version"`
	Protocol      uint32 `json:"protocol"`

	// LaunchConfirmed says agent.start was observed to succeed and the agent
	// reported ready. An attempt whose launch was never confirmed is
	// terminal without ever having been a worker — and is left on disk as
	// diagnostic state, never cleaned up by the failed Start itself.
	LaunchConfirmed bool `json:"launch_confirmed"`

	// DispatchConfirmed says the prompt submission was observed to put the
	// agent into `working`. False after an unconfirmed submission is NOT a
	// failure: a trivial tick can finish before `working` is ever rendered.
	DispatchConfirmed bool `json:"dispatch_confirmed"`

	IssuedAt string              `json:"issued_at"`
	Spec     *subprocess.JobSpec `json:"spec"`
}

// cancelRecord is the DURABLE refusal to reissue. It is written once, before
// any stop is requested, and nothing in this package unwrites it. It is the
// herdr executor's revocation: the credential a herdr attempt spends is the
// dispatch itself, and the record is what makes the refusal to issue it
// again outlive the process that recorded it.
type cancelRecord struct {
	SchemaVersion int    `json:"schema_version"`
	JobID         string `json:"job_id"`
	Attempt       int    `json:"attempt"`
	AcceptedAt    string `json:"accepted_at"`
	Reissue       string `json:"reissue"`
	Order         string `json:"order"`
	StopRequested bool   `json:"stop_requested"`
}

// store is one attempt's state directory.
type store struct {
	dir       string
	writeFile func(path string, data []byte, perm fs.FileMode) error
}

func newStore(dir string) *store {
	return &store{dir: dir, writeFile: atomicWrite}
}

func (s *store) path(name string) string { return filepath.Join(s.dir, name) }

func (s *store) exists(name string) bool {
	_, err := os.Stat(s.path(name))
	return err == nil
}

// atomicWrite writes through a temporary file in the same directory, so a
// reader never sees a half-written record. Inspect runs in another process
// and reads these files at any moment.
func atomicWrite(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *store) writeJSON(name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFile(s.path(name), append(raw, '\n'), 0o644)
}

func (s *store) readJSON(name string, value any) error {
	raw, err := os.ReadFile(s.path(name))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}

// writeAttempt writes the attempt record and READS IT BACK (Appendix A #7):
// a write that silently did not land must not look like a job somebody can
// address.
func (s *store) writeAttempt(record *attemptRecord) error {
	if err := s.writeJSON(fileAttempt, record); err != nil {
		return fmt.Errorf("write the attempt record: %w", err)
	}
	confirmed, err := s.readAttempt()
	if err != nil {
		return fmt.Errorf("the attempt record did not land: %w", err)
	}
	if confirmed.Key != record.Key || confirmed.JobID != record.JobID ||
		confirmed.Attempt != record.Attempt || confirmed.WorkspaceID != record.WorkspaceID {
		return fmt.Errorf("the attempt record read back as %s/%s attempt %d in %s, not %s/%s attempt %d in %s",
			confirmed.Key, confirmed.JobID, confirmed.Attempt, confirmed.WorkspaceID,
			record.Key, record.JobID, record.Attempt, record.WorkspaceID)
	}
	return nil
}

func (s *store) readAttempt() (*attemptRecord, error) {
	var record attemptRecord
	if err := s.readJSON(fileAttempt, &record); err != nil {
		return nil, err
	}
	if record.SchemaVersion != stateSchemaVersion {
		return nil, fmt.Errorf("attempt record schema_version %d, want %d", record.SchemaVersion, stateSchemaVersion)
	}
	if record.Spec == nil {
		return nil, fmt.Errorf("attempt record at %s carries no JobSpec: collect could not say what was asked for", s.dir)
	}
	return &record, nil
}

func (s *store) cancelled() (*cancelRecord, bool) {
	var record cancelRecord
	if err := s.readJSON(fileCancel, &record); err != nil {
		return nil, false
	}
	return &record, true
}

// agentGone is the settlement record: this executor's own observation that
// herdr positively answered "the agent is no longer there".
func (s *store) markAgentGone(at string) error {
	if s.exists(fileAgentGone) {
		return nil
	}
	return s.writeFile(s.path(fileAgentGone), []byte(at+"\n"), 0o644)
}

func (s *store) agentGone() bool { return s.exists(fileAgentGone) }

// observe appends one observation. Append-only and one write per record, so
// a reader in another process sees whole lines.
func (s *store) observe(obs subprocess.Observation) error {
	raw, err := json.Marshal(obs)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(fileObservations), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	return err
}

// observationsFrom resumes the stream at cursor and returns the observations
// after it, with the cursor to hand back next time. The cursor is a byte
// offset into the append-only log: opaque to the caller, exactly as the
// contract says.
func (s *store) observationsFrom(cursor string) ([]subprocess.Observation, string) {
	raw, err := os.ReadFile(s.path(fileObservations))
	if err != nil {
		return nil, "0"
	}
	offset := 0
	if cursor != "" {
		if n, err := strconv.Atoi(cursor); err == nil && n >= 0 && n <= len(raw) {
			offset = n
		}
	}
	rest := string(raw[offset:])
	var out []subprocess.Observation
	for _, line := range strings.Split(rest, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var obs subprocess.Observation
		if err := json.Unmarshal([]byte(line), &obs); err != nil {
			continue
		}
		out = append(out, obs)
	}
	return out, strconv.Itoa(len(raw))
}

// stamp is the executor's own clock, formatted for a record.
func stamp(now func() time.Time) string {
	return now().UTC().Format(time.RFC3339)
}
