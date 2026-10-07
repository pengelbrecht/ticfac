package profile

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The subscription rung is declared in TWO languages and neither imports the
// other: this package's CloudRule.SubscriptionRungs is what a run's routing
// and the cloud executor's door check answer to (tick 6fv), and
// cloudflare/src/claude-sub.ts's SUBSCRIPTION_HARNESS/SUBSCRIPTION_ALIASES is
// what the factory's dispatch decisions answer to — the review boot's lease
// and the worker door's step-down both ask the TypeScript half. The Worker
// and the Go binary deploy together (one deploy-factory run builds both), but
// nothing else keeps the two lists equal, and a DRIFTED one is exactly the
// defect this tick's rule exists to prevent in the other direction: a rung
// the factory admits but no run can route, or one a run routes and the
// executor refuses. The Go parity tests over image/common.sh
// (internal/factory) are the pattern; this guard is the rung's instance of
// it.
func TestTheRuleAndTheFactoryAgreeAboutTheSubscriptionRung(t *testing.T) {
	src, err := os.ReadFile(repoPath(t, "cloudflare", "src", "claude-sub.ts"))
	if err != nil {
		t.Fatalf("reading the factory's claude-sub: %v", err)
	}
	harness := regexp.MustCompile(`SUBSCRIPTION_HARNESS = "([^"]+)"`).FindSubmatch(src)
	if harness == nil {
		t.Fatal("cloudflare/src/claude-sub.ts no longer states SUBSCRIPTION_HARNESS — " +
			"the factory's half of the rung declaration is gone, and this guard must move with it")
	}
	list := regexp.MustCompile(`SUBSCRIPTION_ALIASES = \[([^\]]*)\]`).FindSubmatch(src)
	if list == nil {
		t.Fatal("cloudflare/src/claude-sub.ts no longer states SUBSCRIPTION_ALIASES — " +
			"the factory's half of the rung declaration is gone, and this guard must move with it")
	}
	var aliases []string
	for _, field := range strings.Split(string(list[1]), ",") {
		quoted := regexp.MustCompile(`"([^"]+)"`).FindSubmatch([]byte(field))
		if quoted == nil {
			t.Fatalf("SUBSCRIPTION_ALIASES carries %q, which is not a quoted alias", field)
		}
		aliases = append(aliases, string(quoted[1]))
	}

	if len(aliases) == 0 || len(CloudRule.SubscriptionRungs) == 0 {
		t.Fatal("one side of the subscription rung declares nothing — the rung is a PAIR of halves, in both languages")
	}
	// The rung is the PAIR: one harness, its aliases, in both places. Today
	// the rule declares exactly one rung; a second one is a new declaration
	// in both halves, and this guard grows a table then — never a silent
	// disagreement.
	if len(CloudRule.SubscriptionRungs) != 1 {
		t.Fatalf("CloudRule declares %d subscription rungs; this guard pins the one-declaration shape — extend it when a second rung is added deliberately",
			len(CloudRule.SubscriptionRungs))
	}
	rung := CloudRule.SubscriptionRungs[0]
	if rung.Harness != string(harness[1]) {
		t.Errorf("the rule admits harness %q but the factory dispatches on %q — a rung the factory never selects, or a dispatch the rule refuses",
			rung.Harness, string(harness[1]))
	}
	if strings.Join(rung.Models, "\x00") != strings.Join(aliases, "\x00") {
		t.Errorf("the rule's aliases %q and the factory's %q disagree — one side admits a pairing the other refuses, which is the drift tick 6fv exists to make impossible",
			rung.Models, aliases)
	}
}

// The rung's step-down is declared in Go and performed by the factory, and
// neither imports the other: this package's [CloudRule].SubscriptionRungs
// names the Workers AI model a rung dispatch with no free subscription falls
// back to, and the factory's lease failure is what performs it
// (cloudflare/src/sandbox-executor.ts, claudeSubLeaseForBoot) — onto the
// deployment's standing pair, RUN_WORKER_HARNESS/RUN_WORKER_MODEL in
// wrangler.toml with worker-boot.ts's defaults beneath. The Go executor
// accepts a stepped-down answer as exactly the rung's declared fallback
// PAIR (internal/exec/cloudflaresandbox, tick y38), so a drift between the
// declared fallback and the performed step-down is a start that can never
// succeed: every rung dispatch with no subscription free is refused by its
// own executor. This guard holds the two together — the declared fallback
// pair and the factory's standing pair are one pair, the model compared
// namespace-normalised (the rule spells it in pi's namespace, the factory in
// omp's).
func TestTheRungsDeclaredFallbackAgreesWithTheFactorysStepDown(t *testing.T) {
	if len(CloudRule.SubscriptionRungs) != 1 {
		t.Fatalf("CloudRule declares %d subscription rungs; this guard pins the one-declaration shape — extend it when a second rung is added deliberately",
			len(CloudRule.SubscriptionRungs))
	}
	rung := CloudRule.SubscriptionRungs[0]

	boot, err := os.ReadFile(repoPath(t, "cloudflare", "src", "worker-boot.ts"))
	if err != nil {
		t.Fatalf("reading the factory's worker boot ladder: %v", err)
	}
	defaultHarness := regexp.MustCompile(`export const WORKER_DEFAULT_HARNESS = "([^"]+)"`).FindSubmatch(boot)
	defaultModel := regexp.MustCompile(`export const WORKER_DEFAULT_MODEL = "([^"]+)"`).FindSubmatch(boot)
	if defaultHarness == nil || defaultModel == nil {
		t.Fatal("cloudflare/src/worker-boot.ts no longer states WORKER_DEFAULT_HARNESS and WORKER_DEFAULT_MODEL — " +
			"the factory's floor under a deployment that pins no standing pair is gone, and this guard must move with it")
	}
	wrangler, err := os.ReadFile(repoPath(t, "cloudflare", "wrangler.toml"))
	if err != nil {
		t.Fatalf("reading the deployable factory config: %v", err)
	}
	pinnedHarness := regexp.MustCompile(`(?m)^RUN_WORKER_HARNESS = "([^"]+)"`).FindSubmatch(wrangler)
	pinnedModel := regexp.MustCompile(`(?m)^RUN_WORKER_MODEL = "([^"]+)"`).FindSubmatch(wrangler)
	if pinnedHarness == nil || pinnedModel == nil {
		t.Fatal("cloudflare/wrangler.toml no longer pins RUN_WORKER_HARNESS and RUN_WORKER_MODEL — " +
			"the standing pair the factory's step-down lands on is unstated, and this guard must move with it")
	}

	// The harness half: the step-down lands on the hosted kind — the name a
	// hosted attempt's handle carries whatever the dispatch resolved, and the
	// one the executor's fallback acceptance answers for. A standing harness
	// that is not the hosted kind steps a rung dispatch onto a pair its own
	// executor refuses.
	for _, side := range []struct{ name, harness string }{
		{"worker-boot.ts's WORKER_DEFAULT_HARNESS", string(defaultHarness[1])},
		{"wrangler.toml's RUN_WORKER_HARNESS", string(pinnedHarness[1])},
	} {
		if side.harness != HostedDurableHarness {
			t.Errorf("the factory's standing harness is %q (%s), not the hosted kind %q the rung's step-down acceptance names — "+
				"a stepped-down rung dispatch would be refused by its own executor", side.harness, side.name, HostedDurableHarness)
		}
	}
	// The model half: the declared fallback and the performed step-down are
	// one model, namespace-normalised — the rule's pi spelling and the
	// factory's omp spelling of it.
	for _, side := range []struct{ name, model string }{
		{"worker-boot.ts's WORKER_DEFAULT_MODEL", string(defaultModel[1])},
		{"wrangler.toml's RUN_WORKER_MODEL", string(pinnedModel[1])},
	} {
		if WorkersAIModelCore(side.model) != WorkersAIModelCore(rung.Fallback) {
			t.Errorf("the rung declares fallback %q but the factory's step-down model is %q (%s) — "+
				"a rung dispatch with no free subscription would boot on a model its executor refuses",
				rung.Fallback, side.model, side.name)
		}
	}
}

// repoPath locates a repository-relative path from this package's directory,
// so a guard reads the tree it pins rather than a copy.
func repoPath(t *testing.T, parts ...string) string {
	t.Helper()
	all := append([]string{"..", ".."}, parts...)
	p := filepath.Join(all...)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("locating %s: %v", filepath.Join(parts...), err)
	}
	return p
}
