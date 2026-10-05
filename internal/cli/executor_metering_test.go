package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// The gateway metering join the factory resolves for every LOCAL dispatch
// (tick dm2 for herdr, tick gzv for the subprocess executor): the run id
// the dispatch carries, the gateway ~/.ticfacrc names, and the
// documented-optional state that launches exactly as before.

// isolateHostCredentials points HOME at an empty directory and returns the
// ~/.ticfacrc path to write, so the host's own factory credentials never
// answer for the test.
func isolateHostCredentials(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".ticfacrc")
}

// short: a pure resolution over a temp HOME, no repository is built
func TestTheLocalMeteringJoinNeedsBothHalvesOfTheHostCredential(t *testing.T) {
	rc := isolateHostCredentials(t)
	d := reconcile.Dispatch{RunID: "epic-hn6"}

	// Nothing configured: nil, never an error — the dispatch runs as before.
	if m := localMetering(d); m != nil {
		t.Fatalf("a host with no ~/.ticfacrc got metering %+v, want none: telemetry is optional and the dispatch must not change", *m)
	}

	// The gateway alone is half-set: the token is what authenticates the
	// route, and a join whose requests the gateway would refuse is a broken
	// dispatch, not a metered one.
	if err := os.WriteFile(rc, []byte("factory_gateway_url=https://gateway.ai.cloudflare.com/v1/acct/gw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := localMetering(d); m != nil {
		t.Fatalf("a gateway with no token got metering %+v, want none: half-set is refused, not guessed around", *m)
	}

	// The token alone names a route nothing reads.
	if err := os.WriteFile(rc, []byte("factory_cloudflare_api_token=cft_test_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := localMetering(d); m != nil {
		t.Fatalf("a token with no gateway got metering %+v, want none: half-set is refused, not guessed around", *m)
	}

	// A gateway outside Cloudflare: the requests would route, but the logs
	// API this repository reads would never answer, and routing without the
	// read is spend without the metering.
	if err := os.WriteFile(rc, []byte("factory_gateway_url=https://proxy.example.com/acct/gw\n"+
		"factory_cloudflare_api_token=cft_test_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := localMetering(d); m != nil {
		t.Fatalf("a non-Cloudflare gateway got metering %+v, want none: no logs API, no join", *m)
	}

	// Both halves: the join, carrying the dispatch's own run id — the id the
	// status model's reader filters the gateway logs by.
	if err := os.WriteFile(rc, []byte("factory_gateway_url=https://gateway.ai.cloudflare.com/v1/acct/gw/\n"+
		"factory_cloudflare_api_token=cft_test_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := localMetering(d)
	if m == nil {
		t.Fatal("a fully configured host got no metering: the join is the tick's whole point")
	}
	if m.RunID != "epic-hn6" {
		t.Errorf("the join attributes to run id %q, want the dispatch's own", m.RunID)
	}
	// The resolved join must itself be a usable override: the executor
	// writes it into the attempt state dir.
	path, err := m.WriteExtension(t.TempDir())
	if err != nil {
		t.Fatalf("the resolved join does not write its override: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the override file the agent will load is missing: %v", err)
	}
	// And it keeps the model gate: a non-Workers-AI dispatch never loads it.
	var none *subprocess.GatewayMetering
	if none.Applies("opus") {
		t.Error("a nil join applies to a claude model: nothing is configured")
	}
}

// localMeteringDispatch is the dispatch a local profile routes: the
// subprocess executor's own name, the pi row of its runner table, and a
// Workers AI model the gateway serves.
func localMeteringDispatch(repo string) reconcile.Dispatch {
	return reconcile.Dispatch{
		RunID:   "epic-hn6",
		TickID:  "gzv",
		Attempt: 38,
		Repo:    repo,
		Remote:  "origin",
		Profile: &profile.Profile{
			Role: "implement-tick", Executor: subprocess.ExecutorName,
			Runner: "pi", Model: "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		},
	}
}

// oneCommitRepo makes the one-commit git repository the local executor's
// constructor demands of its Repo.
func oneCommitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustGit("init", "--quiet", "-b", "main")
	mustGit("config", "user.email", "cli@example.com")
	mustGit("config", "user.name", "ticfac test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit("add", "-A")
	mustGit("commit", "--quiet", "-m", "base")
	return dir
}

// The factory wires the SAME join into the local subprocess executor (tick
// gzv): a dispatch its profile routes to the local executor launches pi from
// the runner table too, and its Workers AI spend joins the gateway logs
// exactly as a herdr dispatch's does. Nil on a host that joins nothing, so
// the dispatch runs as it did before the join.
//
// short: one factory call per case over a temp HOME and a one-commit repo;
// nothing is started.
func TestTheFactoryHandsTheLocalSubprocessExecutorTheJoin(t *testing.T) {
	rc := isolateHostCredentials(t)
	d := localMeteringDispatch(oneCommitRepo(t))

	// A host that joins nothing hands the executor nil: the dispatch is
	// honest about its unmetered cost, never stopped over optional telemetry.
	executor, _, err := executorFactory("pi", "")(d)
	if err != nil {
		t.Fatalf("the factory refused a dispatch the honoured set names: %v", err)
	}
	local, ok := executor.(*subprocess.Executor)
	if !ok {
		t.Fatalf("the factory built %T, want the local subprocess executor", executor)
	}
	if m := local.Metering(); m != nil {
		t.Fatalf("a host with no ~/.ticfacrc got metering %+v, want none", *m)
	}

	// A host with both halves hands the executor the join, carrying the
	// dispatch's own run id — the id the status model's reader filters the
	// gateway logs by.
	if err := os.WriteFile(rc, []byte("factory_gateway_url=https://gateway.ai.cloudflare.com/v1/acct/gw\n"+
		"factory_cloudflare_api_token=cft_test_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor, _, err = executorFactory("pi", "")(d)
	if err != nil {
		t.Fatalf("the factory refused a dispatch the honoured set names: %v", err)
	}
	local, ok = executor.(*subprocess.Executor)
	if !ok {
		t.Fatalf("the factory built %T, want the local subprocess executor", executor)
	}
	m := local.Metering()
	if m == nil {
		t.Fatal("a fully configured host got no metering: a subprocess pi dispatch joins by the same resolution a herdr one does")
	}
	if m.RunID != "epic-hn6" {
		t.Errorf("the join attributes to run id %q, want the dispatch's own", m.RunID)
	}
}
