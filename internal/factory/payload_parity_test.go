package factory

// Two parity checks that guarded the factory stayed behind in ticks when the
// factory moved here (tick 3r2 deleted internal/factory from ticks and trimmed
// the two internal/sandbox tests that had read the payload's TypeScript
// directly). This file brings both guards home (tick e08): the door/container
// auth-challenge check and the cold-start-budget check, asserting the same
// things they asserted in ticks.
//
// One difference from the originals, and it is locational only: in ticks the
// container was this repo's own cloud/sandbox and the door was a foreign
// checkout (cloud/factory/src, read across the boundary), so the tests went
// through internal/sandbox's payload locators. Here both halves ship from this
// repository — cloudflare is the embedded Worker bundle and cloud/sandbox
// the embedded image context (see bundle.go) — so each check reads both sides
// straight off the repository root, the way internal/gatewaytrace's Go/TS
// parity check already does.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// payloadPath resolves a path in the embedded payload tree from the
// repository root. The payload is committed here, so a missing file is a
// broken repository, never a reason to skip: a parity check that goes quiet
// when its fixture disappears is the failure CONTRACTS.md warns about.
func payloadPath(t *testing.T, parts ...string) string {
	t.Helper()
	all := append([]string{"..", ".."}, parts...)
	p := filepath.Join(all...)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("locating %s: %v", filepath.Join(parts...), err)
	}
	return p
}

// The two ends of this contract are written in different languages and neither
// imports the other: the door is TypeScript in cloudflare/src, the container
// is bash in cloud/sandbox. `.tick/learnings.md` already has the rule — a
// constant crossing that boundary needs a test that reads both sides — and
// this one had no test at all, which is how a door that parsed Basic without
// ever asking for it shipped.
//
// Ported unchanged from ticks' internal/sandbox/git_door_test.go (tick 3r2
// trimmed it there to the container's half alone; both halves live here now,
// so the full check runs again).
func TestTheDoorAndTheContainerAgreeAboutTheChallenge(t *testing.T) {
	door, err := os.ReadFile(payloadPath(t, "cloudflare", "src", "credentials.ts"))
	if err != nil {
		t.Fatalf("reading the door: %v", err)
	}
	for _, want := range []string{
		// The scheme must be Basic: it is the only one git's credential helper
		// can answer with a username and a password.
		`GIT_AUTH_CHALLENGE = 'Basic `,
		// And it must actually be attached to the 401, not merely declared.
		`"WWW-Authenticate": GIT_AUTH_CHALLENGE`,
	} {
		if !strings.Contains(string(door), want) {
			t.Errorf("cloudflare/src/credentials.ts no longer contains %q — a 401 without a Basic challenge is a clone a read-only run cannot make", want)
		}
	}

	common, err := os.ReadFile(payloadPath(t, "cloud", "sandbox", "common.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(common), "www-authenticate") {
		t.Error("cloud/sandbox/common.sh no longer looks for the challenge, so a door that stops sending one would again be reported as `Authentication failed`")
	}
}

// TestWorkerProbeBudgetCoversTheMeasuredColdStart pins the dispatch layer's
// probe budget against the committed benchmark.
//
// Tick 7go: the budget was 30s while a cold container measurably takes 93.24s,
// so the first real per-tick wave wrote off three healthy containers 21 seconds
// after dispatching them. The suite could not see it — vitest probes
// FakeSandboxes, which answers instantly — so the guard has to live here, where
// the measured artifact and the shipped constant can be compared to each other.
//
// Ported from ticks' internal/sandbox/benchmark_test.go. The raw dated
// measurement moved with the guard (benchmarks/sandbox-start/ — the container
// it measured now ships from this repository); the re-runnable harness
// (scripts/bench_sandbox_start.py) and the prose summary
// (docs/sandbox-start-benchmark.md) remain in ticks, where the measurement
// tooling lives. What the guard asserts is unchanged.
func TestWorkerProbeBudgetCoversTheMeasuredColdStart(t *testing.T) {
	raw, err := os.ReadFile(payloadPath(t, "benchmarks", "sandbox-start", "2026-08-21-docker-amd64.json"))
	if err != nil {
		t.Fatalf("reading the committed sandbox-start benchmark: %v", err)
	}
	var artifact struct {
		Modes struct {
			Cold struct {
				MedianTotalS float64 `json:"median_total_s"`
			} `json:"cold"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatalf("parsing the benchmark artifact: %v", err)
	}
	coldMS := artifact.Modes.Cold.MedianTotalS * 1000
	if coldMS <= 0 {
		t.Fatalf("the artifact records no cold-start median; got %v", coldMS)
	}

	src, err := os.ReadFile(payloadPath(t, "cloudflare", "src", "worker-dispatch.ts"))
	if err != nil {
		t.Fatalf("reading worker-dispatch.ts: %v", err)
	}
	declared := func(name string) float64 {
		re := regexp.MustCompile(`(?m)^export const ` + name + ` = ([0-9_.]+);`)
		m := re.FindSubmatch(src)
		if m == nil {
			t.Fatalf("worker-dispatch.ts no longer declares %s — if it was renamed, this guard has to follow it", name)
		}
		v, err := strconv.ParseFloat(strings.ReplaceAll(string(m[1]), "_", ""), 64)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		return v
	}

	benchmark := declared("COLD_START_BENCHMARK_MS")
	fanout := declared("FANOUT_DEGRADATION_FACTOR")

	// The constant must be what was actually measured, not a number that drifted.
	if diff := benchmark - coldMS; diff > 1000 || diff < -1000 {
		t.Errorf(
			"COLD_START_BENCHMARK_MS is %.0f but the committed artifact measured %.0f — "+
				"re-run scripts/bench_sandbox_start.py and update both together",
			benchmark, coldMS,
		)
	}

	// And the budget derived from it must still clear a cold start at the widest
	// fan-out. This is the assertion tick 7go exists for.
	budget := benchmark * fanout * 1.2
	if budget <= coldMS {
		t.Errorf(
			"the probe budget (%.0fms) does not clear the measured cold start (%.0fms): "+
				"every cold container in a wave would be declared never-dispatched",
			budget, coldMS,
		)
	}
	if budget < coldMS*fanout {
		t.Errorf(
			"the probe budget (%.0fms) does not clear a cold start degraded for fan-out "+
				"(%.0fms x %.2f): the last container of a wide wave would be written off",
			budget, coldMS, fanout,
		)
	}
}
