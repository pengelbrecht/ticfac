package cli

// The command tests for `ticfac sandbox`, ported from ticks'
// cmd/tk/cmd/sandbox_test.go (ticks commit 7b6c0b2f^, before chz made ticks
// tracker-only): ticks' ExecuteArgs-with-captured-output becomes
// Run(args, stdout, stderr) with the exit code asserted, and the tick fixture
// is written as the tracker layout's records rather than through a store.
//
// `ticfac sandbox` is how the image's run scripts reach the repository's own
// `[sandbox]` declaration and boot questions: the container shells out to it
// after its clone rather than teaching a shell script to parse TOML. These
// tests pin that surface, because a boot script depends on it.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

const sandboxValidRunners = `version = 2

[orchestrator]
harness = "claude"

[roles.implement]
kind = "claude"
model = "sonnet"
`

const sandboxRunners = `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[sandbox]
image = "registry.example.com/acme/orchestrator:2.0.0"
toolchain = ["rust@1.90.0", "python@3.13"]
setup = [
  { command = "echo warmed >> $PWD/warm.log", description = "warm the caches" },
]
`

// sandboxRepo builds a checkout carrying the given runners.toml plus the
// epic/tick pair the worker-prompt tests render.
func sandboxRepo(t *testing.T, runners string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".tick", "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ".tick", "issues", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("gy1.json", `{
  "id": "gy1",
  "title": "Herd helper CLI",
  "description": "The epic a1w belongs to",
  "acceptance_criteria": "epic done",
  "priority": 2,
  "type": "epic",
  "owner": "test@example.com",
  "created_by": "test@example.com",
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z",
  "status": "open"
}`)
	write("a1w.json", `{
  "id": "a1w",
  "title": "Deliver the spawn command",
  "description": "deliver the spawn command",
  "acceptance_criteria": "go test green",
  "priority": 2,
  "type": "task",
  "owner": "test@example.com",
  "parent": "gy1",
  "created_by": "test@example.com",
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z",
  "status": "open"
}`)
	if runners != "" {
		if err := os.WriteFile(filepath.Join(dir, ".tick", "runners.toml"), []byte(runners), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSandboxImageFallsBackToTheVersionPinnedBase(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "image", "--root", root, "--tk-version", "0.32.0"})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox image: %s\n%s", stderr.String(), out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "ticks-orchestrator:0.32.0" {
		t.Errorf("image = %q, want the version-pinned base", got)
	}
}

func TestSandboxImageReportsADeclaredImage(t *testing.T) {
	root := sandboxRepo(t, sandboxRunners)
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "image", "--root", root, "--tk-version", "0.32.0"})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox image: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "registry.example.com/acme/orchestrator:2.0.0") {
		t.Errorf("image output does not carry the declared reference:\n%s", out.String())
	}
}

// `--declared-only` is what the entrypoint asks with: it wants to know whether
// the repository is asking for something the control plane did not boot, and
// silence is the answer for the 99% path.
func TestSandboxImageDeclaredOnlyIsSilentWhenNothingIsDeclared(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "image", "--root", root, "--declared-only"})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox image: %s\n%s", stderr.String(), out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "" {
		t.Errorf("output = %q, want nothing", got)
	}
}

// `--declared-only` must answer with NO ticfac checkout above the cwd:
// the entrypoint's declared-image check (image/common.sh repo_setup) runs in
// a worker container that holds the repository it cloned and nothing else,
// and a verb that exits 1 when it cannot read image/Dockerfile off a tree
// it is not in makes the check a no-op there (tick bib).
func TestSandboxImageDeclaredOnlyAnswersOutsideTheModule(t *testing.T) {
	root := sandboxRepo(t, sandboxRunners)
	// The cwd is what makes this the tick-bib reproduction: a temp directory
	// has no go.mod above it, standing in for a worker container, where no
	// ticfac checkout exists beside the binary. Reading the default tk
	// version off the working tree failed there, the verb exited 1, and
	// image/common.sh's `|| declared=""` swallowed it — so the container's
	// declared-image check never refused a mismatched image.
	t.Chdir(t.TempDir())
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "image", "--root", root, "--declared-only"})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox image outside the module: %s\n%s", stderr.String(), out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "registry.example.com/acme/orchestrator:2.0.0" {
		t.Errorf("output = %q, want the declared reference", got)
	}
}

// The same answer for the fallback: `sandbox image` with no --tk-version
// resolves the base image's pin from the embedded image context, so it works
// wherever the binary runs — not only where a ticfac checkout happens to sit
// above the cwd.
func TestSandboxImageResolvesTheDefaultOutsideTheModule(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	t.Chdir(t.TempDir())
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "image", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox image outside the module: %s\n%s", stderr.String(), out.String())
	}
	if got := strings.TrimSpace(out.String()); !strings.HasPrefix(got, "ticks-orchestrator:") {
		t.Errorf("image = %q, want the version-pinned base", got)
	}
}

func TestSandboxToolchainPrintsTheDeclaredPins(t *testing.T) {
	root := sandboxRepo(t, sandboxRunners)
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "toolchain", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox toolchain: %s\n%s", stderr.String(), out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "rust@1.90.0\npython@3.13" {
		t.Errorf("toolchain = %q, want both pins in file order", got)
	}

	plain := sandboxRepo(t, sandboxValidRunners)
	code, out, _ = runCloudArgs(t, []string{"sandbox", "toolchain", "--root", plain})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox toolchain: second repo exited %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "" {
		t.Errorf("output = %q, want nothing for a repo that declares no extra toolchain", got)
	}
}

const unroutedRunners = `version = 2

[roles.implement]
kind = "claude"
`

const orchestratorModelRunners = `version = 2

[orchestrator]
harness = "omp"
kind = "pi"
model = "workers-ai/meta/llama-3.3-70b-instruct-fp8-fast"

[roles.implement]
kind = "claude"
model = "sonnet"
`

// `ticfac sandbox model` is what the entrypoint asks after its clone, so that
// the container never learns to parse routing config itself.
func TestSandboxModelPrintsTheRoutedOrchestratorModel(t *testing.T) {
	root := sandboxRepo(t, orchestratorModelRunners)
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "model", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox model: %s\n%s", stderr.String(), out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "workers-ai/meta/llama-3.3-70b-instruct-fp8-fast" {
		t.Errorf("model = %q, want the orchestrator table's", got)
	}
	if !strings.Contains(stderr.String(), "orchestrator.model") {
		t.Errorf("the note does not name the cell the model came from:\n%s", stderr.String())
	}
}

// No orchestrator model: the role/tier table answers, exactly as it does for
// every other role.
func TestSandboxModelFallsBackToTheRoleTable(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "model", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox model: %s\n%s", stderr.String(), out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "sonnet" {
		t.Errorf("model = %q, want the implement role's", got)
	}
	if !strings.Contains(stderr.String(), "roles.implement.model") {
		t.Errorf("the note does not name the cell the model came from:\n%s", stderr.String())
	}
}

// Nothing routed prints nothing to stdout — the caller's parse must not see a
// substituted default — and succeeds, because refusing to boot is the boot
// script's decision, not this command's.
func TestSandboxModelIsSilentWhenNothingIsRouted(t *testing.T) {
	root := sandboxRepo(t, unroutedRunners)
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "model", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox model: %s\n%s", stderr.String(), out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "" {
		t.Errorf("stdout = %q, want nothing a boot script could mistake for a model", got)
	}
	// Silent to a parser, not to a human: the reason is what turns a refused
	// boot into something an operator can fix.
	if !strings.Contains(stderr.String(), "routes no model") {
		t.Errorf("nothing said why there is no model:\n%s", stderr.String())
	}
}

const environmentRunners = `version = 2

[roles.implement]
kind = "claude"

[environment.commands]
marker = { command = "touch $TICKS_TEST_ENV_MARKER", description = "migrated marker" }
`

func TestSandboxEnvironmentRunsMigratedChecks(t *testing.T) {
	root := sandboxRepo(t, environmentRunners)
	marker := filepath.Join(t.TempDir(), "environment-check-ran")
	t.Setenv("TICKS_TEST_ENV_MARKER", marker)
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "environment", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox environment: %s\n%s\n%s", stderr.String(), out.String(), stderr.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("migrated environment check did not run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "migrated marker") {
		t.Errorf("output does not name the migrated check:\n%s", out.String())
	}
}

func TestSandboxEnvironmentNamesAFailingCheck(t *testing.T) {
	root := sandboxRepo(t, `version = 2

[roles.implement]
kind = "claude"

[environment.commands]
database = { command = "false", description = "database available" }
`)
	code, out, _ := runCloudArgs(t, []string{"sandbox", "environment", "--root", root})
	if code == exitSuccess {
		t.Fatalf("a failing environment check exited 0\n%s", out.String())
	}
	if !strings.Contains(out.String(), "database available") {
		t.Errorf("failure does not name the check:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "environment pre-flight red") {
		t.Errorf("failure does not identify the red pre-flight:\n%s", out.String())
	}
}

func TestSandboxEnvironmentReportsWhenNoChecksAreDeclared(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "environment", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox environment: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "no [environment.commands]") {
		t.Errorf("empty environment declaration is not distinguishable in the log:\n%s", out.String())
	}
}

func TestSandboxSetupRunsAndThenSkips(t *testing.T) {
	root := sandboxRepo(t, sandboxRunners)
	// The stamp lives inside the checkout's git directory, so the fixture has
	// to be a git directory.
	runGitOK(t, root, "init", "-q", "-b", "main")
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "setup", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox setup: %s\n%s", stderr.String(), out.String())
	}
	log, err := os.ReadFile(filepath.Join(root, "warm.log"))
	if err != nil {
		t.Fatalf("the setup command did not run: %v\n%s", err, out.String())
	}
	if strings.Count(string(log), "warmed") != 1 {
		t.Errorf("warm log = %q, want one line", log)
	}

	code, out, stderr = runCloudArgs(t, []string{"sandbox", "setup", "--root", root})
	if code != exitSuccess {
		t.Fatalf("second ticfac sandbox setup: %s\n%s", stderr.String(), out.String())
	}
	log, _ = os.ReadFile(filepath.Join(root, "warm.log"))
	if strings.Count(string(log), "warmed") != 1 {
		t.Errorf("warm log = %q — the same checkout warmed twice", log)
	}
	if !strings.Contains(out.String(), "already warm") {
		t.Errorf("the second run does not say it skipped:\n%s", out.String())
	}
}

func TestSandboxSetupIsANoOpWithoutADeclaration(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	code, out, stderr := runCloudArgs(t, []string{"sandbox", "setup", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox setup: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "nothing to warm") {
		t.Errorf("output does not report the empty declaration:\n%s", out.String())
	}
}

// A repository whose config does not validate authorises nothing — the same
// fail-closed rule the command surface has, applied to the one table that runs
// shell before any worker exists.
func TestSandboxSetupRefusesAnInvalidConfig(t *testing.T) {
	root := sandboxRepo(t, sandboxRunners+"\nbanana = true\n")
	code, _, _ := runCloudArgs(t, []string{"sandbox", "setup", "--root", root})
	if code == exitSuccess {
		t.Fatal("an invalid runners.toml was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "warm.log")); err == nil {
		t.Error("a setup command ran out of an invalid config")
	}
}

// ---------------------------------------------------------------------------
// ticfac sandbox substrate
//
// The container asks this the way it asks for the model: ticfac owns the
// runners.toml parser, so the boot script never learns the format. Two lines on
// stdout are the contract a shell parses — the resolved substrate, then the
// runner-state note to record — and the reasoning goes to stderr, where a boot
// log reads it.
// ---------------------------------------------------------------------------

const substrateRunners = `version = 2

[orchestrator]
harness = "claude"

[orchestration]
substrate = "herdr"
max_parallel = 3

[roles.implement]
kind = "claude"
model = "sonnet"
`

func TestSandboxSubstrateHonoursTheOverride(t *testing.T) {
	root := sandboxRepo(t, substrateRunners)
	t.Setenv(runconfig.SubstrateEnvVar, string(runconfig.SubstrateHarness))
	code, out, errOut := runCloudArgs(t, []string{"sandbox", "substrate", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox substrate: %s", errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want the substrate then the note line", out.String())
	}
	if lines[0] != "harness" {
		t.Errorf("resolved substrate = %q, want harness", lines[0])
	}
	for _, want := range []string{"runner-state: substrate=harness", "config=herdr", "source=" + runconfig.SubstrateEnvVar} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("note line %q missing %q", lines[1], want)
		}
	}
	// The reason a reader needs and the wave width that comes with it.
	for _, want := range []string{runconfig.SubstrateEnvVar, "max_parallel", "3"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut.String())
		}
	}
}

// Without an override the file decides, so a local run is untouched: this repo
// pins herdr, and the command reports herdr as requested.
func TestSandboxSubstrateWithoutAnOverrideReadsTheFile(t *testing.T) {
	root := sandboxRepo(t, substrateRunners)
	t.Setenv(runconfig.SubstrateEnvVar, "")
	code, out, _ := runCloudArgs(t, []string{"sandbox", "substrate", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox substrate exited %d", code)
	}
	note := out.String()
	if !strings.Contains(note, "requested=herdr") {
		t.Errorf("stdout does not report the file's own pin:\n%s", note)
	}
	if strings.Contains(note, "source=") {
		t.Errorf("a run with no override names one:\n%s", note)
	}
}

// Fail closed: a substrate nothing can parse is a stop naming the variable, not
// a silent fall back to the file.
func TestSandboxSubstrateRefusesAnUnknownOverride(t *testing.T) {
	root := sandboxRepo(t, substrateRunners)
	t.Setenv(runconfig.SubstrateEnvVar, "subagents")
	code, _, errOut := runCloudArgs(t, []string{"sandbox", "substrate", "--root", root})
	if code == exitSuccess {
		t.Fatal("an unknown override was accepted")
	}
	if !strings.Contains(errOut.String(), runconfig.SubstrateEnvVar) || !strings.Contains(errOut.String(), "subagents") {
		t.Errorf("the error does not name the variable and its value: %s", errOut.String())
	}
}

// The two ways a run ends up on subagents with no herdr, told apart on the
// channel a boot log actually reads. `auto` finding nothing is the ordinary
// path — stated, not announced — and a pin that cannot be satisfied is loud and
// says what to write instead.
func TestSandboxSubstrateStatesTheOrdinaryPathQuietly(t *testing.T) {
	root := sandboxRepo(t, `version = 2

[orchestrator]
harness = "claude"

[orchestration]
substrate = "auto"
socket = "/tmp/no-such-herdr.sock"

[roles.implement]
kind = "claude"
model = "sonnet"
`)
	t.Setenv(runconfig.SubstrateEnvVar, "")
	t.Setenv(runconfig.EnvVar, "")
	code, out, errOut := runCloudArgs(t, []string{"sandbox", "substrate", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox substrate: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "reason=auto-no-herdr") {
		t.Errorf("the durable note does not distinguish the ordinary path:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), `substrate = "auto"`) {
		t.Errorf("the resolution was not stated at all:\n%s", errOut.String())
	}
	for _, unwanted := range []string{"Falling back", "Cross-vendor role routing"} {
		if strings.Contains(errOut.String(), unwanted) {
			t.Errorf("the ordinary path was announced as a degradation (%q):\n%s", unwanted, errOut.String())
		}
	}
}

func TestSandboxSubstrateStaysLoudForAnUnsatisfiablePin(t *testing.T) {
	root := sandboxRepo(t, `version = 2

[orchestrator]
harness = "claude"

[orchestration]
substrate = "herdr"
socket = "/tmp/no-such-herdr.sock"

[roles.implement]
kind = "claude"
model = "sonnet"
`)
	t.Setenv(runconfig.SubstrateEnvVar, "")
	t.Setenv(runconfig.EnvVar, "")
	code, out, errOut := runCloudArgs(t, []string{"sandbox", "substrate", "--root", root})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox substrate: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "reason=herdr-unavailable") {
		t.Errorf("the durable note does not record the refused assertion:\n%s", out.String())
	}
	for _, want := range []string{"Falling back", `substrate = "auto"`} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr missing %q — the loud case must say what went wrong and what to write instead:\n%s", want, errOut.String())
		}
	}
}

// ---------------------------------------------------------------------------
// ticfac sandbox model --role / ticfac sandbox worker-prompt
//
// The per-tick worker container's two questions. A worker asks the same
// command the orchestrator does, one role cell over, and asks for the job it
// was booted to do — so that image/worker.sh never learns either format.
// ---------------------------------------------------------------------------

// The orchestrator's cell is a frontier one on purpose; routing every per-tick
// container at it is a silent multiple on a wave's bill, so a named role reads
// the role/tier table and NOT `[orchestrator].model`.
func TestSandboxModelForANamedRoleIgnoresTheOrchestratorCell(t *testing.T) {
	root := sandboxRepo(t, orchestratorModelRunners)
	code, out, errOut := runCloudArgs(t, []string{"sandbox", "model", "--root", root, "--role", "implement"})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox model --role implement: %s", errOut.String())
	}
	got := strings.TrimSpace(out.String())
	if strings.Contains(got, "llama") {
		t.Errorf("a worker was routed at the orchestrator's own model %q", got)
	}
	if got == "" && !strings.Contains(errOut.String(), "routes no model for role implement") {
		t.Errorf("nothing said why the implement role routes nothing:\n%s", errOut.String())
	}
}

func TestSandboxModelForANamedRoleReadsThatRolesCell(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	code, out, errOut := runCloudArgs(t, []string{"sandbox", "model", "--root", root, "--role", "implement"})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox model --role implement: %s", errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "sonnet" {
		t.Errorf("model = %q, want the implement role's", got)
	}
	if !strings.Contains(errOut.String(), "roles.implement.model") {
		t.Errorf("the note does not name the cell the model came from:\n%s", errOut.String())
	}
}

// A misspelled tier is a typo in the invocation, caught before any config is
// resolved.
func TestSandboxModelRefusesAnUnknownTier(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	code, _, errOut := runCloudArgs(t, []string{"sandbox", "model", "--root", root, "--role", "implement", "--tier", "supreme"})
	if code != exitUsage {
		t.Fatalf("an unknown tier exited %d, want usage", code)
	}
	if !strings.Contains(errOut.String(), "unknown --tier") {
		t.Errorf("the error does not name the mistake: %s", errOut.String())
	}
}

// One template for both substrates. A container-per-tick worker and a
// herdr-pane worker must be handed the same job, or the two substrates
// disagree about what a tick asked for.
func TestSandboxWorkerPromptRendersTheSharedWorkerTemplate(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	code, out, stderr := runCloudArgs(t, []string{
		"sandbox", "worker-prompt", "--root", root, "--tick", "a1w",
		"--base", "930f1cf4dbcac5505cce506cbf2a8412d8248b92",
	})
	if code != exitSuccess {
		t.Fatalf("ticfac sandbox worker-prompt: %s\n%s", stderr.String(), out.String())
	}
	got := out.String()
	for _, want := range []string{
		"deliver the spawn command", // the tick's description
		"go test green",             // its acceptance criteria
		"Tick ID: a1w",              //
		"tick/gy1/a1w",              // the branch derived from the epic
		"930f1cf4dbcac5505cce506cbf2a8412d8248b92", // the base it must verify
		"RESULT-a1w.md",             // the report it owes
		"Herd helper CLI (gy1)",     // the epic, titled from its record
		"ephemeral cloud container", // the container addendum
		"Do NOT push",               //
		"entrypoint commits",        //
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the worker prompt does not carry %q:\n%s", want, got)
		}
	}
}

func TestSandboxWorkerPromptRefusesWithoutATick(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	code, _, errOut := runCloudArgs(t, []string{"sandbox", "worker-prompt", "--root", root})
	if code != exitUsage {
		t.Fatalf("a worker prompt for no tick exited %d, want usage", code)
	}
	if !strings.Contains(errOut.String(), "--tick is required") {
		t.Errorf("the error does not name the missing input: %s", errOut.String())
	}
}

// An absent tick is a lookup miss (exit 4), never a generic failure: booting a
// worker on a tick that does not exist and booting one on damaged tracker
// state need different answers.
func TestSandboxWorkerPromptReportsAnAbsentTickAsNotFound(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	code, _, errOut := runCloudArgs(t, []string{"sandbox", "worker-prompt", "--root", root, "--tick", "zzz"})
	if code != exitNotFound {
		t.Fatalf("exit code = %d, want %d (not found); stderr:\n%s", code, exitNotFound, errOut.String())
	}
}

// A tick record that exists but cannot be parsed is a real failure, not a
// lookup miss — the same boundary every other lookup draws.
func TestSandboxWorkerPromptReportsADamagedTickAsAFailure(t *testing.T) {
	root := sandboxRepo(t, sandboxValidRunners)
	if err := os.WriteFile(filepath.Join(root, ".tick", "issues", "zzz.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCloudArgs(t, []string{"sandbox", "worker-prompt", "--root", root, "--tick", "zzz"})
	if code != exitGeneric {
		t.Fatalf("exit code = %d, want %d (generic failure); stderr:\n%s", code, exitGeneric, errOut.String())
	}
}

func runGitOK(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// Every `ticfac sandbox` verb answers --json with one document naming its
// schema — and a verb that runs commands (setup, environment) keeps their
// output off stdout, so the document still parses.
func TestSandboxJSONIsOneDocumentPerVerb(t *testing.T) {
	root := sandboxRepo(t, sandboxRunners+`
[environment.commands.echo]
command = "echo check-ran"
`)
	runGitOK(t, root, "init", "-q")
	t.Setenv(runconfig.SubstrateEnvVar, string(runconfig.SubstrateHarness))
	for _, tc := range []struct {
		args   []string
		schema string
		field  string
		want   any
	}{
		{[]string{"image", "--tk-version", "0.32.0"}, "ticfac.sandbox-image.v1", "image", "registry.example.com/acme/orchestrator:2.0.0"},
		{[]string{"toolchain"}, "ticfac.sandbox-toolchain.v1", "toolchain", []any{"rust@1.90.0", "python@3.13"}},
		{[]string{"model", "--role", "implement"}, "ticfac.sandbox-model.v1", "model", "sonnet"},
		{[]string{"substrate"}, "ticfac.sandbox-substrate.v1", "substrate", "harness"},
		{[]string{"setup"}, "ticfac.sandbox-setup.v1", "declared", float64(1)},
		{[]string{"environment"}, "ticfac.sandbox-environment.v1", "passed", float64(1)},
		{[]string{"worker-prompt", "--tick", "a1w"}, "ticfac.sandbox-worker-prompt.v1", "branch", "tick/gy1/a1w"},
	} {
		args := append([]string{"sandbox"}, tc.args...)
		args = append(args, "--root", root, "--json")
		doc, stderr, code := jsonAnswer(t, args)
		if code != exitSuccess {
			t.Fatalf("%v exited %d:\n%s", args, code, stderr)
		}
		mustSchema(t, doc, tc.schema)
		if got, _ := json.Marshal(doc[tc.field]); string(got) != mustJSON(t, tc.want) {
			t.Errorf("%v: %s = %s, want %s", args, tc.field, got, mustJSON(t, tc.want))
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
