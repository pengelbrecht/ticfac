package subprocess

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Liveness by LOCK, not by pid (tick rmc).
//
// This executor used to decide whether an attempt was alive by asking
// kill(pid, 0) about the pids it saved at start. A pid is a number, and the
// kernel hands numbers out again: on a busy macOS host pids were measured
// advancing ~700 a second (47302 -> 55155 -> 62017 at 10s intervals), and
// macOS wraps them at 99999, so the whole space was reused every couple of
// minutes. A dead attempt's pid was, soon enough, somebody else's — and the
// dead attempt read as RUNNING. Settle refuses to release a running attempt,
// so a run stayed stuck on one that no longer existed: that is what failed
// TestALostAttemptIsReleasedByAPersonAndTheNextRunDispatchesANewOne twice in
// one day, finding a1 lost and then, moments later, "running — the executor
// can still address it". Worse, cancel and the test harnesses SIGNALLED those
// saved pids, which on the same host means killing the process group of
// whoever inherited the number.
//
// So liveness is now something the kernel ties to a process's lifetime: an
// flock. Every process this attempt starts holds an exclusive lock on a file
// of its OWN under locks/ for as long as it lives, and the kernel drops that
// lock the instant the last descriptor referring to it is closed — which a
// process that has died cannot avoid, and a process that merely inherited the
// dead one's number cannot fake. "Is it alive?" is "is its lock held?", asked
// with a non-blocking attempt to take a SHARED lock on the same file.
//
// Why one file per process, exclusive for the holder and shared for the
// observer:
//
//   - Shared for the observer so that two observers never mistake each other
//     for a holder. Inspect is asked from more than one process at once (a
//     run and a person's `status`), and an observer that took the lock
//     exclusively would, for the microseconds it held it, make the other
//     observer's probe fail — which reads as "held", which reads as alive.
//   - One file per process, rather than one per attempt, so that a held lock
//     names ONE process and its pid is written inside it. That is what lets
//     cancel prove which process group it is about to signal instead of
//     trusting a pid file, and it is what keeps a guard-off redispatch — a
//     second supervisor over the same state directory, the never_redispatch_live
//     negative control — from contending with the first for a single lock.
//   - A holder can find an observer mid-probe when it takes its lock, so it
//     takes it BLOCKING: an observer lets go within microseconds, and the
//     holder is the only party that ever waits.
//
// The locks directory is also the line between the two eras. An attempt
// started before this change has no locks/ at all, and for THAT attempt, and
// only for it, liveness and cancel still fall back to saved pids (see alive and
// stopTree) — an old supervisor still running across an upgrade holds no lock,
// and "no lock, so nothing is alive" would release it while it spends. The
// moment an attempt has a locks directory, a pid is never again evidence of
// anything on its own.

// The two kinds of process an attempt starts, which is also the prefix of each
// one's lock file.
const (
	lockSupervisor = "supervisor"
	lockRunner     = "runner"
)

// newLock creates a lock file for one process this attempt is about to start,
// and holds it. The caller hands the open file to that process and closes its
// own copy: the lock belongs to the open file DESCRIPTION, so it survives the
// hand-over and outlives the caller.
func (s *store) newLock(kind string) (*os.File, error) {
	dir := s.path(dirLocks)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, kind+"-"+hex.EncodeToString(nonce)+".lock"),
		os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	if err := holdLock(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// stampLock records, inside the lock, the pid of the process that now holds
// it. The pid is only ever READ while the lock is held, and a held lock means
// that process — or, for a runner, a descendant still in its process group —
// is alive, so the number cannot have been handed to anybody else.
func stampLock(f *os.File, pid int) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(pid)+"\n"), 0)
	return err
}

// supervisorLock is the lock the supervisor holds for its whole life. Start
// takes it before the supervisor exists and hands it down as the supervisor's
// fd 3, so there is no instant between "started" and "holding" in which an
// observer would find the attempt unheld and call it lost. A supervisor that
// was not handed one — started by something other than Start — takes its own.
func (s *store) supervisorLock() (*os.File, error) {
	candidates, _ := filepath.Glob(filepath.Join(s.path(dirLocks), lockSupervisor+"-*.lock"))
	if f := inheritedLock(candidates); f != nil {
		return f, nil
	}
	f, err := s.newLock(lockSupervisor)
	if err != nil {
		return nil, err
	}
	if err := stampLock(f, os.Getpid()); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// byLock is whether this attempt's liveness is read from its locks. It is
// false only for an attempt started before liveness moved to locks — see the
// comment at the top of this file.
func (s *store) byLock() bool {
	info, err := os.Stat(s.path(dirLocks))
	return err == nil && info.IsDir()
}

// liveLock is one lock of this attempt that a live process holds.
type liveLock struct {
	path string
	kind string
}

// pid is the process the lock names. It waits a moment for a lock whose
// holder has just been started and not yet stamped, and answers 0 when there
// is still no pid to be had — which callers treat as nothing they may signal.
func (l liveLock) pid() int {
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, _ := os.ReadFile(l.path)
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
			return pid
		}
		if !time.Now().Before(deadline) || !l.held() {
			return 0
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// held asks the kernel again, for a caller waiting for this lock's holder to
// be gone. A lock that cannot be asked about is not held as far as a WAIT is
// concerned: nothing is signalled on the strength of it.
func (l liveLock) held() bool {
	held, err := lockPathHeld(l.path)
	return err == nil && held
}

// liveLocks is every lock of this attempt that a live process holds, runners
// first: a stop takes the runner before the supervisor, because a supervisor
// stopped first stops its runner itself and the order then is nobody's.
func (s *store) liveLocks() ([]liveLock, error) {
	dir := s.path(dirLocks)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var live []liveLock
	for _, entry := range entries {
		name := entry.Name()
		kind, _, ok := strings.Cut(name, "-")
		if !ok || !strings.HasSuffix(name, ".lock") {
			continue
		}
		path := filepath.Join(dir, name)
		held, err := lockPathHeld(path)
		if err != nil {
			return nil, fmt.Errorf("ask whether %s is held: %w", name, err)
		}
		if held {
			live = append(live, liveLock{path: path, kind: kind})
		}
	}
	sort.SliceStable(live, func(i, j int) bool {
		return live[i].kind == lockRunner && live[j].kind != lockRunner
	})
	return live, nil
}

// lockPathHeld opens a lock read-only and asks whether anybody holds it. A
// lock file that is gone is held by nobody: they are never removed while an
// attempt's state directory exists.
func lockPathHeld(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	return lockHeld(f)
}

// stopProven stops every process this attempt's locks prove is alive, and
// says whether there was one. The pid it signals is the one written inside a
// lock that is held at the moment it is read, which is what makes it this
// attempt's process rather than whoever holds that number now.
//
// A runner's lock is inherited by what the runner starts, so it can be held by
// a descendant after the runner itself is gone. That descendant is still in the
// runner's process group unless it deliberately left it, and a process group
// id is not handed out again while the group has a member — so the group
// signal still reaches this attempt's processes. The one case it cannot cover
// is a descendant that left the group (a daemon that called setsid) outliving
// the runner: the runner's number is then free, and the signal is not sent
// for it by anything that can prove whose it is.
func stopProven(st *store, stop func(pgid int, alive func() bool)) (bool, error) {
	live, err := st.liveLocks()
	if err != nil {
		return false, err
	}
	stopped := false
	for _, lock := range live {
		pid := lock.pid()
		if pid <= 0 {
			continue
		}
		stop(pid, lock.held)
		stopped = true
	}
	return stopped, nil
}

// ------------------------------------------------------------ evacuation ---
//
// Two reads a SIGTERM's final flush (ticfac tick ppt) needs from an attempt's
// state directory, exported because the names they read are this package's
// contract with itself while the flush itself lives in the reconciler. The
// reconciler already walks for attempt.json itself (findAttemptState,
// attemptFacts); what it could not spell was runner.exit — the settle marker
// — and the fields of the record that say where an attempt's work IS.

// AttemptSettled reports whether the attempt in stateDir has settled: the
// supervisor recorded its runner's exit. A settled attempt's durability the
// run has already seen to — its final push happens before settlement is
// recorded — so an evacuation reads this to spend its bounded budget only on
// attempts whose work exists nowhere but this container's disk.
func AttemptSettled(stateDir string) bool {
	return newStore(stateDir).settled()
}

// AttemptWork is where one attempt's work lives, as the attempt record
// states it: the worktree whose loss an evacuation exists to make survivable,
// the branch that work is committed on, and the remote it is pushed to.
type AttemptWork struct {
	TickID   string `json:"tick_id"`
	Attempt  int    `json:"attempt"`
	JobID    string `json:"job_id"`
	Branch   string `json:"branch"`
	Worktree string `json:"worktree"`
	Remote   string `json:"remote"`
}

// ReadAttemptWork reads those fields out of one attempt's record. It is the
// minimal decode, not readAttempt: the whole record is closed and carries a
// JobSpec the flush has no use for, and a strict read would refuse a record
// from an older attempt for reasons the flush does not care about.
func ReadAttemptWork(stateDir string) (AttemptWork, error) {
	var work AttemptWork
	raw, err := os.ReadFile(filepath.Join(stateDir, fileAttempt))
	if err != nil {
		return work, err
	}
	if err := json.Unmarshal(raw, &work); err != nil {
		return work, fmt.Errorf("the attempt record at %s: %w", stateDir, err)
	}
	if work.Worktree == "" || work.Branch == "" {
		return work, fmt.Errorf("the attempt record at %s names no worktree or no branch", stateDir)
	}
	return work, nil
}

// KillLiveProcesses SIGKILLs the process group of every process this attempt's
// own locks prove is still alive, and waits up to grace for them to be gone.
// It reports whether anything is still alive when it returns.
//
// It never signals on a saved pid: an attempt whose state has no locks — one
// started before liveness moved to locks — is left alone, because nothing about
// it can prove that a pid it recorded is still its own. It exists for a caller
// that must stop whatever an attempt left behind without a cancel's ceremony:
// a test fixture's teardown, which runs on hosts where every pid it ever saw
// may by then be someone else's (tick rmc).
func KillLiveProcesses(stateDir string, grace time.Duration) (bool, error) {
	st := newStore(stateDir)
	if _, err := stopProven(st, func(pgid int, _ func() bool) { _ = signalGroup(pgid, sigKill()) }); err != nil {
		return true, err
	}
	deadline := time.Now().Add(grace)
	for {
		live, err := st.liveLocks()
		if err != nil {
			return true, err
		}
		if len(live) == 0 {
			return false, nil
		}
		if !time.Now().Before(deadline) {
			return true, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
}
