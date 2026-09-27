package cli

// `ticfac skills` (tick 8v3): the embedded agent-skill bundle — inspect it,
// read it, install it. The shape is tk's own `tk skills` (a group with
// list, get and install), so the two tools' skills install the same way:
// `ticfac skills install ticfac` is the one command, and it lands in the
// same .claude/skills/ and .agents/skills/ directories `tk skills install
// ticks` does.
//
// The skill the bundle carries teaches the loop an agent runs an epic with
// (init, doctor, run, the overview, triage) and states its boundary with
// the ticks skill: ticks owns planning and tick shape, ticfac owns
// execution — the two skills divide the work, and neither answers the
// other's question.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/skills"
)

// newSkillsCommand builds the `skills` group.
func newSkillsCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "the agent skills embedded in this binary: inspect, read, install",
		Long: `Inspect, read and install the agent skills embedded in this binary.

The bundle is version-matched to this build: 'ticfac skills list' reports the
ticfac version each skill ships with, and 'ticfac skills get <name>' prints a
skill's SKILL.md straight from the binary.

'ticfac skills install ticfac' is the one command: it installs the ticfac
skill — the loop an agent runs an epic with, and its boundary with the ticks
skill — into every skill directory the repository carries (.claude/skills/
and .agents/skills/), the same way 'tk skills install ticks' installs the
ticks skill, so the two land beside each other. Re-installing over a stamped
directory is the upgrade path. A target with other content and no stamp is
refused (pass --force to take it over); a target that holds OTHER skills as
children is refused outright — --force does not override that, because that
install would delete every sibling skill.`,
	}
	cmd.AddCommand(
		newSkillsListCommand(stdout, stderr),
		newSkillsGetCommand(stdout, stderr),
		newSkillsInstallCommand(stdout, stderr),
	)
	return cmd
}

// newSkillsListCommand builds `skills list`.
func newSkillsListCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list the skills embedded in this binary",
	}
	fs := flag.NewFlagSet("skills list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print one versioned document (ticfac.skills-list.v1): the skills and the ticfac version they ship with")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) != 0 {
			return newExitError(exitUsage, "skills list takes no positional arguments")
		}
		names := skills.List()
		if *asJSON {
			if names == nil {
				names = []string{}
			}
			return emitAgentJSON(stdout, struct {
				agentDoc
				Version string   `json:"version"`
				Skills  []string `json:"skills"`
			}{
				agentDoc: agentDoc{Schema: agentSchemaID("skills-list"), State: agentStateDone},
				Version:  Version,
				Skills:   names,
			})
		}
		for _, name := range names {
			fmt.Fprintf(stdout, "%s\t%s\n", name, Version)
		}
		return nil
	}
	return cmd
}

// newSkillsGetCommand builds `skills get`.
func newSkillsGetCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "print a skill's SKILL.md, straight from the binary",
	}
	fs := flag.NewFlagSet("skills get", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print one versioned document (ticfac.skills-get.v1) holding the file's content instead of the bare text")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) != 1 || args[0] == "" {
			return newExitError(exitUsage, "exactly one skill name is required")
		}
		name := args[0]
		data, err := skills.Read(name, "SKILL.md")
		if err != nil {
			return newExitError(exitNotFound, "%v", err)
		}
		if *asJSON {
			return emitAgentJSON(stdout, struct {
				agentDoc
				Skill   string `json:"skill"`
				File    string `json:"file"`
				Content string `json:"content"`
			}{
				agentDoc: agentDoc{Schema: agentSchemaID("skills-get"), State: agentStateDone},
				Skill:    name,
				File:     "SKILL.md",
				Content:  string(data),
			})
		}
		_, _ = stdout.Write(data)
		fmt.Fprintln(stdout)
		return nil
	}
	return cmd
}

// skillsFlags is `skills install`'s flag surface.
type skillsFlags struct {
	dir    *string
	force  *bool
	asJSON *bool
}

// newSkillsInstallCommand builds `skills install`.
func newSkillsInstallCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install <name>",
		Short: "install a skill from the embedded bundle to disk",
		Long: `Install a skill from the embedded bundle to disk.

Without --dir, ticfac detects skill directory conventions at the repo root:
.claude/skills/ and .agents/skills/. It installs into every one that
exists, both if both do — each gets the full tree and its own stamp.
Neither existing is an error: create one of them at the repo root, or pass
--dir to install elsewhere.

Install writes a stamp file (.ticfac-skills-version) at the root of each
installed directory, recording the ticfac version and install time.
Re-installing over an already-stamped directory is the normal upgrade path:
the old tree is replaced. Install refuses a target directory that already
has other content and no stamp — it may be hand-edited, or belong to
something else entirely — unless --force is given; refusal and --force
apply independently to each detected target.

--dir names the skill's OWN directory, not the folder your skills live in:
without --dir the skill name is appended for you, with --dir it is not — and
a --dir that holds other skills as children is refused outright, --force
will not override it, because that install would delete every sibling.

Exit codes
  0  installed into every target
  1  no --dir and neither .claude/skills/ nor .agents/skills/ exists at the
     repo root; or a target failed for a reason other than being unmanaged
  2  usage error; a target exists and is not ticfac-managed (pass --force or
     remove it first); or a target holds other skills as children
  3  no --dir and the working directory isn't inside a git repository
  4  no such skill is embedded in this binary`,
	}
	fs := flag.NewFlagSet("skills install", flag.ContinueOnError)
	fl := &skillsFlags{
		dir:    fs.String("dir", "", "the skill's OWN directory, e.g. ~/.claude/skills/ticfac — NOT the parent folder holding your skills (default: detect .claude/skills/, .agents/skills/ at the repo root and append the skill name)"),
		force:  fs.Bool("force", false, "overwrite a target directory even if it has no ticfac stamp"),
		asJSON: fs.Bool("json", false, "print one versioned document (ticfac.skills-install.v1): what each target got, and what a refusal refused"),
	}
	commandFlags(cmd, fs)
	cmd.Args = func(c *cobra.Command, args []string) error {
		if len(args) != 1 || args[0] == "" {
			return newExitError(exitUsage, "exactly one skill name is required")
		}
		return nil
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(skillsInstallCommand(args[0], fl, stdout, stderr))
	}
	return cmd
}

// skillsRepoRoot walks up from the working directory to the repository that
// holds it — the same boundary every tk-ported command uses for "not in a
// repository".
func skillsRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// skillsInstallTargetJSON is one target's outcome as the --json document
// carries it.
type skillsInstallTargetJSON struct {
	Dir   string `json:"dir"`
	State string `json:"state"` // installed | refused | failed
	Error string `json:"error,omitempty"`
}

// skillsInstallJSON is `skills install --json`'s answer,
// ticfac.skills-install.v1.
type skillsInstallJSON struct {
	agentDoc
	Skill   string                    `json:"skill"`
	Version string                    `json:"version"`
	Targets []skillsInstallTargetJSON `json:"targets"`
}

// skillsInstallCommand is `skills install`'s body.
func skillsInstallCommand(name string, fl *skillsFlags, stdout, stderr io.Writer) int {
	if _, err := skills.Paths(name); err != nil {
		fmt.Fprintf(stderr, "ticfac skills install: %v\n", err)
		return exitNotFound
	}

	var targets []string
	if *fl.dir != "" {
		targets = []string{*fl.dir}
	} else {
		root, err := skillsRepoRoot()
		if err != nil {
			fmt.Fprintf(stderr, "ticfac skills install: not inside a git repository; run this command inside a repo with "+
				".claude/skills/ or .agents/skills/ at its root, or pass --dir\n")
			return exitNoRepo
		}
		convDirs, err := skills.DetectConventionDirs(root)
		if err != nil {
			fmt.Fprintf(stderr, "ticfac skills install: %v\n", err)
			return exitGeneric
		}
		if len(convDirs) == 0 {
			fmt.Fprintf(stderr, "ticfac skills install: no skills convention directory found at %s (looked for "+
				".claude/skills/ or .agents/skills/); create one of them, or pass --dir\n", root)
			return exitGeneric
		}
		for _, d := range convDirs {
			targets = append(targets, filepath.Join(d, name))
		}
	}

	doc := skillsInstallJSON{
		agentDoc: agentDoc{Schema: agentSchemaID("skills-install")},
		Skill:    name,
		Version:  Version,
		Targets:  []skillsInstallTargetJSON{},
	}
	// Where the per-target prose goes: stdout for a person, stderr under
	// --json, where the document owns stdout.
	report := io.Writer(stdout)
	if *fl.asJSON {
		report = stderr
	}
	failCount := 0
	failCode := 0
	for _, dir := range targets {
		stamp, err := skills.Install(name, dir, Version, *fl.force)
		switch {
		case err == nil:
			fmt.Fprintf(report, "installed %s %s to %s\n", name, stamp.Version, dir)
			doc.Targets = append(doc.Targets, skillsInstallTargetJSON{Dir: dir, State: "installed"})
		case errors.Is(err, skills.ErrSkillsParent):
			// Checked before ErrUnmanaged and without a --force hint: --force
			// is exactly what turns this mistake into deleted skills. The
			// fix is a different --dir.
			fmt.Fprintf(report, "refused %s: %v\n", dir, err)
			doc.Targets = append(doc.Targets, skillsInstallTargetJSON{Dir: dir, State: "refused", Error: err.Error()})
			failCount++
			if failCode == 0 {
				failCode = exitUsage
			}
		case errors.Is(err, skills.ErrUnmanaged):
			fmt.Fprintf(report, "refused %s: %v (pass --force to overwrite)\n", dir, err)
			doc.Targets = append(doc.Targets, skillsInstallTargetJSON{Dir: dir, State: "refused", Error: err.Error()})
			failCount++
			if failCode == 0 {
				failCode = exitUsage
			}
		default:
			fmt.Fprintf(report, "failed %s: %v\n", dir, err)
			doc.Targets = append(doc.Targets, skillsInstallTargetJSON{Dir: dir, State: "failed", Error: err.Error()})
			failCount++
			failCode = exitGeneric
		}
	}
	if *fl.asJSON {
		if failCount == 0 {
			doc.State = agentStateDone
		} else {
			doc.State = agentStateFailed
		}
		if err := emitAgentJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "ticfac skills install: %v\n", err)
			return exitGeneric
		}
	}
	if failCount > 0 {
		fmt.Fprintf(stderr, "ticfac skills install: %d of %d skill install target(s) failed\n", failCount, len(targets))
		return failCode
	}
	return exitSuccess
}
