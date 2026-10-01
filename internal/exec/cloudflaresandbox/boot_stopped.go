package cloudflaresandbox

import (
	"strconv"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// bootStopped reads the marker a container that stopped in its boot pushed
// beside its worker branch (image/worker.sh boot_stopped,
// sandboximage.WorkerBootStoppedBranch): the exit code and the boot's own stop
// message. Both landing names are tried — the recorded branch and the per-run
// one beside it (runLandingBranch), the container's own fallback when origin
// already had the recorded name from another base. ok is false when there is
// no marker, or one this reader cannot parse: the collect then says what it
// always said.
func (e *Executor) bootStopped(record *attemptRecord) (code int, reason string, ok bool) {
	branches := []string{record.Branch}
	if fallback := runLandingBranch(record.Branch, record.RunID); fallback != "" {
		branches = append(branches, fallback)
	}
	for _, branch := range branches {
		marker := sandboximage.WorkerBootStoppedBranch(branch)
		head, err := e.branchHead(marker)
		if err != nil || head == "" {
			continue
		}
		body, found := showFile(e.opts.Repo, head, sandboximage.WorkerBootStoppedFile(record.TickID))
		if !found {
			continue
		}
		if code, reason, ok := parseBootStopped(body); ok {
			return code, reason, true
		}
	}
	return 0, "", false
}

// parseBootStopped reads the marker's `exit:` and `reason:` lines.
func parseBootStopped(body string) (int, string, bool) {
	code, reason := -1, ""
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, "exit: "); ok && code < 0 {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return 0, "", false
			}
			code = n
		}
		if v, ok := strings.CutPrefix(line, "reason: "); ok && reason == "" {
			reason = strings.TrimSpace(v)
		}
	}
	if code < 0 || reason == "" {
		return 0, "", false
	}
	return code, reason, true
}
