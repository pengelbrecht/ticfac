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

// fakeGh answers the three reads the script makes and records a dispatch.
// runs is the jq answer for "CI runs of the head that were not cancelled".
func fakeGh(t *testing.T, dir string, runs string) {
	t.Helper()
	script := `#!/bin/sh
case "$*" in
  "api repos/o/r/commits/main --jq .sha") echo ` + head + ` ;;
  "api repos/o/r/commits/` + head + ` --jq "*) echo ` + strconv.Itoa(landed) + ` ;;
  "api repos/o/r/actions/workflows/ci.yml/runs?head_sha=` + head + `&per_page=100 --jq "*) echo ` + runs + ` ;;
  "workflow run ci.yml --repo o/r --ref main") echo dispatched >> "$GH_RECORD" ;;
  *) echo "unexpected gh call: $*" >&2; exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, runs string, age int) (string, bool) {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fakeGh(t, bin, runs)
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

// The 03f56af4 shape: main's head has no CI run at all, long after it landed.
// CI is dispatched on main.
func TestAMainHeadWithNoCIRunGetsOneDispatched(t *testing.T) {
	out, dispatched := run(t, "0", 40*60)
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
	if out, dispatched := run(t, "1", 40*60); dispatched {
		t.Errorf("CI was dispatched on a head that already has a run:\n%s", out)
	}
	if out, dispatched := run(t, "0", 60); dispatched {
		t.Errorf("CI was dispatched on a head one minute old, inside the grace period:\n%s", out)
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
