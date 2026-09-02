package subprocess

import (
	"strings"
	"testing"
)

// The MODEL seam: a profile routes a model, and the model has to reach the
// process. A model that is recorded as applied and silently was not is
// provenance that lies — so the flag is in the runner table beside the argv it
// belongs to, and a runner with no flag for it is a REFUSAL rather than a
// silent drop.

// The three runners, their model flags, and the position the flag takes: before
// the prompt, always, because the prompt is a positional argument and a flag
// after it is a flag one of these CLIs would read as part of it.
func TestEveryKnownRunnerCarriesTheModelBeforeThePrompt(t *testing.T) {
	want := map[string]string{"claude": "--model", "codex": "-m", "pi": "--model"}
	for _, name := range KnownRunners() {
		flag, ok := want[name]
		if !ok {
			t.Fatalf("%s is a known runner this test does not pin a model flag for", name)
		}
		argv, err := resolveRunner(name, nil, launch{Prompt: "PROMPT-BODY", GitCommonDir: "/repo/.git", Model: "a-model"})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		at := indexOf(argv, flag)
		if at < 0 {
			t.Errorf("%s: the model flag %s is not in %v", name, flag, argv)
			continue
		}
		if at+1 >= len(argv) || argv[at+1] != "a-model" {
			t.Errorf("%s: %s does not name the model: %v", name, flag, argv)
			continue
		}
		if prompt := indexOf(argv, "PROMPT-BODY"); prompt < at {
			t.Errorf("%s: the model flag comes after the prompt: %v", name, argv)
		}
	}
}

// No model routed, no flag: the runner is launched exactly as it was before,
// on whatever model its own configuration chooses.
func TestNoModelLeavesTheArgvAlone(t *testing.T) {
	at := launch{Prompt: "P", GitCommonDir: "/repo/.git"}
	for _, name := range KnownRunners() {
		argv, err := resolveRunner(name, nil, at)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, arg := range argv {
			if arg == "--model" || arg == "-m" {
				t.Errorf("%s: a model flag appeared with no model routed: %v", name, argv)
			}
		}
	}
}

// The escape hatch still wins. An override is the whole invocation — it is
// what a build with a runner's flags wrong reaches for — so this executor does
// not edit one, and the caller who set it owns whether it names a model.
func TestAnArgvOverrideOwnsTheWholeInvocationIncludingTheModel(t *testing.T) {
	argv, err := resolveRunner("claude", []string{"/bin/sh", "-c", "true"},
		launch{Prompt: "P", Model: "a-model"})
	if err != nil {
		t.Fatal(err)
	}
	if contains(argv, "--model") || contains(argv, "a-model") {
		t.Errorf("the executor edited an argv override: %v", argv)
	}
	if argv[len(argv)-1] != "P" {
		t.Errorf("the override lost the prompt: %v", argv)
	}
}

// FAIL CLOSED. A runner this executor has no model flag for, handed a model, is
// a refusal: launching it anyway would run a model nobody asked for while every
// record said otherwise.
func TestAModelRoutedToARunnerWithNoModelFlagIsRefused(t *testing.T) {
	silent := runnerDef{Argv: []string{"quiet", promptPlaceholder}}
	if _, err := withModel("quiet", silent, "a-model"); err == nil {
		t.Fatal("a model was accepted by a runner that has nowhere to put one")
	} else if !strings.Contains(err.Error(), "a-model") || !strings.Contains(err.Error(), "quiet") {
		t.Errorf("the refusal names neither the runner nor the model: %v", err)
	}
	// And with no model there is nothing to refuse.
	argv, err := withModel("quiet", silent, "")
	if err != nil || strings.Join(argv, " ") != "quiet "+promptPlaceholder {
		t.Errorf("withModel changed an argv it had no model for: %v (%v)", argv, err)
	}
}

// The question the reconciler asks BEFORE it claims a tick: can this runner be
// told which model to use at all?
func TestRunnerAcceptsModelAnswersForEveryKnownRunner(t *testing.T) {
	for _, name := range KnownRunners() {
		if !RunnerAcceptsModel(name) {
			t.Errorf("%s takes no model, and a profile routing one to it would be unapplied", name)
		}
	}
	if RunnerAcceptsModel("emacs") {
		t.Error("an unknown runner claims to accept a model")
	}
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}
