package cloudflaresandbox

import (
	"os"
	"strconv"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runsignal"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// WorkerSlots is how many worker containers a run driven from THIS
// orchestrator container may have live at once: the account's container
// ceiling (sandboximage.EnvFactoryMaxInstances, which the factory sets on the
// orchestrator's boot from its FACTORY_MAX_INSTANCES mirror, tick b6e) less
// the one the orchestrator itself occupies. The reconciler takes it through
// KnownExecutor.MaxLiveJobs and admits no worker past it, so a slot the run
// can count itself out of is never asked of the door (hn6's cloud run,
// 2026-09-30: orchestrator + two workers filled a ceiling of three and the
// third start waited for a container until its client timed out).
//
// Zero — no bound stated — when the variable is absent or unreadable, and the
// door's own no_capacity answer is what bounds the run. It is never less than
// one when a ceiling is stated: a run that could dispatch nothing would hold
// forever, and the door still refuses a start the account cannot host.
//
// A LOCAL orchestrator (`ticfac run --cloud-workers`, which sets the ceiling
// from the factory's answer and runsignal.EnvOrchestrator to "local")
// occupies no container, so the whole ceiling is its workers'.
func WorkerSlots() int {
	return workerSlots(os.Getenv(sandboximage.EnvFactoryMaxInstances), os.Getenv(runsignal.EnvOrchestrator))
}

// OrchestratorLocal is where a `ticfac run --cloud-workers` orchestrator runs.
const OrchestratorLocal = runsignal.OrchestratorLocal

func workerSlots(ceiling, orchestrator string) int {
	n, err := strconv.Atoi(strings.TrimSpace(ceiling))
	if err != nil || n <= 0 {
		return 0
	}
	if strings.TrimSpace(orchestrator) == OrchestratorLocal {
		return n
	}
	if n -= 1; n < 1 {
		return 1
	}
	return n
}
