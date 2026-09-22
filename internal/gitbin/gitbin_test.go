package gitbin

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// An agent's commit starts no maintenance of the object store its worktree
// shares with the run (tick mel).
//
// The runner is launched with WithNoAutoMaintenance's environment and writes
// its own git command lines, so the environment is the only place the rule
// can be said. This commits through that environment into a repository armed
// so that any automatic maintenance runs in the foreground and always packs —
// "started one" is then a pack on disk — with a plain commit first as the
// control that the arming works on this git at all.
func TestAnAgentsCommitStartsNoMaintenance(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// WithoutPinnedConfig, not merely GIT_CONFIG_GLOBAL=/dev/null: git reads
	// GIT_CONFIG_COUNT's entries above every config file, so a control run
	// inheriting the read-only grade's pins is already running with
	// maintenance.auto=false and starts no maintenance — which this fixture
	// would then report as "the arming does not work on this git".
	base := append(WithoutPinnedConfig(os.Environ()),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=ticfac test", "GIT_AUTHOR_EMAIL=ticfac@example.com",
		"GIT_COMMITTER_NAME=ticfac test", "GIT_COMMITTER_EMAIL=ticfac@example.com",
	)
	git := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command(Path(), args...)
		cmd.Dir, cmd.Env = repo, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	packs := func() int {
		t.Helper()
		matches, err := filepath.Glob(filepath.Join(repo, ".git", "objects", "pack", "*.pack"))
		if err != nil {
			t.Fatal(err)
		}
		return len(matches)
	}
	commit := func(env []string, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(env, "add", name)
		git(env, "commit", "--quiet", "-m", name)
	}

	git(base, "init", "--quiet", ".")
	git(base, "config", "maintenance.autoDetach", "false")
	git(base, "config", "maintenance.loose-objects.enabled", "true")
	git(base, "config", "maintenance.loose-objects.auto", "-1")

	before := packs()
	commit(base, "control")
	if packs() == before {
		t.Fatal("a plain commit in the armed repository started no maintenance; this fixture proves nothing")
	}

	before = packs()
	commit(WithNoAutoMaintenance(base), "agent")
	if after := packs(); after != before {
		t.Fatalf("a commit made with WithNoAutoMaintenance's environment started git's automatic maintenance "+
			"(%d packs before, %d after): an agent's commit would start a background repack of the object "+
			"store the run is writing records into (tick mel)", before, after)
	}
}

// The environment form numbers its entries AFTER what is already pinned — the
// read-only grade pins a dozen keys this way — and leaves alone an
// environment whose count git itself would refuse.
func TestNoAutoMaintenanceIsAppendedAfterWhatIsAlreadyPinned(t *testing.T) {
	t.Parallel()
	pinned := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
	}
	got := WithNoAutoMaintenance(pinned)
	for _, want := range []string{
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=gc.auto", "GIT_CONFIG_VALUE_2=0",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("%q is missing from %q", want, got)
		}
	}
	if last := got[len(got)-1]; last != "GIT_CONFIG_COUNT=3" {
		t.Errorf("the effective count is %q, want GIT_CONFIG_COUNT=3: an earlier pin would be renumbered or lost", last)
	}

	bogus := []string{"GIT_CONFIG_COUNT=many"}
	if got := WithNoAutoMaintenance(bogus); !slices.Equal(got, bogus) {
		t.Errorf("an environment git would refuse was rewritten to %q; the error git reports would be masked", got)
	}
}

// WithoutPinnedConfig removes the env-config mechanism and nothing else. The
// "and nothing else" half is the point: a fixture that also lost authorship or
// GIT_TERMINAL_PROMPT would trade one host dependency for another.
func TestWithoutPinnedConfigStripsOnlyTheEnvConfigMechanism(t *testing.T) {
	t.Parallel()
	got := WithoutPinnedConfig([]string{
		"PATH=/usr/bin",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=maintenance.auto",
		"GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=gc.auto",
		"GIT_CONFIG_VALUE_1=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=ticfac test",
	})
	want := []string{
		"PATH=/usr/bin",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=ticfac test",
	}
	if !slices.Equal(got, want) {
		t.Errorf("WithoutPinnedConfig = %q, want %q", got, want)
	}
}

// The round trip a control run depends on: pins applied and then stripped
// leave an environment git reads as carrying no env config at all.
func TestWithoutPinnedConfigUndoesWithNoAutoMaintenance(t *testing.T) {
	t.Parallel()
	base := []string{"PATH=/usr/bin", "GIT_TERMINAL_PROMPT=0"}
	if got := WithoutPinnedConfig(WithNoAutoMaintenance(base)); !slices.Equal(got, base) {
		t.Errorf("round trip = %q, want %q", got, base)
	}
}
