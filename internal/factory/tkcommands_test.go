package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scanner is the whole mechanism: a hand-written list is what went stale
// and produced a container that boots and then dies. These are the commands
// the REAL image-context scripts run; the payload that carries them landed
// with tick b3a and is wired at init, so the assertion runs for real — and
// skips loudly (requireEmbeddedPayload) rather than passing silently if the
// seams are ever unwired. The scanner itself is exercised against a fake
// payload below, so it is proven on bytes written for the purpose too.
func TestEntrypointTkCommandsAreDerivedFromTheScripts(t *testing.T) {
	requireEmbeddedPayload(t)
	got, err := EntrypointTkCommands()
	if err != nil {
		t.Fatalf("EntrypointTkCommands: %v", err)
	}
	want := []string{
		// The orchestrator's parked-question sweep (tick 3c2): tk list
		// --awaiting= is its tk-CLI discovery step, and tk answer is how a
		// collected Telegram reply is written back onto the tick.
		"answer",
		// The container's write side for branch ownership (tick t4y): both
		// entrypoints record the branch they create, so remediation can decide
		// what it may push to from a record rather than from a name.
		"cloud branch",
		"list",
		"sandbox environment",
		"sandbox image",
		"sandbox model",
		"sandbox setup",
		"sandbox substrate",
		"sandbox toolchain",
		// The worker entrypoint's own delegation: the job one per-tick
		// container is given comes from the tracker in the checkout, read by
		// tk rather than by the shell (tick tap).
		"sandbox worker-prompt",
		"version",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("EntrypointTkCommands() = %q, want %q", got, want)
	}
}

// Prose is not an invocation. The scripts tell the operator to run
// `tk factory setup` inside error messages and mention `tk ask` in comments;
// treating those as requirements would gate a deploy on commands the container
// never runs.
func TestEntrypointTkCommandsIgnoreProseAndComments(t *testing.T) {
	requireEmbeddedPayload(t)
	got, err := EntrypointTkCommands()
	if err != nil {
		t.Fatalf("EntrypointTkCommands: %v", err)
	}
	for _, unwanted := range []string{"factory setup", "ask", "is not on path"} {
		for _, c := range got {
			if c == unwanted {
				t.Errorf("the scanner read prose as an invocation: %q", c)
			}
		}
	}
}

// The scanner, against the fake payload: invocations in command position are
// collected (across every script, both roles), the flags and operators after
// a chain do not extend it, and prose — quoted die-messages and whole-line
// comments, including backticked command names in them — reads as prose.
func TestEntrypointTkCommandsScanAFakePayload(t *testing.T) {
	stageFakePayload(t)

	got, err := EntrypointTkCommands()
	if err != nil {
		t.Fatalf("EntrypointTkCommands: %v", err)
	}
	// common.sh: exec tk list --awaiting=ask → "list"; the comment's
	// `tk factory setup` and the quoted "tk ask is not on path" must not
	// appear. worker.sh: two sandbox subcommands. preflight.sh: one.
	// entrypoint.sh: tk version.
	want := []string{"list", "sandbox environment", "sandbox toolchain", "sandbox worker-prompt", "version"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("EntrypointTkCommands() = %q, want %q", got, want)
	}
	for _, unwanted := range []string{"factory setup", "ask", "awaiting"} {
		for _, c := range got {
			if c == unwanted {
				t.Errorf("the scanner read prose or a flag as an invocation: %q", c)
			}
		}
	}
}

// The image build reads the same list from the build context, so a plain
// `docker build image` asserts the same property the deploy does. One
// derivation, two readers — this test is what keeps the committed file from
// becoming the stale hand-written list all over again.
func TestRequiredTkCommandsFileMatchesTheEntrypoint(t *testing.T) {
	requireEmbeddedPayload(t)
	derived, err := EntrypointTkCommands()
	if err != nil {
		t.Fatalf("EntrypointTkCommands: %v", err)
	}
	data, err := ReadSandboxFile(RequiredTkCommandsFile)
	if err != nil {
		t.Fatalf("the image build context does not ship %s: %v", RequiredTkCommandsFile, err)
	}
	var listed []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		listed = append(listed, line)
	}
	if strings.Join(listed, "|") != strings.Join(derived, "|") {
		t.Errorf("%s lists %q but the entrypoint runs %q — regenerate it",
			RequiredTkCommandsFile, listed, derived)
	}
}

// The preflight gate itself (verifyEntrypointTkCommands and its predicate) is
// GONE from this package by decision — see the header of tkcommands.go. What
// was TestVerifyEntrypointTkCommandsNamesTheMissingSubcommand and
// TestVerifyEntrypointTkCommandsPassesACompleteTk in ticks moved with it: the
// check that remains is the Dockerfile-embedded one, which runs against the
// tk the image actually builds from the pinned ref, not against this binary.

// The staged Dockerfile is what wrangler builds, so the pins the deploy
// resolved have to land in it — the same rewrite-in-place contract
// SetDatabaseID has for wrangler.toml.
func TestSetSandboxTkPinsRewritesBothPins(t *testing.T) {
	stageFakePayload(t)
	dir := t.TempDir()
	stageSandbox(t, dir)

	if err := SetSandboxTkPins(dir, "1.2.3", "v1.2.3"); err != nil {
		t.Fatalf("SetSandboxTkPins: %v", err)
	}

	df := readStagedDockerfile(t, dir)
	for _, want := range []string{"\nARG TK_VERSION=1.2.3\n", "\nARG TK_SOURCE_REF=v1.2.3\n"} {
		if !strings.Contains(df, want) {
			t.Errorf("the staged Dockerfile does not carry %q", strings.TrimSpace(want))
		}
	}
}

func TestSetSandboxTkPinsRejectsAnythingThatIsNotAPin(t *testing.T) {
	stageFakePayload(t)
	dir := t.TempDir()
	stageSandbox(t, dir)

	for _, bad := range []string{"1.2.3\nRUN rm -rf /", "v1 2 3", "", "$(id)"} {
		if err := SetSandboxTkPins(dir, "1.2.3", bad); err == nil {
			t.Errorf("SetSandboxTkPins accepted %q as a source ref", bad)
		}
	}
}

func TestSetSandboxTkPinsReportsAnUnpinnableDockerfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := SetSandboxTkPins(dir, "1.2.3", "v1.2.3")
	if err == nil {
		t.Fatal("SetSandboxTkPins silently pinned nothing")
	}
	if !strings.Contains(err.Error(), "TK_SOURCE_REF") {
		t.Errorf("the error does not name the missing ARG: %v", err)
	}
}

// stageSandbox materializes the (fake) image context the pin rewrites run
// against — the same staging the deploy performs, minus the real bytes.
func stageSandbox(t *testing.T, dir string) {
	t.Helper()
	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
}

func readStagedDockerfile(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatalf("staged Dockerfile: %v", err)
	}
	return string(data)
}
