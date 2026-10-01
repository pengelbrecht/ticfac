package reconcile

import (
	"strings"
	"testing"
)

// A changelog conflict is the union of both sides' entries, made by git, not
// a resolve job (the hn6 follow-up to run_ee8e's report conflict).
//
// Two ticks of one wave that each add a CHANGELOG entry under the same
// heading edit the same lines, so git calls it a content conflict and the
// run used to dispatch a model at the ceiling tier to keep both lines. The
// answer is mechanical — both entries stand — and git's own union driver
// gives it. contracts/CHANGELOG.md is one such file in this repository.

const changelogBase = "# Changelog\n\n## Unreleased\n\n## 1.0.0\n\n- the first cut\n"

func changelogWith(entry string) string {
	return strings.Replace(changelogBase, "## Unreleased\n", "## Unreleased\n\n- "+entry+"\n", 1)
}

// short: one small git repository and a single merge, no harness, no runner
func TestAChangelogConflictMergesAsTheUnionOfBothEntries(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"CHANGELOG.md", "contracts/CHANGELOG.md", "CHANGES", "docs/HISTORY.txt"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			dir, g := mergeConflictRepo(t,
				map[string]string{path: changelogBase},
				map[string]string{path: changelogWith("the epic's entry")},
				map[string]string{path: changelogWith("the attempt's entry"), "work.txt": "work\n"},
				nil)
			r, epicHead, head := reportMergeReconciler(t, dir, g)

			merged, conflict, err := r.mergeInWorktree("t1", 1, "side", head, epicHead)
			if err != nil || conflict != nil {
				t.Fatalf("a changelog conflict stopped the merge: conflict %+v, err %v", conflict, err)
			}
			got, _ := showAt(dir, merged, path)
			for _, want := range []string{"- the epic's entry", "- the attempt's entry", "- the first cut"} {
				if !strings.Contains(got, want) {
					t.Errorf("%s lost %q:\n%s", path, want, got)
				}
			}
			if conflictMarkersIn(got) {
				t.Errorf("%s carries conflict markers:\n%s", path, got)
			}
		})
	}
}

// A changelog conflict next to a real one is merged by git, and only the
// code is handed on: the resolve job sees no changelog markers.
//
// short: one small git repository and a single merge, no harness, no runner
func TestAChangelogBesideACodeConflictHandsOnOnlyTheCode(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"CHANGELOG.md": changelogBase, "shared.txt": "base\n"},
		map[string]string{"CHANGELOG.md": changelogWith("the epic's entry"), "shared.txt": "main\n"},
		map[string]string{"CHANGELOG.md": changelogWith("the attempt's entry"), "shared.txt": "side\n"},
		nil)
	r, epicHead, head := reportMergeReconciler(t, dir, g)

	_, conflict, err := r.mergeInWorktree("t1", 1, "side", head, epicHead)
	if err != nil || conflict == nil {
		t.Fatalf("want the code conflict handed on, got conflict %+v, err %v", conflict, err)
	}
	if len(conflict.Files) != 1 || conflict.Files[0] != "shared.txt" {
		t.Errorf("the conflict names %v, want [shared.txt]", conflict.Files)
	}

	conflicted, err := r.conflictedTree(epicHead, head, attemptHandle{TickID: "t1", Attempt: 1,
		WriteRef: "refs/heads/ticfac/run-r/tick-t1/resolve-1"})
	if err != nil {
		t.Fatal(err)
	}
	log, _ := showAt(dir, conflicted, "CHANGELOG.md")
	if conflictMarkersIn(log) || !strings.Contains(log, "the epic's entry") || !strings.Contains(log, "the attempt's entry") {
		t.Errorf("the resolve job's tree carries the changelog as:\n%s", log)
	}
}

// Only a changelog is a union: a file whose name merely starts like one is
// code, and its conflict is still a conflict.
//
// short: one small git repository and a single merge, no harness, no runner
func TestAFileThatOnlyLooksLikeAChangelogStillConflicts(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"changelog_gen.go": "package x // base\n"},
		map[string]string{"changelog_gen.go": "package x // main\n"},
		map[string]string{"changelog_gen.go": "package x // side\n"},
		nil)
	r, epicHead, head := reportMergeReconciler(t, dir, g)

	_, conflict, err := r.mergeInWorktree("t1", 1, "side", head, epicHead)
	if err != nil || conflict == nil || len(conflict.Files) != 1 || conflict.Files[0] != "changelog_gen.go" {
		t.Fatalf("want a content conflict on changelog_gen.go, got conflict %+v, err %v", conflict, err)
	}
}
