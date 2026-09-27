package cli

// `ticfac init` (tick o0d): a repository is ready to run in one step.
//
// The acceptance is four claims, and each has its test here:
//
//   - "init on a fresh repo writes files that validate and run an epic end
//     to end": the written files are fed through EVERY production reader a
//     run constructs from — the validated runners reader, the gate reader,
//     the close-out-rule reader — and then through reconcile.New itself, the
//     production entry point's construction path, on a real git repository
//     with a fake tracker and a fake forge surface. Every refusal a run
//     would make at construction is proven absent; what construction cannot
//     prove (a live dispatch) is the epic's own A1, tick 9sz's run.
//   - "init refuses to overwrite": a repository that already carries a file
//     init would write keeps it, byte for byte.
//   - the gate guess, per repository shape: a Go repo, a Node repo, a
//     Makefile repo, and a repo with none of them (refused without --gate).
//   - the answers: flags, stdin questions, defaults, and the cloud rule
//     (Workers AI models only).
//
// short: file writes, in-memory answers and one `git init` per fixture; the
// probes are seams and the questions read from a buffer.

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// initFixture is a repository fresh enough for init: a real git repository
// (reconcile.New checks for one) with the files a repository shape names.
func initFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	// The fixture owns its copy: a nil map from a caller with no files still
	// needs the README below, and an empty tree cannot commit.
	owned := make(map[string]string, len(files)+1)
	for name, body := range files {
		owned[name] = body
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "--quiet", "-b", "main")
	git("config", "user.email", "init@example.com")
	git("config", "user.name", "init test")
	owned["README.md"] = "a fixture repository\n"
	for name, body := range owned {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "--quiet", "-m", "fixture")
	return repo
}

// runInitOn is `init` against one repository: the flags parsed the way the
// cobra command parses them, the questions answered from stdin.
func runInitOn(t *testing.T, repo, stdin string, args ...string) (int, string, string) {
	t.Helper()
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fl := defineInitFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	*fl.repo = repo
	var stdout, stderr bytes.Buffer
	code := runInit(fl, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// initRunnersPath is the written runners.toml, for the readers below.
func initRunnersPath(repo string) string {
	return filepath.Join(repo, filepath.FromSlash(runconfig.FileName))
}

// TestInitOnAFreshGoRepositoryWritesFilesARunConstructsFrom is the
// acceptance's first clause: a Go repository, init's defaults, and every
// production reader a run constructs from reading the result without a
// refusal — ending in reconcile.New itself, the production entry point's
// construction path.
func TestInitOnAFreshGoRepositoryWritesFilesARunConstructsFrom(t *testing.T) {
	repo := initFixture(t, map[string]string{"go.mod": "module example.com/fresh\n\ngo 1.24\n"})
	code, stdout, stderr := runInitOn(t, repo, "", "--yes")
	if code != exitSuccess {
		t.Fatalf("init exits %d: %s%s", code, stderr, stdout)
	}
	for _, name := range []string{runconfig.FileName, initConfigMDName} {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}
	// A local answer writes no cloud file: the cloud is not a default
	// anybody opted into.
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(initCloudName))); err == nil {
		t.Errorf("%s was written for a local answer", initCloudName)
	}

	// The validated reader: the whole file, shape and cross-references.
	if _, err := runconfig.LoadRepo(repo); err != nil {
		t.Fatalf("the written runners.toml does not validate: %v", err)
	}
	// The gate reader: one command, the guess from go.mod.
	gates, err := reconcile.ReadGateCommands(initRunnersPath(repo))
	if err != nil {
		t.Fatalf("the written gate does not read: %v", err)
	}
	if len(gates) != 1 || gates[0].Command != "go test ./..." {
		t.Fatalf("the gate is %v, want go test ./... guessed from go.mod", gates)
	}
	// The close-out rule reader: the rule declared, with its anchor phrase.
	rule, err := reconcile.ReadCloseoutRule(filepath.Join(repo, filepath.FromSlash(initConfigMDName)))
	if err != nil {
		t.Fatalf("the written config.md does not read: %v", err)
	}
	if !rule.Declared {
		t.Fatal("the written config.md does not declare the PR + CI close-out rule")
	}
	// And the production entry point's construction path, end to end over
	// the three: profiles for every role, the substrate decision, the gate,
	// the rule, and the forge surface the rule demands.
	if _, err := reconcile.New(reconcile.Options{
		Repo: repo, EpicID: "e1", Tracker: &initFakeTracker{},
		NewExecutor: executorFactory("claude", initRunnersPath(repo)),
		Executors:   knownExecutors(), PullRequests: forge.GitHub{Token: "fixture", Repo: "example/name"},
	}); err != nil {
		t.Fatalf("reconcile.New refuses the initialised repository: %v", err)
	}
}

// initFakeTracker answers the four questions the reconciler asks a tracker.
// Construction asks none of them; a run asks all four, and the fake is here
// rather than inlined so each test that constructs a run reads the same one.
type initFakeTracker struct{}

func (f *initFakeTracker) Graph(context.Context, string) (tk.Graph, error) { return tk.Graph{}, nil }
func (f *initFakeTracker) Show(context.Context, string) (tk.Tick, error)   { return tk.Tick{}, nil }
func (f *initFakeTracker) Claim(context.Context, string, string) (tk.Tick, error) {
	return tk.Tick{}, nil
}
func (f *initFakeTracker) Note(context.Context, string, string) (tk.Tick, error) {
	return tk.Tick{}, nil
}
func (f *initFakeTracker) Close(context.Context, string) (tk.Tick, error) { return tk.Tick{}, nil }

// TestInitRefusesToOverwrite is the acceptance's second clause: a repository
// that already carries a file init would write keeps it, byte for byte, and
// the refusal names the file.
func TestInitRefusesToOverwrite(t *testing.T) {
	repo := initFixture(t, map[string]string{"go.mod": "module example.com/fresh\n"})
	const existing = "# routing somebody chose\nversion = 2\n"
	tick := filepath.Join(repo, ".tick")
	if err := os.MkdirAll(tick, 0o755); err != nil {
		t.Fatal(err)
	}
	runners := filepath.Join(tick, "runners.toml")
	if err := os.WriteFile(runners, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runInitOn(t, repo, "", "--yes")
	if code != exitGeneric {
		t.Fatalf("init over an existing runners.toml exits %d, want %d", code, exitGeneric)
	}
	if !strings.Contains(stderr, "refusing to overwrite .tick/runners.toml") {
		t.Errorf("the refusal does not name the file:\n%s", stderr)
	}
	if got, _ := os.ReadFile(runners); string(got) != existing {
		t.Errorf("the existing runners.toml was touched:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(tick, "config.md")); err == nil {
		t.Errorf("init wrote config.md beside a refusal — a refusal must write nothing")
	}
}

// TestInitGuessesTheGateFromTheRepositoriesShape is the acceptance's third
// clause read per shape: a Go repo, a Node repo, a Makefile repo, a repo
// with both halves, and a repo with none of them — which is not a guess but
// a refusal asking for --gate, because a repository that declares no gate
// is one a run refuses to close a tick in.
func TestInitGuessesTheGateFromTheRepositoriesShape(t *testing.T) {
	t.Run("node", func(t *testing.T) {
		repo := initFixture(t, map[string]string{
			"package.json": `{"name":"fresh","scripts":{"test":"vitest run"}}`})
		code, _, stderr := runInitOn(t, repo, "", "--yes")
		if code != exitSuccess {
			t.Fatalf("init exits %d: %s", code, stderr)
		}
		gates, err := reconcile.ReadGateCommands(initRunnersPath(repo))
		if err != nil {
			t.Fatal(err)
		}
		if len(gates) != 1 || gates[0].Command != "pnpm test" {
			t.Fatalf("the gate is %v, want pnpm test", gates)
		}
	})
	t.Run("node without a test script is not a guess", func(t *testing.T) {
		repo := initFixture(t, map[string]string{"package.json": `{"name":"fresh"}`})
		code, _, stderr := runInitOn(t, repo, "", "--yes")
		if code != exitUsage {
			t.Fatalf("init over a testless package.json exits %d, want the gate refusal (%s)", code, stderr)
		}
	})
	t.Run("makefile", func(t *testing.T) {
		repo := initFixture(t, map[string]string{"Makefile": "test:\n\tgo test ./...\n"})
		code, _, stderr := runInitOn(t, repo, "", "--yes")
		if code != exitSuccess {
			t.Fatalf("init exits %d: %s", code, stderr)
		}
		gates, err := reconcile.ReadGateCommands(initRunnersPath(repo))
		if err != nil {
			t.Fatal(err)
		}
		if len(gates) != 1 || gates[0].Command != "make test" {
			t.Fatalf("the gate is %v, want make test", gates)
		}
	})
	t.Run("go and node carry both halves", func(t *testing.T) {
		repo := initFixture(t, map[string]string{
			"go.mod":       "module example.com/fresh\n",
			"package.json": `{"scripts":{"test":"vitest run"}}`,
		})
		code, _, stderr := runInitOn(t, repo, "", "--yes")
		if code != exitSuccess {
			t.Fatalf("init exits %d: %s", code, stderr)
		}
		gates, err := reconcile.ReadGateCommands(initRunnersPath(repo))
		if err != nil {
			t.Fatal(err)
		}
		if len(gates) != 2 {
			t.Fatalf("the gate is %v, want both halves", gates)
		}
	})
	t.Run("neither, with --gate", func(t *testing.T) {
		repo := initFixture(t, nil)
		code, _, stderr := runInitOn(t, repo, "", "--yes", "--gate", "pytest -q")
		if code != exitSuccess {
			t.Fatalf("init with --gate exits %d: %s", code, stderr)
		}
		gates, err := reconcile.ReadGateCommands(initRunnersPath(repo))
		if err != nil {
			t.Fatal(err)
		}
		if len(gates) != 1 || gates[0].Command != "pytest -q" {
			t.Fatalf("the gate is %v, want the named pytest -q", gates)
		}
	})
}

// TestInitAnswersTheQuestionsOnStdin pins the prompt half: the questions a
// flag did not answer are asked, an empty line takes the default, and the
// substrate answer decides which files exist — including the cloud file a
// question (not a flag) added to the set, which the overwrite check must
// still see.
func TestInitAnswersTheQuestionsOnStdin(t *testing.T) {
	repo := initFixture(t, map[string]string{"go.mod": "module example.com/fresh\n"})
	// substrate: both; runner: the default (claude); model: the default
	// (sonnet) — empty lines, one per question.
	code, stdout, _ := runInitOn(t, repo, "both\n\n\n")
	if code != exitSuccess {
		t.Fatalf("init exits %d: %s", code, stdout)
	}
	for _, prompt := range []string{
		"where do runs of this repository execute",
		"which harness dispatches local work",
		"which model does claude run",
	} {
		if !strings.Contains(stdout, prompt) {
			t.Errorf("the question %q was never asked:\n%s", prompt, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(initCloudName))); err != nil {
		t.Fatalf("a both answer wrote no %s: %v", initCloudName, err)
	}
	// The cloud cells: pi on a Workers AI model, whatever the local harness.
	cloud, err := runconfig.LoadFor(initRunnersPath(repo), runconfig.SubstrateCloud)
	if err != nil {
		t.Fatalf("the written cloud file does not read: %v", err)
	}
	for _, role := range []string{"implement", "review", "closeout"} {
		cell := cloud.Roles[role]
		if cell == nil {
			t.Fatalf("the cloud file declares no [%s] cell — a cloud run refuses the role", role)
		}
		if cell.Kind != "pi" {
			t.Errorf("the cloud [%s] cell runs %q, want pi", role, cell.Kind)
		}
	}
	// And the same answers construct a run under the cloud substrate:
	// every role's cloud routing resolves, through reconcile.New.
	if _, err := reconcile.New(reconcile.Options{
		Repo: repo, EpicID: "e1", Tracker: &initFakeTracker{},
		NewExecutor: executorFactory("claude", initRunnersPath(repo)),
		Executors:   knownExecutors(), PullRequests: forge.GitHub{Token: "fixture", Repo: "example/name"},
	}); err != nil {
		t.Fatalf("reconcile.New refuses the both answer's repository: %v", err)
	}
}

// TestInitRefusesACloudModelThatIsNotWorkersAI pins the cloud rule at the
// boundary init writes it: what runs in Cloudflare runs Workers AI models
// only, and a routing that named anything else is one a run would refuse
// three ticks into an epic — init refuses it before a word is written.
func TestInitRefusesACloudModelThatIsNotWorkersAI(t *testing.T) {
	repo := initFixture(t, map[string]string{"go.mod": "module example.com/fresh\n"})
	code, _, stderr := runInitOn(t, repo, "", "--yes", "--substrate", "cloud", "--model", "gpt-9-max")
	if code != exitUsage {
		t.Fatalf("init on a non-Workers-AI cloud model exits %d, want the usage refusal", code)
	}
	if !strings.Contains(stderr, "Workers AI models only") {
		t.Errorf("the refusal does not name the rule:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(runconfig.FileName))); err == nil {
		t.Errorf("the refusal still wrote a routing — a refusal must write nothing")
	}
}

// TestInitOnACloudRepositoryWritesTheCloudsOwnRouting: a cloud-only answer
// skips the harness question (the cloud's harness is pi, not a choice), and
// writes the cloud's routing into the base file beside the cloud file that
// declares it.
func TestInitOnACloudRepositoryWritesTheCloudsOwnRouting(t *testing.T) {
	repo := initFixture(t, map[string]string{"go.mod": "module example.com/fresh\n"})
	code, stdout, _ := runInitOn(t, repo, "cloud\n\n")
	if code != exitSuccess {
		t.Fatalf("init exits %d: %s", code, stdout)
	}
	if strings.Contains(stdout, "which harness") {
		t.Errorf("a cloud-only answer was asked for a harness — the cloud's is pi, not a choice:\n%s", stdout)
	}
	cfg, err := runconfig.LoadRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Substrate() != runconfig.SubstrateCloud {
		t.Errorf("the base file declares substrate %q, want cloud", cfg.Substrate())
	}
	if _, err := reconcile.New(reconcile.Options{
		Repo: repo, EpicID: "e1", Tracker: &initFakeTracker{},
		NewExecutor: executorFactory("claude", initRunnersPath(repo)),
		Executors:   knownExecutors(), PullRequests: forge.GitHub{Token: "fixture", Repo: "example/name"},
	}); err != nil {
		t.Fatalf("reconcile.New refuses the cloud answer's repository: %v", err)
	}
}

// TestInitRefusesAValueThatNamesNoChoice: the answers are a closed
// vocabulary, and a typo is a refusal naming the choices, not a guess.
func TestInitRefusesAValueThatNamesNoChoice(t *testing.T) {
	repo := initFixture(t, map[string]string{"go.mod": "module example.com/fresh\n"})
	if code, _, stderr := runInitOn(t, repo, "", "--yes", "--substrate", "lunar"); code != exitUsage {
		t.Fatalf("substrate=lunar exits %d, want the usage refusal: %s", code, stderr)
	}
	if code, _, stderr := runInitOn(t, repo, "", "--yes", "--runner", "HAL 9000"); code != exitUsage {
		t.Fatalf("runner=HAL exits %d, want the usage refusal: %s", code, stderr)
	}
}

// TestInitWithNoAnswerOnStdinRefuses: a prompt fed nothing refuses — an
// empty stdin is not "take the defaults", because a script that wired init
// into a pipe must learn it answered nothing, not receive a routing nobody
// confirmed.
func TestInitWithNoAnswerOnStdinRefuses(t *testing.T) {
	repo := initFixture(t, map[string]string{"go.mod": "module example.com/fresh\n"})
	code, _, stderr := runInitOn(t, repo, "")
	if code != exitUsage {
		t.Fatalf("init on an empty stdin exits %d, want the usage refusal", code)
	}
	if !strings.Contains(stderr, "no answer on stdin") {
		t.Errorf("the refusal does not say what was missing:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(runconfig.FileName))); err == nil {
		t.Errorf("the refusal still wrote a routing — a refusal must write nothing")
	}
}
