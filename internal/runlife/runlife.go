// Package runlife is the run-epic process's own account of itself: whether it
// is alive, and what it said before it stopped.
//
// It exists because a run could die and nobody could tell. During the ticks pwp
// production run the orchestrator died three times; each death was invisible in
// the feed, whose last line was an ordinary success, and the operator's
// liveness check — `pgrep -f "ticfac run-epic"` — matched the operator's OWN
// watcher and reported a dead run alive for ten minutes. The cause of the first
// death was never recovered, because the process had been launched without its
// output captured.
//
// So liveness is a fact ticfac reports, not a pattern an operator invents, and a
// death leaves a body regardless of how the process was launched:
//
//   - run.pid names the process AND its start time, so a recycled pid cannot
//     read as the run.
//   - run.log carries everything the process wrote to stderr, plus any panic.
//
// Both live beside the feed, under .ticfac/logs/<run-id>/, which ticfac's
// gitignore fragment already marks as exhaust, and which `ticfac events`
// already resolves. Tickets udp and ix9.
package runlife

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

const (
	PIDName = "run.pid"
	LogName = "run.log"

	SchemaVersion = 1
)

// Dir is the run's log directory: the feed's, so there is one place to look.
func Dir(repo, runID string) string {
	return filepath.Join(repo, runstate.Root, "logs", runID)
}

// Record is what run.pid holds.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	PID           int    `json:"pid"`
	// ProcessStart is the OS's own start time for PID, verbatim, as
	// `ps -o lstart= -p <pid>` prints it. It is compared as a string and never
	// parsed: a pid the OS has handed to another process answers with a
	// different start time, and equality is the whole test.
	ProcessStart string `json:"process_start"`
	StartedAt    string `json:"started_at"`
	Host         string `json:"host"`
}

// ErrAlreadyLive refuses a second process for a run that has a live one.
var ErrAlreadyLive = errors.New("a live process already drives this run")

// Life is a claimed run: its pidfile is written and its log is open.
type Life struct {
	dir    string
	record Record
	mu     sync.Mutex
	log    *os.File
	done   bool
}

// Claim records this process as the run's live driver and opens its log. It
// refuses when run.pid already names a LIVE process with the same start time: a
// second run-epic for one run is a second reconciler, and the run-state CAS is
// the last line of defence against that, not the first.
func Claim(repo, runID string) (*Life, error) {
	dir := Dir(repo, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	if existing, ok, err := readRecord(dir); err == nil && ok {
		if start, alive := processStart(existing.PID); alive && start == existing.ProcessStart && existing.PID != os.Getpid() {
			return nil, fmt.Errorf("%w: pid %d, started %s (%s)", ErrAlreadyLive, existing.PID, existing.StartedAt, filepath.Join(dir, PIDName))
		}
	}

	pid := os.Getpid()
	start, _ := processStart(pid)
	host, _ := os.Hostname()
	record := Record{
		SchemaVersion: SchemaVersion,
		RunID:         runID,
		PID:           pid,
		ProcessStart:  start,
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
		Host:          host,
	}
	if err := writeRecord(dir, record); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(dir, LogName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		_ = os.Remove(filepath.Join(dir, PIDName))
		return nil, fmt.Errorf("open %s: %w", filepath.Join(dir, LogName), err)
	}
	l := &Life{dir: dir, record: record, log: log}
	l.Logf("run %s started as pid %d on %s", runID, pid, host)
	return l, nil
}

// Log is the writer stderr is teed into.
func (l *Life) Log() io.Writer { return lockedWriter{l} }

// Logf writes one stamped line to the run log.
func (l *Life) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.log == nil {
		return
	}
	fmt.Fprintf(l.log, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// Release records how the process ended and removes run.pid. Only a process
// that REACHES Release removes it, so a run.pid naming a process that is gone
// is itself the evidence that the run stopped without saying so.
func (l *Life) Release(outcome string) {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	l.mu.Unlock()
	l.Logf("run %s ended: %s", l.record.RunID, outcome)
	_ = os.Remove(filepath.Join(l.dir, PIDName))
	l.mu.Lock()
	if l.log != nil {
		_ = l.log.Close()
		l.log = nil
	}
	l.mu.Unlock()
}

type lockedWriter struct{ l *Life }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.l.mu.Lock()
	defer w.l.mu.Unlock()
	if w.l.log == nil {
		return len(p), nil
	}
	return w.l.log.Write(p)
}

// State is the answer to "is this run alive?".
type State string

const (
	// Alive: run.pid names a process that exists with the recorded start time.
	Alive State = "alive"
	// Dead: run.pid names a process that is gone, or whose pid now belongs to
	// another process. The run stopped without releasing — it died.
	Dead State = "dead"
	// NotRunning: no run.pid. The last process released it on the way out, or
	// no process has claimed this run on this checkout.
	NotRunning State = "not_running"
	// Unknown: the OS could not be asked.
	Unknown State = "unknown"
)

// Status is everything `ticfac status` reports.
type Status struct {
	RunID     string         `json:"run_id"`
	State     State          `json:"state"`
	Reason    string         `json:"reason"`
	Record    *Record        `json:"record,omitempty"`
	LastEvent *runfeed.Event `json:"last_event,omitempty"`
	EventAge  string         `json:"last_event_age,omitempty"`
	Log       string         `json:"log"`
}

// Probe answers whether the run is alive, from run.pid and the OS, and how long
// ago it last said anything, from the feed.
func Probe(repo, runID string, now time.Time) Status {
	dir := Dir(repo, runID)
	status := Status{RunID: runID, Log: filepath.Join(dir, LogName)}

	if events, err := runfeed.Read(runfeed.Path(repo, runID)); err == nil && len(events) > 0 {
		last := events[len(events)-1]
		status.LastEvent = &last
		if at, err := time.Parse(time.RFC3339, last.At); err == nil {
			status.EventAge = now.Sub(at).Round(time.Second).String()
		}
	}

	record, ok, err := readRecord(dir)
	switch {
	case err != nil:
		status.State, status.Reason = Unknown, fmt.Sprintf("run.pid is unreadable: %v", err)
		return status
	case !ok:
		status.State, status.Reason = NotRunning, "no process holds this run: the last one released it, or none has claimed it here"
		return status
	}
	status.Record = &record

	if _, err := exec.LookPath("ps"); err != nil {
		status.State, status.Reason = Unknown, "ps is not available, so the process table cannot be asked"
		return status
	}
	start, exists := processStart(record.PID)
	switch {
	case !exists:
		status.State = Dead
		status.Reason = fmt.Sprintf("pid %d is gone and never released the run: it died; its last words are in %s", record.PID, status.Log)
	case start != record.ProcessStart:
		status.State = Dead
		status.Reason = fmt.Sprintf("pid %d now belongs to a different process (started %s, the run's started %s): the run died and its pid was reused", record.PID, start, record.ProcessStart)
	default:
		status.State = Alive
		status.Reason = fmt.Sprintf("pid %d has been running since %s", record.PID, record.StartedAt)
	}
	return status
}

// processStart asks the OS when pid started. Both BSD and procps ps print
// lstart; an absent pid makes ps exit non-zero with no output.
func processStart(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	start := strings.TrimSpace(string(out))
	if err != nil || start == "" {
		return "", false
	}
	return start, true
}

func readRecord(dir string) (Record, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, PIDName))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return Record{}, false, fmt.Errorf("decode %s: %w", PIDName, err)
	}
	return r, true, nil
}

// writeRecord replaces run.pid atomically, so a reader never sees half a file.
func writeRecord(dir string, r Record) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, PIDName+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, PIDName))
}
