package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The workflow half of the integrated gate (tick cwa).
//
// The declared commands prove the TREE, and a command that compiles "everything
// that exists" cannot fail over a path that does not — so a CI workflow naming
// a package the tree deleted keeps the repo's own PRs red forever while every
// gate stays green. That is the pwp incident, and these tests pin the
// structural check that closes it: the gate refuses, before any close, over a
// workflow whose package patterns the gated tree cannot resolve.

// workflowNamingADeletedPackage is a CI configuration that is green as far as
// the tree is concerned and can never be green on a forger: its go step names a
// package the tree does not carry. It carries the other two shapes the check
// must NOT refuse over: the root wildcard `./...`, which resolves as long as
// the tree has a root, and a bare `./cmd/ticfac` reference, which can be a
// build OUTPUT in an honest workflow and is therefore out of this check's
// scope. A comment names a second deleted package: comments are not commands,
// and CI does not fail over one.
const workflowNamingADeletedPackage = `name: CI

on: [push]

jobs:
  go:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      # a comment naming a package nothing runs: ./gone/by/comment/...
      - name: go test
        run: go test ./... ./internal/gather/... ./cmd/ticfac
`

// commitWorkflow lands files on the fixture repo's base branch, where every
// attempt's merge carries them. The workflow goes on main before the run
// starts, exactly where the pwp incident's rotting workflow sat.
func commitWorkflow(t *testing.T, repo *testRepo, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(repo.Dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, full, content)
	}
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "the repo's own CI configuration")
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", "main")
}

// A workflow naming a package the tree does not carry fails the integrated
// gate — the acceptance test for tick cwa, and the pwp incident's exact shape:
// every declared command compiles only what exists and stays green while the
// repo's own CI is red on every push and pull_request, so the gate has to see
// the workflow itself.
func TestTheGateRefusesAWorkflowThatNamesADeletedPackage(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	commitWorkflow(t, f.Repo, map[string]string{
		".github/workflows/ci.yml": workflowNamingADeletedPackage,
	})

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedGate {
		t.Fatalf("the run failed as %+v, want %s: a green gate over a workflow CI can never run "+
			"is the blindness this tick closes", result.Failure, RefusedGate)
	}
	message := result.Failure.Message
	if !strings.Contains(message, "./internal/gather/...") {
		t.Errorf("the refusal does not name the deleted package it refused over: %s", message)
	}
	if !strings.Contains(message, ".github/workflows/ci.yml") {
		t.Errorf("the refusal does not name the workflow file that carries it: %s", message)
	}
	if strings.Contains(message, "./gone/by/comment/...") {
		t.Errorf("the refusal names a package only a comment carries: %s", message)
	}

	// The tick is NOT closed behind the refusal: the tracker is the authority
	// on closure, and it must not say closed.
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Fatalf("a1 closed behind a refused gate; the refusal says the close is refused, the tracker must agree")
	}

	// And the rejection is durable, so a restart reads it from origin rather
	// than redispatching over work that is already on the integration branch.
	store, err := runstate.Open(runstate.Options{
		Repo: f.Repo.Dir, Remote: "origin", Branch: "epic/qeu", RunID: "r-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("no checkpoint on origin: %v", err)
	}
	rejected := false
	for _, ts := range checkpoint.Ticks {
		if ts.TickID == "a1" && ts.State == "rejected" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("the checkpoint on origin does not say a1 was rejected: %+v", checkpoint.Ticks)
	}
}

// The control on the other side: the same workflow, a tree that carries what
// it names. A check that refused honest workflows would block the close of
// every tick in the repo, so the resolving case is asserted as loudly as the
// refusing one.
func TestTheGatePassesAWorkflowWhosePackagePatternsResolve(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	commitWorkflow(t, f.Repo, map[string]string{
		".github/workflows/ci.yml": workflowNamingADeletedPackage,
		"internal/gather/main.go":  "package gather\n",
	})

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s); a workflow whose patterns resolve must not be refused",
			result.State, result.Reason)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Fatalf("a1 is %s; the gate passed and the close is owed", current.Status)
	}
}

// What the extraction reads, and what it refuses to read. A false positive
// here is a refusal of an honest workflow, so every narrowing is pinned.
func TestWorkflowPackagePatternExtraction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		document string
		want     []string
	}{
		{
			name:     "a package wildcard on a run line",
			document: "        run: go test ./internal/gather/...\n",
			want:     []string{"./internal/gather/..."},
		},
		{
			name:     "several on one line, first-seen order",
			document: "        run: go test ./b/... ./a/...\n",
			want:     []string{"./b/...", "./a/..."},
		},
		{
			name:     "duplicates collapse",
			document: "        run: go test ./a/... ./a/...\n        run: go vet ./a/...\n",
			want:     []string{"./a/..."},
		},
		{
			name:     "the root wildcard always resolves",
			document: "        run: go test ./...\n",
			want:     nil,
		},
		{
			name:     "a comment line is not a command",
			document: "      # go test ./gone/...\n        run: go test ./a/...\n",
			want:     []string{"./a/..."},
		},
		{
			name:     "an inline comment ends the line",
			document: "        run: go test ./a/... # ./gone/...\n",
			want:     []string{"./a/..."},
		},
		{
			name:     "a quoted pattern still counts",
			document: "        run: go test \"./a/...\"\n",
			want:     []string{"./a/..."},
		},
		{
			name:     "a module path is not a repo path",
			document: "        run: go test github.com/example/thing/...\n",
			want:     nil,
		},
		{
			name:     "a bare path is not a package wildcard",
			document: "        run: go build -o ./bin/tool ./cmd/tool\n",
			want:     nil,
		},
		{
			name:     "a workflow with no wildcard names nothing",
			document: "        run: make test-short\n",
			want:     nil,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := workflowPackagePatterns(testCase.document)
			if len(got) != len(testCase.want) {
				t.Fatalf("workflowPackagePatterns(%q) = %q, want %q", testCase.document, got, testCase.want)
			}
			for i := range got {
				if got[i] != testCase.want[i] {
					t.Fatalf("workflowPackagePatterns(%q) = %q, want %q", testCase.document, got, testCase.want)
				}
			}
		})
	}
}
