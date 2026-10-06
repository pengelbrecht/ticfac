//go:build linux

package subprocess

import (
	"os"
	"strconv"
	"strings"
)

// THE ZOMBIE ANSWER, for a host whose PID 1 is no init.
//
// Inside the factory container PID 1 is the sandbox control server, and it
// never reaps: every process whose parent dies before collecting it — which
// is exactly what every group kill this package sends leaves behind, the tool
// groups included — reparents to PID 1 and stays a ZOMBIE forever. A zombie
// answers signal 0, so a liveness check built on kill(pid, 0) alone reads
// every corpse the container never reaps as a live one: tick dax's gate
// found all three of this package's process-group tests red in the container
// —
// TestInterruptingAHungToolKillsItsGroupAndSparesTheRunner ("the tool's own
// child outlived the interrupt"), TestAStoppedSupervisorInterruptsTheRunners-
// DetachedTools and TestACancelInterruptsTheRunnersDetachedTools ("survived
// the stop/cancel") — while the groups were demonstrably dead: the pids the
// tests read were State: Z, PPid: 1, PGid: their own tool group (tick 8ct is
// the backlog record of the finding). On a laptop
// PID 1 is launchd or systemd, which reaps in milliseconds, which is why the
// same tests pass there, and why this gap never closed from a Mac.
//
// Existence is therefore not life: a process is dead the moment the kernel
// has read its exit status, whether or not anyone ever collects it, and a
// group is gone when every member is. tick 58z found the same disease in
// internal/reconcile's gate kill and fixed it there; the /proc that says
// which is a Linux fact, so the answer lives here, and zombie_other.go
// carries the platforms that have nothing to read and no gap to close.
//
// Answering dead for a corpse runs no pid-reuse risk (tick rmc) — the one
// dead state that is SAFE to read as dead: a zombie's number is still taken
// by the corpse itself, so no stranger can be holding it yet.

// processZombie answers whether pid has DIED as far as /proc can tell: its
// exit status read by the kernel and collected by no one — a corpse awaiting
// a reap that may never come — or already reaped. It is what lets
// processAlive tell a dead process from a live one without trusting anyone to
// have reaped the dead one.
func processZombie(pid int) bool {
	return deadByProcState(procState(pid))
}

// groupZombie answers whether every member of the process group has died as
// far as /proc can tell: each one a state of Z — a corpse awaiting a reap that
// may never come — or X, or already reaped out of the table. A group whose
// members cannot be read at all is NOT dead: groupAlive errs towards alive,
// and so does this, because the group signal a caller goes on sending to a
// group it cannot see reaches at most its own corpses, whose numbers no
// reaper has handed to anybody else.
func groupZombie(pgid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			// Gone between the listing and the read: reaped, and not running.
			continue
		}
		state, pgrp, ok := parseProcStatState(string(raw))
		if !ok || pgrp != pgid {
			continue
		}
		if !deadByProcState(state) {
			// A member is running, or stopped, or waiting on IO: the group
			// still has a process, and a stop that quits here would leave it.
			return false
		}
	}
	return true
}

// procState is the state letter out of /proc/<pid>/stat, or "" when the pid is
// gone or unreadable. The comm field can contain spaces and parentheses, so
// the fields are parsed after the LAST ')' rather than counted from the front
// — the same counting ParseProcStat uses for the CPU columns.
func procState(pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	state, _, ok := parseProcStatState(string(raw))
	if !ok {
		return ""
	}
	return state
}

// deadByProcState reads a /proc state letter as life or death. A state that
// cannot be read is DEATH, not life: processAlive asks signal 0 first and
// /proc second, and a zombie reaped between the two leaves no /proc entry.
// Reading that gap as "not a zombie, so alive" is the failure tick 58z
// closed in internal/reconcile (PR #113's go race job, 2026-09-28, in 0.31s:
// its wait loop saw the killed child dead, and the very next check, a reap
// later, saw it "alive").
func deadByProcState(state string) bool {
	return state == "" || state == "Z" || state == "X"
}

// parseProcStatState reads the state letter and the process group out of one
// /proc/<pid>/stat line: field 3 (state) and field 5 (pgrp). The command in
// parentheses may itself contain spaces and parentheses, so the fields are
// counted from the LAST ')'.
func parseProcStatState(line string) (state string, pgrp int, ok bool) {
	open := strings.IndexByte(line, '(')
	closing := strings.LastIndexByte(line, ')')
	if open < 0 || closing < open {
		return "", 0, false
	}
	rest := strings.Fields(line[closing+1:])
	// rest[0] is field 3 (state), so field n is rest[n-3].
	if len(rest) < 3 {
		return "", 0, false
	}
	pgrp, err := strconv.Atoi(rest[2])
	if err != nil {
		return "", 0, false
	}
	return rest[0], pgrp, true
}
