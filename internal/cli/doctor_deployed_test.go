package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/factory"
)

// doctor's factory line names what the factory runs, from the factory's own
// answer, and says when ~/.ticfacrc remembers something else (CI deploys the
// factory now, so the local record goes stale). A factory that cannot say is
// reported, never a failure.
func TestDoctorDescribesWhatTheFactoryRuns(t *testing.T) {
	facts := &factory.DeployedFacts{
		Version:         "6aad18546d46",
		WorkerVersionID: "becc1446-5594-43fb-acfd-1d6c71008891",
		ImageDigest:     "sha256:2a90df5ba491dd6b329bde76ec153d3500741b468710ec5220845d2b75d17e72",
	}

	got := describeDeployed(facts, "dev", nil)
	for _, want := range []string{"runs 6aad18546d46", "worker becc1446-5594-43fb-acfd-1d6c71008891", "image sha256:2a90df5ba491", "records dev"} {
		if !strings.Contains(got, want) {
			t.Errorf("describeDeployed = %q, want it to contain %q", got, want)
		}
	}

	if got := describeDeployed(facts, "6aad18546d46", nil); strings.Contains(got, "records") {
		t.Errorf("describeDeployed = %q, want no disagreement when the local record matches", got)
	}

	if got := describeDeployed(nil, "", errors.New("boom")); !strings.Contains(got, "unknown: boom") {
		t.Errorf("describeDeployed = %q, want the read's failure named", got)
	}
}
