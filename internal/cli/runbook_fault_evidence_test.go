package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fault-evidence half of the post-merge cloud runbook (epic 43y, tick
// ewd): the runbook's "What 'observed' means for [A2]'s real-run half"
// section tells the operator to prove the two [A2] faults inside the real
// cloud run by reading EXACT lines out of the attempt's log — the resume
// line after a mid-tool host loss, the two container-loss restore lines —
// and to address the mid-turn destroy by an EXACT container name. Those
// strings live in the harness host's TypeScript
// (harness/src/host/worker-attempt.ts) and the door's executor
// (cloudflare/src/sandbox-executor.ts): a host that rewords one leaves the
// runbook's operator hunting a line that no longer prints, and a test that
// only read the doc could not tell which side drifted.
//
// So this guard pins each string against BOTH sides, the same cross-tree
// parity pattern as internal/factory's payload parity checks (a constant
// crossing a language boundary gets a test that reads both sides). A
// missing committed file is a broken repository, never a reason to skip.
func TestTheCloudRunbooksA2FaultEvidenceMatchesTheHarnessHost(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	read := func(parts ...string) string {
		t.Helper()
		rel := filepath.Join(parts...)
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		return string(b)
	}
	runbook := read("docs", "pi-durable-cloud-run-runbook.md")
	host := read("harness", "src", "host", "worker-attempt.ts")

	// Markdown wraps long citations across source lines; a log line does not
	// wrap in the attempt's log. Comparing against the whitespace-collapsed
	// text makes the pin about the line's text, not the doc's column width.
	oneline := func(s string) string {
		return strings.Join(strings.Fields(s), " ")
	}

	// Every log line the observation criteria name, and the prefix every
	// one of them carries in the attempt's log (the host's own `say`).
	for _, line := range []string{
		"ticfac-harness: ",
		"a new host life resumed the conversation from its storage (submission ",
		"the container was lost between rounds; workspace restored to ",
		"a tracked bash found a fresh container; the workspace was restored to ",
		"wip checkpoint ",
	} {
		if !strings.Contains(oneline(host), line) {
			t.Errorf("the harness host no longer prints %q: the runbook's observation "+
				"criteria name a line worker-attempt.ts does not emit", line)
		}
		if !strings.Contains(oneline(runbook), line) {
			t.Errorf("the runbook no longer names %q: the observation criteria for [A2]'s "+
				"real-run half cite log lines by their text, and this one is gone from "+
				"docs/pi-durable-cloud-run-runbook.md", line)
		}
	}

	// The container name the mid-turn destroy matches, pinned against the
	// executor that formats it: `<run>-<tick>-<attempt>[-<slot>]` in the
	// runbook is `${runID}-${tickID}-${attempt}[-${slot}]` in the code, and
	// the destroy is safe only when the name is matched exactly.
	executorSrc := read("cloudflare", "src", "sandbox-executor.ts")
	if !strings.Contains(executorSrc, "${runID}-${tickID}-${attempt}") {
		t.Errorf("the executor no longer formats the attempt container name as " +
			"`${runID}-${tickID}-${attempt}`: the runbook's destroy step matches that " +
			"exact shape and must be rewritten with it")
	}
	if !strings.Contains(runbook, "<run>-<tick>-<attempt>") {
		t.Errorf("the runbook no longer spells the attempt container name " +
			"`<run>-<tick>-<attempt>`: the mid-turn destroy matches it exactly, and " +
			"the runbook must name what the executor formats")
	}
}
