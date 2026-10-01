package cloudflaresandbox

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// The boot-marker sweep.
//
// A worker container that stops in its boot pushes its exit code and reason
// to `<worker branch>-boot-stopped` (image/worker.sh boot_stopped, #176):
// beside its worker branch, never on it. While the tick is open that marker
// is the reason a person or a resume reads; once the tick is closed nothing
// reads it, and every boot that stopped would otherwise leave one on origin
// for good. This executor is the one that knows the spelling, so it owns
// taking them down — through the run's own leftover sweep
// (internal/reconcile/sweep.go), which already runs where the run knows a
// tick needs nothing more: at the tick's close, and at run start, resume and
// end. A marker of a tick that is open or held is left where it is.
//
// The sweep needs no factory credential and no door: the markers are on the
// remote the orchestrator's checkout already pushes to, so the sweeper is
// built from the checkout, its remote, the epic and the run alone.

// LeftoverSweeper sweeps one epic's boot markers.
type LeftoverSweeper struct {
	repo, remote, epic, runID string
}

// NewLeftoverSweeper is the sweeper for one run of one epic, over the
// orchestrator's own checkout and the remote it pushes to.
func NewLeftoverSweeper(repo, remote, epic, runID string) *LeftoverSweeper {
	if remote == "" {
		remote = "origin"
	}
	return &LeftoverSweeper{repo: repo, remote: remote, epic: epic, runID: runID}
}

// SweepLeftovers deletes, on the remote, every boot marker of this epic whose
// tick the scope says is sweepable. It never returns an error: a failure is a
// line in the report and the next sweep retries, because an error here would
// make the reconciler leave the git half of the whole sweep for later.
func (s *LeftoverSweeper) SweepLeftovers(_ context.Context, scope subprocess.SweepScope) (subprocess.SweepReport, error) {
	var report subprocess.SweepReport
	if s.repo == "" || s.epic == "" || scope.Sweepable == nil {
		return report, nil
	}
	prefix := "refs/heads/" + sandboximage.WorkerBranchPrefix + s.epic + "/"
	out, err := git(s.repo, "ls-remote", s.remote, prefix+"*")
	if err != nil {
		report.Failed = append(report.Failed, fmt.Sprintf(
			"the boot markers of epic %s on %s could not be listed (%s); the next sweep retries",
			s.epic, s.remote, firstLineOf(err.Error())))
		return report, nil
	}
	var doomed []string
	for _, line := range strings.Split(out, "\n") {
		_, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		branch := strings.TrimPrefix(ref, "refs/heads/")
		tick, ok := s.tickOfMarker(branch)
		if ok && scope.Sweepable(tick) {
			doomed = append(doomed, branch)
		}
	}
	if len(doomed) == 0 {
		return report, nil
	}
	sort.Strings(doomed)
	args := []string{"push", "--quiet", s.remote}
	for _, branch := range doomed {
		args = append(args, ":refs/heads/"+branch)
	}
	if _, err := git(s.repo, args...); err != nil {
		report.Failed = append(report.Failed, fmt.Sprintf(
			"the boot markers %s on %s could not be deleted (%s); the next sweep retries",
			strings.Join(doomed, ", "), s.remote, firstLineOf(err.Error())))
		return report, nil
	}
	for _, branch := range doomed {
		report.Removed = append(report.Removed, fmt.Sprintf(
			"deleted the boot marker %s on %s — its tick is closed", branch, s.remote))
	}
	return report, nil
}

// otherRunSuffix is the per-run fallback landing name's suffix of a run that
// is not this one (runLandingBranch): `-run_` and the run id's 32 hex digits.
var otherRunSuffix = regexp.MustCompile(`-run_[0-9a-f]{32}$`)

// tickOfMarker reads the tick a boot marker of this epic is for: the worker
// branch's last segment, less the marker suffix and, for the per-run fallback
// name, less the run id. ok is false for anything that is not a marker.
func (s *LeftoverSweeper) tickOfMarker(branch string) (string, bool) {
	rest, ok := strings.CutPrefix(branch, sandboximage.WorkerBranchPrefix+s.epic+"/")
	if !ok {
		return "", false
	}
	worker, ok := strings.CutSuffix(rest, sandboximage.WorkerBootStoppedBranch(""))
	if !ok {
		return "", false
	}
	slash := strings.LastIndex(worker, "/")
	if slash < 0 {
		return "", false
	}
	tick := worker[slash+1:]
	if s.runID != "" {
		tick = strings.TrimSuffix(tick, "-"+s.runID)
	}
	tick = otherRunSuffix.ReplaceAllString(tick, "")
	return tick, tick != ""
}

func firstLineOf(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}
