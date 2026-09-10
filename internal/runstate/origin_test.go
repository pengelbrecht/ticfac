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
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the path")
	}
	root := t.TempDir()
	o := &origin{t: t, root: root, bare: filepath.Join(root, "origin.git"), branch: testBranch}

	gitRun(t, root, "init", "--quiet", "--bare", o.bare)

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

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ticfac test", "GIT_AUTHOR_EMAIL=ticfac@example.com",
		"GIT_COMMITTER_NAME=ticfac test", "GIT_COMMITTER_EMAIL=ticfac@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}
