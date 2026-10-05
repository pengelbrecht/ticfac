package ciwatchdog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// 2026-10-01: the squash merge of #178 (03f56af4) landed on main and GitHub
// emitted no push event for it — no CI run of any status — so deploy-factory,
// which runs on CI's workflow_run, never shipped it. The watchdog notices a main
// head with no CI run and dispatches one.

const head = "03f56af47b80085a120408dc7a509f955e10e38d"

// landed is 2026-10-01T07:14:38Z, the merge's committer date.
const landed = 1790838878

// parent is the commit before head on main in the tracker-only cases.
const parent = "9f1c2a7d4e5b6c8a0d1e2f3a4b5c6d7e8f9a0b1c"

// mainState is what the fake forge answers: CI runs (not cancelled) per
// commit, and the paths that changed between parent and head.
type mainState struct {
	headRuns, parentRuns string
	changed              []string
}

// fakeGh answers the reads the script makes and records a dispatch.
func fakeGh(t *testing.T, dir string, st mainState) {
	t.Helper()
	files := strings.Join(st.changed, "\n")
	script := `#!/bin/sh
case "$*" in
  "api repos/o/r/commits/main --jq .sha") echo ` + head + ` ;;
  "api repos/o/r/commits/` + head + ` --jq "*) echo ` + strconv.Itoa(landed) + ` ;;
  "api repos/o/r/actions/workflows/ci.yml/runs?head_sha=` + head + `&per_page=100 --jq "*) echo ` + st.headRuns + ` ;;
  "api repos/o/r/actions/workflows/ci.yml/runs?head_sha=` + parent + `&per_page=100 --jq "*) echo ` + st.parentRuns + ` ;;
  "api repos/o/r/commits?sha=` + head + `&per_page=30 --jq .[].sha") printf '%s\n' ` + head + ` ` + parent + ` ;;
  "api repos/o/r/compare/` + parent + `...` + head + ` --jq .files[].filename") printf '` + files + `\n' ;;
  "workflow run ci.yml --repo o/r --ref main") echo dispatched >> "$GH_RECORD" ;;
  *) echo "unexpected gh call: $*" >&2; exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, st mainState, age int) (string, bool) {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fakeGh(t, bin, st)
	record := filepath.Join(bin, "dispatches")
	cmd := exec.Command("bash", filepath.Join(root, ".github", "scripts", "main-ci-watchdog.sh"))
	cmd.Env = []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GITHUB_REPOSITORY=o/r",
		"GH_RECORD=" + record,
		"CI_WATCHDOG_NOW=" + strconv.Itoa(landed+age),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the watchdog failed: %v\n%s", err, out)
	}
	_, statErr := os.Stat(record)
	return string(out), statErr == nil
}

// The 03f56af4 shape: main's head has no CI run at all, long after it landed,
// and neither does the commit before it. CI is dispatched on main.
func TestAMainHeadWithNoCIRunGetsOneDispatched(t *testing.T) {
	out, dispatched := run(t, mainState{headRuns: "0", parentRuns: "0"}, 40*60)
	if !dispatched {
		t.Fatalf("a main head with no CI run 40 minutes after it landed was left alone:\n%s", out)
	}
	if !strings.Contains(out, head) {
		t.Errorf("the watchdog does not name the head it dispatched for:\n%s", out)
	}
}

// A head whose CI ran (or runs) is left alone, and so is one that only just
// landed: its push's run may still be on its way.
func TestTheWatchdogLeavesAHeadWithCIOrAYoungHeadAlone(t *testing.T) {
	if out, dispatched := run(t, mainState{headRuns: "1"}, 40*60); dispatched {
		t.Errorf("CI was dispatched on a head that already has a run:\n%s", out)
	}
	if out, dispatched := run(t, mainState{headRuns: "0"}, 60); dispatched {
		t.Errorf("CI was dispatched on a head one minute old, inside the grace period:\n%s", out)
	}
}

// Ticks 5ob/ciw: ci.yml starts no run for a push that changes only run state
// and tracker records, so a `tick:` commit on main has no run by design. The
// watchdog leaves it alone when the commit before it has a run - and still
// dispatches when what changed since includes code, which is a missed run.
func TestTheWatchdogLeavesATrackerOnlyHeadAloneButNotAMissedCodeCommit(t *testing.T) {
	tracker := []string{".tick/issues/5ob.json", ".tick/activity/activity.jsonl", ".tick/pending/q1.json", ".ticfac/runs/r/checkpoint.json"}
	if out, dispatched := run(t, mainState{headRuns: "0", parentRuns: "1", changed: tracker}, 40*60); dispatched {
		t.Errorf("CI was dispatched on a tracker-only head whose parent has a run:\n%s", out)
	}
	withCode := append([]string{"internal/reconcile/land.go"}, tracker...)
	if out, dispatched := run(t, mainState{headRuns: "0", parentRuns: "1", changed: withCode}, 40*60); !dispatched {
		t.Errorf("a head carrying a code change with no CI run was left alone:\n%s", out)
	}
	config := []string{".tick/runners.toml"}
	if out, dispatched := run(t, mainState{headRuns: "0", parentRuns: "1", changed: config}, 40*60); !dispatched {
		t.Errorf("a head changing .tick/runners.toml (read by drift guards) with no CI run was left alone:\n%s", out)
	}
}

// The watchdog is wired: a schedule runs it with the token scope that can
// dispatch CI.
func TestTheWatchdogRunsOnASchedule(t *testing.T) {
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci-watchdog.yml"))
	if err != nil {
		t.Fatal(err)
	}
	wf := string(raw)
	for _, want := range []string{"schedule:", "cron:", "actions: write", ".github/scripts/main-ci-watchdog.sh"} {
		if !strings.Contains(wf, want) {
			t.Errorf("ci-watchdog.yml does not carry %q", want)
		}
	}
}
