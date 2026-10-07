package cli

// The skills surface's tests (tick 8v3): the skill installs with one
// command, into the same conventions tk's installs into; the refusals that
// keep an install from destroying what it does not own; and the boundary
// the skill states with the ticks skill — ticks owns planning, ticfac owns
// execution — checked against the tree so the skill cannot teach a command
// that does not exist or contradict the boundary in code.
import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/skills"
)

// These tests move the working directory (install detects conventions from
// it), so they are serial — no t.Chdir under t.Parallel.
//
// skillsRepoFixture makes a repository root with both convention directories
// opted in (the state a repo that has installed the ticks skill is in), and
// returns it with the cwd moved there — install's detection reads the
// working directory.
func skillsRepoFixture(t *testing.T, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	return root
}

// The one command: `ticfac skills install ticfac` into a repo that carries
// both conventions installs the skill into both, stamped, with the SKILL.md
// an agent loads.
func TestSkillsInstallIsOneCommand(t *testing.T) {
	root := skillsRepoFixture(t, ".claude/skills", ".agents/skills")

	var stdout, stderr stringsBuilder
	code := Run([]string{"skills", "install", "ticfac"}, &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	for _, dir := range []string{
		filepath.Join(root, ".claude", "skills", "ticfac"),
		filepath.Join(root, ".agents", "skills", "ticfac"),
	} {
		data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			t.Fatalf("the skill was not installed to %s: %v", dir, err)
		}
		if !strings.Contains(string(data), "name: ticfac") {
			t.Errorf("%s/SKILL.md is not the ticfac skill:\n%s", dir, data)
		}
		stamp, err := skills.ReadStamp(dir)
		if err != nil {
			t.Fatalf("%s carries no stamp — re-install would be a blind overwrite: %v", dir, err)
		}
		if stamp.Skill != "ticfac" {
			t.Errorf("the stamp names %q, want ticfac", stamp.Skill)
		}
	}
	// The upgrade path: a stamped directory is replaced, not refused.
	stdout.Reset()
	if code := Run([]string{"skills", "install", "ticfac"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("re-install over a stamped directory exited %d, want 0 (the upgrade path): %s%s",
			code, stdout.String(), stderr.String())
	}
}

// The refusal that keeps install honest: a target with other content and no
// stamp may be hand-edited, so it is a usage error naming --force — and
// --force takes it over deliberately.
func TestSkillsInstallRefusesUnmanagedTargets(t *testing.T) {
	root := skillsRepoFixture(t, ".claude/skills")
	hand := filepath.Join(root, ".claude", "skills", "ticfac")
	if err := os.MkdirAll(hand, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hand, "SKILL.md"), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr stringsBuilder
	code := Run([]string{"skills", "install", "ticfac"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("an unmanaged target exited %d, want %d (usage: the operator decides with --force)", code, exitUsage)
	}
	if !strings.Contains(stdout.String(), "refused") || !strings.Contains(stdout.String(), "--force") {
		t.Errorf("the refusal does not name the stamp and the --force escape:\n%s%s", stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(hand, "SKILL.md"))
	if err != nil || string(data) != "# mine\n" {
		t.Errorf("the refusal overwrote content install does not own: %q, %v", string(data), err)
	}

	stdout.Reset()
	if code := Run([]string{"skills", "install", "ticfac", "--force"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("--force over an unmanaged target exited %d, want 0: %s%s", code, stdout.String(), stderr.String())
	}
}

// The refusal --force must NOT override: a --dir that holds other skills as
// children would delete every sibling, and the cost of being wrong is the
// whole skills directory.
func TestSkillsInstallRefusesASkillsParentOutright(t *testing.T) {
	root := skillsRepoFixture(t, ".claude/skills")
	sibling := filepath.Join(root, ".claude", "skills", "some-other-skill")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "SKILL.md"), []byte("# other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr stringsBuilder
	code := Run([]string{"skills", "install", "ticfac", "--dir", filepath.Join(root, ".claude", "skills"), "--force"},
		&stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("a skills parent exited %d, want %d — --force must not delete sibling skills", code, exitUsage)
	}
	if _, err := os.Stat(filepath.Join(sibling, "SKILL.md")); err != nil {
		t.Fatalf("the sibling skill did not survive: %v", err)
	}
}

// No convention directory at the repo root: exit 1 naming both conventions
// and the --dir escape, never a silent install into a made-up place.
func TestSkillsInstallWithoutAConventionDirectory(t *testing.T) {
	skillsRepoFixture(t)

	var stdout, stderr stringsBuilder
	code := Run([]string{"skills", "install", "ticfac"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("no convention directory exited %d, want %d", code, exitGeneric)
	}
	if !strings.Contains(stderr.String(), ".claude/skills") || !strings.Contains(stderr.String(), "--dir") {
		t.Errorf("the refusal does not name the conventions and the escape:\n%s%s", stdout.String(), stderr.String())
	}
}

// No repository under the working directory: the tk-family code for "not in
// a repository" (3), kept so an orchestrator branching on it does not retry
// it as a generic failure.
func TestSkillsInstallOutsideARepository(t *testing.T) {
	// A temp directory holds no .git and no parent does (the test binary's
	// own cwd is under this repository, so the fixture must chdir first).
	outside := t.TempDir()
	t.Chdir(outside)

	var stdout, stderr stringsBuilder
	code := Run([]string{"skills", "install", "ticfac"}, &stdout, &stderr)
	if code != exitNoRepo {
		t.Fatalf("outside a repository the install exited %d, want %d", code, exitNoRepo)
	}
}

// An unknown skill is the not-found code: the install target cannot be
// something the binary does not carry.
func TestSkillsInstallUnknownSkill(t *testing.T) {
	t.Parallel()

	var stdout, stderr stringsBuilder
	if code := Run([]string{"skills", "install", "no-such-skill"}, &stdout, &stderr); code != exitNotFound {
		t.Fatalf("an unknown skill exited %d, want %d", code, exitNotFound)
	}
}

// --json answers what install did as one versioned document, one entry per
// target — the same report the prose lines give, as fields.
func TestSkillsInstallJSONReportsTargets(t *testing.T) {
	root := skillsRepoFixture(t, ".claude/skills")

	var stdout, stderr stringsBuilder
	code := Run([]string{"skills", "install", "ticfac", "--json"}, &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	var doc struct {
		Schema  string `json:"schema"`
		State   string `json:"state"`
		Skill   string `json:"skill"`
		Targets []struct {
			Dir   string `json:"dir"`
			State string `json:"state"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &doc); err != nil {
		t.Fatalf("the --json report does not parse: %v\n%s", err, stdout.String())
	}
	if doc.Schema != "ticfac.skills-install.v1" || doc.State != "done" || doc.Skill != "ticfac" {
		t.Errorf("the report's header is wrong: %+v", doc)
	}
	if len(doc.Targets) != 1 || doc.Targets[0].State != "installed" ||
		!strings.HasSuffix(doc.Targets[0].Dir, "ticfac") {
		t.Errorf("the report's targets are wrong: %+v", doc.Targets)
	}
	_ = root
}

// stringsBuilder is the test writer Run takes.
type stringsBuilder struct {
	b strings.Builder
}

func (s *stringsBuilder) Write(p []byte) (int, error) { return s.b.Write(p) }
func (s *stringsBuilder) String() string              { return s.b.String() }
func (s *stringsBuilder) Reset()                      { s.b.Reset() }

// The skill's own contract (tick 8v3): it teaches the loop, it states its
// boundary with the ticks skill — ticks owns planning, ticfac owns
// execution — and every command it names exists in the tree, so the two
// skills cannot drift apart or contradict each other through a command
// that is not there.
func TestTheEmbeddedSkillTeachesTheLoopAndStatesItsBoundary(t *testing.T) {
	t.Parallel()

	data, err := skills.Read("ticfac", "SKILL.md")
	if err != nil {
		t.Fatalf("read the embedded skill: %v", err)
	}
	// Phrases are matched over the text with its line wrapping folded, so
	// re-flowing a paragraph can neither fail nor pass the contract.
	skill := strings.Join(strings.Fields(string(data)), " ")

	// The boundary, stated: planning is the ticks skill's, execution is
	// this one's. A skill that answered the other's question is the
	// contradiction the acceptance refuses.
	for _, boundary := range []string{"ticks", "PLANNING", "EXECUTION", "ticfac skills install ticfac"} {
		if !strings.Contains(skill, boundary) {
			t.Errorf("the skill does not state %q — the boundary with the ticks skill is the contract", boundary)
		}
	}

	// The loop the acceptance names: init, doctor, run, the overview,
	// triage — plus the follow surface and the exit table.
	for _, step := range []string{
		"ticfac init", "ticfac doctor", "ticfac run", "ticfac status",
		"ticfac events", "ticfac watch", "ticfac triage",
	} {
		if !strings.Contains(skill, step) {
			t.Errorf("the skill does not teach %q — an agent with only the two skills cannot run an epic without it", step)
		}
	}
	// The bare overview — `ticfac` with no arguments — is named its own way.
	if !strings.Contains(skill, "`ticfac` with no arguments") {
		t.Errorf("the skill does not teach the bare overview")
	}
	// The exit table's held and running classes, the two an agent branches
	// on without prose.
	if !strings.Contains(skill, "3 the run ended holding") || !strings.Contains(skill, "5 the command ended while the run is still going") {
		t.Errorf("the skill does not teach the held and running exit classes")
	}
}

// skillText is every markdown file the embedded ticfac skill installs —
// SKILL.md and its references/ — as one text: the guard below holds the
// whole bundle, because detail moved into a reference is still a teaching.
func skillText(t *testing.T) string {
	t.Helper()
	paths, err := skills.Paths("ticfac")
	if err != nil {
		t.Fatalf("list the embedded skill: %v", err)
	}
	var b strings.Builder
	for _, p := range paths {
		if !strings.HasSuffix(p, ".md") {
			continue
		}
		data, err := skills.Read("ticfac", p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		b.Write(data)
		b.WriteString("\n")
	}
	return b.String()
}

// skillTree is the command tree a skill is checked against, with cobra's
// lazily-added `help` and `completion` commands materialised so the skill
// may name them like any other.
func skillTree() *cobra.Command {
	root := newRootCommand(discardWriter{}, discardWriter{})
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	return root
}

// childNamed is the child of cmd called name, or nil.
func childNamed(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// skillCommandCheck reads every `ticfac …` backtick span of a skill's text
// against the tree and returns what does not exist — an unknown command,
// subcommand or --flag — plus the set of command paths ("run", "cloud
// logs") the text names. Commands are read off the BACKTICK SPANS — the
// markdown convention the skill writes commands in — because the skill
// also uses the name in prose ("the ticfac skill owns EXECUTION"), and
// prose is not a teaching. Splitting on whitespace instead reads `ticfac
// init` as the two fields `ticfac and init`, checks neither, and no
// subcommand is ever looked up — the vacuous loop of tick 7ht.
//
// A position may name alternatives (`skills list|get|install`, or `\|`
// inside a table cell); each is checked. A word in angle or square
// brackets is a placeholder, and the walk stops at the first argument of a
// command that has no subcommands.
func skillCommandCheck(text string, root *cobra.Command) (unknown []string, named map[string]bool) {
	named = map[string]bool{}
	for _, span := range backtickSpans(text) {
		fields := strings.Fields(span)
		if len(fields) < 2 || fields[0] != "ticfac" {
			continue // prose, or the bare overview
		}
		// Resolve the command path, at most two command words deep.
		cmd := root
		path := ""
		for depth, f := range fields[1:] {
			if depth >= 2 || !cmd.HasSubCommands() || isSkillArgWord(f) {
				break
			}
			alts := splitAlternatives(f)
			var next *cobra.Command
			for _, alt := range alts {
				full := strings.TrimSpace(path + " " + alt)
				child := childNamed(cmd, alt)
				if child == nil {
					unknown = append(unknown, full)
					continue
				}
				named[full] = true
				next = child
			}
			if len(alts) != 1 || next == nil {
				cmd = nil // alternatives or an unknown: no single command owns the flags
				break
			}
			cmd = next
			path = strings.TrimSpace(path + " " + alts[0])
		}
		if cmd == nil || cmd == root || cmd.DisableFlagParsing {
			continue
		}
		for _, f := range fields {
			for _, alt := range splitAlternatives(strings.Trim(f, "[]()")) {
				if !strings.HasPrefix(alt, "--") {
					continue
				}
				name := strings.TrimPrefix(alt, "--")
				if i := strings.IndexByte(name, '='); i >= 0 {
					name = name[:i]
				}
				if name == "" || name == "help" {
					continue
				}
				if cmd.Flags().Lookup(name) == nil && cmd.InheritedFlags().Lookup(name) == nil {
					unknown = append(unknown, path+" --"+name)
				}
			}
		}
	}
	return unknown, named
}

// isSkillArgWord reports whether a span word is an argument, a flag or a
// placeholder rather than a command name.
func isSkillArgWord(f string) bool {
	return f == "" || strings.HasPrefix(f, "-") || strings.HasPrefix(f, "[") ||
		strings.HasPrefix(f, "'") || strings.HasPrefix(f, ".") || strings.ContainsAny(f, "=<>…")
}

// splitAlternatives splits `a|b|c` (or a table cell's `a\|b`) into its
// alternatives, trimming the punctuation prose leaves on a span's edge.
func splitAlternatives(f string) []string {
	var out []string
	for _, a := range strings.Split(strings.ReplaceAll(f, `\|`, "|"), "|") {
		a = strings.Trim(a, ",.;:()[]")
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

// backtickSpans returns the text between each pair of backticks — the spans
// markdown marks code with.
func backtickSpans(text string) []string {
	var spans []string
	for {
		open := strings.IndexByte(text, '`')
		if open < 0 {
			return spans
		}
		rest := text[open+1:]
		close := strings.IndexByte(rest, '`')
		if close < 0 {
			return spans
		}
		spans = append(spans, rest[:close])
		text = rest[close+1:]
	}
}

// The skill cannot rot silently (2026-10-07: nine days of commands — named
// configs, --cloud-workers, steer, factory wait-deployed, sweep — had
// shipped with no word in the skill). Both directions:
//
//   - every `ticfac …` the skill or its references name exists — command,
//     subcommand and flag — so it never teaches what the binary lacks;
//   - every visible command in the tree, and every subcommand of a group
//     (cloud, factory, skills, herd, sweep, sandbox), is named somewhere in
//     the skill bundle, so a command added to the tree fails here until the
//     skill says what it is for.
func TestTheSkillAndTheCommandTreeNameEachOther(t *testing.T) {
	t.Parallel()

	root := skillTree()
	unknown, named := skillCommandCheck(skillText(t), root)
	for _, u := range unknown {
		t.Errorf("the skill teaches `ticfac %s`, which the tree does not carry", u)
	}
	for _, cmd := range root.Commands() {
		if cmd.Hidden || cmd.Name() == "help" {
			continue
		}
		if !named[cmd.Name()] {
			t.Errorf("the skill never names `ticfac %s` — say what it is for in skills/ticfac/SKILL.md or its references/", cmd.Name())
		}
		if cmd.Name() == "completion" {
			continue // the shells are completion's arguments, not teachings
		}
		for _, sub := range cmd.Commands() {
			if sub.Hidden || sub.Name() == "help" {
				continue
			}
			if path := cmd.Name() + " " + sub.Name(); !named[path] {
				t.Errorf("the skill never names `ticfac %s` — say what it is for in skills/ticfac/SKILL.md or its references/", path)
			}
		}
	}
}

// The command check must CHECK (tick 7ht): it used to split the skill on
// whitespace, so `ticfac init` read as the two fields `ticfac and init` —
// the first trimmed to the bare overview and skipped, the second without
// the prefix — and no subcommand was ever looked up, so a skill teaching a
// command the tree does not carry passed. A skill that names a nonexistent
// command, subcommand or flag must be caught, and one that names real ones
// must not be.
func TestSkillCommandCheckCatchesACommandTheTreeDoesNotCarry(t *testing.T) {
	t.Parallel()

	root := skillTree()
	for _, skill := range []string{
		"run it with `ticfac init`, then `ticfac frobnicate <run-id>`",
		"resume with `ticfac unwatch` and see",                      // a nonexistent command with no argument
		"read `ticfac cloud tail <epic>`",                           // a nonexistent subcommand
		"start `ticfac run <epic> --clod`",                          // a nonexistent flag
		"pick `ticfac skills list|frob`",                            // a nonexistent alternative
		"then `ticfac amendment <epic> <key> --confirm\\|--rejekt`", // a nonexistent flag alternative
	} {
		if unknown, _ := skillCommandCheck(skill, root); len(unknown) == 0 {
			t.Errorf("a skill teaching %q was not caught", skill)
		}
	}

	// Real commands, subcommands, alternatives and flags — the check's own
	// negative control.
	real := "1. `ticfac init --yes` — make it runnable.\n" +
		"2. `ticfac doctor --cloud` — ready?\n" +
		"3. `ticfac run <epic> --cloud-workers --config claude`, `ticfac status <run-id>`, `ticfac events <run-id> --follow`,\n" +
		"4. `ticfac` with no arguments, `ticfac skills install ticfac`, `ticfac skills list|get|install`\n" +
		"5. `ticfac cloud logs <epic> -f [--tick <id>]`, `ticfac factory wait-deployed <sha>`\n" +
		"6. `ticfac amendment <epic> <key> --confirm\\|--reject --by <who>`, `ticfac completion bash|zsh|fish`\n"
	unknown, named := skillCommandCheck(real, root)
	if len(unknown) != 0 {
		t.Errorf("real commands were flagged: %v", unknown)
	}
	for _, path := range []string{"init", "run", "skills install", "skills get", "cloud logs", "factory wait-deployed", "completion"} {
		if !named[path] {
			t.Errorf("`ticfac %s` was not read as named", path)
		}
	}

	// Prose around the name is not a teaching: the skill's boundary section
	// says “the ticfac skill owns EXECUTION”, and that must stay legal.
	prose := "the ticfac skill owns EXECUTION; plan with `tk`, run with `ticfac`."
	if unknown, named := skillCommandCheck(prose, root); len(unknown) != 0 || len(named) != 0 {
		t.Errorf("prose was read as command teachings: %v %v", unknown, named)
	}
}

// The upgrade path, end to end: a copy a previous binary installed —
// stamped with its version, carrying files the new bundle no longer has
// and an old SKILL.md — is replaced by an install from this binary, with
// this binary's version on the stamp and the bundle's whole tree
// (references/ included), and nothing of the old tree left behind.
func TestSkillsInstallFromANewBinaryUpgradesAStampedCopy(t *testing.T) {
	root := skillsRepoFixture(t, ".claude/skills")
	dir := filepath.Join(root, ".claude", "skills", "ticfac")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":              "---\nname: ticfac\n---\n# the old skill\n",
		"references/retired.md": "# a reference the new bundle dropped\n",
		skills.StampFile:        `{"skill":"ticfac","version":"v0.0.1-old","installed_at":"2026-09-28T00:00:00Z"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr stringsBuilder
	if code := Run([]string{"skills", "install", "ticfac"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("installing over an older stamped copy exited %d, want 0: %s%s", code, stdout.String(), stderr.String())
	}

	stamp, err := skills.ReadStamp(dir)
	if err != nil {
		t.Fatalf("the upgraded copy carries no stamp: %v", err)
	}
	if stamp.Version != Version {
		t.Errorf("the stamp names %q after the upgrade, want this binary's %q", stamp.Version, Version)
	}
	paths, err := skills.Paths("ticfac")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		want, err := skills.Read("ticfac", p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Errorf("the upgrade did not install %s: %v", p, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s is not the embedded bundle's after the upgrade", p)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "references", "retired.md")); !os.IsNotExist(err) {
		t.Errorf("a file the new bundle dropped survived the upgrade (stat err %v)", err)
	}
}
