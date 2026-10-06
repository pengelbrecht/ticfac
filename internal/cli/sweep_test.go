package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/refsweep"
)

func sweepGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// `ticfac sweep refs --dry-run --json` answers one document naming what would
// go and deletes nothing; without --dry-run the closed epic's merged branch
// is gone and a person's branch is not. The tracker seam is package state, so
// this test is serial.
func TestSweepRefsCommand(t *testing.T) {
	root := t.TempDir()
	origin, work := filepath.Join(root, "origin.git"), filepath.Join(root, "work")
	sweepGit(t, root, "init", "--quiet", "--bare", "--initial-branch=main", origin)
	sweepGit(t, root, "init", "--quiet", "--initial-branch=main", work)
	sweepGit(t, work, "remote", "add", "origin", origin)
	sweepGit(t, work, "commit", "--quiet", "--allow-empty", "-m", "base")
	sweepGit(t, work, "push", "--quiet", "origin", "HEAD:refs/heads/main",
		"HEAD:refs/heads/ticfac/run-epic-gone/tick-a/attempt-1", "HEAD:refs/heads/someones/branch")

	saved := sweepEpicLookup
	t.Cleanup(func() { sweepEpicLookup = saved })
	sweepEpicLookup = func(string) func(context.Context, string) (refsweep.Epic, error) {
		return func(_ context.Context, id string) (refsweep.Epic, error) {
			return refsweep.Epic{Known: true, Closed: id == "gone"}, nil
		}
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"sweep", "refs", "--dry-run", "--json", "--repo", work}, &stdout, &stderr); code != 0 {
		t.Fatalf("sweep refs --dry-run exited %d: %s", code, stderr.String())
	}
	var doc struct {
		Schema  string `json:"schema"`
		State   string `json:"state"`
		Planned int    `json:"planned"`
		Deleted int    `json:"deleted"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("not one document: %v\n%s", err, stdout.String())
	}
	if doc.Schema != "ticfac.sweep-refs.v1" || doc.State != "done" || doc.Planned != 1 || doc.Deleted != 0 {
		t.Fatalf("the dry run's document is %+v", doc)
	}
	if sweepGit(t, origin, "for-each-ref", "refs/heads/ticfac/") == "" {
		t.Fatal("a dry run deleted a ref")
	}

	stdout.Reset()
	if code := Run([]string{"sweep", "refs", "--repo", work}, &stdout, &stderr); code != 0 {
		t.Fatalf("sweep refs exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "deleted refs/heads/ticfac/run-epic-gone/tick-a/attempt-1") {
		t.Errorf("the sweep does not name what it deleted:\n%s", stdout.String())
	}
	if left := sweepGit(t, origin, "for-each-ref", "refs/heads/ticfac/"); left != "" {
		t.Errorf("the closed epic's merged branch is still on origin: %s", left)
	}
	if sweepGit(t, origin, "for-each-ref", "refs/heads/someones/") == "" {
		t.Error("a person's branch was deleted")
	}
}
