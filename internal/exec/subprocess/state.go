package subprocess

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The executor's durable state for one attempt.
//
// Everything a restarted controller needs is here or in git, because the
// handle it holds carries nothing but this directory's path. The rule the
// layout is built around is Appendix A #7: a write is confirmed by RE-READING
// it before anything acts on it. `start` does not return a handle until the
// attempt record it just wrote has been read back — a write that silently did
// not land must not look like a job somebody can address.

const stateSchemaVersion = 1

// The files one attempt owns. Names are part of the executor's contract with
// itself: `inspect` in a second process finds an attempt by reading these.
const (
	fileAttempt       = "attempt.json"
	fileCredential    = "credential"
	fileCancel        = "cancel.json"
	fileObservations  = "observations.jsonl"
	fileRunnerLog     = "runner.log"
	fileRunnerExit    = "runner.exit"
	fileRunnerPID     = "runner.pid"
	fileSupervisorPID = "supervisor.pid"
	fileWallExceeded  = "wall_clock_exceeded"
	fileLastPush      = "last_push"
	filePrompt        = "prompt.md"
	fileResult        = "result.json"
	dirWorktree       = "worktree"
)

// attemptRecord is the durable description of one attempt: enough for the
// supervisor to run it, and enough for a controller holding only a handle to
// re-address it.
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
	Worktree string `json:"worktree"`
	State    string `json:"state"`

	// ResultPath is ABSOLUTE and inside the attempt worktree; ResultRel is the
	// same path relative to the worktree, so a report the worker committed can
	// still be read out of the branch after the worktree is gone.
	ResultPath string `json:"result_path"`
	ResultRel  string `json:"result_rel"`

	Runner     string   `json:"runner"`
	RunnerArgv []string `json:"runner_argv"`
	RunnerEnv  []string `json:"runner_env"`

	WallSeconds  int `json:"wall_seconds"`
	PushInterval int `json:"push_interval_seconds"`

	// PushOnTimer is Appendix A #5's guard, carried into the supervisor
	// process because that is where the timer runs. It is always true outside
	// the invariants suite's negative control.
	PushOnTimer bool `json:"push_on_timer"`

	Remote      string `json:"remote"`
	SourceGrade string `json:"source_grade"`

	IssuedAt      string `json:"issued_at"`
	SupervisorPID int    `json:"supervisor_pid"`

	Spec *JobSpec `json:"spec"`
}

// store is one attempt's state directory. The writer is a field so a test can
// inject a write that silently does not land — which is the only way to see
// whether the read-back is doing anything (Appendix A #7's negative control).
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
// reader never sees a half-written record. `inspect` runs in another process
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

// writeAttempt writes the attempt record and READS IT BACK. The read-back is
// the guard: with it off, a write that did not land leaves a handle nobody can
// resolve, and the caller believes the job started.
func (s *store) writeAttempt(record *attemptRecord, readBack bool) error {
	if err := s.writeJSON(fileAttempt, record); err != nil {
		return fmt.Errorf("write the attempt record: %w", err)
	}
	if !readBack {
		return nil
	}
	confirmed, err := s.readAttempt()
	if err != nil {
		return fmt.Errorf("the attempt record did not land: %w", err)
	}
	if confirmed.Key != record.Key || confirmed.JobID != record.JobID || confirmed.Attempt != record.Attempt {
		return fmt.Errorf("the attempt record read back as %s/%s attempt %d, not %s/%s attempt %d",
			confirmed.Key, confirmed.JobID, confirmed.Attempt, record.Key, record.JobID, record.Attempt)
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

// ------------------------------------------------------------ credentials ---

// cancelRecord is the DURABLE refusal to issue. It is written once, before any
// stop is requested, and nothing in this package unwrites it: a kill switch is
// a refusal to ISSUE checked before every boot, not a revocation a restart can
// undo.
type cancelRecord struct {
	SchemaVersion   int     `json:"schema_version"`
	JobID           string  `json:"job_id"`
	Attempt         int     `json:"attempt"`
	AcceptedAt      string  `json:"accepted_at"`
	Reissue         string  `json:"reissue"`
	Order           string  `json:"order"`
	StopRequested   bool    `json:"stop_requested"`
	SalvageDeadline *string `json:"salvage_deadline,omitempty"`
	Reason          string  `json:"reason,omitempty"`
}

func (s *store) cancelled() (*cancelRecord, bool) {
	var record cancelRecord
	if err := s.readJSON(fileCancel, &record); err != nil {
		return nil, false
	}
	return &record, true
}

// credentialLive is the question the pusher asks before every push, and it is
// a FILE rather than a flag in memory because the process that revokes and the
// process that would spend are not the same process.
func (s *store) credentialLive() bool { return s.exists(fileCredential) }

func (s *store) issueCredential(token string) error {
	return s.writeFile(s.path(fileCredential), []byte(token+"\n"), 0o600)
}

func (s *store) revokeCredential() error {
	err := os.Remove(s.path(fileCredential))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ----------------------------------------------------------- observations ---

// observe appends one observation. Append-only and one write per record, so a
// reader in another process sees whole lines.
func (s *store) observe(obs Observation) error {
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
// contract says, and meaningful to nobody else.
func (s *store) observationsFrom(cursor string) ([]Observation, string) {
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
	var out []Observation
	for _, line := range strings.Split(rest, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var obs Observation
		if err := json.Unmarshal([]byte(line), &obs); err != nil {
			continue
		}
		out = append(out, obs)
	}
	return out, strconv.Itoa(len(raw))
}

// ------------------------------------------------------------- settlement ---

// settled says the supervisor recorded an exit. It is NOT completion: a
// settled attempt with no report is `failed`, which is a different fact from
// both "succeeded" and "still running".
func (s *store) settled() bool { return s.exists(fileRunnerExit) }

func (s *store) exitCode() (int, bool) {
	raw, err := os.ReadFile(s.path(fileRunnerExit))
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, false
	}
	return code, true
}

func (s *store) wallClockExceeded() bool { return s.exists(fileWallExceeded) }

func (s *store) runnerPID() int     { return s.pidIn(fileRunnerPID) }
func (s *store) supervisorPID() int { return s.pidIn(fileSupervisorPID) }

func (s *store) pidIn(name string) int {
	raw, err := os.ReadFile(s.path(name))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return pid
}

// jsonIndent and jsonUnmarshal keep the executor from importing encoding/json
// in three files for two calls.
func jsonIndent(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func jsonUnmarshal(raw []byte, value any) error { return json.Unmarshal(raw, value) }
