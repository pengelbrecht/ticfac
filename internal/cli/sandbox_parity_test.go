package cli

// The ported verbs held to the implementation they were ported FROM (tick
// 46x, acceptance A2).
//
// `ticfac sandbox *` and `ticfac cloud branch` replace `tk sandbox *` and
// `tk cloud branch`, which ticks deleted when it became tracker-only (ticks
// epic chz, commit 7b6c0b2f). The image's scripts parse what these verbs
// print, so a port that is "the same logic" is not enough: it has to print
// the same bytes. testdata/sandbox-parity.json holds what tk itself printed
// for every case below — recorded by running a tk built from 7b6c0b2f^ (the
// last ticks commit that had the verbs) on the same fixtures — and this test
// runs ticfac on those fixtures and compares.
//
// What is compared, per step:
//
//   - the exit code, exactly (tk's 0/1/2/3/4 are ticfac's too);
//   - stdout, exactly — it is the machine contract a boot script reads;
//   - on success, stderr exactly — the notes a boot log carries;
//   - on failure, the error's own message: the first stderr line with each
//     tool's prefix removed ("Error: " for tk, "ticfac <verb>: " for ticfac).
//     tk followed it with cobra's usage block, which is presentation;
//   - for `cloud branch`, the request the factory received as well.
//
// Every place ticfac deliberately differs is in parityRenames, named, so a
// difference is a decision someone wrote down rather than one this test
// learned to tolerate.
//
// To re-record (only ever against the ticks commit named above):
//
//	go build -o /tmp/tk-chz-parent github.com/pengelbrecht/ticks/cmd/tk  # at 7b6c0b2f^
//	go test ./internal/cli -run TestSandboxVerbsMatchTheTicksImplementation \
//	    -sandbox-parity-record=/tmp/tk-chz-parent

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

var sandboxParityRecord = flag.String("sandbox-parity-record", "",
	"path to a tk built from ticks 7b6c0b2f^; re-records testdata/sandbox-parity.json from it")

const sandboxParityGoldens = "testdata/sandbox-parity.json"

// parityRenames are the deliberate differences between tk's output and
// ticfac's, applied to tk's recorded output before it is compared.
var parityRenames = []struct {
	from *regexp.Regexp
	to   string
	why  string
}{
	{
		from: regexp.MustCompile(regexp.QuoteMeta("(tk update --status in_progress, tk herd spawn)")),
		to:   "(tk update --status in_progress, ticfac herd spawn)",
		why:  "the herd spawner is ticfac's since ticks became tracker-only; the tracker claim stays tk's",
	},
	{
		from: regexp.MustCompile(`herdclient: dial (\S+): dial unix`),
		to:   "herd/client: herdr is not running at $1: dial unix",
		why: "the read-only herdr probe is ticfac's own client (internal/herd/client), and the " +
			"dial error it wraps is worded by that client; the probe, its verdict and the socket it names are the same",
	},
}

// parityStep is one invocation. `<ROOT>` in args is the case's checkout.
type parityStep struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env,omitempty"`
	// Factory, when set, is the answer a fake factory gives `cloud branch`.
	Factory *parityFactory `json:"factory,omitempty"`
}

type parityFactory struct {
	Status int            `json:"status"`
	Body   map[string]any `json:"body"`
}

// parityCase is one fixture checkout and the steps run in it, in order —
// `sandbox setup` twice in one checkout is a case with two steps.
type parityCase struct {
	Name string `json:"name"`
	// Runners is .tick/runners.toml; empty writes none.
	Runners string `json:"runners,omitempty"`
	// Issues are .tick/issues/<id>.json records.
	Issues map[string]string `json:"issues,omitempty"`
	// Git makes the checkout a git repository (setup's stamp lives in it).
	Git   bool         `json:"git,omitempty"`
	Steps []parityStep `json:"steps"`
}

// parityOutcome is what one step printed and, for `cloud branch`, sent.
type parityOutcome struct {
	Exit    int            `json:"exit"`
	Stdout  string         `json:"stdout"`
	Stderr  string         `json:"stderr"`
	Request map[string]any `json:"request,omitempty"`
}

const parityRunners = `version = 2

[orchestrator]
harness = "claude"

[roles.implement]
kind = "claude"
model = "sonnet"
`

const parityRunnersFull = `version = 2

[orchestrator]
harness = "claude"
model = "orchestrator-model"

[orchestration]
substrate = "herdr"
socket = "/tmp/no-such-herdr-46x.sock"
max_parallel = 3

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.strong]
model = "opus"

[roles.review]
kind = "claude"
model = "haiku"

[sandbox]
image = "registry.example.com/acme/orchestrator:2.0.0"
toolchain = ["rust@1.90.0", "python@3.13"]
setup = [
  { command = "echo warmed", description = "warm the caches" },
  { command = "echo second" },
]

[environment.commands.go]
command = "echo go-ok"
description = "go is on PATH"

[environment.commands.a-first]
command = "echo first-ok"
`

const parityRunnersTiers = `version = 2

[orchestrator]
harness = "claude"

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.frontier]
model = "opus-frontier"
`

const parityRunnersFailing = `version = 2

[orchestrator]
harness = "claude"

[roles.implement]
kind = "claude"
model = "sonnet"

[sandbox]
setup = [
  { command = "echo before; exit 3", description = "a step that fails" },
]

[environment.commands.broken]
command = "echo nope >&2; exit 5"
description = "a check that fails"

[environment.commands.fine]
command = "true"
`

const parityRunnersCloud = `version = 2

[orchestrator]
harness = "claude"

[orchestration]
substrate = "cloud"
max_parallel = 4

[roles.implement]
kind = "claude"
model = "sonnet"
`

const parityRunnersAuto = `version = 2

[orchestrator]
harness = "claude"

[orchestration]
substrate = "auto"
socket = "/tmp/no-such-herdr-46x.sock"

[roles.implement]
kind = "claude"
model = "sonnet"
`

const parityRunnersNoModel = `version = 2

[orchestrator]
harness = "claude"

[roles.implement]
kind = "claude"
`

const parityEpic = `{
  "id": "gy1",
  "title": "Herd helper CLI",
  "description": "The epic a1w belongs to",
  "acceptance_criteria": "epic done",
  "priority": 2,
  "type": "epic",
  "owner": "test@example.com",
  "created_by": "test@example.com",
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z",
  "status": "open"
}`

const parityTick = `{
  "id": "a1w",
  "title": "Deliver the spawn command",
  "description": "deliver the spawn command\n\nwith a second paragraph",
  "acceptance_criteria": "go test green",
  "priority": 2,
  "type": "task",
  "owner": "test@example.com",
  "parent": "gy1",
  "created_by": "test@example.com",
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z",
  "status": "open"
}`

const parityOrphan = `{
  "id": "o9p",
  "title": "A tick with an epic this checkout lacks",
  "description": "",
  "priority": 2,
  "type": "task",
  "owner": "test@example.com",
  "parent": "zz9",
  "created_by": "test@example.com",
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z",
  "status": "open"
}`

func sandboxParityCases() []parityCase {
	step := func(args ...string) parityStep { return parityStep{Args: args} }
	withEnv := func(s parityStep, kv ...string) parityStep {
		s.Env = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			s.Env[kv[i]] = kv[i+1]
		}
		return s
	}
	issues := map[string]string{"gy1": parityEpic, "a1w": parityTick, "o9p": parityOrphan, "bad": "{not json"}
	branch := func(status int, body map[string]any, args ...string) parityStep {
		return parityStep{Args: append([]string{"cloud", "branch"}, args...), Factory: &parityFactory{Status: status, Body: body}}
	}

	return []parityCase{
		// image
		{Name: "image/base", Runners: parityRunners, Steps: []parityStep{
			step("sandbox", "image", "--root", "<ROOT>", "--tk-version", "0.32.0"),
			step("sandbox", "image", "--root", "<ROOT>", "--tk-version", "0.32.0", "--declared-only"),
		}},
		{Name: "image/no-config", Steps: []parityStep{
			step("sandbox", "image", "--root", "<ROOT>", "--tk-version", "dev"),
		}},
		{Name: "image/declared", Runners: parityRunnersFull, Steps: []parityStep{
			step("sandbox", "image", "--root", "<ROOT>", "--tk-version", "0.32.0"),
			step("sandbox", "image", "--root", "<ROOT>", "--tk-version", "0.32.0", "--declared-only"),
		}},
		{Name: "image/broken-config", Runners: "version = 2\n[sandbox]\nimage = 7\n", Steps: []parityStep{
			step("sandbox", "image", "--root", "<ROOT>", "--tk-version", "0.32.0"),
		}},

		// toolchain
		{Name: "toolchain/none", Runners: parityRunners, Steps: []parityStep{
			step("sandbox", "toolchain", "--root", "<ROOT>"),
		}},
		{Name: "toolchain/declared", Runners: parityRunnersFull, Steps: []parityStep{
			step("sandbox", "toolchain", "--root", "<ROOT>"),
		}},

		// model
		{Name: "model/orchestrator-table", Runners: parityRunnersFull, Steps: []parityStep{
			step("sandbox", "model", "--root", "<ROOT>"),
			step("sandbox", "model", "--root", "<ROOT>", "--role", "implement"),
			step("sandbox", "model", "--root", "<ROOT>", "--role", "implement", "--tier", "strong"),
			step("sandbox", "model", "--root", "<ROOT>", "--role", "review"),
			step("sandbox", "model", "--root", "<ROOT>", "--role", "plan"),
		}},
		{Name: "model/role-fallback", Runners: parityRunners, Steps: []parityStep{
			step("sandbox", "model", "--root", "<ROOT>"),
		}},
		{Name: "model/frontier-tier", Runners: parityRunnersTiers, Steps: []parityStep{
			step("sandbox", "model", "--root", "<ROOT>"),
			step("sandbox", "model", "--root", "<ROOT>", "--role", "implement", "--tier", "economy"),
		}},
		{Name: "model/nothing-routed", Runners: parityRunnersNoModel, Steps: []parityStep{
			step("sandbox", "model", "--root", "<ROOT>"),
			step("sandbox", "model", "--root", "<ROOT>", "--role", "implement"),
		}},
		{Name: "model/no-config", Steps: []parityStep{
			step("sandbox", "model", "--root", "<ROOT>"),
		}},
		{Name: "model/unknown-tier", Runners: parityRunners, Steps: []parityStep{
			step("sandbox", "model", "--root", "<ROOT>", "--role", "implement", "--tier", "supreme"),
		}},
		{Name: "model/broken-config", Runners: "version = 2\n[roles.implement\n", Steps: []parityStep{
			step("sandbox", "model", "--root", "<ROOT>"),
		}},

		// substrate
		{Name: "substrate/override", Runners: parityRunnersFull, Steps: []parityStep{
			withEnv(step("sandbox", "substrate", "--root", "<ROOT>"), runconfig.SubstrateEnvVar, "harness"),
			withEnv(step("sandbox", "substrate", "--root", "<ROOT>"), runconfig.SubstrateEnvVar, "cloud"),
			withEnv(step("sandbox", "substrate", "--root", "<ROOT>"), runconfig.SubstrateEnvVar, "herdr"),
			withEnv(step("sandbox", "substrate", "--root", "<ROOT>"), runconfig.SubstrateEnvVar, "subagents"),
		}},
		{Name: "substrate/unsatisfiable-pin", Runners: parityRunnersFull, Steps: []parityStep{
			step("sandbox", "substrate", "--root", "<ROOT>"),
		}},
		{Name: "substrate/declared-cloud", Runners: parityRunnersCloud, Steps: []parityStep{
			step("sandbox", "substrate", "--root", "<ROOT>"),
			withEnv(step("sandbox", "substrate", "--root", "<ROOT>"), runconfig.SubstrateEnvVar, "harness"),
		}},
		{Name: "substrate/auto", Runners: parityRunnersAuto, Steps: []parityStep{
			step("sandbox", "substrate", "--root", "<ROOT>"),
		}},

		// setup
		{Name: "setup/runs-then-skips", Runners: parityRunnersFull, Git: true, Steps: []parityStep{
			step("sandbox", "setup", "--root", "<ROOT>"),
			step("sandbox", "setup", "--root", "<ROOT>"),
			step("sandbox", "setup", "--root", "<ROOT>", "--force"),
		}},
		{Name: "setup/no-git-dir", Runners: parityRunnersFull, Steps: []parityStep{
			step("sandbox", "setup", "--root", "<ROOT>"),
		}},
		{Name: "setup/none-declared", Runners: parityRunners, Git: true, Steps: []parityStep{
			step("sandbox", "setup", "--root", "<ROOT>"),
		}},
		{Name: "setup/failing", Runners: parityRunnersFailing, Git: true, Steps: []parityStep{
			step("sandbox", "setup", "--root", "<ROOT>"),
		}},

		// environment
		{Name: "environment/green", Runners: parityRunnersFull, Steps: []parityStep{
			step("sandbox", "environment", "--root", "<ROOT>"),
		}},
		{Name: "environment/none", Runners: parityRunners, Steps: []parityStep{
			step("sandbox", "environment", "--root", "<ROOT>"),
		}},
		{Name: "environment/red", Runners: parityRunnersFailing, Steps: []parityStep{
			step("sandbox", "environment", "--root", "<ROOT>"),
		}},

		// worker-prompt
		{Name: "worker-prompt/rendered", Runners: parityRunners, Issues: issues, Steps: []parityStep{
			step("sandbox", "worker-prompt", "--root", "<ROOT>", "--tick", "a1w", "--base", "930f1cf4dbcac5505cce506cbf2a8412d8248b92"),
			step("sandbox", "worker-prompt", "--root", "<ROOT>", "--tick", "a1w", "--branch", "tick-run/custom"),
			step("sandbox", "worker-prompt", "--root", "<ROOT>", "--tick", "o9p"),
			step("sandbox", "worker-prompt", "--root", "<ROOT>", "--tick", "gy1"),
		}},
		{Name: "worker-prompt/refusals", Runners: parityRunners, Issues: issues, Steps: []parityStep{
			step("sandbox", "worker-prompt", "--root", "<ROOT>"),
			step("sandbox", "worker-prompt", "--root", "<ROOT>", "--tick", "zzz"),
			step("sandbox", "worker-prompt", "--root", "<ROOT>", "--tick", "bad"),
		}},

		// cloud branch
		{Name: "cloud-branch/answers", Steps: []parityStep{
			branch(http.StatusCreated, map[string]any{"recorded": true}, "tick-run/epic1", "--detail", "  run branch  "),
			branch(http.StatusOK, map[string]any{"recorded": false}, "tick-run/epic1"),
			branch(http.StatusBadRequest, map[string]any{"error": "branch_outside_epic", "detail": `tick/other/meo belongs to epic "other"`}, "tick/other/meo"),
			branch(http.StatusUnauthorized, map[string]any{}, "tick-run/epic1"),
			branch(http.StatusCreated, map[string]any{"recorded": true}, "  "),
		}},
		{Name: "cloud-branch/naked-container", Steps: []parityStep{
			step("cloud", "branch", "tick-run/epic1"),
		}},
	}
}

// parityEnvKeys are the variables a step may set; every one is cleared for a
// step that does not, so the host's environment cannot reach a case.
var parityEnvKeys = []string{runconfig.SubstrateEnvVar, runconfig.EnvVar, runconfig.SocketEnvVar, cloudEnvFactoryURL, cloudEnvFactoryToken}

func TestSandboxVerbsMatchTheTicksImplementation(t *testing.T) {
	cases := sandboxParityCases()

	if *sandboxParityRecord != "" {
		recordSandboxParity(t, cases, *sandboxParityRecord)
		return
	}

	raw, err := os.ReadFile(sandboxParityGoldens)
	if err != nil {
		t.Fatalf("the recorded tk outputs are what this port is held to: %v", err)
	}
	var goldens map[string][]parityOutcome
	if err := json.Unmarshal(raw, &goldens); err != nil {
		t.Fatal(err)
	}
	if len(goldens) != len(cases) {
		t.Errorf("%s records %d cases and this test defines %d — re-record, or remove the stale ones",
			sandboxParityGoldens, len(goldens), len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			want, ok := goldens[tc.Name]
			if !ok {
				t.Fatalf("no recorded tk output for %q — a case with nothing to compare against proves nothing", tc.Name)
			}
			if len(want) != len(tc.Steps) {
				t.Fatalf("recorded %d steps, the case has %d", len(want), len(tc.Steps))
			}
			root := parityCheckout(t, tc)
			for i, s := range tc.Steps {
				got := runParityStep(t, root, s, func(args []string) (int, string, string) {
					var out, errOut bytes.Buffer
					code := Run(args, &out, &errOut)
					return code, out.String(), errOut.String()
				})
				compareParity(t, i, s, applyParityRenames(want[i]), got)
			}
		})
	}
}

func recordSandboxParity(t *testing.T, cases []parityCase, tk string) {
	goldens := map[string][]parityOutcome{}
	for _, tc := range cases {
		root := parityCheckout(t, tc)
		for _, s := range tc.Steps {
			goldens[tc.Name] = append(goldens[tc.Name], runParityStep(t, root, s, func(args []string) (int, string, string) {
				cmd := exec.Command(tk, args...)
				cmd.Dir = root
				var out, errOut bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &errOut
				err := cmd.Run()
				code := 0
				if exitErr, ok := err.(*exec.ExitError); ok {
					code = exitErr.ExitCode()
				} else if err != nil {
					t.Fatalf("running the recording tk: %v", err)
				}
				return code, out.String(), errOut.String()
			}))
		}
	}
	body, err := json.MarshalIndent(goldens, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sandboxParityGoldens, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %d cases from %s into %s", len(goldens), tk, sandboxParityGoldens)
}

// parityCheckout writes one case's fixture. The path is resolved through
// symlinks so the `<ROOT>` substitution sees the same spelling both tools print.
func parityCheckout(t *testing.T, tc parityCase) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".tick", "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	if tc.Runners != "" {
		if err := os.WriteFile(filepath.Join(root, ".tick", "runners.toml"), []byte(tc.Runners), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for id, body := range tc.Issues {
		if err := os.WriteFile(filepath.Join(root, ".tick", "issues", id+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if tc.Git {
		runGitOK(t, root, "init", "-q")
	}
	return root
}

// runParityStep runs one step through run, with the step's environment and,
// for `cloud branch`, a fake factory that records what it was sent.
func runParityStep(t *testing.T, root string, s parityStep, run func([]string) (int, string, string)) parityOutcome {
	t.Helper()
	env := map[string]string{}
	for k, v := range s.Env {
		env[k] = v
	}
	var request map[string]any
	if s.Factory != nil {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var decoded map[string]any
			_ = json.Unmarshal(body, &decoded)
			request = map[string]any{
				"method":        r.Method,
				"path":          r.URL.Path,
				"authorization": r.Header.Get("Authorization"),
				"content_type":  r.Header.Get("Content-Type"),
				"body":          decoded,
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.Factory.Status)
			_ = json.NewEncoder(w).Encode(s.Factory.Body)
		}))
		defer server.Close()
		env[cloudEnvFactoryURL] = server.URL + "/"
		env[cloudEnvFactoryToken] = "tkr_run_scoped"
	}
	for _, k := range parityEnvKeys {
		t.Setenv(k, env[k])
	}

	args := make([]string, len(s.Args))
	for i, a := range s.Args {
		args[i] = strings.ReplaceAll(a, "<ROOT>", root)
	}
	code, stdout, stderr := run(args)
	var server string
	if s.Factory != nil {
		server = env[cloudEnvFactoryURL]
	}
	normalize := func(text string) string {
		text = strings.ReplaceAll(text, root, "<ROOT>")
		if server != "" {
			text = strings.ReplaceAll(text, strings.TrimSuffix(server, "/"), "<FACTORY>")
		}
		return text
	}
	return parityOutcome{Exit: code, Stdout: normalize(stdout), Stderr: normalize(stderr), Request: request}
}

func applyParityRenames(o parityOutcome) parityOutcome {
	for _, r := range parityRenames {
		o.Stdout = r.from.ReplaceAllString(o.Stdout, r.to)
		o.Stderr = r.from.ReplaceAllString(o.Stderr, r.to)
	}
	return o
}

// parityErrorPrefix is each tool's own framing of a refusal: tk's "Error: ",
// ticfac's "ticfac <verb>: ".
var parityErrorPrefix = regexp.MustCompile(`^(?:Error: |ticfac (?:sandbox [a-z-]+|cloud branch): )`)

// parityErrorMessage is a refusal's message: its first stderr line, unframed.
func parityErrorMessage(stderr string) string {
	first, _, _ := strings.Cut(stderr, "\n")
	return parityErrorPrefix.ReplaceAllString(first, "")
}

func compareParity(t *testing.T, i int, s parityStep, want, got parityOutcome) {
	t.Helper()
	label := strings.Join(s.Args, " ")
	if len(s.Env) > 0 {
		keys := make([]string, 0, len(s.Env))
		for k, v := range s.Env {
			keys = append(keys, k+"="+v)
		}
		sort.Strings(keys)
		label = strings.Join(keys, " ") + " " + label
	}
	if got.Exit != want.Exit {
		t.Errorf("step %d (%s): exit %d, tk exited %d\n--stderr--\n%s", i, label, got.Exit, want.Exit, got.Stderr)
	}
	if got.Stdout != want.Stdout {
		t.Errorf("step %d (%s): stdout differs from tk's\n--- tk ---\n%s\n--- ticfac ---\n%s", i, label, want.Stdout, got.Stdout)
	}
	if want.Exit == 0 {
		if got.Stderr != want.Stderr {
			t.Errorf("step %d (%s): stderr differs from tk's\n--- tk ---\n%s\n--- ticfac ---\n%s", i, label, want.Stderr, got.Stderr)
		}
	} else if w, g := parityErrorMessage(want.Stderr), parityErrorMessage(got.Stderr); g != w {
		t.Errorf("step %d (%s): the refusal differs from tk's\n--- tk ---\n%s\n--- ticfac ---\n%s", i, label, w, g)
	}
	if want.Request != nil || got.Request != nil {
		w, _ := json.Marshal(want.Request)
		g, _ := json.Marshal(got.Request)
		if !bytes.Equal(w, g) {
			t.Errorf("step %d (%s): the factory was sent something other than what tk sent\n--- tk ---\n%s\n--- ticfac ---\n%s", i, label, w, g)
		}
	}
}
