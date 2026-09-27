package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StampFile is the name of the marker file Install writes at the root of
// every directory it installs, recording the ticfac version and install
// time. Its presence is what makes a directory "ticfac-managed": Install
// refuses to overwrite a non-empty directory that lacks it unless force is
// true. The name is ticfac's own, not tk's, so each tool's stamp says whose
// tree it is.
const StampFile = ".ticfac-skills-version"

// Stamp is the parsed content of a skill directory's StampFile.
type Stamp struct {
	Skill       string    `json:"skill"`
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
}

// ErrUnmanaged reports that Install's target directory exists, is non-empty,
// and carries no StampFile — so overwriting it would destroy content ticfac
// never wrote. Install wraps it; callers match with errors.Is.
var ErrUnmanaged = errors.New("target directory is not ticfac-managed (no " + StampFile + " stamp)")

// ErrSkillsParent reports that Install's target directory is a skills
// PARENT — it holds other skills as children rather than being one skill's
// own directory. Installing there would delete every sibling skill, which
// is why --force does NOT bypass this check (the accident tk learned the
// hard way, kept here so it cannot happen twice).
var ErrSkillsParent = errors.New("target directory holds other skills as children — installing here would delete them")

// skillsInParent reports the names of child directories of dir that look
// like installed skills (they contain a SKILL.md). A directory with its own
// SKILL.md at the root is a skill — possibly one being upgraded — and is
// never a parent, whatever its children look like. Symlinked children
// count: that is how skill installers that share one tree work.
func skillsInParent(dir string) []string {
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
		return nil // dir is itself a skill
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []string
	for _, e := range entries {
		child := filepath.Join(dir, e.Name())
		info, err := os.Stat(child)
		if err != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(child, "SKILL.md")); err == nil {
			found = append(found, e.Name())
		}
	}
	return found
}

// ReadStamp reads and parses the StampFile at the root of dir.
func ReadStamp(dir string) (Stamp, error) {
	var s Stamp
	data, err := os.ReadFile(filepath.Join(dir, StampFile))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse %s: %w", StampFile, err)
	}
	return s, nil
}

// Install writes the named skill's embedded bundle to dir, replacing
// whatever is there. It refuses to touch a dir that exists, is non-empty,
// and has no StampFile (wraps ErrUnmanaged) unless force is true; it refuses
// a skills PARENT unconditionally. An empty existing directory is treated
// as a nonexistent one: there is nothing to lose.
//
// The swap is atomic-ish: the full tree is written to a temp directory
// alongside dir, then dir is removed and the temp renamed into place.
// Nothing from the old tree is preserved.
func Install(name, dir, version string, force bool) (Stamp, error) {
	var stamp Stamp

	paths, err := Paths(name)
	if err != nil {
		return stamp, err
	}

	info, statErr := os.Stat(dir)

	if statErr == nil && info.IsDir() {
		if siblings := skillsInParent(dir); len(siblings) > 0 {
			shown := siblings
			if len(shown) > 5 {
				shown = shown[:5]
			}
			listed := strings.Join(shown, ", ")
			if len(shown) < len(siblings) {
				listed += fmt.Sprintf(", and %d more", len(siblings)-len(shown))
			}
			return stamp, fmt.Errorf(
				"install %s to %s: %w (%d found: %s) — did you mean %s?",
				name, dir, ErrSkillsParent, len(siblings), listed,
				filepath.Join(dir, name))
		}
	}

	switch {
	case statErr == nil && !info.IsDir():
		return stamp, fmt.Errorf("install %s: %s exists and is not a directory", name, dir)
	case statErr == nil && !force:
		managed, err := isManaged(dir)
		if err != nil {
			return stamp, fmt.Errorf("install %s: %w", name, err)
		}
		if !managed {
			return stamp, fmt.Errorf("install %s to %s: %w", name, dir, ErrUnmanaged)
		}
	case statErr != nil && !errors.Is(statErr, os.ErrNotExist):
		return stamp, fmt.Errorf("install %s: stat %s: %w", name, dir, statErr)
	}
	exists := statErr == nil

	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return stamp, fmt.Errorf("install %s: %w", name, err)
	}

	// Sweep stale temp trees from crashed installs: a fully populated
	// sibling contains a complete SKILL.md and a harness may load it as a
	// duplicate skill.
	if stale, _ := filepath.Glob(filepath.Join(parent, filepath.Base(dir)+".ticfac-tmp-*")); len(stale) > 0 {
		for _, d := range stale {
			_ = os.RemoveAll(d)
		}
	}

	tmp, err := os.MkdirTemp(parent, filepath.Base(dir)+".ticfac-tmp-*")
	if err != nil {
		return stamp, fmt.Errorf("install %s: %w", name, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(tmp)
		}
	}()

	for _, p := range paths {
		data, err := Read(name, p)
		if err != nil {
			return stamp, fmt.Errorf("install %s: %w", name, err)
		}
		dest := filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return stamp, fmt.Errorf("install %s: %w", name, err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return stamp, fmt.Errorf("install %s: %w", name, err)
		}
	}

	stamp = Stamp{Skill: name, Version: version, InstalledAt: time.Now().UTC()}
	stampData, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return stamp, fmt.Errorf("install %s: encode stamp: %w", name, err)
	}
	if err := os.WriteFile(filepath.Join(tmp, StampFile), stampData, 0o644); err != nil {
		return stamp, fmt.Errorf("install %s: %w", name, err)
	}

	if exists {
		if err := os.RemoveAll(dir); err != nil {
			return stamp, fmt.Errorf("install %s: removing old %s: %w", name, dir, err)
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		return stamp, fmt.Errorf("install %s: %w", name, err)
	}
	cleanup = false

	return stamp, nil
}

// isManaged reports whether dir is safe for Install to overwrite without
// --force: empty (nothing to lose), or carrying a StampFile.
func isManaged(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return true, nil
	}
	if _, err := os.Stat(filepath.Join(dir, StampFile)); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", filepath.Join(dir, StampFile), err)
	}
	return false, nil
}
