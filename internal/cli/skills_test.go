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
	skill := string(data)

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

	// Every `ticfac <command>` the skill names exists in the tree: a skill
	// that teaches a command the binary does not carry is a contradiction
	// between the two skills' shared world, caught here.
	if unknown := skillUnknownCommands(skill, knownCommands()); len(unknown) != 0 {
		for _, sub := range unknown {
			t.Errorf("the skill teaches `ticfac %s`, which the tree does not carry", sub)
		}
	}
}

// knownCommands is the set of subcommand names the tree carries — what a
// skill's teachings are checked against, built from the root so a command
// added to the tree is known here without an edit.
func knownCommands() map[string]bool {
	root := newRootCommand(discardWriter{}, discardWriter{})
	known := map[string]bool{}
	for _, cmd := range root.Commands() {
		known[cmd.Name()] = true
	}
	return known
}

// skillUnknownCommands returns every `ticfac <command>` the skill text
// names that the tree does not carry. Commands are read off the BACKTICK
// SPANS — the markdown convention this skill writes commands in — because
// the skill also uses the name in prose ("the ticfac skill owns
// EXECUTION"), and prose is not a teaching. Splitting on whitespace
// instead reads `ticfac init` as the two fields `ticfac and init`, checks
// neither, and no subcommand is ever looked up — the vacuous loop of tick
// 7ht.
func skillUnknownCommands(skill string, known map[string]bool) []string {
	var unknown []string
	for _, span := range backtickSpans(skill) {
		fields := strings.Fields(span)
		if len(fields) == 0 || fields[0] != "ticfac" {
			continue
		}
		if len(fields) == 1 {
			continue // the bare overview, real
		}
		sub := strings.Trim(fields[1], "|,.;:()")
		if sub == "" || strings.HasPrefix(sub, "-") || strings.HasPrefix(sub, "<") {
			continue // a flag or a placeholder like <epic>, not a command name
		}
		if !known[sub] {
			unknown = append(unknown, sub)
		}
	}
	return unknown
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

// The command check must CHECK (tick 7ht): it used to split the skill on
// whitespace, so `ticfac init` read as the two fields `ticfac and init` —
// the first trimmed to the bare overview and skipped, the second without
// the prefix — and no subcommand was ever looked up, so a skill teaching a
// command the tree does not carry passed. A skill that names a nonexistent
// command must be caught, and a skill that names real ones must not be.
func TestSkillCommandCheckCatchesACommandTheTreeDoesNotCarry(t *testing.T) {
	t.Parallel()

	known := knownCommands()
	for _, skill := range []string{
		"run it with `ticfac init`, then `ticfac frobnicate <run-id>`",
		"resume with `ticfac unwatch` and see", // a nonexistent command with no argument
	} {
		if unknown := skillUnknownCommands(skill, known); len(unknown) == 0 {
			t.Errorf("a skill teaching %q was not caught", skill)
		}
	}

	// The commands the real skill teaches, all real in the tree — the
	// check's own negative control.
	real := "1. `ticfac init` — make it runnable.\n" +
		"2. `ticfac doctor` — ready?\n" +
		"3. `ticfac run <epic>`, `ticfac status <run-id>`, `ticfac events <run-id> --follow`,\n" +
		"4. `ticfac` with no arguments, `ticfac skills install ticfac`\n"
	if unknown := skillUnknownCommands(real, known); len(unknown) != 0 {
		t.Errorf("real commands were flagged: %v", unknown)
	}

	// Prose around the name is not a teaching: the skill's boundary section
	// says “the ticfac skill owns EXECUTION”, and that must stay legal.
	prose := "the ticfac skill owns EXECUTION; plan with `tk`, run with `ticfac`."
	if unknown := skillUnknownCommands(prose, known); len(unknown) != 0 {
		t.Errorf("prose was read as command teachings: %v", unknown)
	}
}
