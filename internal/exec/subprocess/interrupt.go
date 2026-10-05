package subprocess

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// THE TOOL INTERRUPT (tick l6n): what makes the stuck steer reach a worker
// whose tool is hung.
//
// A steer is placed AFTER THE CURRENT TOOL ROUND (steer.go). A hung tool's
// round never ends, so a steer alone is a message nobody reads: the watch
// steered, waited StuckAfter, and stopped the attempt — where the CLI
// runner's interrupt-and-re-prompt had killed the tool and recovered the
// worker. So when the stuck nudge steers a runner that has a tool running,
// it also ends that tool, exactly the way pi-durable's own abort and timeout
// end one: SIGKILL to the tool's process group. pi-durable's NodeExecutionEnv
// spawns every shell `detached`, so each tool leads a group of its own under
// the runner; its bash call then returns, exit 137, the round ends, and the
// steer is the next thing the conversation reads.
//
// What is a tool is decided by GROUP, not by depth: a process under the
// runner that leads a group other than the runner's. Anything in the
// runner's own group — a launcher's real node, a helper the harness keeps —
// is the harness, never a tool, and is never signalled here. A runner whose
// tools are not group leaders gets no interrupt, and the ladder is what it
// was: steered, then stopped.
//
// Nothing is interrupted at a stuck look unless the look found the runner
// quiet on every signal for StuckAfter — CPU included — so a group alive
// then is a tool that has done nothing for the whole window: hung, by the
// watch's own definition.

// toolInterruptPersistence bounds how long an interrupt keeps re-sending
// SIGKILL to a tool group that is still there (killUntilGone says why one
// signal is not enough on macOS).
const toolInterruptPersistence = 2 * time.Second

// procTable is the process table the interrupt walks: SystemProcs, and a
// seam for a test.
var procTable ProcTable = SystemProcs

// hungToolGroups names the tool process groups under runnerPID: every
// descendant that leads a group other than the runner's own. It reads, and
// signals nothing.
func hungToolGroups(runnerPID int) ([]int, error) {
	procs, err := procTable()
	if err != nil {
		return nil, err
	}
	return toolGroupsIn(procs, runnerPID), nil
}

func toolGroupsIn(procs []Proc, runnerPID int) []int {
	runnerGroup, err := processGroupOf(runnerPID)
	if err != nil {
		return nil
	}
	var groups []int
	for _, pid := range descendantsOf(procs, runnerPID) {
		pg, err := processGroupOf(pid)
		if err != nil || pg != pid || pg == runnerGroup {
			continue
		}
		groups = append(groups, pg)
	}
	sort.Ints(groups)
	return groups
}

// interruptToolGroups kills the named tool groups that are STILL tool groups
// under runnerPID, and returns the ones it killed. The steer goes out
// between the look that named the groups and this call, and a group that
// ended on its own in that gap may be a number somebody else holds now; so
// the table is read again and only a group it still finds led under the
// runner is signalled.
func interruptToolGroups(runnerPID int, groups []int) []int {
	if len(groups) == 0 {
		return nil
	}
	procs, err := procTable()
	if err != nil {
		return nil
	}
	still := map[int]bool{}
	for _, pg := range toolGroupsIn(procs, runnerPID) {
		still[pg] = true
	}
	var killed []int
	for _, pg := range groups {
		if !still[pg] {
			continue
		}
		pg := pg
		killUntilGone(pg, func() bool { return groupAlive(pg) }, toolInterruptPersistence)
		killed = append(killed, pg)
	}
	return killed
}

// descendantsOf lists every process under root (root itself excluded),
// walked by pid from the one pid the caller owns — never by matching names.
func descendantsOf(procs []Proc, root int) []int {
	children := map[int][]int{}
	for _, p := range procs {
		if p.PID != p.PPID {
			children[p.PPID] = append(children[p.PPID], p.PID)
		}
	}
	var out []int
	seen := map[int]bool{root: true}
	var walk func(pid int)
	walk = func(pid int) {
		for _, c := range children[pid] {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
			walk(c)
		}
	}
	walk(root)
	return out
}

// joinInts is pids for a sentence: "4211, 4230".
func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}
