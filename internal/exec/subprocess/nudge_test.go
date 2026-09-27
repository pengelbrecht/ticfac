package subprocess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// epic-2jn, vqc's resolve job (2026-09-27): claude started the gate as a
// background task and ended its turn to wait for it. In print mode that ends
// the process — exit 0, one commit, no report — and the job was judged
// missing-result, which halted the run. A runner that exits 0 without its
// report is prompted again now, bounded, before it is judged (nudge.go).

func nudges(observations []Observation) []Observation {
	var out []Observation
	for _, o := range observations {
		if IsNudge(o) {
			out = append(out, o)
		}
	}
	return out
}

func TestARunnerThatEndsEarlyAndFinishesWhenNudgedSucceeds(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "stop_early"})
	handle := f.Start(f.spec("run-2jn/tick-vqc/resolve-50", "vqc"))
	f.waitSettled(handle)

	status := f.inspect(handle)
	collected := f.collect(handle)
	if collected.Verdict != VerdictReadyToMerge {
		t.Fatalf("verdict %s, want %s: a runner that ended its turn early and finished when re-prompted "+
			"was judged on its first exit\n%s", collected.Verdict, VerdictReadyToMerge,
			formatObservations(status.Observations))
	}
	if got := nudges(status.Observations); len(got) != 1 {
		t.Fatalf("%d nudge observations, want 1:\n%s", len(got), formatObservations(status.Observations))
	} else if !strings.Contains(got[0].Detail, "nudge 1 of 2") || !strings.Contains(got[0].Detail, "without writing its report") {
		t.Errorf("the nudge observation does not say what happened: %s", got[0].Detail)
	}
	if collected.Result.Source.Commits != 2 {
		t.Errorf("%d commits, want both the first run's and the nudged run's", collected.Result.Source.Commits)
	}

	// The fake runner has no session, so the nudge is a fresh run on the same
	// worktree, and its prompt says a run before it already worked there.
	local, _ := handle.Local()
	seen, err := os.ReadFile(filepath.Join(local.Worktree, "nudge-1.txt"))
	if err != nil {
		t.Fatalf("the nudged run left no trace: %v", err)
	}
	if !strings.Contains(string(seen), "already ran once on this worktree") {
		t.Errorf("the fresh nudge prompt does not say a run before it worked here:\n%s", seen)
	}
}

func TestARunnerThatNeverReportsIsMissingResultAfterTheNudgesAreSpent(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "silent"})
	handle := f.Start(f.spec("run-2jn/tick-nnn/attempt-1", "nnn"))
	f.waitSettled(handle)

	status := f.inspect(handle)
	if got := nudges(status.Observations); len(got) != MaxNudges {
		t.Fatalf("%d nudges, want exactly %d:\n%s", len(got), MaxNudges, formatObservations(status.Observations))
	}
	if collected := f.collect(handle); collected.Verdict != VerdictMissingResult {
		t.Fatalf("verdict %s, want %s", collected.Verdict, VerdictMissingResult)
	}
}

// A runner that FAILED is not nudged: its non-zero exit and its own words are
// what collect classifies, and prompting it again would spend a turn on a
// broken argv or an exhausted quota.
func TestAFailedRunnerIsNotNudged(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "usage_error"})
	handle := f.Start(f.spec("run-2jn/tick-fff/attempt-1", "fff"))
	f.waitSettled(handle)

	if got := nudges(f.inspect(handle).Observations); len(got) != 0 {
		t.Fatalf("a runner that exited non-zero was nudged: %v", got)
	}
}

// claude runs without background tasks: in print mode a background task is
// a turn that ends while the work it waits on is still running.
func TestClaudeIsLaunchedWithoutBackgroundTasks(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "background_env"})
	handle := f.Start(f.spec("run-2jn/tick-bbb/attempt-1", "bbb"))
	f.waitSettled(handle)

	local, _ := handle.Local()
	seen, err := os.ReadFile(filepath.Join(local.Worktree, "background-env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(seen) != "1" {
		t.Fatalf("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS reached the claude runner as %q, want 1", seen)
	}
}

// The runners with a session this executor can name are nudged in THAT
// session: claude starts with --session-id and resumes with --resume, pi uses
// --session-id for both. codex has none, and is run again with the whole
// prompt and the fresh-run section.
func TestTheNudgeResumesTheRunnersOwnSession(t *testing.T) {
	record := &attemptRecord{Branch: "b", ResultPath: "/abs/RESULT.md"}
	cases := map[string]struct{ start, resume string }{
		"claude": {"--session-id", "--resume"},
		"pi":     {"--session-id", "--session-id"},
	}
	for name, flags := range cases {
		session, err := sessionFor(name, nil)
		if err != nil || len(session) != 36 {
			t.Fatalf("%s: session %q, err %v", name, session, err)
		}
		at := launch{Prompt: "PROMPT-BODY", GitCommonDir: "/repo/.git", Model: "m", Session: session}
		argv, err := resolveRunner(name, nil, at)
		if err != nil {
			t.Fatal(err)
		}
		if i := indexOf(argv, flags.start); i < 0 || argv[i+1] != session || i > indexOf(argv, "PROMPT-BODY") {
			t.Errorf("%s: the launch does not start session %s before the prompt: %v", name, session, argv)
		}
		nudge, err := nudgeArgv(name, nil, at, record)
		if err != nil {
			t.Fatal(err)
		}
		if i := indexOf(nudge, flags.resume); i < 0 || nudge[i+1] != session {
			t.Errorf("%s: the nudge does not resume session %s: %v", name, session, nudge)
		}
		last := nudge[len(nudge)-1]
		if strings.Contains(last, "PROMPT-BODY") || !strings.Contains(last, record.ResultPath) ||
			!strings.Contains(last, HeadlessLine) {
			t.Errorf("%s: the resumed session is not told what is missing and where: %q", name, last)
		}
		if indexOf(nudge, "--model") < 0 {
			t.Errorf("%s: the nudge drops the routed model: %v", name, nudge)
		}
	}

	if session, _ := sessionFor("codex", nil); session != "" {
		t.Errorf("codex was given a session %q it has no flag for", session)
	}
	nudge, err := nudgeArgv("codex", nil, launch{Prompt: "PROMPT-BODY", GitCommonDir: "/repo/.git"}, record)
	if err != nil {
		t.Fatal(err)
	}
	if last := nudge[len(nudge)-1]; !strings.HasPrefix(last, "PROMPT-BODY") ||
		!strings.Contains(last, "already ran once on this worktree") {
		t.Errorf("codex's nudge is not the whole prompt plus the fresh-run section: %q", last)
	}

	// An override is the whole invocation: no session is inserted into it.
	if session, _ := sessionFor("claude", []string{"/bin/true"}); session != "" {
		t.Errorf("an override argv was given a session %q", session)
	}
}

func TestThePromptSaysEndingTheTurnEndsTheJob(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"", "# resolve-conflict\n\nResolve it."} {
		prompt := renderPrompt(&attemptRecord{TickID: "abc", Branch: "b", BaseSHA: "0123", RolePrompt: role},
			&JobSpec{ArtifactPrefix: "runs/r/abc"})
		if !strings.Contains(prompt, HeadlessLine) {
			t.Errorf("the prompt (role %q) does not say ending the turn ends the job:\n%s", role, prompt)
		}
	}
}
