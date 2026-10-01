package cli

import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/cloudflaresandbox"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// A cloud run's leftover sweep reaches the boot markers its workers leave on
// origin (#176): a cloudflare-sandbox profile gets the executor's own
// sweeper, and building it needs no factory credential — a sweep at run
// start or end must not fail for want of TICKS_FACTORY_TOKEN.
//
// short: a pure factory call; no repository, no door.
func TestACloudProfileGetsTheBootMarkerSweeper(t *testing.T) {
	t.Setenv("TICKS_FACTORY_URL", "")
	t.Setenv("TICKS_FACTORY_TOKEN", "")
	sweeper, err := sweeperFactory("")(reconcile.Dispatch{
		RunID: "r1", EpicID: "xte", Repo: t.TempDir(), Remote: "origin",
		Profile: &profile.Profile{Executor: cloudflaresandbox.ExecutorName},
	})
	if err != nil {
		t.Fatalf("sweeperFactory: %v", err)
	}
	if _, ok := sweeper.(*cloudflaresandbox.LeftoverSweeper); !ok {
		t.Fatalf("a cloudflare-sandbox profile got sweeper %T, want the boot-marker sweeper", sweeper)
	}
}
