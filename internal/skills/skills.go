// Package skills serves the agent-skill bundle embedded in this binary
// (tick 8v3): the `ticfac` skill that teaches the loop an agent runs an
// epic with, inspectable and installable from the executable alone.
//
// The design is ticks' own internal/skills, re-implemented here (the config
// rule forbids importing a ticks Go package): same shapes, same refusal
// vocabulary, a stamp of our own. `ticfac skills install ticfac` installs
// into the harness skill directories the repo already carries —
// .claude/skills/ and .agents/skills/ — exactly the way `tk skills install
// ticks` does, so the two skills install the same way and live beside each
// other.
package skills

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	ticfac "github.com/pengelbrecht/ticfac"
)

// bundleRoot is the directory prefix the embedded FS uses for the skills
// tree.
const bundleRoot = "skills"

// ticfacSkills is the embedded bundle, behind a one-line seam so tests can
// hold it (the embed is a compile-time fact, the function is the callable
// one).
var ticfacSkills = func() fs.FS { return ticfac.SkillsFS() }

// List returns the names of the embedded skills, sorted. Today that is just
// "ticfac"; anything added under skills/ is picked up automatically.
func List() []string {
	fsys := ticfacSkills()
	entries, err := fs.ReadDir(fsys, bundleRoot)
	if err != nil {
		// Unreachable: the directory is embedded at compile time.
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// Files returns an fs.FS rooted at the named skill, so "SKILL.md" is a valid
// path. It fails for an unknown skill.
func Files(name string) (fs.FS, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	fsys := ticfacSkills()
	sub, err := fs.Sub(fsys, path.Join(bundleRoot, name))
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", name, err)
	}
	// fs.Sub succeeds for a non-existent directory, so probe it.
	if _, err := fs.ReadDir(sub, "."); err != nil {
		return nil, fmt.Errorf("unknown skill %q", name)
	}
	return sub, nil
}

// Read returns the contents of one file inside a skill.
func Read(name, filePath string) ([]byte, error) {
	fsys, err := Files(name)
	if err != nil {
		return nil, err
	}
	clean := path.Clean(filePath)
	if !fs.ValidPath(clean) || clean == "." {
		return nil, fmt.Errorf("invalid path %q in skill %q", filePath, name)
	}
	data, err := fs.ReadFile(fsys, clean)
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", name, err)
	}
	return data, nil
}

// Paths returns every file in a skill, sorted, as slash-separated paths
// relative to the skill root.
func Paths(name string) ([]string, error) {
	fsys, err := Files(name)
	if err != nil {
		return nil, err
	}
	var out []string
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", name, err)
	}
	sort.Strings(out)
	return out, nil
}

func validName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("invalid skill name %q", name)
	}
	return nil
}
