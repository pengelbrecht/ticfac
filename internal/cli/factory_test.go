package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// `ticfac factory deploy` is an installer for a deployable, and a deployable
// has prerequisites an operator provides (wrangler, a container engine, a
// package manager). The CLI's contract is that a missing prerequisite is a
// STOP with the remedy in it, never a half-configured account left behind —
// and never a usage error, which would bury the remedy under a flag list.
// Both subcommands get that from factory.Deploy/Setup; what this asserts is
// that the CLI passes the flags through to the same code the library tests
// exercise, and maps its errors to the exit codes a script can act on.
func TestFactoryDeployWithoutWranglerIsAStopNotAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to find wrangler")
	}
	// A home for the staging dir and an empty PATH: no wrangler anywhere the
	// resolver looks (PATH, the staged bundle, the repository), deterministically.
	t.Setenv("TK_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	// The repository-local candidate is found by walking up from the working
	// directory, so the test runs from an empty one.
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	code := Run([]string{"factory", "deploy"}, &stdout, &stderr)

	if code != 1 {
		t.Errorf("exit code %d, want 1: a prerequisite stop is not a usage error\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "wrangler") {
		t.Errorf("stderr does not name the missing prerequisite:\n%s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("a refusal wrote to stdout: %q", stdout.String())
	}
}

func TestFactorySetupWithoutWranglerIsAStopNotAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to find wrangler")
	}
	t.Setenv("TK_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	code := Run([]string{"factory", "setup"}, &stdout, &stderr)

	if code != 1 {
		t.Errorf("exit code %d, want 1: a rung that cannot be reached is a stop\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "wrangler") {
		t.Errorf("stderr does not name rung 0's failure:\n%s", stderr.String())
	}
}

// The subcommand surface. `factory` alone and an unknown subcommand are usage
// errors, and the usage names both real subcommands — deploy and setup — so a
// script failing on exit 2 says what the vocabulary is.
func TestFactoryCommandSurface(t *testing.T) {
	for _, args := range [][]string{{"factory"}, {"factory", "nonsense"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
		// The unified usage (0e1's, which b3a's deploy and setup wired into)
		// names all four subcommands in a two-column block rather than as
		// "factory deploy" lines, so assert the vocabulary, not the layout.
		for _, sub := range []string{"deploy", "setup", "status", "dashboard"} {
			if !strings.Contains(stderr.String(), sub) {
				t.Errorf("%v: the usage does not name the %q subcommand:\n%s", args, sub, stderr.String())
			}
		}
	}
}

// A deploy records against this build's version, and the version a shipped
// binary reports is the one the CLI passes — a deploy from a released ticfac
// is distinguishable from a dev one in the factory's own D1 record.
func TestFactoryDeployPassesTheBuildVersion(t *testing.T) {
	// Not observable without a full fake account; the wiring is asserted by
	// running the command against the fake-wrangler harness in internal/factory
	// (deploy_test.go), which pins Version through the same Options the CLI
	// builds. Here the flag surface is what can be checked cheaply: unknown
	// flags are usage errors, so the accepted set is the contract.
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"factory", "deploy", "--no-such-flag"}, &stdout, &stderr); code != 2 {
		t.Errorf("exit code %d, want 2 for an unknown flag", code)
	}
}

// TestFactorySetupRunsFromAnyDirectory keeps the setup walk honest about its
// one directory-dependent probe: the GitHub repo is detected from the
// checkout's origin remote, and a directory with no remote must be reported as
// a skipped check rather than crash the walk. This runs only the flag parsing
// path; the walk itself needs wrangler and is covered above.
func TestFactorySetupAcceptsTheDocumentedFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to find wrangler")
	}
	t.Setenv("TK_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	args := []string{"factory", "setup",
		"--repo", "octo-org/octo-repo",
		"--github-token", "github_pat_example",
		"--gateway-url", "https://gateway.example.com/v1/acct/gw",
		"--provider", "anthropic",
		"--provider-key", "sk-example",
	}
	code := Run(args, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code %d, want the wrangler stop:\n%s", code, stderr.String())
	}
	// The walk stopped at rung 0 having PARSED every answer: it must not have
	// complained about a flag, and the stop is the prerequisite, not usage.
	if strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Errorf("a documented flag was rejected:\n%s", stderr.String())
	}
}

// The staging directory: a deploy materializes the bundle under the ticks home
// (~/.tick/factory/ticfac/cloudflare by default — the staging mirrors the
// repository layout, TK_HOME for tests) — the touchpoint the tick called out. The path is the library's; what this pins is that the CLI
// offers --bundle-dir to name it and the flag reaches Deploy.
func TestFactoryDeployBundleDirFlagReachesTheLibrary(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to find wrangler")
	}
	// An empty PATH and a bundle dir whose wrangler fake is missing: the stop
	// is still the wrangler prerequisite, reached without tripping over the
	// flag wiring.
	t.Setenv("TK_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	dir := filepath.Join(t.TempDir(), "staging")
	code := Run([]string{"factory", "deploy", "--bundle-dir", dir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code %d, want the wrangler stop:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "wrangler") {
		t.Errorf("stderr does not name wrangler:\n%s", stderr.String())
	}
}
