package subprocess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The gateway metering wiring of THIS executor (tick gzv, absorbing dm2's
// finding): the herdr executor joins its pi workers' Workers AI spend to the
// operator's AI Gateway by loading a generated provider override through
// `--extension`, and the local subprocess executor launches pi too — from its
// runner table's own pi row — so a dispatch through it must load the same
// join, or its cost line stays honestly unmetered while the identical
// dispatch through herdr is metered.
//
// The override is written into the ATTEMPT's state directory and rides every
// argv that launches pi for the attempt: the launch, the nudge, the pushback
// and the stuck re-prompt, because a re-prompted session spends too.

// The join rides the pi table row's own launch: --extension goes in front of
// the prompt, exactly like the model flag, because the prompt is a positional
// argument and a flag after it is a flag the CLI would read as part of it.
func TestAMeteredPiLaunchCarriesTheExtensionBeforeThePrompt(t *testing.T) {
	const ext = "/state/run-1/tick-gzv/attempt-1/gateway-metering.mjs"
	const model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

	at := launch{Prompt: "PROMPT-BODY", GitCommonDir: "/repo/.git", Model: model, Extension: ext}
	argv, err := resolveRunner("pi", nil, at)
	if err != nil {
		t.Fatal(err)
	}
	i := indexOf(argv, "--extension")
	if i < 0 {
		t.Fatalf("the pi launch carries no extension flag: %v", argv)
	}
	if i+1 >= len(argv) || argv[i+1] != ext {
		t.Fatalf("the extension flag names %v, want the override's path %s", argv[i+1:], ext)
	}
	if prompt := indexOf(argv, "PROMPT-BODY"); prompt < i {
		t.Errorf("the extension flag comes after the prompt: %v", argv)
	}
	if indexOf(argv, "--model") < 0 || argv[indexOf(argv, "--model")+1] != model {
		t.Errorf("the join replaced the routed model: %v", argv)
	}

	// The same launch in a named session: the session flags keep their place
	// and the join keeps its.
	session, err := sessionFor("pi", nil)
	if err != nil {
		t.Fatal(err)
	}
	at.Session = session
	argv, err = resolveRunner("pi", nil, at)
	if err != nil {
		t.Fatal(err)
	}
	if i := indexOf(argv, "--extension"); i < 0 || argv[i+1] != ext || i > indexOf(argv, "PROMPT-BODY") {
		t.Errorf("a sessioned pi launch does not carry the join before its prompt: %v", argv)
	}
}

// Every argv that re-launches pi for the attempt carries the join too: the
// nudge, the report pushback and the stuck re-prompt are all fresh pi
// processes whose model calls spend through the same provider.
func TestTheRePromptsCarryTheJoinToo(t *testing.T) {
	const ext = "/state/run-1/tick-gzv/attempt-1/gateway-metering.mjs"
	record := &attemptRecord{Branch: "b", ResultPath: "/abs/RESULT.md"}
	session, err := sessionFor("pi", nil)
	if err != nil {
		t.Fatal(err)
	}
	at := launch{
		Prompt:       "PROMPT-BODY",
		GitCommonDir: "/repo/.git",
		Model:        "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		Session:      session,
		Extension:    ext,
	}

	nudge, err := nudgeArgv("pi", nil, at, record)
	if err != nil {
		t.Fatal(err)
	}
	if !carriesExtension(nudge, ext) {
		t.Errorf("the nudge drops the join: %v", nudge)
	}
	pushback, err := lintPushbackArgv("pi", nil, at, record)
	if err != nil {
		t.Fatal(err)
	}
	if !carriesExtension(pushback, ext) {
		t.Errorf("the report pushback drops the join: %v", pushback)
	}
	stuck, err := stuckArgv("pi", nil, at, record, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !carriesExtension(stuck, ext) {
		t.Errorf("the stuck re-prompt drops the join: %v", stuck)
	}
}

// carriesExtension reports whether argv loads the override at ext, in front of
// the prompt positional.
func carriesExtension(argv []string, ext string) bool {
	for i, arg := range argv {
		if arg == "--extension" && i+1 < len(argv) && argv[i+1] == ext {
			return true
		}
	}
	return false
}

// The flag is PI's own: another runner handed one would read it as its own
// argument or refuse it, and the refusal is this executor's fail-closed
// answer to a caller that would hand it one.
func TestTheExtensionFlagIsThePiCLIsOwn(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		_, err := resolveRunner(name, nil,
			launch{Prompt: "P", GitCommonDir: "/repo/.git", Extension: "/state/gateway-metering.mjs"})
		if err == nil {
			t.Errorf("%s accepted --extension: the flag is the pi CLI's own, and %s would misread it", name, name)
		}
	}
}

// An argv override owns the whole invocation — the escape hatch a build with
// a runner's flags wrong reaches for — so the join never edits one, exactly
// as it never inserts a model into one.
func TestAnArgvOverrideOwnsTheWholeInvocationIncludingTheJoin(t *testing.T) {
	argv, err := resolveRunner("pi", []string{"/bin/sh", "-c", "true"},
		launch{Prompt: "P", Model: "cloudflare-workers-ai/@cf/zai-org/glm-5.3", Extension: "/state/gateway-metering.mjs"})
	if err != nil {
		t.Fatal(err)
	}
	if contains(argv, "--extension") {
		t.Errorf("the executor edited an argv override: %v", argv)
	}
}

// The gate the executor applies itself: only a pi launch with no argv
// override, on a Workers AI model the gateway serves, with metering
// configured, writes and loads the override. Everything else joins nothing
// and leaves no file behind.
func TestOnlyAMeteredWorkersAIPiLaunchJoins(t *testing.T) {
	metered := &GatewayMetering{RunID: "epic-hn6", GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw"}

	cases := []struct {
		name       string
		runner     string
		runnerArgv []string
		model      string
		metering   *GatewayMetering
	}{
		{"the pi row on a Workers AI model", "pi", nil, "cloudflare-workers-ai/@cf/zai-org/glm-5.3", metered},
		{"no metering configured", "pi", nil, "cloudflare-workers-ai/@cf/zai-org/glm-5.3", nil},
		{"a model the gateway does not serve", "pi", nil, "opus", metered},
		{"no model routed", "pi", nil, "", metered},
		{"another runner", "claude", nil, "cloudflare-workers-ai/@cf/zai-org/glm-5.3", metered},
		{"an argv override", "pi", []string{"/bin/sh", "-c", "true"}, "cloudflare-workers-ai/@cf/zai-org/glm-5.3", metered},
	}
	for _, c := range cases {
		repo := newRepo(t, c.name)
		e, err := New(Options{Repo: repo.Dir, Runner: c.runner, RunnerArgv: c.runnerArgv, Model: c.model,
			Metering: c.metering})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		dir := t.TempDir()
		path, err := e.meteringExtension(newStore(dir))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.name == "the pi row on a Workers AI model" {
			if path == "" {
				t.Errorf("%s: a metered Workers AI pi launch joined nothing", c.name)
			}
			if path != filepath.Join(dir, extensionFile) {
				t.Errorf("%s: the override lives at %s, want the attempt state dir", c.name, path)
			}
			continue
		}
		if path != "" {
			t.Errorf("%s: joined anyway (%s)", c.name, path)
		}
		if _, err := os.Stat(filepath.Join(dir, extensionFile)); err == nil {
			t.Errorf("%s: a launch that joins nothing still wrote an override file", c.name)
		}
	}
}

// The whole launch path, end to end: a pi attempt dispatched with metering
// configured actually EXECUTES `pi … --extension <state dir>/gateway-metering.mjs
// …`, the file the flag names is the join (run id, gateway route), and the
// attempt's recorded re-prompt argvs carry it too. The pi on PATH for the
// supervisor is a stub that records its argv — the real CLI never runs here.
func TestASubprocessPiAttemptJoinsItsSpendToTheGateway(t *testing.T) {
	stubDir := t.TempDir()
	argsLog := filepath.Join(stubDir, "pi-args.log")
	stub := "#!/bin/sh\nprintf '%s\\0' \"$@\" >> " + argsLog + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(stubDir, "pi"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	f := newFixture(t, fixtureOptions{
		runner: "pi",
		model:  "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		metering: &GatewayMetering{
			RunID:      "epic-hn6",
			GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw",
		},
	})
	handle := f.Start(f.spec("run-1/tick-gzv/attempt-1", "gzv"))
	f.waitSettled(handle)

	st := f.store(handle)
	ext := st.path(extensionFile)
	if _, err := os.Stat(ext); err != nil {
		t.Fatalf("the attempt state dir holds no override at %s: %v", ext, err)
	}
	raw, err := os.ReadFile(ext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `\"run_id\":\"epic-hn6\"`) {
		t.Errorf("the override the flag names does not tag the run id:\n%s", raw)
	}
	if !strings.Contains(string(raw), "/workers-ai/v1") {
		t.Errorf("the override the flag names does not route the provider at the gateway:\n%s", raw)
	}

	// What actually crossed the exec: the stub's own record of its argv.
	argsRaw, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("the stub pi never ran: %v", err)
	}
	args := strings.Split(strings.TrimRight(string(argsRaw), "\x00"), "\x00")
	i := indexOf(args, "--extension")
	if i < 0 || i+1 >= len(args) || args[i+1] != ext {
		t.Fatalf("the exec'd pi argv carries no --extension %s: %v", ext, args)
	}
	if i >= len(args)-1 {
		t.Errorf("the extension flag sits after the prompt positional: %v", args)
	}

	// And the re-prompts the attempt recorded: every argv that would
	// relaunch pi carries the join.
	record, err := st.readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	for name, argv := range map[string][]string{
		"the launch":          record.RunnerArgv,
		"the nudge":           record.NudgeArgv,
		"the pushback":        record.LintArgv,
		"the stuck re-prompt": record.StuckArgv,
	} {
		if !carriesExtension(argv, ext) {
			t.Errorf("%s drops the join: %v", name, argv)
		}
	}
}
