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
