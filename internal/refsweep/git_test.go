package refsweep

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/gittest"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// fakeRemote is a bare origin and a work clone that pushes to it.
type fakeRemote struct {
	t      *testing.T
	origin string
	work   string
}

func newFakeRemote(t *testing.T) *fakeRemote {
	t.Helper()
	root := t.TempDir()
	f := &fakeRemote{t: t, origin: filepath.Join(root, "origin.git"), work: filepath.Join(root, "work")}
	f.git(root, "init", "--quiet", "--bare", "--initial-branch=main", f.origin)
	f.git(root, "init", "--quiet", "--initial-branch=main", f.work)
	f.git(f.work, "remote", "add", "origin", f.origin)
	f.commit("README", "base")
	f.git(f.work, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	return f
}

func (f *fakeRemote) git(dir string, args ...string) string {
	f.t.Helper()
	// gittest.Run states the whole hermetic environment (tick pqs): the
	// entries this builder spelled out and the ones it never did.
	return strings.TrimSpace(gittest.Run(f.t, dir, args...))
}

// commit writes one file on the current HEAD and answers the new sha.
func (f *fakeRemote) commit(path, content string) string {
	f.t.Helper()
	full := filepath.Join(f.work, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(f.work, "add", "-A")
	f.git(f.work, "commit", "--quiet", "-m", path)
	return f.git(f.work, "rev-parse", "HEAD")
}

func (f *fakeRemote) checkout(rev string) { f.git(f.work, "checkout", "--quiet", "--detach", rev) }

func (f *fakeRemote) push(sha, ref string) {
	f.git(f.work, "push", "--quiet", "--force", "origin", sha+":"+ref)
}

func (f *fakeRemote) checkpoint(run, epic string, state runstate.State, seq int, at time.Time) string {
	return f.commit(runstate.CheckpointPath(run), fmt.Sprintf(
		`{"schema_version":1,"run_id":%q,"epic_id":%q,"sequence":%d,"state":%q,"reason":"x","updated_at":%q}`,
		run, epic, seq, state, at.UTC().Format(time.RFC3339)))
}

func (f *fakeRemote) remoteRefs() []string {
	out := f.git(f.origin, "for-each-ref", "--format=%(refname)")
	refs := strings.Fields(out)
	sort.Strings(refs)
	return refs
}

func contains(refs []string, ref string) bool {
	for _, r := range refs {
		if r == ref {
			return true
		}
	}
	return false
}

// The done-when of tick 6is against a real (bare) origin: a closed epic's
// merged and empty job branches go, its unmerged one survives until the grace
// expires, a stopped-but-resumable run keeps everything, and nothing that is
// not ticfac's — main, epic branches, tags, a person's branch — is touched.
func TestSweepRetiresEndedRunsOnAFakeRemote(t *testing.T) {
	f := newFakeRemote(t)
	base := f.git(f.work, "rev-parse", "HEAD")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	// Epic "shut": its run merged one attempt into epic/shut, left one empty
	// and one rejected (unmerged); the epic is closed.
	f.checkout(base)
	merged := f.commit("a.txt", "a")
	f.push(merged, "refs/heads/ticfac/run-epic-shut/tick-a/attempt-1")
	f.push(base, "refs/heads/ticfac/run-epic-shut/tick-b/attempt-1") // empty
	f.push(base, "refs/ticfac/start/run-epic-shut/tick-a/"+base)
	f.checkout(merged)
	epicHead := f.checkpoint("epic-shut", "shut", runstate.StateCompleted, 5, now.Add(-20*24*time.Hour))
	f.push(epicHead, "refs/heads/epic/shut")
	f.checkout(base)
	rejected := f.commit("r.txt", "rejected")
	f.push(rejected, "refs/heads/ticfac/run-epic-shut/tick-c/attempt-1")
	f.push(rejected, "refs/ticfac/wip/run-epic-shut/tick-c/attempt-1")

	// Epic "stop": its only run stopped (failed) and may be resumed by id.
	f.checkout(base)
	stopEpic := f.checkpoint("epic-stop", "stop", runstate.StateFailed, 3, now.Add(-72*time.Hour))
	f.push(stopEpic, "refs/heads/epic/stop")
	f.push(base, "refs/heads/ticfac/run-epic-stop/tick-a/attempt-1")
	f.checkout(base)
	stopWork := f.commit("s.txt", "s")
	f.push(stopWork, "refs/heads/ticfac/run-epic-stop/tick-b/attempt-1")

	// Things that are not ticfac's.
	f.push(base, "refs/heads/someones/branch")
	f.push(base, "refs/heads/tick/7mj")
	f.git(f.work, "tag", "ticfac/run-epic-shut", epicHead)
	f.git(f.work, "push", "--quiet", "origin", "refs/tags/ticfac/run-epic-shut")

	closedAt := now.Add(-10 * 24 * time.Hour)
	opts := Options{
		Git:  Git{Repo: f.work, Remote: "origin"},
		Base: "main",
		Epic: func(_ context.Context, id string) (Epic, error) {
			if id == "shut" {
				return Epic{Known: true, Closed: true, ClosedAt: closedAt}, nil
			}
			return Epic{Known: true}, nil
		},
		Now: now,
	}
	before := f.remoteRefs()

	// A dry run deletes nothing and plans the merged, empty and start refs.
	dry := opts
	dry.DryRun = true
	report, err := Sweep(context.Background(), dry)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.remoteRefs(); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("a dry run changed origin:\nbefore %v\nafter  %v", before, got)
	}
	var planned []string
	for _, ref := range report.Doomed() {
		planned = append(planned, ref.Name)
	}
	wantPlanned := []string{
		"refs/heads/ticfac/run-epic-shut/tick-a/attempt-1",
		"refs/heads/ticfac/run-epic-shut/tick-b/attempt-1",
		"refs/ticfac/start/run-epic-shut/tick-a/" + base,
	}
	if strings.Join(planned, ",") != strings.Join(wantPlanned, ",") {
		t.Fatalf("the dry run planned %v, want %v\n%+v", planned, wantPlanned, report.Verdicts)
	}

	// For real, inside the grace: exactly those go.
	if _, err := Sweep(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	after := f.remoteRefs()
	for _, gone := range wantPlanned {
		if contains(after, gone) {
			t.Errorf("%s is still on origin", gone)
		}
	}
	for _, kept := range []string{
		"refs/heads/ticfac/run-epic-shut/tick-c/attempt-1", // unmerged, inside the grace
		"refs/ticfac/wip/run-epic-shut/tick-c/attempt-1",
		"refs/heads/ticfac/run-epic-stop/tick-a/attempt-1", // resumable run
		"refs/heads/ticfac/run-epic-stop/tick-b/attempt-1",
		"refs/heads/main", "refs/heads/epic/shut", "refs/heads/epic/stop",
		"refs/heads/someones/branch", "refs/heads/tick/7mj", "refs/tags/ticfac/run-epic-shut",
	} {
		if !contains(after, kept) {
			t.Errorf("%s was deleted; it must survive", kept)
		}
	}
	if len(after) != len(before)-len(wantPlanned) {
		t.Errorf("origin went from %d to %d refs, want %d", len(before), len(after), len(before)-len(wantPlanned))
	}
	// The sweep left no scratch refs behind in the checkout.
	if out := f.git(f.work, "for-each-ref", "refs/ticfac/peek/"); out != "" {
		t.Errorf("the sweep left scratch refs:\n%s", out)
	}

	// Past the grace, the unmerged ones go too; the resumable run still
	// keeps everything.
	late := opts
	late.Now = closedAt.Add(DefaultGrace + time.Hour)
	if _, err := Sweep(context.Background(), late); err != nil {
		t.Fatal(err)
	}
	after = f.remoteRefs()
	for _, gone := range []string{
		"refs/heads/ticfac/run-epic-shut/tick-c/attempt-1", "refs/ticfac/wip/run-epic-shut/tick-c/attempt-1",
	} {
		if contains(after, gone) {
			t.Errorf("%s outlived the grace", gone)
		}
	}
	for _, kept := range []string{
		"refs/heads/ticfac/run-epic-stop/tick-a/attempt-1", "refs/heads/ticfac/run-epic-stop/tick-b/attempt-1",
	} {
		if !contains(after, kept) {
			t.Errorf("%s of a stopped, resumable run was deleted", kept)
		}
	}
}

// A completed run's end retires its own merged refs and nothing else: not
// another run's, not its unmerged attempt (the epic is still open).
func TestRetireRunTakesOnlyTheRunsOwnMergedRefs(t *testing.T) {
	f := newFakeRemote(t)
	base := f.git(f.work, "rev-parse", "HEAD")
	work := f.commit("w.txt", "w")
	f.push(work, "refs/heads/ticfac/run-epic-e/tick-a/attempt-1")
	f.push(work, "refs/heads/ticfac/run-epic-other/tick-a/attempt-1")
	f.push(work, "refs/heads/tick/e/attempt-1/a")
	f.push(work, "refs/heads/epic/e")
	f.checkout(base)
	loose := f.commit("l.txt", "l")
	f.push(loose, "refs/heads/ticfac/run-epic-e/tick-b/attempt-1")

	report, err := RetireRun(context.Background(), Options{Git: Git{Repo: f.work, Remote: "origin"}},
		Run{ID: "epic-e", Epic: "e", State: runstate.StateCompleted}, []string{work})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Deleted) != 1 || report.Deleted[0].Name != "refs/heads/ticfac/run-epic-e/tick-a/attempt-1" {
		t.Fatalf("the run end deleted %+v, want only its merged attempt\n%+v", report.Deleted, report.Verdicts)
	}
	after := f.remoteRefs()
	for _, kept := range []string{
		"refs/heads/ticfac/run-epic-other/tick-a/attempt-1", "refs/heads/tick/e/attempt-1/a",
		"refs/heads/ticfac/run-epic-e/tick-b/attempt-1", "refs/heads/epic/e",
	} {
		if !contains(after, kept) {
			t.Errorf("%s was deleted at another run's end", kept)
		}
	}
}

// A ref that moved after the verdict was made is not deleted: the lease is
// on the sha the plan judged.
func TestDeleteHoldsTheLease(t *testing.T) {
	f := newFakeRemote(t)
	base := f.git(f.work, "rev-parse", "HEAD")
	f.push(base, "refs/heads/ticfac/run-epic-e/tick-a/attempt-1")
	moved := f.commit("m.txt", "m")
	f.push(moved, "refs/heads/ticfac/run-epic-e/tick-a/attempt-1")
	g := Git{Repo: f.work, Remote: "origin"}
	deleted, failed := g.Delete(context.Background(), []Ref{{Name: "refs/heads/ticfac/run-epic-e/tick-a/attempt-1", SHA: base}})
	if len(deleted) != 0 || len(failed) != 1 {
		t.Fatalf("a moved ref was deleted under a stale lease: deleted %v, failed %v", deleted, failed)
	}
	if !contains(f.remoteRefs(), "refs/heads/ticfac/run-epic-e/tick-a/attempt-1") {
		t.Fatal("the moved ref is gone")
	}
}
