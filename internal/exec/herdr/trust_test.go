package herdr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("the config is not JSON after the registration: %v\n%s", err, raw)
	}
	return config
}

func trusted(config map[string]any, dir string) bool {
	projects, _ := config["projects"].(map[string]any)
	entry, _ := projects[dir].(map[string]any)
	return entry["hasTrustDialogAccepted"] == true
}

// The registration writes the key Claude Code reads and nothing else: every
// other top-level key, every other project, and every other field of the
// worktree's own entry survive the read-modify-write, as does a value the
// HTML-escaping encoder would re-spell.
//
// short: one temp file, no processes.
func TestRegisterClaudeTrustPreservesTheRestOfTheConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	worktree := filepath.Join(dir, "worktrees", "repo", "ticfac-run-epic-e-tick-t-attempt-1")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree, _ = filepath.EvalSymlinks(worktree)
	original := `{
  "numStartups": 42,
  "oauthAccount": {"emailAddress": "someone@example.com"},
  "history": "a <b> & c",
  "projects": {
    "/elsewhere": {"hasTrustDialogAccepted": true, "allowedTools": ["Bash"]},
    "` + worktree + `": {"allowedTools": ["Edit"], "hasTrustDialogAccepted": false}
  }
}
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RegisterClaudeTrust(path, worktree); err != nil {
		t.Fatal(err)
	}
	config := readConfig(t, path)
	if !trusted(config, worktree) {
		t.Fatalf("the worktree is not trusted after the registration: %v", config["projects"])
	}
	if config["numStartups"] != float64(42) || config["history"] != "a <b> & c" {
		t.Errorf("top-level keys changed: numStartups=%v history=%v", config["numStartups"], config["history"])
	}
	if account, _ := config["oauthAccount"].(map[string]any); account["emailAddress"] != "someone@example.com" {
		t.Errorf("a nested key was lost: %v", config["oauthAccount"])
	}
	projects := config["projects"].(map[string]any)
	elsewhere := projects["/elsewhere"].(map[string]any)
	if elsewhere["hasTrustDialogAccepted"] != true || len(elsewhere["allowedTools"].([]any)) != 1 {
		t.Errorf("another project's entry changed: %v", elsewhere)
	}
	own := projects[worktree].(map[string]any)
	if tools, _ := own["allowedTools"].([]any); len(tools) != 1 || tools[0] != "Edit" {
		t.Errorf("the worktree's own entry lost its other fields: %v", own)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "\\u003c") {
		t.Errorf("a carried value was re-spelled by HTML escaping:\n%s", raw)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the config's mode changed: %v %v", info.Mode(), err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Errorf("the config lock was left behind: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".claude.json.ticfac-*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files were left behind: %v", leftovers)
	}
}

// A host where claude never ran interactively has no config: the file is
// created with the one entry, in a directory created for it.
//
// short: one temp file, no processes.
func TestRegisterClaudeTrustToleratesAMissingConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config-dir", ".claude.json")
	if err := RegisterClaudeTrust(path, dir); err != nil {
		t.Fatal(err)
	}
	if !trusted(readConfig(t, path), filepath.Clean(dir)) {
		t.Fatal("a missing config did not become one trusting the worktree")
	}
	// Idempotent: a second registration of a trusted path does not rewrite.
	before, _ := os.Stat(path)
	time.Sleep(20 * time.Millisecond)
	if err := RegisterClaudeTrust(path, dir); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Error("an already-trusted path rewrote the config")
	}
}

// A config this cannot parse is the operator's whole Claude Code state: it is
// left byte-for-byte as it was and the registration fails.
//
// short: one temp file, no processes.
func TestRegisterClaudeTrustNeverRewritesAConfigItCannotRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"truncated":    `{"projects": {`,
		"not-object":   `[1, 2, 3]`,
		"bad-projects": `{"projects": "nope"}`,
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RegisterClaudeTrust(path, dir); err == nil {
			t.Errorf("%s: the registration succeeded over a config it cannot read", name)
		}
		if raw, _ := os.ReadFile(path); string(raw) != content {
			t.Errorf("%s: the unreadable config was rewritten to %q", name, raw)
		}
	}
}

// claude keys a project by the working directory it sees, which on macOS is
// the symlink-resolved path (/private/var/... for /var/...): both spellings
// are registered.
//
// short: one temp file and one symlink, no processes.
func TestRegisterClaudeTrustRegistersTheResolvedPathToo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude.json")
	if err := RegisterClaudeTrust(path, link); err != nil {
		t.Fatal(err)
	}
	config := readConfig(t, path)
	resolved, _ := filepath.EvalSymlinks(link)
	if !trusted(config, link) || !trusted(config, resolved) {
		t.Errorf("want both %s and %s trusted: %v", link, resolved, config["projects"])
	}
	if err := RegisterClaudeTrust(path, "relative/path"); err == nil {
		t.Error("a relative path was registered: claude keys projects by absolute path")
	}
}

// The registration waits for Claude Code's own config lock, and breaks one
// left behind by a dead process once it is stale — proper-lockfile's rule.
//
// short: one temp file, a lock directory, ~100ms of waiting.
func TestRegisterClaudeTrustHonoursClaudesConfigLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	lock := path + ".lock"
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- RegisterClaudeTrust(path, dir) }()
	select {
	case err := <-done:
		t.Fatalf("the registration did not wait for a live lock (err %v)", err)
	case <-time.After(150 * time.Millisecond):
	}
	if pathExists(path) {
		t.Fatal("the config was written while another writer held its lock")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !trusted(readConfig(t, path), filepath.Clean(dir)) {
		t.Fatal("the registration did not land after the lock was released")
	}

	// A stale lock — untouched past proper-lockfile's 10s — is broken.
	other := filepath.Join(dir, "other.json")
	if err := os.Mkdir(other+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(other+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	if err := RegisterClaudeTrust(other, dir); err != nil {
		t.Fatalf("a stale lock was not broken: %v", err)
	}
}

// ClaudeConfigPath resolves where the pinned Claude Code reads its config:
// $CLAUDE_CONFIG_DIR/.claude.json, the legacy .config.json when it exists,
// and ~/.claude.json otherwise.
//
// short: environment and stat calls only.
// serial: it sets CLAUDE_CONFIG_DIR and HOME with t.Setenv, which a parallel test may not.
func TestClaudeConfigPathFollowsClaudeCode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if got, err := ClaudeConfigPath(); err != nil || got != filepath.Join(dir, ".claude.json") {
		t.Errorf("with CLAUDE_CONFIG_DIR: %q %v", got, err)
	}
	legacy := filepath.Join(dir, ".config.json")
	if err := os.WriteFile(legacy, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := ClaudeConfigPath(); got != legacy {
		t.Errorf("with a legacy .config.json: %q, want %q", got, legacy)
	}
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", home)
	if got, _ := ClaudeConfigPath(); got != filepath.Join(home, ".claude.json") {
		t.Errorf("without CLAUDE_CONFIG_DIR: %q", got)
	}
}
