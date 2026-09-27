package cli

// `ticfac init` (tick o0d): make a repository ready to run an epic, in one
// step, by answering a few questions.
//
// What a run of a repository READS from that repository is three files, and
// before this command each of them was hand-authored by copying another
// repository's and editing it — the drift hazard the vendored-contract
// discipline exists for, pointed at the operator's own checkout:
//
//	.tick/runners.toml        the routing every dispatch resolves against,
//	                          and the [testing.commands] the integrated gate
//	                          runs — the reconciler REFUSES a repository that
//	                          declares none
//	.tick/runners.cloud.toml  the cloud substrate's role cells; a cloud run
//	                          refuses a role this file does not declare
//	.tick/config.md           the PR + CI close-out rule the close-out holds
//	                          on, read as prose by an anchor phrase
//
// init writes those, guessing the two things the repository can answer for
// itself — the testing gate, from the tree it stands in, and the close-out
// rule, from the origin remote it pushes through (tick 6vp: a rule written
// for a repository with no GitHub origin is one `ticfac run` refuses to
// start on) — and asking the four things only a person can answer: where
// runs execute (local, cloud or both), which harness dispatches the work,
// which model, and whether the close-out holds on a GitHub pull request
// when the guess is wrong. Every question has a flag, because the same
// surface serves an agent; --yes takes every default and the guesses
// without asking.
//
// It refuses to overwrite: a repository that already carries any file init
// would write is a repository whose routing somebody chose, and a silent
// rewrite would be the "tracker state nobody owns" failure with a different
// file name. The refusal names what is already there.
//
// What init does NOT write is as deliberate:
//
//   - no .tick/runners.local.toml. The base file IS this machine's routing
//     (a local run reads the base cells); runners.local.toml exists for
//     per-MACHINE overrides of a repository's settled routing, and which
//     machine this is is not a question a repository's init can answer.
//   - no [tier_policy]. Without one every dispatch runs at the role's base
//     values — a policy that escalates on failure is a tuning decision a
//     repository should make from measurements it has, not inherit a guess.
//   - no .github/workflows/ci.yml. The close-out rule init writes when the
//     answer names it names CI as the gate's second half; whether that
//     workflow exists and what it runs is the repository's CI, not ticfac's
//     to invent.

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The substrate answers init accepts, and what each writes.
const (
	initSubstrateLocal = "local" // the base file routes this machine's runs
	initSubstrateCloud = "cloud" // the base file routes the cloud, and runners.cloud.toml declares the cloud cells
	initSubstrateBoth  = "both"  // the base file routes local, runners.cloud.toml routes cloud
)

// The harness answers init accepts. Cloud is not among the choices because
// the cloud is not one: what runs in Cloudflare runs Workers AI models only,
// through the factory's gateway ([profile.CloudRule]), so a cloud routing is
// pi on a Workers AI model whatever the local answer is.
const (
	initRunnerClaude = "claude"
	initRunnerPi     = "pi"
)

// The close-out answers init accepts (tick 6vp). `pr` writes the PR + CI
// close-out rule; `none` writes a config.md that declares no rule — the
// answer a repository whose origin is not a GitHub remote needs, because
// the rule holds on a GitHub pull request a run would refuse to start
// without, and nothing the operator could pass would fix it.
const (
	initCloseoutPR   = "pr"
	initCloseoutNone = "none"
)

// The model each harness runs when the answer names none. The claude alias
// over a full model id is deliberate — aliases survive model refreshes; and
// the pi default is the Workers AI pairing this repository's own routing was
// verified with.
const (
	initDefaultClaudeModel = "sonnet"
	initDefaultPiModel     = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
)

// initConfigMDName and friends are the repo-relative paths init owns. The
// runners path is runconfig's constant, re-used rather than restated.
const (
	initConfigMDName = ".tick/config.md"
	initCloudName    = ".tick/runners.cloud.toml"
)

// initFlags is `init`'s flag surface: the same answers the questions ask,
// so a script (or an agent) never has to drive a prompt.
type initFlags struct {
	repo, substrate, runner, model, gate, closeout *string
	yes                                            *bool
	asJSON                                         *bool
}

func defineInitFlags(fs *flag.FlagSet) *initFlags {
	return &initFlags{
		repo:      fs.String("repo", "", "the repository to make ready (default: cwd)"),
		substrate: fs.String("substrate", "", "local | cloud | both — where runs of this repo execute (default: local)"),
		runner:    fs.String("runner", "", "claude | pi, the harness local dispatches run on (default: claude)"),
		model:     fs.String("model", "", "the model every role routes to (default: per runner)"),
		gate:      fs.String("gate", "", "the testing gate, when it cannot be guessed from the repository"),
		closeout:  fs.String("closeout", "", "pr | none — whether the close-out holds on a GitHub pull request with CI (default: guessed from the origin remote)"),
		yes:       fs.Bool("yes", false, "take every default and the guessed gate without asking"),
		asJSON:    fs.Bool("json", false, "print one versioned document (ticfac.init.v1) naming the answers and the files written; any question goes to stderr"),
	}
}

// newInitCommand builds the cobra command for `init`.
func newInitCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "make this repository ready to run an epic",
		Long: `Make this repository ready to run an epic, in one step.

Asks where runs execute (local, cloud or both), which harness dispatches
local work (claude or pi), which model, and whether the close-out holds
on a GitHub pull request with CI — the one question, like the gate, the
repository can guess for itself, from its origin remote — then writes:

  .tick/runners.toml        the dispatch routing and the [testing.commands]
                            the integrated gate runs — guessed from the
                            repository (go test ./..., pnpm test, make test)
                            and shown for confirmation
  .tick/runners.cloud.toml  the cloud's role cells, when the answer names
                            the cloud — the cloud runs Workers AI models only
  .tick/config.md           the close-out rule the close-out holds on — the
                            PR + CI one when the answer names it, no rule
                            when it does not

Every question has a flag, so a script never has to drive a prompt; --yes
takes every default and the guess. It refuses to overwrite: a repository
that already carries a file init would write keeps what it has, and the
refusal names it.

Run 'ticfac doctor' afterwards: init makes the repository ready, doctor
says what the machine a run starts on still needs.`,
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fl := defineInitFlags(fs)
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) != 0 {
			fmt.Fprintf(stderr, "ticfac init: takes no positional arguments — the answers are flags or questions\n")
			return &printedExit{code: exitUsage}
		}
		return codeToErr(runInit(fl, os.Stdin, stdout, stderr))
	}
	return cmd
}

// runInit is `init`'s body. stdin is where the questions are answered (the
// caller passes the process's own stdin; tests pass a buffer) — every answer
// a flag carried is never asked. With --json the questions, if any remain,
// go to stderr — a prompt on stdout would corrupt the one document the flag
// promises — and the answers still read from stdin.
func runInit(fl *initFlags, stdin io.Reader, stdout, stderr io.Writer) int {
	repo := *fl.repo
	if repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "ticfac init: cannot read the working directory: %v\n", err)
			return exitGeneric
		}
		repo = wd
	}

	// What this repository already declares. The refusal comes FIRST, before
	// any question is asked: a repository that carries routing somebody wrote
	// is not one to interview, and a person who runs init by habit must not
	// be three answers into a prompt before learning it was moot.
	targets := initTargets(repo, *fl.substrate)
	var existing []string
	for _, name := range targets {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(name))); err == nil {
			existing = append(existing, name)
		}
	}
	if len(existing) > 0 {
		fmt.Fprintf(stderr, "ticfac init: refusing to overwrite %s — this repository already carries %s. "+
			"init writes a repository's first routing, never its second: edit the file by hand\n",
			strings.Join(existing, ", "), pluralFiles(existing))
		return exitGeneric
	}

	// The answers. A flag's value is final; anything a flag left empty is a
	// question, answered on stdin (empty line = the default) — unless --yes
	// takes the default without asking. Under --json the prompts are
	// stderr's, so stdout stays the document's alone.
	promptOut := stdout
	if *fl.asJSON {
		promptOut = stderr
	}
	answers, code := resolveInitAnswers(fl, stdin, promptOut, stderr, repo)
	if code != exitSuccess {
		return code
	}

	gates := initGateCommands(answers.gate, repo)
	if gates == nil {
		fmt.Fprintf(stderr, "ticfac init: no testing gate could be guessed from this repository, and the "+
			"integrated gate is what closes a tick — a run REFUSES a repository that declares none. "+
			"Name the command that runs this repository's tests: --gate '<command>'\n")
		return exitUsage
	}

	// The writes, and the overwrite check on the EXACT set the answers name:
	// the early check above covered what the flags already said, and an
	// answer the questions settled can add the cloud file to the set — a
	// repository that carries one keeps it whatever the flags were. The
	// check runs before any write, so a refusal leaves nothing behind.
	writes := initWrites(repo, answers, gates)
	for _, w := range writes {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(w.name))); err == nil {
			fmt.Fprintf(stderr, "ticfac init: refusing to overwrite %s — this repository already carries it. "+
				"init writes a repository's first routing, never its second: edit the file by hand\n", w.name)
			return exitGeneric
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, ".tick"), 0o755); err != nil {
		fmt.Fprintf(stderr, "ticfac init: %v\n", err)
		return exitGeneric
	}
	for _, w := range writes {
		if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(w.name)), []byte(w.body), 0o644); err != nil {
			fmt.Fprintf(stderr, "ticfac init: %v\n", err)
			return exitGeneric
		}
		// The per-file line is prose: under --json it goes to stderr, so the
		// one document is stdout's only content.
		fmt.Fprintf(promptOut, "wrote %s — %s\n", w.name, w.note)
	}
	if *fl.asJSON {
		if err := emitInitJSON(answers, gates, writes, stdout); err != nil {
			fmt.Fprintf(stderr, "ticfac init: %v\n", err)
			return exitGeneric
		}
		return exitSuccess
	}
	fmt.Fprintf(stdout, "testing gate: %s\n", gateLine(gates))
	fmt.Fprintf(stdout, "%s is ready to run an epic — `ticfac doctor` says what this machine still needs\n", repo)
	return exitSuccess
}

// pluralFiles renders one file name or a pair for a refusal that names what
// it refused to touch.
func pluralFiles(names []string) string {
	switch len(names) {
	case 1:
		return "it"
	default:
		return "them"
	}
}

// initTargets is what init would write into repo, before any answer is known
// well enough to narrow it: the two files every answer produces, plus the
// cloud file when the substrate flag already names the cloud. An answer the
// QUESTIONS settle can add the cloud file; that half of the overwrite check
// re-runs in runInit after the answers resolve, on the exact set.
func initTargets(repo, substrateFlag string) []string {
	names := []string{runconfig.FileName, initConfigMDName}
	if substrateFlag == initSubstrateCloud || substrateFlag == initSubstrateBoth {
		names = append(names, initCloudName)
	}
	return names
}

// initAnswers is the resolved input half: what the flags said, what stdin
// answered, what the defaults supply.
type initAnswers struct {
	substrate string
	runner    string
	model     string
	gate      string
	closeout  string
}

// resolveInitAnswers asks the questions the flags left blank, and refuses the
// answers that name no real choice. The one question the repository answers
// for itself — the testing gate — is asked after these, when its guess can be
// shown as the default.
func resolveInitAnswers(fl *initFlags, stdin io.Reader, stdout, stderr io.Writer, repo string) (initAnswers, int) {
	ask := func() func(prompt, def string) (string, error) {
		reader := bufio.NewReader(stdin)
		return func(prompt, def string) (string, error) {
			fmt.Fprintf(stdout, "%s [%s] ", prompt, def)
			line, err := reader.ReadString('\n')
			if err != nil && line == "" {
				fmt.Fprintln(stdout)
				return "", fmt.Errorf("no answer on stdin — pass the flags, or --yes to take every default")
			}
			answer := strings.TrimSpace(line)
			if answer == "" {
				return def, nil
			}
			return answer, nil
		}
	}()
	questions := ask
	if *fl.yes {
		questions = func(prompt, def string) (string, error) { return def, nil }
	}

	answers := initAnswers{
		substrate: *fl.substrate,
		runner:    *fl.runner,
		model:     *fl.model,
		gate:      *fl.gate,
		closeout:  *fl.closeout,
	}

	if answers.substrate == "" {
		answer, err := questions("where do runs of this repository execute: local, cloud or both?", initSubstrateLocal)
		if err != nil {
			fmt.Fprintf(stderr, "ticfac init: %v\n", err)
			return answers, exitUsage
		}
		answers.substrate = answer
	}
	switch answers.substrate {
	case initSubstrateLocal, initSubstrateCloud, initSubstrateBoth:
	default:
		fmt.Fprintf(stderr, "ticfac init: %q is not a substrate init can route for — local, cloud or both\n",
			answers.substrate)
		return answers, exitUsage
	}

	// The cloud question is not the harness question: what runs in Cloudflare
	// runs Workers AI models through the factory's gateway, so the cloud's
	// harness is pi whatever the local one is, and asking "claude or pi" of a
	// cloud-only repository would be asking for an answer the run refuses.
	cloudOnly := answers.substrate == initSubstrateCloud
	if !cloudOnly && answers.runner == "" {
		answer, err := questions("which harness dispatches local work: claude or pi?", initRunnerClaude)
		if err != nil {
			fmt.Fprintf(stderr, "ticfac init: %v\n", err)
			return answers, exitUsage
		}
		answers.runner = answer
	}
	if cloudOnly {
		answers.runner = initRunnerPi
	}
	switch answers.runner {
	case initRunnerClaude, initRunnerPi:
	default:
		fmt.Fprintf(stderr, "ticfac init: %q is not a harness init routes for — claude or pi\n", answers.runner)
		return answers, exitUsage
	}

	if answers.model == "" {
		def := initDefaultClaudeModel
		if answers.runner == initRunnerPi {
			def = initDefaultPiModel
		}
		answer, err := questions(fmt.Sprintf("which model does %s run?", answers.runner), def)
		if err != nil {
			fmt.Fprintf(stderr, "ticfac init: %v\n", err)
			return answers, exitUsage
		}
		answers.model = answer
	}
	// The cloud rule, at the boundary init writes it. It applies to a
	// CLOUD-ONLY answer, whose one model is the cloud's; a "both" answer's
	// model is the LOCAL half's, and the cloud's is derived below (the
	// default Workers AI pairing when the local runner is not pi) — refusing
	// the local model there would be refusing an answer nobody gave. A
	// cloud-only routing whose model is not a Workers AI model is one the
	// run refuses at construction, three ticks into an epic (tick 78v keys
	// the check on the final resolved value) — so init refuses it here,
	// naming the rule and the fix.
	if cloudOnly && !profile.IsWorkersAIModel(answers.model) {
		fmt.Fprintf(stderr, "ticfac init: %q is not a Workers AI model, and what runs in Cloudflare runs "+
			"Workers AI models only, through the factory's gateway (provider namespaces %s) — name a model "+
			"from one of them, or keep the cloud out of the substrate\n",
			answers.model, strings.Join(profile.CloudRule.ModelNamespaces, ", "))
		return answers, exitUsage
	}

	// The close-out answer (tick 6vp): the one question besides the gate
	// the repository can guess for itself, from its origin — a remote that
	// names a GitHub repository is one whose epics can integrate through a
	// PR, and no origin is one whose runs must integrate locally, because
	// the rule written anyway is a rule the run refuses to start on. A flag
	// carries the answer past the guess; the guess is the question's default.
	if answers.closeout == "" {
		answer, err := questions(fmt.Sprintf(
			"does the close-out of an epic here hold on a pull request with CI on GitHub: %s or %s?",
			initCloseoutPR, initCloseoutNone), initCloseoutDefault(repo))
		if err != nil {
			fmt.Fprintf(stderr, "ticfac init: %v\n", err)
			return answers, exitUsage
		}
		answers.closeout = answer
	}
	switch answers.closeout {
	case initCloseoutPR, initCloseoutNone:
	default:
		fmt.Fprintf(stderr, "ticfac init: %q is not a close-out init can write — %s (the close-out "+
			"holds on a GitHub pull request with CI) or %s (it holds on nothing, and a run "+
			"integrates locally)\n",
			answers.closeout, initCloseoutPR, initCloseoutNone)
		return answers, exitUsage
	}
	return answers, exitSuccess
}

// initCloseoutDefault guesses the close-out answer from the repository's
// own origin (tick 6vp): a remote forge.ParseRepo resolves — the SAME
// reader the run's own surface resolves the remote through (tick vo4's
// discipline, so init's guess, doctor's check and the run's demand cannot
// disagree) — is a repository whose epics can integrate through a PR; no
// origin, or one that does not resolve, is a repository whose runs must
// integrate locally.
func initCloseoutDefault(repo string) string {
	url, err := exec.Command("git", "-C", repo, "remote", "get-url", "origin").Output()
	if err != nil {
		return initCloseoutNone
	}
	if _, err := forge.ParseRepo(string(url)); err != nil {
		return initCloseoutNone
	}
	return initCloseoutPR
}

// initGateCommands is the testing gate: the answer's own command when it named
// one, else what the repository's tree guesses. Nil means no gate at all —
// the caller refuses, because a repository that declares no gate is one a run
// refuses to close a tick in.
func initGateCommands(named string, repo string) []guessedGate {
	if named != "" {
		return []guessedGate{{
			ID:          "test",
			Command:     named,
			Description: "the repository's tests (named with --gate)",
		}}
	}
	return guessGate(repo)
}

// guessedGate is one [testing.commands] entry init would write.
type guessedGate struct {
	ID          string
	Command     string
	Description string
}

// makefileTestTarget matches a make test target: `test:` at the start of a
// recipe line (a `.PHONY: test` or a variable named test* does not count).
var makefileTestTarget = regexp.MustCompile(`(?m)^test:`)

// guessGate reads the repository's tree for the command that runs its tests.
// The order is the description's own — go test, pnpm test, make test — and a
// repository carrying two halves (this one: Go and TypeScript) gets both,
// because the gate is every check a tick's commit must pass, not a choice
// between them.
func guessGate(repo string) []guessedGate {
	var gates []guessedGate
	if initFileExists(repo, "go.mod") {
		gates = append(gates, guessedGate{
			ID: "go", Command: "go test ./...",
			Description: "Go tests, as `go test ./...` runs them (guessed from go.mod)",
		})
	}
	if hasNodeTestScript(repo) {
		gates = append(gates, guessedGate{
			ID: "node", Command: "pnpm test",
			Description: "Node tests through the package.json test script (guessed; pnpm only, never npm or yarn)",
		})
	}
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		raw, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			continue
		}
		if makefileTestTarget.Match(raw) {
			gates = append(gates, guessedGate{
				ID: "make", Command: "make test",
				Description: "make's test target (guessed from " + name + ")",
			})
			break
		}
	}
	return gates
}

// hasNodeTestScript reports whether the repository's package.json carries a
// test script `pnpm test` could run. A package.json without one is not a
// guess — `pnpm test` against no script fails, and a gate that always fails
// is worse than a gate init refused to invent.
func hasNodeTestScript(repo string) bool {
	raw, err := os.ReadFile(filepath.Join(repo, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return false
	}
	return strings.TrimSpace(pkg.Scripts["test"]) != ""
}

func initFileExists(repo, name string) bool {
	_, err := os.Stat(filepath.Join(repo, name))
	return err == nil
}

// initWrite is one file init writes, with the one-line note its output says.
type initWrite struct {
	name string
	body string
	note string
}

// initWrites builds every file the answers call for. The cloud model is the
// answer's own when it is a Workers AI model and the local runner is pi —
// one model, both worlds — else the default Workers AI pairing: a repository
// whose local work runs claude has no local model to lend the cloud, and the
// cloud's is not a default inherited by accident (the pi default is this
// repository's own verified pairing, stated as such).
func initWrites(repo string, answers initAnswers, gates []guessedGate) []initWrite {
	substrateDeclared := "auto"
	if answers.substrate == initSubstrateCloud {
		substrateDeclared = "cloud"
	}
	cloudModel := answers.model
	if answers.runner != initRunnerPi {
		cloudModel = initDefaultPiModel
	}

	writes := []initWrite{{
		name: runconfig.FileName,
		body: runnersTOML(substrateDeclared, answers.runner, answers.model, gates),
		note: fmt.Sprintf("every dispatch resolves against it; runs %s on %s", answers.runner, answers.model),
	}}
	if answers.substrate != initSubstrateLocal {
		writes = append(writes, initWrite{
			name: initCloudName,
			body: cloudTOML(cloudModel),
			note: "cloud role cells: what runs in Cloudflare runs Workers AI models only",
		})
	}
	writes = append(writes, initWrite{
		name: initConfigMDName,
		body: initConfigMD(answers.closeout == initCloseoutPR),
		note: initConfigNote(answers.closeout == initCloseoutPR),
	})
	return writes
}

// initConfigNote is the one-line note beside the config write: the rule when
// the answer named it, its absence when it did not.
func initConfigNote(closeout bool) string {
	if closeout {
		return "the PR + CI close-out rule the close-out holds on"
	}
	return "no close-out rule declared — a run integrates this repository locally"
}

// runnersTOML is the base routing. The shape is the one this repository's own
// .tick/runners.toml settled (which the vendored contract documents), with
// only the parts init can answer: substrate, the three role cells' kind and
// model, and the gate.
func runnersTOML(substrate, kind, model string, gates []guessedGate) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# Worker routing for epic runs, written by `+"`ticfac init`"+`.
# Schema and semantics: the ticks skill's references/runners-config.md.
version = 2

[orchestration]
`)
	switch substrate {
	case "cloud":
		b.WriteString("# Every worker boots in a Cloudflare container through the factory; the\n" +
			"# role cells below are the cloud's, and .tick/runners.cloud.toml declares them.\n")
	default:
		b.WriteString("# Runs on this machine: herdr when a server answers, a plain harness otherwise.\n")
	}
	fmt.Fprintf(&b, "substrate = %q\n", substrate)
	b.WriteString("max_parallel = 4\n\n")

	for _, role := range []struct{ name, what string }{
		{"implement", "the workers that implement ticks"},
		{"review", "the epic review"},
		{"closeout", "the close-out"},
	} {
		fmt.Fprintf(&b, "[roles.%s]\n# %s.\n", role.name, role.what)
		fmt.Fprintf(&b, "kind = %q\n", kind)
		fmt.Fprintf(&b, "model = %q\n", model)
		b.WriteString("effort = \"high\"\n")
		if kind == initRunnerPi {
			// pi needs no permission-bypass flag (verified live in this
			// repository's own routing, tick gjk): `--approve` covers
			// project-local file trust, and that is the whole of its
			// full-auto story.
			b.WriteString("args = [\"--approve\"]\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("[testing.commands]\n")
	b.WriteString("# The integrated gate: what every tick's commit must pass before it is\n" +
		"# merged. Guessed from this repository's tree by `ticfac init` — edit the\n" +
		"# commands, keep the shape.\n")
	for _, gate := range gates {
		fmt.Fprintf(&b, "%s = { command = %q, description = %q }\n", gate.ID, gate.Command, gate.Description)
	}
	return b.String()
}

// cloudTOML declares the cloud substrate's role cells. Every role the run
// dispatches must be declared here — under the cloud substrate a role this
// file does not declare is a refusal naming the role, never a fall back to
// the common cell — so init writes all three.
func cloudTOML(model string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# Cloud substrate overrides, written by `+"`ticfac init`"+`: merged over
# .tick/runners.toml for cloud runs. What runs in Cloudflare runs Workers AI
# models only, through the factory's Workers AI gateway — every cell below
# names one, and a role this file does not declare is refused at run start.
version = 2

`)
	for _, role := range []string{"implement", "review", "closeout"} {
		fmt.Fprintf(&b, "[roles.%s]\nkind = %q\nmodel = %q\neffort = \"high\"\nargs = [\"--approve\"]\n\n",
			role, initRunnerPi, model)
	}
	return b.String()
}

// initConfigMD is the repository config init writes. When the answer names
// the rule, the Rules section's first bullet is the PR + CI close-out rule,
// written with the anchor phrase (`PR + CI gate`) the close-out's reader
// recognises — the rule's own stable vocabulary, present verbatim, because
// a paraphrase the reader fuzzy-matched would fail open on every repo that
// reworded it. When it does not (tick 6vp), the config declares NO rule —
// and the anchor phrase must not appear in that variant at all, not even as
// an instruction for adding it, because the reader anchors on the phrase
// and nothing else — so the section says the rule is absent, neutrally, and
// a run reads a repository that integrates locally and needs no forge.
func initConfigMD(closeout bool) string {
	if !closeout {
		return `# Tick Run Configuration

## Rules

- No close-out rule is declared: a run integrates this repository locally
  and needs no GitHub credential. To make the close-out of an epic hold on
  a GitHub pull request with CI instead, give this repository a GitHub
  origin and state that rule here in the repository's own words.

## Standing orders

This section is the repository's own practice, as a run's workers read it.
Edit it as the work settles: library choice within the stack, naming,
internal API shape, file layout, test strategy, wave partitioning, and
discovered bugs — create a tick rather than fixing beside the point.
`
	}
	return `# Tick Run Configuration

## Rules

- Epic integration goes through a PR + CI gate: the orchestrator pushes the
  epic branch and opens a PR, and the epic close-out may not complete until
  CI is green on that PR. No direct merges of epic branches to the default
  branch — the merge itself is a person's, and deliberately so.

## Standing orders

This section is the repository's own practice, as a run's workers read it.
Edit it as the work settles: library choice within the stack, naming,
internal API shape, file layout, test strategy, wave partitioning, and
discovered bugs — create a tick rather than fixing beside the point.
`
}

// gateLine is the one sentence the output says about the gate it wrote: the
// commands, in the order the gate runs them (serially, one verdict at a
// time).
func gateLine(gates []guessedGate) string {
	parts := make([]string, 0, len(gates))
	for _, gate := range gates {
		parts = append(parts, gate.Command)
	}
	return strings.Join(parts, ", ")
}

// initGateJSON is one [testing.commands] entry the --json document carries.
type initGateJSON struct {
	ID          string `json:"id"`
	Command     string `json:"command"`
	Description string `json:"description"`
}

// initFileJSON is one file the --json document says was written, with the
// one-line note the prose surface says beside it.
type initFileJSON struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

// initJSON is `init --json`'s answer, ticfac.init.v1: the resolved answers —
// the flags, the prompts and the defaults folded into one place to read what
// the repository was configured with — and the files written.
type initJSON struct {
	agentDoc
	Substrate string         `json:"substrate"`
	Runner    string         `json:"runner"`
	Model     string         `json:"model"`
	Closeout  string         `json:"closeout"`
	Gate      []initGateJSON `json:"gate"`
	Files     []initFileJSON `json:"files"`
}

// emitInitJSON prints the one document. The answers are the resolved ones,
// so a caller reading the document knows exactly what the repository was
// left configured with — not what was asked.
func emitInitJSON(answers initAnswers, gates []guessedGate, writes []initWrite, stdout io.Writer) error {
	doc := initJSON{
		agentDoc:  agentDoc{Schema: agentSchemaID("init"), State: agentStateDone},
		Substrate: answers.substrate,
		Runner:    answers.runner,
		Model:     answers.model,
		Closeout:  answers.closeout,
		Gate:      make([]initGateJSON, 0, len(gates)),
		Files:     make([]initFileJSON, 0, len(writes)),
	}
	for _, gate := range gates {
		doc.Gate = append(doc.Gate, initGateJSON{ID: gate.ID, Command: gate.Command, Description: gate.Description})
	}
	for _, w := range writes {
		doc.Files = append(doc.Files, initFileJSON{Name: w.name, Note: w.note})
	}
	return emitAgentJSON(stdout, doc)
}
