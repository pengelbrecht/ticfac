//go:build !windows

package sandboximage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tick this test closes, re-stated for the port (46x): the image's run
// scripts call `ticfac sandbox …`, and every other entrypoint test stubs
// ticfac — deliberately, because they isolate the entrypoint's delegation —
// which means none of them can see the failure where a stub answers a verb
// this source's real binary does not have. (The tk original of this failure
// was the image pinning a RELEASED tk that predated `tk sandbox`: a real
// container booted, streamed, and died at exit 6 with "unknown command".)
//
// This runs the entrypoint against a ticfac BUILT FROM THIS SOURCE, which is
// exactly what the image carries (`ticfac factory deploy` cross-compiles the
// deploying checkout into the staged build context). It is the closest thing
// to booting the container that does not need Cloudflare.
//
// Ported from ticks internal/sandbox's entrypoint_realtk_test.go, with tk
// replaced by ticfac wherever the scripts ask the verbs of.

func TestEntrypointReachesTheSkillLoopWithTheRealTicfac(t *testing.T) {
	ticfac := buildRealTicfac(t)

	f := newFixture(t, "")
	// Replace the stub with the real binary. The stub tk stays: tk still
	// answers the tracker commands (the version check), and its answer has to
	// agree with the pin the entrypoint verifies. "dev" is what the
	// ldflag-free build and the entrypoint pin both say.
	if err := os.Remove(filepath.Join(f.binDir, "ticfac")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ticfac, filepath.Join(f.binDir, "ticfac")); err != nil {
		t.Fatal(err)
	}
	f.env[EnvTkVersion] = "dev"
	f.env["TICKS_TEST_TK_VERSION"] = "dev"

	out, code := f.run()

	if strings.Contains(out, "unknown command") {
		t.Fatalf("the entrypoint hit an unknown ticfac subcommand:\n%s", out)
	}
	if code != 0 {
		t.Fatalf("exit %d, want 0 — the entrypoint did not reach the skill loop\n%s", code, out)
	}
	rec := f.harnessRecord()
	mustContain(t, rec, "BIN=pi", "the harness never started")
	mustContain(t, rec, "ticks", "the prompt names the skill")
}

// The model half of the same lesson: `ticfac sandbox model` is a command the
// entrypoint runs, so a stub answering it proves delegation and nothing about
// whether the real ticfac can read a routed model out of a committed config.
// This runs the whole path — commit routing, clone at that SHA, ask the real
// ticfac, probe the route, start the harness on the model it named.
func TestEntrypointRoutesTheModelWithTheRealTicfac(t *testing.T) {
	ticfac := buildRealTicfac(t)

	f := newFixture(t, "")
	f.env[EnvHarness] = "omp" // the assertions below are omp's provider naming
	f.routeModelThroughTheRepository(`version = 2

[orchestrator]
harness = "omp"
kind = "pi"

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.frontier]
model = "workers-ai/meta/llama-3.3-70b-instruct-fp8-fast"
`, "")
	// Nothing must answer for the real binary, including the stub's own hint.
	delete(f.env, "TICKS_TEST_SANDBOX_MODEL")
	if err := os.Remove(filepath.Join(f.binDir, "ticfac")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ticfac, filepath.Join(f.binDir, "ticfac")); err != nil {
		t.Fatal(err)
	}
	f.env[EnvTkVersion] = "dev"
	f.env["TICKS_TEST_TK_VERSION"] = "dev"

	out, code := f.run()
	if strings.Contains(out, "unknown command") {
		t.Fatalf("the entrypoint hit an unknown ticfac subcommand:\n%s", out)
	}
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	rec := f.harnessRecord()
	mustContain(t, rec, "TICKS_MODEL=workers-ai/meta/llama-3.3-70b-instruct-fp8-fast",
		"the real ticfac resolved the orchestrator's model from the role/tier table")
	mustContain(t, rec, "TICKS_MODEL_ID=@cf/meta/llama-3.3-70b-instruct-fp8-fast",
		"the id in the provider's own namespace is what the route was proved with")
	mustContain(t, rec, "ARG=cloudflare-ai-gateway/@cf/meta/llama-3.3-70b-instruct-fp8-fast",
		"the harness runs on the routed model, through the provider omp names for that route")
	mustContain(t, f.probeCalls(), "/workers-ai/v1/chat/completions",
		"the route was proved before the harness started")
}

// The substrate half, end to end against the real binary: a checkout whose
// tracked config pins `substrate = "herdr"` — correct for that repository's
// LOCAL runs — must produce a container that resolves the HARNESS substrate,
// says so, and carries the note. This is the run that actually happened: the
// stub cannot prove that the real decision procedure honours the override, and
// the first cloud run to complete a turn stopped precisely here.
func TestEntrypointResolvesTheSubstrateWithTheRealTicfac(t *testing.T) {
	ticfac := buildRealTicfac(t)

	f := newFixture(t, "")
	f.routeModelThroughTheRepository(`version = 2

[orchestrator]
harness = "omp"
model = "workers-ai/@cf/meta/llama-3.3-70b-instruct-fp8-fast"

[orchestration]
substrate = "herdr"
max_parallel = 3

[roles.implement]
kind = "claude"
model = "sonnet"
`, "")
	delete(f.env, "TICKS_TEST_SANDBOX_MODEL")
	if err := os.Remove(filepath.Join(f.binDir, "ticfac")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ticfac, filepath.Join(f.binDir, "ticfac")); err != nil {
		t.Fatal(err)
	}
	f.env[EnvTkVersion] = "dev"
	f.env["TICKS_TEST_TK_VERSION"] = "dev"

	out, code := f.run()
	if strings.Contains(out, "unknown command") {
		t.Fatalf("the entrypoint hit an unknown ticfac subcommand:\n%s", out)
	}
	if code != 0 {
		t.Fatalf("exit %d, want 0 — a herdr pin must not stop a cloud run\n%s", code, out)
	}
	mustContain(t, out, "substrate harness", "the boot log states the resolved substrate")
	mustContain(t, out, "config=herdr", "the note keeps the checkout's configured intent")
	mustContain(t, out, "source="+EnvSubstrate, "the note names what displaced it")
	mustContain(t, out, "wave width 3", "the other [orchestration] key a cloud boot inherits is reported, not silent")

	rec := f.harnessRecord()
	mustContain(t, rec, "runner-state: substrate=harness requested=harness config=herdr",
		"the prompt carries the exact note the orchestrator must record")
	mustContain(t, rec, EnvSubstrate+"=harness", "everything the harness spawns resolves the same substrate")

	// The tracked file is what the workers will branch from: it still says herdr.
	pinned, err := os.ReadFile(filepath.Join(f.workdir, ".tick", "runners.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pinned), `substrate = "herdr"`) {
		t.Errorf("the container rewrote the checkout's tracked config:\n%s", pinned)
	}
}

// buildRealTicfac builds this source's ticfac into the test's own temporary
// directory — the same binary the deploy cross-compiles into the image.
func buildRealTicfac(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ticfac")
	cmd := exec.Command("go", "build", "-o", path, "./cmd/ticfac")
	cmd.Dir = moduleRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building ticfac from this source: %v\n%s", err, out)
	}
	return path
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := Dir()
	if err != nil {
		t.Fatalf("locating the module root: %v", err)
	}
	// Dir() is <module>/image.
	return filepath.Dir(dir)
}
