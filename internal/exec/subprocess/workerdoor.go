package subprocess

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// A durable worker's door from OUTSIDE the run (tick y03): `ticfac watch
// <run> <tick>` and `ticfac steer` reach a live pi-durable worker through the
// same steer socket the supervisor's stuck watch uses (steer.go; the
// protocol is pinned in harness/src/local/steer-socket.ts). The run assigns
// each attempt its state directory as <state root>/<run>/<tick>/<attempt>
// (reconcile's ExecStateRoot), and this executor records the attempt under
// it; the door is read from that record, never recomputed from a hash, so a
// caller holding only the run, the tick and the state root finds the socket
// the supervisor itself would steer through.

// WorkerDoor is one durable attempt's live door, as its record states it.
type WorkerDoor struct {
	// StateDir is the attempt's state directory (the one attempt.json is in).
	StateDir string
	TickID   string
	Attempt  int
	JobID    string
	Runner   string
	// SteerSock is the socket a live runner process listens on; empty for
	// a runner with no conversation to watch or steer.
	SteerSock string
}

// Settled reports whether the attempt's runner has exited for the last
// time: nothing will listen on the door again.
func (d WorkerDoor) Settled() bool { return AttemptSettled(d.StateDir) }

// Listening reports whether a runner process is listening on the door now.
// The socket file outlives nothing: the harness removes it on exit and a
// relaunched process makes it again, so its presence is the cheap fact a
// watcher polls between one process and the next.
func (d WorkerDoor) Listening() bool {
	if d.SteerSock == "" {
		return false
	}
	info, err := os.Stat(d.SteerSock)
	return err == nil && info.Mode()&fs.ModeSocket != 0
}

// ErrNoDurableWorker is a door that exists but has no conversation behind
// it: the attempt runs a CLI runner (claude, codex), whose session lives
// inside a process nobody can talk to.
var ErrNoDurableWorker = errors.New("not a pi-durable worker")

// FindWorkerDoor finds one attempt's door under the run's state root:
// <root>/<run>/<tick>/<attempt>. Attempt 0 means the tick's newest attempt
// there.
func FindWorkerDoor(root, runID, tickID string, attempt int) (WorkerDoor, error) {
	tickDir := filepath.Join(root, runID, tickID)
	if attempt == 0 {
		newest, err := newestAttempt(tickDir)
		if err != nil {
			return WorkerDoor{}, err
		}
		attempt = newest
	}
	attemptDir := filepath.Join(tickDir, strconv.Itoa(attempt))
	stateDir, ok := findRecordDir(attemptDir)
	if !ok {
		return WorkerDoor{}, fmt.Errorf("no attempt record for tick %s attempt %d of run %s under %s", tickID, attempt, runID, attemptDir)
	}
	return ReadWorkerDoor(stateDir)
}

// ReadWorkerDoor reads the door out of one attempt's state directory.
func ReadWorkerDoor(stateDir string) (WorkerDoor, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, fileAttempt))
	if err != nil {
		return WorkerDoor{}, err
	}
	var record struct {
		JobID     string `json:"job_id"`
		Attempt   int    `json:"attempt"`
		TickID    string `json:"tick_id"`
		Runner    string `json:"runner"`
		SteerSock string `json:"steer_sock"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return WorkerDoor{}, fmt.Errorf("read the attempt record at %s: %w", stateDir, err)
	}
	door := WorkerDoor{
		StateDir: stateDir, TickID: record.TickID, Attempt: record.Attempt,
		JobID: record.JobID, Runner: record.Runner, SteerSock: record.SteerSock,
	}
	if door.SteerSock == "" {
		return door, fmt.Errorf("tick %s attempt %d runs the %q runner: %w — only a pi-durable worker has a live conversation to watch or steer",
			door.TickID, door.Attempt, record.Runner, ErrNoDurableWorker)
	}
	return door, nil
}

// newestAttempt is the highest-numbered attempt directory under tickDir.
func newestAttempt(tickDir string) (int, error) {
	entries, err := os.ReadDir(tickDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("no attempt of this tick was dispatched on this machine (nothing under %s)", tickDir)
		}
		return 0, err
	}
	var attempts []int
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() && n > 0 {
			attempts = append(attempts, n)
		}
	}
	if len(attempts) == 0 {
		return 0, fmt.Errorf("no attempt of this tick was dispatched on this machine (nothing under %s)", tickDir)
	}
	sort.Ints(attempts)
	return attempts[len(attempts)-1], nil
}

// findRecordDir walks for the attempt record, the way the reconciler's
// adopt and teardown locate an attempt's state (findAttemptState).
func findRecordDir(dir string) (string, bool) {
	found := ""
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() && entry.Name() == fileAttempt {
			found = filepath.Dir(path)
			return fs.SkipAll
		}
		return nil
	})
	return found, found != ""
}

// Steer submits one steer to a live durable runner and waits for its durable
// admission (steer.go's exchange, bounded by steerTimeout).
func Steer(sock, text, requestID string) error { return steerRunner(sock, text, requestID) }

// OpenWatch opens a watch on a live durable runner: the connection is
// answered with one frame per line (internal/workerview reads them) until
// the runner's process exits or the caller closes it.
func OpenWatch(sock string) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", sock, steerTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial the worker's socket at %s: %w", sock, err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(steerTimeout)); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := conn.Write([]byte(`{"type":"watch"}` + "\n")); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ask the worker for a watch: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})
	return conn, nil
}
