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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

const (
	PIDName = "run.pid"
	LogName = "run.log"

	// SchemaVersion 2 records the process start time in a pinned TZ/LC_ALL
	// environment; 1 recorded whatever the writer's environment rendered.
	SchemaVersion = 2
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
		if matched, _ := startMatches(existing); matched && existing.PID != os.Getpid() {
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

	// Attempts are the run's in-flight attempts with their own gaps (tick
	// 7zs): how long since each one's branch last moved and its worktree
	// last changed — the honest measurement of "is it getting anywhere",
	// which liveness never answered. An attempt is in flight here when its
	// worktree still stands in the repo the run works in: the run's own
	// teardown is what removes one, so a worktree that stands is an attempt
	// nobody has collected, whether the run is driving it or died beside
	// it. Null when the census could not be read; empty when no attempt
	// stands — different claims, kept apart. Neither gap is a verdict and
	// neither changes any: a worker thinking hard legitimately commits
	// nothing for a while, and the exit code stays liveness's answer alone.
	Attempts []runprogress.Attempt `json:"attempts"`

	// WallClocks are the run's own typed statements that an IN-FLIGHT
	// attempt's wall clock has fired (tick q1e) — one entry per standing
	// attempt whose bound the run said passed, joined to Attempts by the
	// tick and attempt identity on both. The fact is the feed's
	// `wall_clock_fired` line and only its typed half: the stage and the
	// identity it carries. The line's detail rides along VERBATIM for the
	// person reading the text surface — the executor's last word, which the
	// run already put on the line — but no part of it is ever matched: a
	// line whose prose merely NAMES the wall clock (the stall warning's
	// "the wall clock of 3600s is still the bound") reports nothing, which
	// is exactly the acceptance's "neither is derived by matching observation
	// prose". The run's own half of that derives the line from the durable
	// marker's issue time plus WallSeconds, never from an observation; this
	// half derives its report from the typed line those facts produced.
	// Null when the census that would say which attempts are in flight
	// cannot be read; empty when it read and no standing attempt's bound
	// has fired — different claims, kept apart, the same as Attempts.
	WallClocks []WallClock `json:"wall_clocks"`
}

// WallClock is one in-flight attempt's wall clock firing, as the run said it.
type WallClock struct {
	TickID  string `json:"tick_id"`
	Attempt int    `json:"attempt"`
	// FiredAt is when the run said the bound fired; FiredAgo is the same
	// fact measured against the `now` the probe was stamped with, so a
	// reader never does clock arithmetic. FiredAt stays null rather than
	// guessed when the line's own stamp cannot be read.
	FiredAt  *time.Time            `json:"fired_at,omitempty"`
	FiredAgo *runprogress.Duration `json:"fired_ago,omitempty"`
	// Detail is the run's own firing line, verbatim: what the executor was
	// last seen doing belongs to the watcher deciding what to do about an
	// attempt that is past its bound. It is carried, never matched — prose
	// is the person's half, not the machine's.
	Detail string `json:"detail,omitempty"`
}

// attemptKey is a feed line's attempt identity: the tick and the 1-based
// attempt number the line carries. A firing is a fact about ONE attempt, so
// a line for tick b1 or for attempt 2 is not a fact about attempt 1 of a1.
type attemptKey struct {
	tick    string
	attempt int
}

// wallFiredAt answers the moment the run said each attempt's wall clock
// fired, from the feed's typed lines: the latest `wall_clock_fired` line
// per (tick, attempt) — the reconciler announces once per incarnation and a
// resumed run re-announces, so the last line is the current fact. Nothing
// else on a line is examined: the stage and the identity are the machine's
// half, and the detail never enters the decision.
func wallFiredAt(events []runfeed.Event) map[attemptKey]runfeed.Event {
	fired := map[attemptKey]runfeed.Event{}
	for _, event := range events {
		if event.Stage != reconcile.StageWallClock || event.TickID == nil || event.Attempt == nil {
			continue
		}
		fired[attemptKey{tick: *event.TickID, attempt: *event.Attempt}] = event
	}
	return fired
}

// wallClocksOf joins the standing attempts to the run's firing lines: one
// WallClock per IN-FLIGHT attempt the run said the bound passed, sorted by
// tick and attempt so a watcher's two reads differ only in the numbers. A
// firing for an attempt that no longer stands is not a fact about an
// in-flight attempt and stays on the feed, where it was written; a line
// whose own stamp cannot be read carries no FiredAt rather than a guess —
// the same honesty the gaps keep.
func wallClocksOf(standing []runprogress.Attempt, fired map[attemptKey]runfeed.Event, now time.Time) []WallClock {
	out := []WallClock{}
	for _, a := range standing {
		line, ok := fired[attemptKey{tick: a.TickID, attempt: a.Attempt}]
		if !ok {
			continue
		}
		w := WallClock{TickID: a.TickID, Attempt: a.Attempt, Detail: line.Detail}
		if at, err := time.Parse(time.RFC3339, line.At); err == nil {
			firedAt := at
			ago := runprogress.Duration(now.Sub(at))
			w.FiredAt, w.FiredAgo = &firedAt, &ago
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TickID != out[j].TickID {
			return out[i].TickID < out[j].TickID
		}
		return out[i].Attempt < out[j].Attempt
	})
	return out
}

// Probe answers whether the run is alive, from run.pid and the OS, how long
// ago it last said anything, from the feed, and — for every attempt whose
// worktree still stands — how long since it last produced anything durable
// (tick 7zs) and whether the run has said its wall clock fired (tick q1e):
// the gaps are a reason to look and never a verdict, and the firing is a
// fact the run already wrote, reported where a watcher reads.
func Probe(repo, runID string, now time.Time) Status {
	dir := Dir(repo, runID)
	status := Status{RunID: runID, Log: filepath.Join(dir, LogName)}

	// The feed is read before the census so the wall-clock facts the lines
	// carry can ride the attempts the census finds — one read of the file,
	// one pass over the lines, and the same `now` stamps every gap the
	// status reports.
	fired := map[attemptKey]runfeed.Event{}
	if events, err := runfeed.Read(runfeed.Path(repo, runID)); err == nil && len(events) > 0 {
		last := events[len(events)-1]
		status.LastEvent = &last
		if at, err := time.Parse(time.RFC3339, last.At); err == nil {
			status.EventAge = now.Sub(at).Round(time.Second).String()
		}
		fired = wallFiredAt(events)
	}

	// The attempts' gaps are a measurement, never a gate: a census that
	// cannot be read leaves the field null and the liveness answer alone,
	// because a watcher's question deserves "not measured", not a guess
	// and not a silence that could be an empty run (tick 7zs).
	if standing, err := runprogress.Standing(repo, runID, now); err == nil {
		status.Attempts = standing
		status.WallClocks = wallClocksOf(standing, fired, now)
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
	matched, start := startMatches(record)
	exists := start != ""
	switch {
	case !exists:
		status.State = Dead
		status.Reason = fmt.Sprintf("pid %d is gone and never released the run: it died; its last words are in %s", record.PID, status.Log)
	case !matched:
		status.State = Dead
		status.Reason = fmt.Sprintf("pid %d now belongs to a different process (started %s, the run's started %s): the run died and its pid was reused", record.PID, start, record.ProcessStart)
	default:
		status.State = Alive
		status.Reason = fmt.Sprintf("pid %d has been running since %s", record.PID, record.StartedAt)
	}
	return status
}

// processStart asks the OS when pid started, in a form that does not change
// with the environment.
//
// `ps -o lstart=` renders a human date, so the SAME process prints
// "Tue Sep 16 14:19:19 2026" locally and "Tue Sep 16 12:19:19 2026" under
// TZ=UTC, and a different month name under another locale. Comparing that text
// across environments — a run started from a shell, probed from cron, launchd
// or a container — makes a live run read dead, and a dead-looking run is one a
// second run-epic will happily claim. Found by the Phase 3 review, reproduced.
//
// So both the record and the comparison pin TZ and LC_ALL. The value stays
// opaque text compared verbatim: parsing it is what the locale would break.
func processStart(pid int) (string, bool) {
	return processStartWith(pid, true)
}

// processStartWith reads the start time, optionally in the legacy (unpinned)
// environment, so a pidfile written before SchemaVersion 2 can still be
// compared against the environment it was written in.
func processStartWith(pid int, pinned bool) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	if pinned {
		cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	}
	out, err := cmd.Output()
	start := strings.TrimSpace(string(out))
	if err != nil || start == "" {
		return "", false
	}
	return start, true
}

// startMatches compares a recorded start time with the OS's answer now.
//
// A record written before SchemaVersion 2 holds an unpinned string, so it is
// compared unpinned — weaker, and honest about which guarantee that record can
// support. Anything at 2 or above is compared in the pinned environment.
func startMatches(record Record) (bool, string) {
	pinned, ok := processStartWith(record.PID, true)
	if !ok {
		return false, ""
	}
	if pinned == record.ProcessStart {
		return true, pinned
	}
	if record.SchemaVersion < 2 {
		if legacy, ok := processStartWith(record.PID, false); ok && legacy == record.ProcessStart {
			return true, legacy
		}
	}
	return false, pinned
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
