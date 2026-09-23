package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stagedEntrypoint is the staged image context as the deploy leaves it: the
// embedded tree materialized, then the orchestrator entrypoint rewritten. It
// returns the path of the staged entrypoint.
func stagedEntrypoint(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
	if err := SetSandboxOrchestratorEntrypoint(dir); err != nil {
		t.Fatalf("SetSandboxOrchestratorEntrypoint: %v", err)
	}
	return filepath.Join(dir, ticfacEntrypointName)
}

// The acceptance criterion at the level this package owns: the entrypoint the
// image installs as /usr/local/bin/ticks-orchestrator boots ticfac, and boots
// it with a real base branch rather than the literal "HEAD" (tick udu).
//
// Run against the VENDORED image/entrypoint.sh this fails, which is the point:
// that script execs a harness on the ticks skill loop.
func TestStagedOrchestratorEntrypointExecsTicfac(t *testing.T) {
	path := stagedEntrypoint(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)

	for _, want := range []string{
		"exec \"${cmd[@]}\"",
		"ticfac run-epic",
		`--base "$base_sha"`,
		`--repo "$workdir"`,
		// The run's gateway token under the name pi's cloudflare-workers-ai
		// provider reads (tick mdw): common.sh exports every vendor credential
		// it knows, but pi reads one it does not.
		`export CLOUDFLARE_API_KEY="$gateway_token"`,
		// The substrate the run executes on, stated by whatever booted it (tick
		// 84z): role routing resolves against it, the cloud overlays in the
		// target repo's [roles.*.substrates.cloud] apply, and a role nobody
		// declared cloud routing for refuses the run at start — never a
		// silent fall back to the base cell, which is how a container reached
		// a claude process nobody chose.
		`export TICKS_SUBSTRATE="cloud"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the staged orchestrator entrypoint does not carry %q — it is not booting ticfac", want)
		}
	}
	// The claude CLI's root check went with the claude worker (tick mdw): pi
	// has no permission gate whose refusal IS_SANDBOX would answer, so an
	// export that survives here is one that names nobody's requirement.
	if strings.Contains(text, "export IS_SANDBOX") {
		t.Error("the staged orchestrator entrypoint still exports IS_SANDBOX — that was the claude CLI's refusal of bypassPermissions under root, and pi has no equivalent check")
	}

	// Everything before the exec is KEPT. The keeper above all: it is why
	// committed work outlives an evicted container.
	for _, want := range []string{"start_keeper", "run_preflight", "clone_at_sha", "TK_ACTOR"} {
		if !strings.Contains(text, want) {
			t.Errorf("the staged orchestrator entrypoint lost %q — the boot before the exec must not change", want)
		}
	}
}

// The staged script has to be a script. bash -n is what the image's own build
// runs over it, so a rewrite that produced a syntax error would fail the docker
// build rather than the suite — much later, and much more expensively.
func TestStagedOrchestratorEntrypointParses(t *testing.T) {
	path := stagedEntrypoint(t)
	if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("bash -n on the staged entrypoint failed: %v\n%s", err, out)
	}
}

// A second application is a refusal, not a second definition of one function.
// MaterializeSandbox writes the staged copy fresh on every deploy, so a second
// insertion means the staging order is wrong and the deploy should say so.
func TestSetSandboxOrchestratorEntrypointRefusesASecondApplication(t *testing.T) {
	dir := t.TempDir()
	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
	if err := SetSandboxOrchestratorEntrypoint(dir); err != nil {
		t.Fatalf("first application: %v", err)
	}
	err := SetSandboxOrchestratorEntrypoint(dir)
	if err == nil {
		t.Fatal("a second application was accepted; the staged entrypoint now defines start_harness twice")
	}
	if !strings.Contains(err.Error(), "already execs ticfac") {
		t.Errorf("refusal does not say what happened: %v", err)
	}
}

// Every anchor this rewrite depends on is a STOP when it moves.
//
// image/ is vendored from ticks and gets bumped with sandbox.pin.json. The
// outcome being guarded against is the quiet one: a staged script that bash
// still accepts and that still boots, but whose override is never reached — a
// model orchestrating again, invisible until somebody reads a run log. So the
// deploy must fail rather than ship it.
func TestSetSandboxOrchestratorEntrypointRefusesEveryMovedAnchor(t *testing.T) {
	for _, c := range []struct {
		name   string
		script string
		says   string
	}{
		{
			// Nothing to insert before: a function defined after the call that
			// uses it is a function bash never sees.
			name:   "no main call to insert before",
			script: "#!/usr/bin/env bash\nstart_harness() {\n\t:\n}\n\tstart_harness\n",
			says:   "no final",
		},
		{
			// Nothing to capture for the review phase, and nothing to replace.
			name:   "no start_harness to replace",
			script: "#!/usr/bin/env bash\nrun_the_agent() {\n\t:\n}\nmain() {\n\trun_the_agent\n}\nmain \"$@\"\n",
			says:   "defines no start_harness",
		},
		{
			// THE QUIET ONE. This script takes the override, parses, builds and
			// boots — and runs the harness, because main calls it by another
			// name and nothing ever reaches the redefinition.
			name:   "start_harness is defined but never called by name",
			script: "#!/usr/bin/env bash\nstart_harness() {\n\t:\n}\nboot=start_harness\nmain() {\n\t$boot\n}\nmain \"$@\"\n",
			says:   "never calls start_harness by name",
		},
		{
			// Defined below its own caller: bash reaches main first.
			name:   "start_harness is defined after main runs",
			script: "#!/usr/bin/env bash\nmain() {\n\tstart_harness\n}\nmain \"$@\"\nstart_harness() {\n\t:\n}\n",
			says:   "AFTER its own",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ticfacEntrypointName), []byte(c.script), 0o755); err != nil {
				t.Fatal(err)
			}
			err := SetSandboxOrchestratorEntrypoint(dir)
			if err == nil {
				t.Fatal("accepted; the deploy would have shipped a container that boots a model on the skill loop")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("refusal does not name what moved: %v", err)
			}
			// Every one of them ends with the same remedy and the same owner.
			if !strings.Contains(err.Error(), "re-derived against it") {
				t.Errorf("refusal carries no remedy: %v", err)
			}
		})
	}
}

// What the override actually DOES, proved by running it rather than by reading
// it. The block is evaluated on top of a stand-in for the parts of the
// entrypoint it uses, with `exec` and `git` replaced so the command it would
// have become is observable.
func runOverride(t *testing.T, phase string, extra string) string {
	t.Helper()
	block := ticfacEntrypointBlock

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// The two binaries the override requires on PATH, and a git that answers
	// the default-branch question the way a real remote does.
	for _, name := range []string{"ticfac", "ticfac-exec-subprocess"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitStub := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *symbolic-ref*) exit 1 ;;\n" +
		"  *is-shallow-repository*) echo true ; exit 0 ;;\n" +
		"  *ls-remote*) printf 'ref: refs/heads/trunk\\tHEAD\\n' ; exit 0 ;;\n" +
		"  *fetch*) printf 'GIT-FETCH: %s\\n' \"$*\" ; exit 0 ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(gitStub), 0o755); err != nil {
		t.Fatal(err)
	}

	script := `set -uo pipefail
PATH="` + bin + `:$PATH"
EXIT_CONFIG=3; EXIT_CLONE=4; EXIT_MODEL=5
ACTOR="cloud:orchestrator"
workdir="` + dir + `"
epic="e1"; run_id="r1"; run_branch="tick-run/e1"; run_pass=""
base_sha="521b4805ff865a34265878c4d6b49ab113f61710"
factory_url=""; factory_token=""; factory_project=""
model="sonnet"
gateway_token="run-token"
phase="` + phase + `"
say() { printf 'say: %s\n' "$*"; }
warn() { printf 'warn: %s\n' "$*"; }
die() { shift; printf 'die: %s\n' "$*"; exit 1; }
start_keeper() { printf 'keeper watching %s\n' "$1"; }
start_harness() { printf 'HARNESS\n'; }
exec() { printf 'CLOUDFLARE_API_KEY=%s\n' "${CLOUDFLARE_API_KEY:-}"; printf 'EXEC: %s\n' "$*"; }
` + extra + `
` + block + `
start_harness
`
	path := filepath.Join(dir, "case.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The exit status is not the assertion here: a case that REFUSES exits
	// non-zero on purpose, and what it said is what the test reads.
	out, _ := exec.Command("bash", path).CombinedOutput()
	return string(out)
}

func TestOverrideExecsTicfacRunEpicOnTheRemotesDefaultBranch(t *testing.T) {
	out := runOverride(t, "run", "")
	if !strings.Contains(out, "EXEC: ticfac run-epic") {
		t.Fatalf("the override did not exec ticfac run-epic:\n%s", out)
	}
	// tick rf3: --base is the SUBMITTED COMMIT — where the integration branch
	// is cut from. Cutting from the default branch threw the submission away:
	// an epic that existed only on the submitted branch was not found. (tick
	// udu's concern, that refresh then folds nothing, is the reconciler's to
	// answer now: it resolves the remote's default branch itself — tick wvd.)
	if !strings.Contains(out, "--base 521b4805ff865a34265878c4d6b49ab113f61710") {
		t.Errorf("the override did not cut the run from the submitted commit:\n%s", out)
	}
	if strings.Contains(out, "--base trunk") || strings.Contains(out, "--base HEAD") {
		t.Errorf("the override passed a branch or HEAD as the cut point instead of the submitted commit:\n%s", out)
	}
	// And the base has to be a ref the CHECKOUT holds: the clone is a fetch of
	// one SHA, so rev-parse of a bare branch name fails until it is fetched,
	// and the reconciler refuses the integration branch at its first leg.
	// Proved by running a container without this: "the base \"main\" for
	// epic/lf0 is not a commit this checkout has".
	if !strings.Contains(out, "GIT-FETCH: ") || !strings.Contains(out, "refs/heads/trunk:refs/heads/trunk") {
		t.Errorf("the override did not fetch the base branch into the checkout:\n%s", out)
	}
	// ticfac merges the default branch into the integration branch, and the
	// ticks clone is depth 1: without full history git refuses the merge as
	// "unrelated histories". A shallow checkout is unshallowed before the exec.
	if !strings.Contains(out, "--unshallow") {
		t.Errorf("the override did not fetch full history into a shallow checkout:\n%s", out)
	}
	if !strings.Contains(out, "--run-id r1") || !strings.HasSuffix(strings.TrimSpace(out), "e1") {
		t.Errorf("the override did not name the run and the epic:\n%s", out)
	}
	// The keeper is started before the exec, watching this process.
	if !strings.Contains(out, "keeper watching") {
		t.Errorf("the override did not start the run keeper:\n%s", out)
	}
	if strings.Contains(out, "HARNESS") {
		t.Errorf("a run boot reached the harness:\n%s", out)
	}
}

func TestOverrideLeavesTheReviewPhaseOnTheHarness(t *testing.T) {
	out := runOverride(t, "review", "")
	if !strings.Contains(out, "HARNESS") {
		t.Fatalf("a review boot did not reach the harness it still needs:\n%s", out)
	}
	if strings.Contains(out, "ticfac run-epic") {
		t.Errorf("a review boot tried to reconcile an epic:\n%s", out)
	}
}

// A run routed off the Anthropic route is exactly the run this container is
// for (tick mdw): the worker is pi, which speaks the gateway's workers-ai
// route, so the boot that used to refuse it — a refusal that existed only
// because the worker was the claude CLI — now execs the reconciler and hands
// the workers the token under the name pi reads.
func TestOverrideExecsANonAnthropicRoute(t *testing.T) {
	out := runOverride(t, "run", `TICKS_MODEL_PROVIDER="workers-ai"; export TICKS_MODEL_PROVIDER; model="cloudflare-workers-ai/@cf/zai-org/glm-5.3"`)
	if !strings.Contains(out, "EXEC: ticfac run-epic") {
		t.Fatalf("a run routed to workers-ai was not exec'd:\n%s", out)
	}
	if strings.Contains(out, "die:") {
		t.Errorf("the boot refused a non-Anthropic route, which rejected exactly the workers-ai route this container wires:\n%s", out)
	}
	// The one substitution (tick mdw): the run's gateway token under
	// CLOUDFLARE_API_KEY, the name pi's cloudflare-workers-ai provider reads —
	// common.sh exports the token under every vendor name it knows, and pi's
	// is the one it does not.
	if !strings.Contains(out, "CLOUDFLARE_API_KEY=run-token") {
		t.Errorf("the run's gateway token was not exported under the name pi reads:\n%s", out)
	}
}

func TestOverrideRefusesAnImageWithoutTheExecutor(t *testing.T) {
	// The executor removed from PATH: ticfac would refuse at the first
	// dispatch anyway, but by then the clone and the pre-flight are paid for.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ticfac"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := runOverride(t, "run", `PATH="`+dir+`"`)
	if !strings.Contains(out, "ticfac-exec-subprocess") || !strings.Contains(out, "die:") {
		t.Fatalf("an image missing the executor was not refused:\n%s", out)
	}
}
