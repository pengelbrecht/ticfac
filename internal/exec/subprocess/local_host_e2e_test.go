package subprocess

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	shorttest "github.com/pengelbrecht/ticfac/internal/shorttest"
	"github.com/pengelbrecht/ticfac/internal/workerview"
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
	// The wip checkpoints reached the attempt branch DURING the run (the
	// runner log says every push), and the finish phase retired them and
	// salvaged the uncommitted tree (tick nou): the branch the run reads
	// ends at the salvage commit the supervisor's final push landed — never
	// at a snapshot a fast-forward push would be refused over, and never
	// empty for a worker that settled without committing.
	rawLog, err := os.ReadFile(filepath.Join(local.State, fileRunnerLog))
	if err != nil {
		t.Errorf("the runner log could not be read: %v", err)
	} else if !strings.Contains(string(rawLog), "wip checkpoint pushed") {
		t.Errorf("no wip checkpoint reached the attempt branch during the run:\n%s", rawLog)
	}
	log := runGit(f.t, f.Repo.Origin, "log", "--format=%s", "tick/"+tick)
	if !strings.Contains(log, "work in progress salvaged") {
		t.Errorf("the attempt branch carries no salvage commit:\n%s", log)
	}
	if strings.Contains(log, "wip: tool round") {
		t.Errorf("the attempt branch still carries a wip snapshot the finish never retired:\n%s", log)
	}
	if got := runGit(f.t, f.Repo.Origin, "show", "tick/"+tick+":harness-ran.txt"); got != "the harness ran" {
		t.Errorf("the salvaged work is not on the origin the run reads: %q", got)
	}
}

// THE READ-ONLY HALF (tick x8e): a dispatched read-only attempt — the
// review grade — on the local pi runner runs the same harness with the
// workspace checkpoints OFF, because its grade pins every push to refusal
// (grade.go) and there is no ref to checkpoint to. Before the fix the
// config carried the remote unconditionally, so every tool round's wip
// push was refused, threw into onReport and logged a failed checkpoint, and
// the finish phase went on to salvage — `git add -A && git commit` — a tree
// onto an attempt whose grade granted no ref to advance: a dispatched review
// ran visibly broken at its first tool round.
//
// The same shape of guards as the write-grade test above: the model is
// scripted, everything else is real — the supervisor, the harness, the tools
// in the attempt worktree, and the REAL report checker on every yield.
func TestAReadOnlyLocalDurableRunHasNoWorkspaceCheckpoints(t *testing.T) {
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
	if _, err := os.Stat(filepath.Join(harnessDir, "node_modules", "@earendil-works", "pi-durable")); err != nil {
		t.Skipf("the harness package's dependencies are not installed: %v — run pnpm install in harness/ to run this end-to-end test", err)
	}
	_ = node

	// The socket the harness listens on has to live somewhere a Unix socket
	// can be bound; this suite's own TMPDIR is deeper than that bound.
	t.Setenv("TICFAC_HARNESS_DIR", harnessDir)
	t.Setenv("TICFAC_STEER_SOCK_DIR", shortSocketDir(t))

	const jobID = "run-x8e/tick-hro/attempt-1"
	const tick = "hro"

	// A dispatched REVIEW: the role whose grade is read-only. One tool round
	// of reading, the report with the review's own verdict, a final answer.
	reportRel := "runs/" + jobID + "/RESULT-" + tick + ".md"
	writeReport := "mkdir -p runs/run-x8e && printf '# " + tick + "\\n\\nThe review read the epic.\\n\\nREVIEW-VERDICT: READY\\n\\nSTATUS: DONE\\n' > " + reportRel
	transcript := filepath.Join(t.TempDir(), "transcript.json")
	script, err := json.Marshal([]map[string]any{
		{"toolCalls": []any{map[string]any{
			"name": "bash",
			"args": map[string]any{"command": "echo the read-only round ran > review-ran.txt"},
		}}},
		{"toolCalls": []any{map[string]any{
			"name": "bash",
			"args": map[string]any{"command": writeReport},
		}}},
		{"text": "the review is done and reported"},
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
	spec := f.spec(jobID, tick)
	// The review grade, exactly as the reconciler issues it
	// (internal/reconcile/dispatch.go sourceCredentialFor): read-only, no
	// write_ref_prefix — the contract refuses one, and the point is that
	// the issuer hands out no push credential at all.
	spec.Role = "review-epic"
	spec.OutputSchema = "ticfac.job-result.review-epic.v1"
	spec.Credentials.Source = SourceCredential{Grant: &SourceGrant{Issuer: "host", Grade: gradeReadOnly}}
	handle := f.Start(spec)
	f.waitSettled(handle)

	status := f.inspect(handle)
	if status.State != StateSucceeded {
		local, logErr := handle.Local()
		if logErr == nil {
			if raw, err := os.ReadFile(filepath.Join(local.State, fileRunnerLog)); err == nil {
				t.Logf("the runner's log:\n%s", raw)
			}
		}
		t.Fatalf("state %s, want succeeded:\n%s",
			status.State, formatObservations(status.Observations))
	}
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}

	// The config the harness reads carries NO remote: the checkpoints, the
	// restore and the finish phase all key on it (worker-host.ts), and a
	// read-only grade pins every push to refusal — there is nothing to
	// checkpoint to.
	rawConfig, err := os.ReadFile(filepath.Join(local.State, fileWorkerConfig))
	if err != nil {
		t.Fatal(err)
	}
	var config workerConfig
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		t.Fatal(err)
	}
	if config.Remote != "" {
		t.Errorf("the read-only attempt's worker.json carries remote %q: the harness installs the wip checkpoints on it", config.Remote)
	}

	// And the run shows it: no checkpoint was attempted (a failed one is a
	// push the pins refused — the broken behaviour this tick fixes — and a
	// pushed one would be a push the grade forbids), no salvage was committed
	// onto the attempt, and the attempt's own branch never reached the origin.
	rawLog, err := os.ReadFile(filepath.Join(local.State, fileRunnerLog))
	if err != nil {
		t.Fatal(err)
	}
	log := string(rawLog)
	for _, noise := range []string{"wip checkpoint", "salvaged", "harness report"} {
		if strings.Contains(log, noise) {
			t.Errorf("the read-only run's log mentions %q:\n%s", noise, log)
		}
	}
	if ran, err := os.ReadFile(filepath.Join(local.Worktree, "review-ran.txt")); err != nil || strings.TrimSpace(string(ran)) != "the read-only round ran" {
		t.Errorf("the tool round's file = %q, %v: a read-only run still reads and works", string(ran), err)
	}
	if refs := runGit(f.t, f.Repo.Origin, "for-each-ref", "--format=%(refname)", "refs/heads/tick/"+tick); refs != "" {
		t.Errorf("the read-only attempt advanced a ref on the origin:\n%s", refs)
	}

	// The review collects by its own rule: the deliverable is the answer, so
	// an empty branch is what a correct attempt looks like.
	collected := f.collect(handle)
	if collected.Verdict != VerdictReadyToMerge {
		t.Fatalf("verdict %s, want ready-to-merge:\n%s", collected.Verdict, collected.Message)
	}
	if collected.Result.Source.Commits != 0 {
		t.Errorf("the review collected %d commits; a read-only attempt pushed nothing", collected.Result.Source.Commits)
	}
}

// THE OPERATOR'S HALF (tick y03): a person watches a live durable worker
// and steers it, through the Go clients `ticfac watch <run> <tick>` and
// `ticfac steer` are made of, against the REAL harness the real supervisor
// launched. The watch folds the real commit stream (internal/workerview);
// the steer is admitted durably while a tool runs and comes back through
// the same watch as the conversation's input, after that tool's result —
// the round trip, observed from outside the process that owns the
// conversation.
func TestAnOperatorWatchesAndSteersALiveDurableWorker(t *testing.T) {
	shorttest.EndToEnd(t)
	if testing.Short() {
		t.Skip("short mode: this one runs a real supervisor and a real Node harness")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("the pi-durable harness runs on node: %v", err)
	}
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	harnessDir := filepath.Join(root, "harness")
	if _, err := os.Stat(filepath.Join(harnessDir, "node_modules", "@earendil-works", "pi-durable")); err != nil {
		t.Skipf("the harness package's dependencies are not installed: %v — run pnpm install in harness/ to run this end-to-end test", err)
	}
	t.Setenv("TICFAC_HARNESS_DIR", harnessDir)
	t.Setenv("TICFAC_STEER_SOCK_DIR", shortSocketDir(t))

	const jobID = "run-y03/tick-wy3/attempt-1"
	const tick = "wy3"
	reportRel := "runs/" + jobID + "/RESULT-" + tick + ".md"
	writeReport := "mkdir -p runs/run-y03 && printf '# " + tick + "\\n\\nWatched and steered.\\n\\nSTATUS: DONE\\n' > " + reportRel
	transcript := filepath.Join(t.TempDir(), "transcript.json")
	script, err := json.Marshal([]map[string]any{
		{"thinking": "A slow round first, so the operator can watch it.", "toolCalls": []any{map[string]any{
			"name": "bash",
			"args": map[string]any{"command": "echo watching; sleep 3; echo done > slow.txt"},
		}}},
		{"thinking": "The operator steered: report now.", "toolCalls": []any{map[string]any{
			"name": "bash",
			"args": map[string]any{"command": writeReport},
		}}},
		{"text": "watched, steered, reported"},
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
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}
	door, err := ReadWorkerDoor(local.State)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the worker's door to listen", 30*time.Second, door.Listening)

	conn, err := OpenWatch(door.SteerSock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var mu sync.Mutex
	model := workerview.New()
	read := make(chan error, 1)
	go func() {
		read <- workerview.ReadFrames(conn, func(fr workerview.Frame) error {
			mu.Lock()
			defer mu.Unlock()
			return model.Apply(fr, time.Now())
		})
	}()
	seen := func(check func(*workerview.Model) bool) func() bool {
		return func() bool {
			mu.Lock()
			defer mu.Unlock()
			return check(model)
		}
	}
	waitFor(t, "the slow bash to run, seen through the watch", 30*time.Second, seen(func(m *workerview.Model) bool {
		for _, run := range m.Running {
			if run.Name == "bash" && strings.Contains(run.Args, "sleep 3") {
				return true
			}
		}
		return false
	}))

	if err := Steer(door.SteerSock, "Report now, please.", "operator-1"); err != nil {
		t.Fatalf("the steer was not admitted: %v", err)
	}
	f.waitSettled(handle)
	select {
	case <-read:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch did not end when the worker's process exited")
	}

	mu.Lock()
	defer mu.Unlock()
	steerAt, resultAt := -1, -1
	for i, it := range model.Items {
		switch {
		case it.Kind == workerview.KindSteer && it.Text == "Report now, please.":
			steerAt = i
		case it.Kind == workerview.KindToolResult && strings.Contains(it.Text, "watching") && resultAt == -1:
			resultAt = i
		}
	}
	if steerAt == -1 || resultAt == -1 || steerAt < resultAt {
		var lines []string
		for _, it := range model.Items {
			lines = append(lines, workerview.PlainLine(it))
		}
		t.Fatalf("the steer did not come back after the slow tool's result (steer %d, result %d):\n%s",
			steerAt, resultAt, strings.Join(lines, "\n"))
	}
	if model.Heartbeat.Commits == 0 || model.Heartbeat.ModelCalls != 3 || model.Heartbeat.ToolCalls != 2 {
		t.Errorf("the heartbeat did not follow the commit sequence: %+v", model.Heartbeat)
	}
	if model.Ended == "" {
		t.Errorf("the watch's end was not said")
	}
	if status := f.inspect(handle); status.State != StateSucceeded {
		t.Errorf("state %s, want succeeded", status.State)
	}
}

// THE STUCK LADDER THROUGH A HUNG TOOL, on the real harness (tick l6n): the
// scripted worker's first tool hangs, the supervisor's stuck watch steers it
// and interrupts that tool — the round ends, exit 137, the way pi-durable's
// own abort ends one — and the conversation reads the steer and carries on
// to its report. Before the fix the steer was placed after a round that
// never ended, and the attempt was stopped as stuck one window later.
func TestAStuckDurableWorkerInAHungToolIsSteeredThroughIt(t *testing.T) {
	shorttest.EndToEnd(t)
	if testing.Short() {
		t.Skip("short mode: this one runs a real supervisor and a real Node harness")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("the pi-durable harness runs on node: %v", err)
	}
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	harnessDir := filepath.Join(root, "harness")
	if _, err := os.Stat(filepath.Join(harnessDir, "node_modules", "@earendil-works", "pi-durable")); err != nil {
		t.Skipf("the harness package's dependencies are not installed: %v — run pnpm install in harness/ to run this end-to-end test", err)
	}
	t.Setenv("TICFAC_HARNESS_DIR", harnessDir)
	t.Setenv("TICFAC_STEER_SOCK_DIR", shortSocketDir(t))

	const jobID = "run-l6n/tick-hu6/attempt-1"
	const tick = "hu6"
	reportRel := "runs/" + jobID + "/RESULT-" + tick + ".md"
	writeReport := "mkdir -p runs/run-l6n && printf '# " + tick + "\\n\\nSteered through a hung tool.\\n\\nSTATUS: DONE\\n' > " + reportRel
	transcript := filepath.Join(t.TempDir(), "transcript.json")
	script, err := json.Marshal([]map[string]any{
		{"thinking": "Run the command that will hang.", "toolCalls": []any{map[string]any{
			"name": "bash",
			"args": map[string]any{"command": "echo hanging; sleep 3600; echo never"},
		}}},
		{"thinking": "The supervisor interrupted the hung command and steered me: report.", "toolCalls": []any{map[string]any{
			"name": "bash",
			"args": map[string]any{"command": writeReport},
		}}},
		{"text": "steered through the hung tool, reported"},
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
		// The window has to outlast the harness's boot — the steer door
		// must be listening when the watch first knocks, or the ladder
		// takes its fallback — and that is well under a second here; the
		// hung tool then holds the worker quiet for the whole window.
		stuckAfter:     8 * time.Second,
		fauxTranscript: transcript,
		model:          "faux/faux-1",
	})
	handle := f.Start(f.spec(jobID, tick))
	f.waitSettled(handle)

	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}
	status := f.inspect(handle)
	rawLog, _ := os.ReadFile(filepath.Join(local.State, fileRunnerLog))
	if status.State != StateSucceeded {
		t.Fatalf("state %s, want succeeded — recovered through the hung tool, not stopped:\n%s\nthe runner's log:\n%s",
			status.State, formatObservations(status.Observations), rawLog)
	}
	var steered, restarts, stops int
	for _, o := range status.Observations {
		switch {
		case IsStuckNudge(o):
			steered++
			if !strings.Contains(o.Detail, "steered in its own conversation") || !strings.Contains(o.Detail, "hung tool") {
				t.Errorf("the stuck nudge does not say it steered and interrupted the hung tool: %s", o.Detail)
			}
		case IsStuckStop(o):
			stops++
		case o.Kind == ObsStarted && strings.Contains(o.Detail, "re-prompted as stuck"):
			restarts++
		}
	}
	if steered != 1 || restarts != 0 || stops != 0 {
		t.Fatalf("steers %d, interrupt-restarts %d, stuck stops %d; want 1, 0 and 0:\n%s\nthe runner's log:\n%s",
			steered, restarts, stops, formatObservations(status.Observations), rawLog)
	}
	if !strings.Contains(string(rawLog), "steer admitted (stuck-nudge-1)") {
		t.Errorf("the harness never admitted the stuck steer:\n%s", rawLog)
	}
	if _, err := os.Stat(filepath.Join(local.Worktree, reportRel)); err != nil {
		t.Errorf("the worker did not carry on to its report: %v", err)
	}
}
