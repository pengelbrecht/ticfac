package cloudflaresandbox

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The executor's durable state for one attempt.
//
// The door needs none of this — it re-addresses a container by the identity
// the credential names — but this executor's own decisions do: an attempt
// whose record exists is one whose dispatch once landed, and "the record
// exists and the door cannot address the container" is a HOLD, never a
// redispatch. The rule the layout is built around is Appendix A #7: a write
// is confirmed by RE-READING it before anything acts on it. Start does not
// return a handle until the attempt record it just wrote has been read back.

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
// reader never sees a half-written record. A later leg — the reconciler's
// adoption walk, a teardown in another process — reads these files at any
// moment.
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
// address. The read-back compares the identity, not the whole record — a
// field this writer does not round-trip identically is a bug to catch in a
// test, not a dispatch to refuse.
func (s *store) writeAttempt(record *attemptRecord) error {
	if err := s.writeJSON(fileAttempt, record); err != nil {
		return fmt.Errorf("write the attempt record: %w", err)
	}
	confirmed, err := s.readAttempt()
	if err != nil {
		return fmt.Errorf("the attempt record did not land: %w", err)
	}
	if confirmed.JobID != record.JobID || confirmed.Attempt != record.Attempt ||
		confirmed.TickID != record.TickID || confirmed.Sandbox != record.Sandbox {
		return fmt.Errorf("the attempt record read back as %s tick %s attempt %d in %s, not %s tick %s attempt %d in %s",
			confirmed.JobID, confirmed.TickID, confirmed.Attempt, confirmed.Sandbox,
			record.JobID, record.TickID, record.Attempt, record.Sandbox)
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
		return nil, fmt.Errorf("attempt record at %s carries no JobSpec: a later leg could not say what was asked for", s.dir)
	}
	return &record, nil
}
