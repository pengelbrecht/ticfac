package gittest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hermetic environment, entry by entry, each because a fixture without
// it measures its host. The entries are asserted FROM BOTH SIDES: present
// when they must be, absent when the host tried to supply one.
func TestEnvCarriesNoConfigOrIdentityOfTheHost(t *testing.T) {
	// The read-only source grade's pins, and an operator's, arriving the way
	// they really arrive: in the test process's own environment, where the
	// fixture would inherit them above every config file.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.https://forge.example.com/.pushInsteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://elsewhere.example.com/")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'foo.bar=baz'")
	t.Setenv("GIT_ASKPASS", "/usr/local/bin/host-askpass")

	env := Env()
	has := func(name string) (string, bool) {
		for _, entry := range env {
			if value, ok := strings.CutPrefix(entry, name+"="); ok {
				return value, true
			}
		}
		return "", false
	}

	// Nothing inherited survives: the pin mechanism, git's `-c` pass-through,
	// and the host's askpass are all gone.
	for _, name := range []string{
		"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_PARAMETERS", "GIT_ASKPASS",
	} {
		if _, ok := has(name); ok {
			t.Errorf("Env() carries inherited %s: a pin the host set reaches the fixture above every config file", name)
		}
	}
	// The maintenance pins are the helper's OWN, numbered from nothing: count
	// 2, keys 0 and 1, after the inherited count was stripped.
	if count, ok := has("GIT_CONFIG_COUNT"); !ok || count != "2" {
		t.Errorf("Env() carries GIT_CONFIG_COUNT=%q ok=%v, want the helper's own count of 2", count, ok)
	}
	if key, ok := has("GIT_CONFIG_KEY_0"); !ok || key != "maintenance.auto" {
		t.Errorf("Env() GIT_CONFIG_KEY_0=%q ok=%v, want maintenance.auto", key, ok)
	}
	// No config file of the host's is read, the identity is stated in the
	// environment above every file, and the prompt is off.
	if global, ok := has("GIT_CONFIG_GLOBAL"); !ok || global != "/dev/null" {
		t.Errorf("Env() GIT_CONFIG_GLOBAL=%q ok=%v, want /dev/null", global, ok)
	}
	if system, ok := has("GIT_CONFIG_SYSTEM"); !ok || system != "/dev/null" {
		t.Errorf("Env() GIT_CONFIG_SYSTEM=%q ok=%v, want /dev/null", system, ok)
	}
	for name, want := range map[string]string{
		"GIT_AUTHOR_NAME":     AuthorName,
		"GIT_AUTHOR_EMAIL":    AuthorEmail,
		"GIT_COMMITTER_NAME":  CommitterName,
		"GIT_COMMITTER_EMAIL": CommitterEmail,
	} {
		if got, ok := has(name); !ok || got != want {
			t.Errorf("Env() %s=%q ok=%v, want %q", name, got, ok, want)
		}
	}
	if prompt, ok := has("GIT_TERMINAL_PROMPT"); !ok || prompt != "0" {
		t.Errorf("Env() GIT_TERMINAL_PROMPT=%q ok=%v, want 0", prompt, ok)
	}
	// And the transport bound is carried, because a fixture's git can reach
	// a remote too.
	if _, ok := has("GIT_SSH_COMMAND"); !ok {
		t.Errorf("Env() carries no GIT_SSH_COMMAND: a fixture's git that reaches a remote is unbounded")
	}
}

// ControlEnv is Env minus the maintenance pins and nothing else: a control
// git can start maintenance, and inherits nothing else from anywhere.
func TestControlEnvIsEnvMinusTheMaintenancePins(t *testing.T) {
	control := ControlEnv()
	has := func(env []string, name string) bool {
		for _, entry := range env {
			if strings.HasPrefix(entry, name+"=") {
				return true
			}
		}
		return false
	}
	if has(control, "GIT_CONFIG_COUNT") {
		t.Errorf("ControlEnv() carries maintenance pins: a control git that cannot start maintenance proves nothing")
	}
	if !has(control, "GIT_CONFIG_GLOBAL") || !has(control, "GIT_CONFIG_SYSTEM") {
		t.Errorf("ControlEnv() reads the host's config files: a control under a host that arms or pins " +
			"maintenance measures the environment and reports it as the tree's")
	}
	if !has(control, "GIT_AUTHOR_NAME") {
		t.Errorf("ControlEnv() states no identity: a control commit then depends on the host's global identity")
	}
}

// The lkd class, reproduced both ways in one repository: a commit with no
// identity configured anywhere fails with exit 128 under a plain inherited
// environment (the exact CI failure), and the same commit through the helper
// works, because the identity is the helper's own statement.
//
// The control runs through Under, the door for a fixture whose subject is a
// constructed environment — the plain inherited environment IS the subject
// here — so even this reproduction goes through the package's own doors.
func TestACommitWithNoConfiguredIdentityWorksThroughTheHelper(t *testing.T) {
	repo := t.TempDir()
	Run(t, repo, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("a fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Run(t, repo, "add", "-A")
	Run(t, repo, "commit", "--quiet", "-m", "fixture")

	// The control: the same commit with the environment a worker under the
	// stripped gate inherits — no global config, no identity configured,
	// useConfigOnly refusing the implicit fallback — fails with exit 128,
	// which is what CI saw. If this ever starts succeeding, the reproduction
	// has rotted and the assertion above is proving nothing. Under, not
	// Command: the plain inherited environment IS this control's subject.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.useConfigOnly")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	plain := Under(os.Environ(), repo, "commit", "--quiet", "-m", "control", "--allow-empty")
	if out, err := plain.CombinedOutput(); err == nil {
		t.Fatalf("a commit with no identity at all succeeded without the helper — the lkd reproduction "+
			"has rotted; %s", out)
	} else if !strings.Contains(string(out), "empty ident") &&
		!strings.Contains(string(out), "no email was given") &&
		!strings.Contains(string(out), "no name was given") &&
		!strings.Contains(string(out), "useConfigOnly") {
		t.Fatalf("the control commit failed, but not on identity: %v\n%s", err, out)
	}
}

// Command refuses an inherited or relative working directory at runtime: the
// one structural rule the AST guard cannot see through a helper call.
func TestCommandRefusesAWorkingDirectoryItWouldInherit(t *testing.T) {
	for _, dir := range []string{"", "relative/path"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Command(%q) did not refuse: a git resolving its repository against the "+
						"caller's working directory reads the checkout the gate happens to run in", dir)
				}
			}()
			_ = Command(dir, "status")
		}()
	}
}

// Tracked answers the files git knows about, and only those.
//
// short: one git process against the tree's own index
func TestTrackedAnswersTheTreesOwnFiles(t *testing.T) {
	root := moduleRoot(t)
	files := Tracked(t, root)
	if len(files) == 0 {
		t.Fatal("Tracked found no files: the scan cannot be silently empty")
	}
	var sawGitbin bool
	for _, path := range files {
		if !filepath.IsAbs(path) {
			t.Errorf("Tracked answered the relative path %q: a scanner must join the root, not the cwd", path)
		}
		// A .git directory, a worktree metadata file, or a scratch clone under
		// the root must never appear — tracked files only.
		if strings.HasPrefix(filepath.ToSlash(path), filepath.ToSlash(filepath.Join(root, ".git"))+"/") {
			t.Errorf("Tracked answered %q: git's own directories are not part of the tree the scanner wants", path)
		}
		// A file tracked since the repository's first commit: new files are
		// visible to ls-files the moment they are STAGED (git add), and this
		// assertion deliberately asks for one that needs no staging, so the
		// test is green in any working tree rather than only a clean one.
		if strings.HasSuffix(path, "internal/gitbin/gitbin.go") {
			sawGitbin = true
		}
	}
	if !sawGitbin {
		t.Errorf("Tracked did not answer internal/gitbin/gitbin.go: a file tracked since the first commit is missing from the scan")
	}
}

// moduleRoot walks up from this package's directory to the module root, the
// way internal/gitbin's tests do — this package keeps its imports to gitbin
// alone, so the repository's contracts.RepoRoot does not come in here.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package: cannot locate the module root")
		}
		dir = parent
	}
}
