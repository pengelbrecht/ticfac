package gitbin

import (
	"os"
	"os/exec"
	"runtime"
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
