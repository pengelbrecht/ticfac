package factory

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Deploy 67c531eb (run 36841664063): wrangler gave up after its fixed 15
// minutes waiting for Cloudflare to prepare the durable_object application's
// image, while the preparation carried on server-side. A deploy whose only
// failure is that timeout runs `wrangler deploy` again.
func TestDeployRunsWranglerAgainWhenOnlyTheImagePreparationTimedOut(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_WRANGLER_PREPARATION_TIMEOUTS", "2")
	var out bytes.Buffer
	opts := h.rolloutOptions()
	opts.Out = &out
	result, err := Deploy(context.Background(), opts)
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, out.String())
	}
	if !result.RolloutConfirmed {
		t.Error("the retried deploy did not confirm its rollout")
	}
	if n := countLines(h.logLines(), "deploy"); n != 3 {
		t.Errorf("wrangler deploy ran %d times, want 3 (two timeouts, then success):\n%s", n, h.log())
	}
	if !strings.Contains(out.String(), "still preparing the container image (attempt 2 of 3)") {
		t.Errorf("the retry was not announced:\n%s", out.String())
	}
}

// The retries are bounded, and any other failure is not retried.
func TestDeployGivesUpOnTheImagePreparationAfterItsAttempts(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_WRANGLER_PREPARATION_TIMEOUTS", "9")
	opts := h.rolloutOptions()
	_, err := Deploy(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), imagePreparationTimeout) {
		t.Fatalf("Deploy error = %v, want the preparation timeout", err)
	}
	if n := countLines(h.logLines(), "deploy"); n != imagePreparationAttempts {
		t.Errorf("wrangler deploy ran %d times, want %d", n, imagePreparationAttempts)
	}
}
