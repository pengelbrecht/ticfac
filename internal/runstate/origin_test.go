package runstate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A real origin: a bare repository in a temp directory, with the EpicRun
// integration branch on it, and one clone per actor.
//
// The whole point of this file is that nothing here is a model. The guard is
// `git push --force-with-lease` against a repository git is enforcing, the
// views are real fetches, and a refusal is git refusing.

const testBranch = "epic/qeu"

type origin struct {
	t      *testing.T
	bare   string
	root   string
	branch string
	clones int
}

func newOrigin(t *testing.T) *origin {
	t.Helper()
	shorttest.EndToEnd(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the path")
	}
	root := t.TempDir()
	o := &origin{t: t, root: root, bare: filepath.Join(root, "origin.git"), branch: testBranch}

	gitRun(t, root, "init", "--quiet", "--bare", o.bare)

	// The bare origin is pinned in its OWN config, not through the
	// environment, because the environment cannot reach it: a push to a
	// local path starts git-receive-pack with the repo-local environment
	// stripped — and GIT_CONFIG_COUNT is one of the variables git's local
	// transport removes (connect.c pushes local_repo_env, and
	// CONFIG_COUNT_ENVIRONMENT is in it) — so gitbin.WithNoAutoMaintenance's
	// entries never arrive at the process that ends a push by starting
	// `git maintenance run --auto --detach` IN THIS REPOSITORY (tick 35l;
	// the incident the pin prevents is reconcile's qsn). What receive-pack
	// consults is maintenance.auto (prepare_auto_maintenance, upstream
	// v2.50.1), and receive.autogc is the outer gate that says the same to a
	// git old enough to run `gc --auto` directly; gc.auto=0 neuters that
	// child wherever it is still started. Every push this package's tests
	// make — gitRun's seeding and the store's own — lands here, and this
	// repository is a t.TempDir the test is about to delete.
	gitRun(t, o.bare, "config", "maintenance.auto", "false")
	gitRun(t, o.bare, "config", "receive.autogc", "false")
	gitRun(t, o.bare, "config", "gc.auto", "0")

	// Seed the branch: an EpicRun integration branch always exists before the
	// run does, and a store guards against a ref, not against nothing.
	seed := filepath.Join(root, "seed")
	gitRun(t, root, "init", "--quiet", "-b", o.branch, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("the target repository\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "-A")
	gitRun(t, seed, "commit", "--quiet", "-m", "seed")
	gitRun(t, seed, "push", "--quiet", o.bare, o.branch)
	return o
}

// actor is one reconciler: its own clone, its own store, its own view of
// origin. Actors share origin and nothing else — which is what makes "B's
// update is refused because A moved the ref after B fetched" expressible.
func (o *origin) actor(name string, runID string) *Store {
	o.t.Helper()
	o.clones++
	dir := filepath.Join(o.root, "actor-"+name+"-"+strconv.Itoa(o.clones))
	// --no-checkout: the store never touches a working tree, and a clone
	// without one makes that impossible to get wrong by accident.
	gitRun(o.t, o.root, "clone", "--quiet", "--no-checkout", o.bare, dir)

	at := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	s, err := Open(Options{
		Repo:   dir,
		Remote: "origin",
		Branch: o.branch,
		RunID:  runID,
		Now:    func() time.Time { at = at.Add(time.Minute); return at },
	})
	if err != nil {
		o.t.Fatalf("open a store for %s: %v", name, err)
	}
	return s
}

// files is everything under .ticfac/ on origin, decoded. Read from the bare
// repository, because durable means pushed and this is the only place that
// answers whether a record exists.
func (o *origin) files() map[string]map[string]any {
	o.t.Helper()
	out := gitRun(o.t, o.bare, "ls-tree", "-r", o.branch, "--", Root)
	files := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		_, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var document map[string]any
		raw := gitRun(o.t, o.bare, "show", o.branch+":"+path)
		if err := json.Unmarshal([]byte(raw), &document); err != nil {
			o.t.Fatalf("%s on origin is not JSON: %v", path, err)
		}
		files[path] = document
	}
	return files
}

// commits counts the run's commits on the integration branch: one per write,
// because write-commit-push is one operation.
func (o *origin) commits() int {
	o.t.Helper()
	out := gitRun(o.t, o.bare, "rev-list", "--count", o.branch)
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		o.t.Fatalf("count commits on %s: %v", o.branch, err)
	}
	return n - 1 // the seed
}

func (o *origin) tags() []string {
	o.t.Helper()
	out := strings.TrimSpace(gitRun(o.t, o.bare, "tag", "--list"))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// gitCmd is the ONE way this package's tests start a git, and the only shape
// a test may build one in that is not an explicit CONTROL (unpinnedGit,
// maintenance_test.go). Everything the fixtures run by hand — the bare origin
// newOrigin builds, the seed clone it commits in, the pushes and fetches the
// views read with — goes through here, so the rule below is stated once
// instead of at every call site. maintenance_guard_test.go holds both of
// those facts down.
//
// The rule is gitbin.WithNoAutoMaintenance, and tick 35l (reconcile's qsn
// is the incident) is what it is for. A `git commit`, `git push` and `git
// fetch` each END by starting `git maintenance run --auto --detach` — git's
// own daemonize: fork, setsid, and the parent returns while the child goes
// on writing in the repository's object store. A test that returns with one
// of those alive is the CI failure reconcile paid two days for: t.TempDir's
// RemoveAll walks the repository while the maintenance writes into it —
// "directory not empty" — and the failing test is the NEXT one, wherever
// the RemoveAll happened to lose the race. See internal/reconcile's
// harnessCommand for the incident told in full.
//
// Said through the ENVIRONMENT rather than through `-c` on purpose. `-c`
// reaches one command line; GIT_CONFIG_COUNT is read above every config
// file there is and is inherited by every git those commands start in turn.
type gitCmd struct {
	args []string
	env  []string
}

func gitCommand(args ...string) gitCmd {
	return gitCmd{args: args,
		env: gitbin.WithNoAutoMaintenance(append(os.Environ(),
			"GIT_AUTHOR_NAME=ticfac test", "GIT_AUTHOR_EMAIL=ticfac@example.com",
			"GIT_COMMITTER_NAME=ticfac test", "GIT_COMMITTER_EMAIL=ticfac@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_TERMINAL_PROMPT=0"))}
}

// withEnv appends environment entries AFTER the pins, which is where a
// caller's own statement belongs: git reads GIT_CONFIG_COUNT above every
// config file, and appending keeps the numbering WithNoAutoMaintenance
// produced intact. Its one caller is the guard, which turns tracing on for
// the invocations it is watching.
func (c gitCmd) withEnv(entries ...string) gitCmd {
	c.env = append(append([]string{}, c.env...), entries...)
	return c
}

func (c gitCmd) command(dir string) *exec.Cmd {
	cmd := exec.Command(gitbin.Path(), c.args...)
	cmd.Dir = dir
	cmd.Env = c.env
	return cmd
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCommand(args...).command(dir).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}
