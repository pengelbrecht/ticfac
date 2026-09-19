package gitbin

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// gitBinary is the git this process runs, resolved once.
//
// On macOS `/usr/bin/git` is not git. It is a stub that asks xcrun where the
// active developer directory is and then execs the real binary inside it, and
// that lookup costs more than git's own startup: measured on this project's
// host, 8.9ms through /usr/bin/git against 2.9ms through the binary it ends up
// running. Three times the cost, paid on every invocation.
//
// That matters here because this store talks to git by spawning it. A single
// reconcile end-to-end test spawns over a thousand git processes, so the stub
// alone accounted for roughly a third of that test's wall clock — and it is
// paid by real runs too, not only by tests.
//
// Resolving past the stub is not a change of behaviour: it is the SAME binary
// the stub would have chosen, asked for once instead of once per process. If
// anything about that resolution fails, the answer is plain "git" and the old
// path, because a store that cannot find git at all is a worse outcome than a
// slow one.
//
// TICFAC_GIT overrides everything, for a host that wants to say which git.
// Path is the git this process runs.
func Path() string {
	gitOnce.Do(func() {
		gitPath = resolve()
	})
	return gitPath
}

var (
	gitOnce sync.Once
	gitPath string
)

// NoAutoMaintenance is the configuration every git a run starts in the
// repository it is WRITING TO is run with: git's automatic maintenance off.
//
// A fetch, a merge and a commit each end by starting `git maintenance run
// --auto --detach`. On a git whose automatic maintenance defaults to the
// geometric strategy — CI's 2.55 does; the 2.50 this was first built on does
// not — that maintenance runs `git repack -d` as soon as objects/17 holds two
// loose objects — which a run's own records put there within seconds — and it runs
// DETACHED, in the background, while the command that started it returns.
// The repack's prune-packed then removes every object fan-out directory it
// empties. git writes a new loose object by creating objects/xx and then a
// temporary file inside it, and a repack that removes objects/xx between
// those two steps fails the write with:
//
//	error: unable to create temporary file: No such file or directory
//
// That is tick mel. CI (git 2.55) failed a tracker publish's write-tree with
// exactly that line. One run of the test it failed in was measured, on the
// same git, starting 125 background maintenances in the repository the run
// writes into: 115 from the run's own fetches, 4 from its merges, 6 from the
// fake agent's commits in its worktree — while the run went on writing
// trees, blobs and commits there. Whether a given maintenance repacks at all
// depends on object ids, and whether a repack lands inside a write's window
// is timing: one CI run in forty. A git whose automatic maintenance is still
// `gc --auto` does nothing until 6700 loose objects, which is why it passed
// on every machine but CI.
//
// So nothing a run starts runs maintenance in that repository. Deferring it
// costs nothing: the operator's next fetch or commit runs it, with no run in
// the middle of a write. gc.auto=0 says the same to a git older than
// `maintenance` (2.29), which ran `gc --auto` directly.
//
// What this does NOT stop is maintenance somebody else starts in the same
// repository — an operator's own commit mid-run, a scheduled `git
// maintenance start`. Those race a write the same way; this removes the
// triggers the run itself was pulling, not every one there is.
var NoAutoMaintenance = configArgs(noAutoMaintenance)

var noAutoMaintenance = [][2]string{{"maintenance.auto", "false"}, {"gc.auto", "0"}}

func configArgs(pairs [][2]string) []string {
	var args []string
	for _, pair := range pairs {
		args = append(args, "-c", pair[0]+"="+pair[1])
	}
	return args
}

// WithNoAutoMaintenance is NoAutoMaintenance for a process whose command line
// this package does not write — an agent, whose `git commit` in its worktree
// starts maintenance of the same object store the run writes into. It is
// said through GIT_CONFIG_COUNT, which git reads above every config file and
// which every git the process starts inherits.
//
// Entries are APPENDED after whatever GIT_CONFIG_COUNT the environment
// already carries — the read-only grade's pins, or an operator's own — so
// nothing already pinned is renumbered or lost. An environment whose count
// git itself would refuse is returned as it is: masking it would turn a
// configuration error git reports into one nobody sees.
func WithNoAutoMaintenance(env []string) []string {
	count := 0
	for _, entry := range env {
		// The LAST value of a duplicated variable is the one a process
		// started with this environment sees (os/exec), so it is the one
		// the new entries are numbered after.
		if value, ok := strings.CutPrefix(entry, "GIT_CONFIG_COUNT="); ok {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return env
			}
			count = n
		}
	}
	out := append([]string{}, env...)
	for _, pair := range noAutoMaintenance {
		out = append(out,
			"GIT_CONFIG_KEY_"+strconv.Itoa(count)+"="+pair[0],
			"GIT_CONFIG_VALUE_"+strconv.Itoa(count)+"="+pair[1])
		count++
	}
	return append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
}

func resolve() string {
	if named := strings.TrimSpace(os.Getenv("TICFAC_GIT")); named != "" {
		return named
	}
	found, err := exec.LookPath("git")
	if err != nil {
		return "git"
	}
	// Only macOS has the stub, and only at this path. Everywhere else
	// LookPath has already found the real thing.
	if runtime.GOOS != "darwin" || found != "/usr/bin/git" {
		return found
	}
	out, err := exec.Command("xcrun", "--find", "git").Output()
	if err != nil {
		return found
	}
	real := strings.TrimSpace(string(out))
	if real == "" || real == found {
		return found
	}
	if info, err := os.Stat(real); err != nil || info.IsDir() {
		return found
	}
	return real
}
