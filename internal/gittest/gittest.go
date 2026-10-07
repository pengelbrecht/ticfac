// Package gittest is the one hermetic way this repository's tests start git.
//
// A test fixture that runs git inherits three things from the machine the
// gate happens to run on, and each of the three has already failed a tick on
// some host while passing on every other:
//
//   - the caller's git CONFIGURATION, through GIT_CONFIG_* pins the test
//     process was started under (the read-only source grade pins
//     maintenance.auto=false into its workers, and git reads those entries
//     above every config file), through the host's global and system config
//     (an operator's pushInsteadOf rewrote a review launch's push; an
//     operator's global identity made a fixture that commits with no identity
//     at all pass every local gate and fail CI with exit 128);
//   - the host's git MAINTENANCE, which every clone, commit and fetch ends by
//     starting detached — a background repack that races t.TempDir's
//     RemoveAll and fails the NEXT test's teardown (tick qsn, tick mel);
//   - the caller's WORKING DIRECTORY, which a git started with an inherited
//     or relative path resolves a repository from — in a linked worktree
//     that is the worktree's own checkout, so the fixture measures the
//     checkout it happens to run in rather than the tree.
//
// Every git a test starts goes through this package, and a guard in this
// package's own tests fails the suite when one does not. Fixtures whose
// SUBJECT is one of those three things — a maintenance control that must be
// able to start maintenance, a sandbox-grade fixture whose environment is
// the thing under test — use the named doors for exactly that, so the
// exception is visible in the call.
//
// This package is for tests. Production code states its own git environment
// (gitbin, runstate's safeArgs, the sandbox grades); nothing under internal/
// may import gittest from a non-test file, and the transport guard in
// internal/gitbin reads this file as production only because it scans every
// non-test .go file — which is also why Command carries TransportEnv.
package gittest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

// The identity every hermetic git commits with, stated rather than inherited
// or guessed. The values are the ones internal/runstate's builder already
// used: they are example.com addresses, as the repository's rules require,
// and they are in the ENVIRONMENT — above every config file — so a fixture
// that commits never depends on any machine's global identity. That is the
// exit-128 class in one line: a scratch clone with no configured identity
// passed every local gate on a laptop with a global identity and failed CI.
const (
	AuthorName     = "ticfac test"
	AuthorEmail    = "ticfac@example.com"
	CommitterName  = "ticfac test"
	CommitterEmail = AuthorEmail
)

// Env is the environment every git a test starts runs with, unless the
// fixture's subject is the environment itself (Control, Under).
//
// Each entry exists because a fixture without it measures its host:
//
//   - GIT_CONFIG_GLOBAL=/dev/null and GIT_CONFIG_SYSTEM=/dev/null: no config
//     file of the machine's is read at all. This is what kills the host's
//     pushInsteadOf, its identity, its rerere cache, its maintenance
//     registration and its credential helpers in one move — the same shape
//     CI's machines have, where none of those exist.
//
//   - the GIT_CONFIG_* mechanism stripped first (WithoutPinnedConfig), so a
//     pin the test process INHERITED — the read-only source grade's, an
//     operator's — cannot reach the fixture above the /dev/null files, which
//     is git's own precedence and the property that makes those files alone
//     not enough. GIT_CONFIG_PARAMETERS (git's internal pass-through of a
//     parent's `-c` arguments) and the askpass variables are stripped for the
//     same reason: they are configuration and prompting reaching the fixture
//     through the environment rather than through a file.
//
//   - identity in the environment, above every config file, so a fixture
//     that commits works on a machine with no global identity at all.
//
//   - GIT_TERMINAL_PROMPT=0: a git that would ask a question fails instead
//     of hanging the gate on a prompt nobody will answer.
//
//   - gitbin.WithNoAutoMaintenance: no maintenance this git starts, because
//     a detached `git maintenance run --auto` outlives the command and races
//     the next test's teardown (tick qsn).
//
//   - gitbin.TransportEnv: the ssh and https bounds, because a fixture's git
//     can reach a remote too, and a transport that goes silent is waited on
//     forever without them (tick pul, epic 6in).
func Env() []string {
	return gitbin.WithNoAutoMaintenance(hermeticBase())
}

// hermeticBase is the two doors' shared half: an environment carrying no
// configuration, identity, prompt or transport bound of the machine's, with
// the identity stated in the environment above every config file.
func hermeticBase() []string {
	env := stripHostConfig(gitbin.WithoutPinnedConfig(os.Environ()))
	env = append(env,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME="+AuthorName,
		"GIT_AUTHOR_EMAIL="+AuthorEmail,
		"GIT_COMMITTER_NAME="+CommitterName,
		"GIT_COMMITTER_EMAIL="+CommitterEmail,
	)
	return append(env, gitbin.TransportEnv()...)
}

// ControlEnv is Env minus the maintenance pins, for the CONTROL half of a
// fixture that asserts a git starts (or a run's git does not start)
// maintenance. An assertion that a plain git starts a detached maintenance
// is worth nothing on a machine where no git would have, so those fixtures
// run their control deliberately unpinned — but still with no inherited
// pins and no config file of the host's, because a control under a host
// that pins or arms maintenance measures the environment and reports it as
// the tree's, which is the failure this package exists to end.
//
// Maintenance tests are the ONLY sanctioned use; the guard in this package
// names the two functions that may build one. The transport bounds are still
// carried: a control git that reaches a remote is as hangable as any other.
func ControlEnv() []string {
	return hermeticBase()
}

// Command builds one hermetic git command for the repository at dir.
//
// dir must be ABSOLUTE: a relative or empty dir is a fixture resolving its
// repository against the caller's working directory, which in a linked
// worktree is the worktree's own checkout — the test measures the checkout
// it happens to run in. Command refuses at runtime rather than inherit.
//
// The returned command carries Env; a caller may append to cmd.Env (a
// GIT_AUTHOR_DATE, a trace), and anything it appends that is not a
// GIT_CONFIG_KEY/VALUE pair lands after the pins intact. State git
// configuration through `-c` arguments, never through GIT_CONFIG_* — the
// pins' numbering belongs to WithNoAutoMaintenance.
func Command(dir string, args ...string) *exec.Cmd {
	requireAbsDir(dir)
	cmd := exec.Command(gitbin.Path(), args...)
	cmd.Dir = dir
	cmd.Env = Env()
	return cmd
}

// Run is Command with a fixture's error reporting: the command's combined
// output comes back, and a refusal fails the test naming the command.
func Run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := Command(dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// Control builds the deliberate control git for a maintenance fixture: every
// hermetic property of Command except the maintenance pins, so the git CAN
// end by starting `git maintenance run --auto --detach`. See ControlEnv.
func Control(dir string, args ...string) *exec.Cmd {
	requireAbsDir(dir)
	cmd := exec.Command(gitbin.Path(), args...)
	cmd.Dir = dir
	cmd.Env = ControlEnv()
	return cmd
}

// Under builds a git command running under an environment the CALLER
// constructed. It exists for fixtures whose subject is a constructed
// environment — internal/exec/subprocess's sandbox grades, whose push
// probes run under exactly the environment a grade launches — where the
// hermetic Env would erase the thing under test. It states the binary and
// the working directory the way Command does, and that is all: the
// environment is the caller's statement, on purpose and in the open.
func Under(env []string, dir string, args ...string) *exec.Cmd {
	requireAbsDir(dir)
	cmd := exec.Command(gitbin.Path(), args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd
}

// Tracked answers the files git tracks under root, as absolute paths.
//
// Tests that scan the repository must scan THESE, not the directory: a
// checkout is not a clean room — an agent's worktree under .claude/worktrees/,
// a scratch clone, any directory a person or a tool left there — and a
// directory walk reads all of it and fails the gate on a file no party to
// the tree wrote. That is the 2026-09-24 incident, verbatim: an old branch's
// cloudflare/src/epic-reconciler.ts under an agent worktree failed
// parity_decision_test on main.
//
// Tracked reads the INDEX, so a file is visible the moment it is staged —
// `git add` — and a directory git does not know about is invisible, which is
// the point. Tracked itself runs git, so it goes through Command: the one
// rule has no exceptions, including for the rule's own enforcement.
func Tracked(t testing.TB, root string) []string {
	t.Helper()
	out, err := Command(root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files in %s: %v", root, err)
	}
	var files []string
	for _, field := range bytes.Split(out, []byte{0}) {
		if len(field) == 0 {
			continue
		}
		path := filepath.Join(root, string(field))
		// A tracked file deleted without `git rm` is still in the index; a
		// directory walk never saw it either, so it is skipped rather than
		// read.
		if _, err := os.Stat(path); err != nil {
			continue
		}
		files = append(files, path)
	}
	return files
}

// stripHostConfig removes the environment's other channels for host git
// configuration and prompting, which neither /dev/null config file reaches:
// git's internal pass-through of a parent's `-c` arguments, and the askpass
// programs a host may point at credentials with.
func stripHostConfig(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			out = append(out, entry)
			continue
		}
		if name == "GIT_CONFIG_PARAMETERS" ||
			name == "GIT_ASKPASS" ||
			name == "SSH_ASKPASS" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// requireAbsDir is Command's, Control's and Under's one structural rule: no
// git this package builds may resolve its repository from the caller's
// working directory. It panics rather than fails a test because an empty or
// relative dir is a programming error in the fixture, not a condition the
// tree can be wrong about.
func requireAbsDir(dir string) {
	if dir == "" {
		panic("gittest: the repository directory is empty — a git started with an inherited " +
			"working directory reads the caller's checkout; pass the fixture's absolute directory")
	}
	if !filepath.IsAbs(dir) {
		panic("gittest: the repository directory " + dir + " is relative — it resolves against the " +
			"caller's working directory, which in a linked worktree is the checkout under test; pass an absolute path")
	}
}
