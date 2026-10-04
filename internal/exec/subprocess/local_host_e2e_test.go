package subprocess

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	shorttest "github.com/pengelbrecht/ticfac/internal/shorttest"
)

// THE TICK'S ACCEPTANCE, at the level only a Go test can prove (epic 43y
// step 7, tick hpk): a dispatched attempt's `pi` runner IS the Node
// pi-durable harness — the real supervisor launches the real entry from the
// repository's own harness package, on the real local SQLite storage, with
// the real tools in the attempt worktree, and the work lands on the attempt
// branch and the report inside it, the way a local epic run's worker does.
//
// The model alone is scripted (pi-ai's faux provider, through the executor's
// test seam): a test that needs a model credential cannot run on every
// host, and the harness's own node suite (harness/test/node/
// local-host.test.ts) drives the same entry against the same faux provider
// from the other side of the Go/harness boundary. The two overlap on
// purpose: neither can see the other's half of the argv, the config and the
// supervisor's ladders.

// TheDurableRunnerRunsAWholeWorkerOnTheHarness skips itself, naming what is
// missing, wherever this tree cannot run it: the same shape as the
// cloudflaresandbox end-to-end test's node guards.
func TestTheDurableRunnerRunsAWholeWorkerOnTheHarness(t *testing.T) {
	shorttest.EndToEnd(t)
	if testing.Short() {
		t.Skip("short mode: this one runs a real supervisor and a real Node harness")
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("the pi-durable harness runs on node: %v", err)
	}
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	harnessDir := filepath.Join(root, "harness")
	for _, marker := range []string{
		filepath.Join(harnessDir, "node_modules", "@earendil-works", "pi-durable"),
	} {
		if _, err := os.Stat(marker); err != nil {
			t.Skipf("the harness package's dependencies are not installed: %v — run pnpm install in harness/ to run this end-to-end test", err)
		}
	}
	_ = node

	// The socket the harness listens on has to live somewhere a Unix socket
	// can be bound; this suite's own TMPDIR is deeper than that bound.
	t.Setenv("TICFAC_HARNESS_DIR", harnessDir)
	t.Setenv("TICFAC_STEER_SOCK_DIR", shortSocketDir(t))

	const jobID = "run-hpk/tick-he2/attempt-1"
	const tick = "he2"

	// The scripted model: one tool round of work, the report the collector
	// reads, and a final answer. The report is the shape the real report
	// checker accepts — the harness runs the REAL one (the supervisor's own
	// binary) on every yield.
	reportRel := "runs/" + jobID + "/RESULT-" + tick + ".md"
	writeReport := "mkdir -p runs/run-hpk && printf '# " + tick + "\\n\\nThe local worker host ran end to end.\\n\\nSTATUS: DONE\\n' > " + reportRel
	transcript := filepath.Join(t.TempDir(), "transcript.json")
	script, err := json.Marshal([]map[string]any{
		{"toolCalls": []any{map[string]any{
			"name": "bash",
			"args": map[string]any{"command": "echo the harness ran > harness-ran.txt"},
		}}},
		{"toolCalls": []any{map[string]any{
			"name": "bash",
			"args": map[string]any{"command": writeReport},
		}}},
		{"text": "the work is done and reported"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, script, 0o644); err != nil {
		t.Fatal(err)
	}

	f := newFixture(t, fixtureOptions{
		runner:         "pi",
		noFakeRunner:   true,
		stuckAfter:     time.Minute,
		fauxTranscript: transcript,
		model:          "faux/faux-1",
	})
	handle := f.Start(f.spec(jobID, tick))
	f.waitSettled(handle)

	status := f.inspect(handle)
	if status.State != StateSucceeded {
		// The harness's own words are the runner log — its boot refusals, its
		// steering notes, the model's final answer — and they are what a
		// failure here has to be read from.
		local, logErr := handle.Local()
		if logErr == nil {
			if raw, err := os.ReadFile(filepath.Join(local.State, fileRunnerLog)); err == nil {
				t.Logf("the runner's log:\n%s", raw)
			}
		}
		t.Fatalf("state %s, want succeeded:\n%s",
			status.State, formatObservations(status.Observations))
	}
	collected := f.collect(handle)
	if collected.Verdict != VerdictReadyToMerge {
		t.Fatalf("verdict %s, want ready-to-merge:\n%s", collected.Verdict, collected.Message)
	}
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}

	// The worker's own work landed, through the harness's tools, in the
	// attempt worktree; the conversation's storage sits beside the record.
	if ran, err := os.ReadFile(filepath.Join(local.Worktree, "harness-ran.txt")); err != nil || strings.TrimSpace(string(ran)) != "the harness ran" {
		t.Errorf("the tool round's file = %q, %v", string(ran), err)
	}
	if _, err := os.Stat(filepath.Join(local.State, fileWorkerStorage)); err != nil {
		t.Errorf("the conversation's SQLite storage is missing: %v", err)
	}
	// The wip checkpoints really reached the attempt branch — the
	// carried-work mechanism, at tool-round granularity, on the origin the
	// run reads.
	log := runGit(f.t, f.Repo.Origin, "log", "--format=%s", "tick/"+tick)
	if got := strings.Split(log, "\n"); len(got) < 2 || !strings.Contains(log, "wip: tool round") {
		t.Errorf("the attempt branch carries no wip checkpoint:\n%s", log)
	}
}
