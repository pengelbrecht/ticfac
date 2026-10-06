package subprocess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The durable runner's per-attempt interface (tick hpk): the re-prompt argvs
// are the SAME argv with a different message, and the config the harness
// reads is the whole rest of the interface. These are the pure decisions;
// the supervisor-level halves are in steer_test.go.

// A durable runner's re-prompt is the same argv with the follow-up as the
// message: no session flag, no fresh-run section, the model and the harness
// and state paths all intact — the conversation is in the storage, so a
// relaunch needs nothing but the message.
func TestADurableRunnersRepromptsAreTheSameArgvWithAMessage(t *testing.T) {
	record := &attemptRecord{Branch: "b", ResultPath: "/abs/RESULT.md", TickID: "stk"}
	at := launch{
		Prompt:       "PROMPT-BODY",
		GitCommonDir: "/repo/.git",
		HarnessDir:   "/repo/harness",
		StateDir:     "/state/attempt",
		Model:        "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
	}
	base, err := resolveRunner("pi", nil, at)
	if err != nil {
		t.Fatal(err)
	}
	same := func(name string, argv []string, err error, want string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(argv) != len(base) {
			t.Errorf("%s is not the same argv: %v (launch %v)", name, argv, base)
		}
		for i := range argv {
			if argv[i] != base[i] && i != len(argv)-1 {
				t.Errorf("%s rewrote more than the message: %v", name, argv)
			}
		}
		if argv[len(argv)-1] != want {
			t.Errorf("%s message = %q, want the %s", name, argv[len(argv)-1], want)
		}
		if indexOf(argv, "--model") < 0 || indexOf(argv, at.Model) < 0 {
			t.Errorf("%s drops the routed model: %v", name, argv)
		}
	}

	nudge, err := nudgeArgv("pi", nil, at, record)
	same("the nudge", nudge, err, nudgePrompt(record))
	if strings.Contains(nudge[len(nudge)-1], "already ran once on this worktree") {
		t.Errorf("the nudge tells a durable runner its worktree is fresh: %q", nudge[len(nudge)-1])
	}

	after := 3 * time.Minute
	stuck, err := stuckArgv("pi", nil, at, record, after)
	same("the stuck re-prompt", stuck, err, StuckPrompt("the supervisor stopped its tool processes and re-prompted it", after))

	pushback, err := lintPushbackArgv("pi", nil, at, record)
	same("the report pushback", pushback, err, LintPushbackPrompt(record.ResultPath, record.LintCommand, lintErrorsPlaceholder))
}

// A CLI runner keeps the old shapes: codex's nudge is the whole prompt plus
// the fresh-run section, because its session died with its process.
func TestACliRunnersNudgeStillAppendsTheFreshRunSection(t *testing.T) {
	record := &attemptRecord{Branch: "b", ResultPath: "/abs/RESULT.md"}
	at := launch{Prompt: "PROMPT-BODY", GitCommonDir: "/repo/.git"}
	nudge, err := nudgeArgv("codex", nil, at, record)
	if err != nil {
		t.Fatal(err)
	}
	if last := nudge[len(nudge)-1]; !strings.HasPrefix(last, "PROMPT-BODY") {
		t.Errorf("codex's nudge lost the prompt: %q", last)
	}
}

// The config the harness reads is the whole per-attempt interface, rendered
// once at Start so every process the attempt runs reads the same one.
func TestWorkerConfigCarriesTheWholeInterface(t *testing.T) {
	dir := t.TempDir()
	record := &attemptRecord{
		Worktree:    filepath.Join(dir, "worktree"),
		Branch:      "ticfac/run-43y/tick-hpk/attempt-9",
		BaseSHA:     "abc123",
		ResultPath:  filepath.Join(dir, "worktree", "runs", "RESULT-hpk.md"),
		TickID:      "hpk",
		Model:       "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		Remote:      "origin",
		SourceGrade: gradeWrite,
		State:       dir,
		IssuedAt:    "2026-10-04T12:00:00Z",
		WallSeconds: 28800,
		Spec:        &JobSpec{Role: "implement"},
	}
	opts := &Options{SupervisorArgv: []string{"/bin/ticfac-exec-subprocess", "supervise"}}
	if err := writeWorkerConfig(func(path string, data []byte, perm os.FileMode) error {
		return os.WriteFile(path, data, perm)
	}, dir, record, opts); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, fileWorkerConfig))
	if err != nil {
		t.Fatal(err)
	}
	var config workerConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.Storage != filepath.Join(dir, fileWorkerStorage) {
		t.Errorf("storage = %q", config.Storage)
	}
	if config.SteerSock != steerSockPath(dir) {
		t.Errorf("steerSock = %q", config.SteerSock)
	}
	if config.Worktree != record.Worktree || config.Branch != record.Branch || config.Base != record.BaseSHA {
		t.Errorf("the workspace facts did not travel: %+v", config)
	}
	if config.Report != record.ResultPath || config.Tick != "hpk" || config.Role != "implement" {
		t.Errorf("the report facts did not travel: %+v", config)
	}
	if config.Checker != "/bin/ticfac-exec-subprocess" {
		t.Errorf("checker = %q", config.Checker)
	}
	if config.Model != record.Model {
		t.Errorf("model = %q", config.Model)
	}
	// A WRITE grade carries the remote: the wip checkpoints after every tool
	// round are the local rung's durability, and the harness installs them on
	// exactly this field being non-empty.
	if config.Remote != "origin" {
		t.Errorf("a write grade's remote = %q, want origin", config.Remote)
	}
	// The wall is ABSOLUTE: issued-at plus the wall seconds, so a relaunched
	// runner arms the wall the attempt already runs under.
	issued, err := time.Parse(time.RFC3339, record.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if want := issued.Add(time.Duration(record.WallSeconds) * time.Second).UnixMilli(); config.WallDeadlineMS != want {
		t.Errorf("wallDeadlineMs = %d, want %d", config.WallDeadlineMS, want)
	}
	if config.FauxTranscript != "" {
		t.Errorf("a production config carries a faux transcript: %+v", config)
	}

	// The test seam travels when the options name one, and only then.
	opts.HarnessFauxTranscript = "/tmp/transcript.json"
	if err := writeWorkerConfig(func(path string, data []byte, perm os.FileMode) error {
		return os.WriteFile(path, data, perm)
	}, dir, record, opts); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, fileWorkerConfig))
	_ = json.Unmarshal(raw, &config)
	if config.FauxTranscript != "/tmp/transcript.json" {
		t.Errorf("the test seam did not travel: %+v", config)
	}
}

// A READ-ONLY attempt's worker.json carries no remote (tick x8e): its runner
// is launched pinned so that every push fails at the transport (grade.go),
// so a config that named the remote would install the wip checkpoints whose
// every push the pins refuse — one failed checkpoint a tool round, thrown
// into onReport — and a finish phase whose salvage commits a tree onto a ref
// the grade granted no write to. Empty is the harness's own "the
// checkpoints are off" (worker-host.ts), which is what a read-only local run
// must run with: it has no push, so there is nothing to checkpoint to.
func TestAReadOnlyWorkersConfigCarriesNoRemote(t *testing.T) {
	record := &attemptRecord{
		Branch:      "ticfac/run-43y/tick-hpk/attempt-9",
		ResultPath:  "/abs/RESULT.md",
		TickID:      "hpk",
		Model:       "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		Remote:      "origin",
		SourceGrade: gradeReadOnly,
	}
	dir := t.TempDir()
	if err := writeWorkerConfig(func(path string, data []byte, perm os.FileMode) error {
		return os.WriteFile(path, data, perm)
	}, dir, record, &Options{SupervisorArgv: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, fileWorkerConfig))
	if err != nil {
		t.Fatal(err)
	}
	var config workerConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.Remote != "" {
		t.Errorf("a read-only grade's config carries remote %q: the harness would install the wip checkpoints on it", config.Remote)
	}
	// The branch still travels: it is the worktree's own checked-out branch,
	// what the report's collector and the boundary diff read — a grade that
	// refuses a push does not refuse a name.
	if config.Branch != record.Branch {
		t.Errorf("branch = %q, want %q", config.Branch, record.Branch)
	}

	// And it fails CLOSED, the same sentence the grade itself is: a grade
	// this build does not recognise is not a write grant, so it gets no
	// remote either.
	record.SourceGrade = "a grade this build does not know"
	closed := t.TempDir()
	if err := writeWorkerConfig(func(path string, data []byte, perm os.FileMode) error {
		return os.WriteFile(path, data, perm)
	}, closed, record, &Options{SupervisorArgv: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	closedRaw, err := os.ReadFile(filepath.Join(closed, fileWorkerConfig))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(closedRaw, &config)
	if config.Remote != "" {
		t.Errorf("an unrecognised grade's config carries remote %q: the gate must fail closed", config.Remote)
	}
}

// The wall deadline is refused rather than guessed when the issued-at stamp
// cannot be read: a runner arming a wrong wall is a runner that may abort a
// healthy conversation or never arm at all.
func TestWorkerConfigRefusesAnUnreadableIssuedStamp(t *testing.T) {
	record := &attemptRecord{WallSeconds: 60, IssuedAt: "not a stamp"}
	err := writeWorkerConfig(func(string, []byte, os.FileMode) error { return nil }, t.TempDir(), record, &Options{SupervisorArgv: []string{"x"}})
	if err == nil || !strings.Contains(err.Error(), "issued-at") {
		t.Fatalf("err = %v, want a refusal naming the stamp", err)
	}
}

// A runner with no wall carries no deadline: the harness runs until its
// supervisor's own wall stops it.
func TestWorkerConfigWithNoWallCarriesNoDeadline(t *testing.T) {
	record := &attemptRecord{WallSeconds: 0, IssuedAt: "2026-10-04T12:00:00Z"}
	dir := t.TempDir()
	if err := writeWorkerConfig(func(string, []byte, os.FileMode) error { return nil }, dir, record, &Options{SupervisorArgv: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, fileWorkerConfig))
	var config workerConfig
	_ = json.Unmarshal(raw, &config)
	if config.WallDeadlineMS != 0 {
		t.Errorf("wallDeadlineMs = %d, want none", config.WallDeadlineMS)
	}
}
