package herdr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Claude Code's folder trust, registered for the worktrees this executor
// creates (tick o35).
//
// An interactive claude started in a directory it has never seen stops at
// its folder-trust dialog ("Quick safety check: Is this a project you
// created or one you trust?"), and --permission-mode bypassPermissions does
// NOT skip it. A herdr pane is interactive claude in a worktree ticfac just
// created, so a pane could sit at that dialog for the whole startup budget:
// start_failed, and a run held for a person (thorpeleverage epic spq,
// 2026-10-11).
//
// What Claude Code actually records, verified against the pinned 2.1.296
// binary (its config code, and empirically: claude on a pty in a scratch
// HOME, never the operator's real config):
//
//   - The record is the global config file, ~/.claude.json — or
//     $CLAUDE_CONFIG_DIR/.claude.json when that is set, and the legacy
//     <config dir>/.config.json when THAT file exists. A project is trusted
//     when projects["<absolute path>"].hasTrustDialogAccepted is true. The
//     dialog's own "Yes, I trust this folder" writes exactly that key.
//   - Trust is looked up at the project path, then walking UP from the
//     working directory — but the walk stops at the git root. A trusted
//     PARENT directory (~/.herdr/worktrees, say) therefore does not cover a
//     worktree, which is its own git root: measured, the dialog still shows.
//     The main checkout's trust did cover its linked worktree on this
//     version (the config keys a worktree's project to its canonical repo),
//     yet the incident above shows that cannot be relied on across hosts and
//     versions, so the exact worktree path is registered.
//   - CLAUDE_CODE_SANDBOXED=1 skips the dialog — but it also tells Claude
//     Code it runs in a sandbox, which these panes are not, so it is not
//     used. No CLI flag skips it.
//
// So: before claude is launched in a worktree THIS executor created (and
// only then — the operator's own directories are theirs to trust), the
// exact worktree path is written into the config, with an atomic
// read-modify-write that preserves every other key, under the same lock
// Claude Code takes for its own config writes (proper-lockfile's
// "<file>.lock" directory), so a live claude session's concurrent save and
// this one serialise rather than one silently dropping the other. A missing
// file is created with that one entry; an unreadable or malformed one is
// left untouched and the registration fails (the pane then reports the
// dialog precisely instead, startup.go).

// claudeTrustKey is the per-project flag the dialog's acceptance writes.
const claudeTrustKey = "hasTrustDialogAccepted"

// ClaudeConfigPath is the global config file Claude Code reads trust from,
// resolved the way the pinned version resolves it: the legacy
// <config dir>/.config.json when it exists, else .claude.json in
// $CLAUDE_CONFIG_DIR, else in the home directory.
func ClaudeConfigPath() (string, error) {
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	home, homeErr := os.UserHomeDir()
	legacyDir := configDir
	if legacyDir == "" {
		if homeErr != nil {
			return "", fmt.Errorf("resolve Claude Code's config: no home directory: %w", homeErr)
		}
		legacyDir = filepath.Join(home, ".claude")
	}
	if legacy := filepath.Join(legacyDir, ".config.json"); pathExists(legacy) {
		return legacy, nil
	}
	if configDir != "" {
		return filepath.Join(configDir, ".claude.json"), nil
	}
	if homeErr != nil {
		return "", fmt.Errorf("resolve Claude Code's config: no home directory: %w", homeErr)
	}
	return filepath.Join(home, ".claude.json"), nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// The lock Claude Code's own config writes take: proper-lockfile's
// mkdir-as-lock at "<file>.lock", considered stale after 10s untouched.
const (
	claudeLockStale    = 10 * time.Second
	claudeLockPatience = 15 * time.Second
	claudeLockRetry    = 50 * time.Millisecond
)

// RegisterClaudeTrust marks dir trusted in the Claude Code config at
// configPath: projects[dir].hasTrustDialogAccepted = true, every other key of
// the file and of the project's entry preserved. dir is registered as given
// and, when it differs, as its symlink-resolved path, because claude keys the
// project by the working directory it sees. It is idempotent: an entry that
// is already trusted is not rewritten.
func RegisterClaudeTrust(configPath, dir string) error {
	if configPath == "" {
		return errors.New("register claude trust: no config path")
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("register claude trust: %q is not an absolute path", dir)
	}
	paths := []string{filepath.Clean(dir)}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != paths[0] {
		paths = append(paths, resolved)
	}

	release, err := lockClaudeConfig(configPath)
	if err != nil {
		return err
	}
	defer release()

	config := map[string]json.RawMessage{}
	mode := fs.FileMode(0o600)
	raw, err := os.ReadFile(configPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A host where claude never ran interactively: the file is created
		// with the one entry, and Claude Code fills in its defaults on read.
	case err != nil:
		return fmt.Errorf("register claude trust: read %s: %w", configPath, err)
	default:
		if info, statErr := os.Stat(configPath); statErr == nil {
			mode = info.Mode().Perm()
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			// Never overwrite a config this cannot read: it is the operator's
			// whole Claude Code state, and "fixing" it here would destroy it.
			return fmt.Errorf("register claude trust: %s is not a JSON object this can safely rewrite: %w",
				configPath, err)
		}
		if config == nil {
			config = map[string]json.RawMessage{}
		}
	}

	projects := map[string]map[string]json.RawMessage{}
	if rawProjects, ok := config["projects"]; ok && string(rawProjects) != "null" {
		if err := json.Unmarshal(rawProjects, &projects); err != nil {
			return fmt.Errorf("register claude trust: the projects table in %s is not an object of objects: %w",
				configPath, err)
		}
	}
	changed := false
	for _, path := range paths {
		entry := projects[path]
		if entry == nil {
			entry = map[string]json.RawMessage{}
		}
		if string(entry[claudeTrustKey]) == "true" {
			continue
		}
		entry[claudeTrustKey] = json.RawMessage("true")
		projects[path] = entry
		changed = true
	}
	if !changed {
		return nil
	}
	encodedProjects, err := encodeJSON(projects, false)
	if err != nil {
		return fmt.Errorf("register claude trust: %w", err)
	}
	config["projects"] = encodedProjects
	out, err := encodeJSON(config, true)
	if err != nil {
		return fmt.Errorf("register claude trust: %w", err)
	}
	return writeConfigAtomically(configPath, out, mode)
}

// encodeJSON encodes without HTML escaping, so a value carried through
// untouched (a prompt history entry holding "<" or "&") keeps its spelling
// rather than being re-spelled as \u003c.
func encodeJSON(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if !indent {
		return bytes.TrimRight(buf.Bytes(), "\n"), nil
	}
	return buf.Bytes(), nil
}

// writeConfigAtomically replaces path through a temporary file in its own
// directory, so a claude reading the config never sees half of it.
func writeConfigAtomically(path string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".ticfac-*")
	if err != nil {
		return fmt.Errorf("register claude trust: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op once the rename landed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("register claude trust: write %s: %w", name, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("register claude trust: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("register claude trust: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("register claude trust: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("register claude trust: replace %s: %w", path, err)
	}
	return nil
}

// lockClaudeConfig takes the lock Claude Code's own config writes take: the
// "<file>.lock" directory, created atomically by mkdir. A lock left behind
// by a process that died (untouched past claudeLockStale) is broken, as
// proper-lockfile itself breaks it.
func lockClaudeConfig(configPath string) (func(), error) {
	lock := configPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return nil, fmt.Errorf("register claude trust: %w", err)
	}
	deadline := time.Now().Add(claudeLockPatience)
	for {
		err := os.Mkdir(lock, 0o700)
		if err == nil {
			return func() { _ = os.Remove(lock) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("register claude trust: take the config lock %s: %w", lock, err)
		}
		if info, statErr := os.Stat(lock); statErr == nil && time.Since(info.ModTime()) > claudeLockStale {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("register claude trust: the config lock %s is held past %s", lock, claudeLockPatience)
		}
		time.Sleep(claudeLockRetry)
	}
}
