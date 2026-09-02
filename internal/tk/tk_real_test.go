package tk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestManifestCommandsAgainstRealTk is the behavioural half of the manifest
// reader. It is deliberately an integration test: the fake runner tests the
// client mechanics, while this table proves that a real tk binary produces
// documents accepted by every published JSON schema and that both exit-code
// drivers are callable.
func TestManifestCommandsAgainstRealTk(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real tk manifest integration in -short mode")
	}
	tkPath, err := exec.LookPath("tk")
	if err != nil {
		t.Skip("tk binary not on PATH; skipping real tk manifest integration")
	}

	repo := t.TempDir()
	gitInit(t, repo)
	init := exec.Command(tkPath, "init")
	init.Dir = repo
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("tk init: %v\n%s", err, output)
	}
	seedFixture(t, repo)

	client, err := New(Options{Binary: tkPath, Dir: repo})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	tests := []struct {
		name  string
		call  func() error
		check func(t *testing.T)
	}{
		{
			name: "version",
			call: func() error {
				version, err := client.Version(ctx)
				if err == nil && (version.Contract != Contract || version.Tk == "") {
					return fmt.Errorf("unexpected cached version %#v", version)
				}
				return err
			},
		},
		{
			name: "show",
			call: func() error {
				tick, err := client.Show(ctx, "a01")
				if err == nil && tick.ID != "a01" {
					return fmt.Errorf("show returned %q", tick.ID)
				}
				return err
			},
		},
		{
			name: "list",
			call: func() error {
				list, err := client.List(ctx)
				if err == nil && len(list.Ticks) < 3 {
					return fmt.Errorf("list returned %d ticks, want fixture records", len(list.Ticks))
				}
				return err
			},
		},
		{
			name: "ready",
			call: func() error {
				list, err := client.Ready(ctx)
				if err == nil && len(list.Ticks) == 0 {
					return fmt.Errorf("ready returned no fixture task")
				}
				return err
			},
		},
		{
			name: "next",
			call: func() error {
				next, err := client.Next(ctx)
				if err == nil && (next == nil || next.ID == "") {
					return fmt.Errorf("next returned %#v", next)
				}
				return err
			},
		},
		{
			name: "deps",
			call: func() error {
				deps, err := client.Deps(ctx, "a01")
				if err == nil && len(deps.Blocks) == 0 {
					return fmt.Errorf("deps returned no dependant fixture")
				}
				return err
			},
		},
		{
			name: "graph",
			call: func() error {
				graph, err := client.Graph(ctx, "e01")
				if err == nil && graph.Epic.ID != "e01" {
					return fmt.Errorf("graph returned epic %q", graph.Epic.ID)
				}
				return err
			},
		},
		{
			name: "status",
			call: func() error {
				_, err := client.Status(ctx)
				return err
			},
		},
		{
			name: "claim",
			call: func() error {
				tick, err := client.Claim(ctx, "a01", "fixture-worker")
				if err == nil && (tick.Status != "in_progress" || tick.Owner != "fixture-worker") {
					return fmt.Errorf("claim returned status=%q owner=%q", tick.Status, tick.Owner)
				}
				return err
			},
		},
		{
			name: "update",
			call: func() error {
				tick, err := client.Update(ctx, "a01", "updated by fixture")
				if err == nil && !strings.Contains(tick.Notes, "updated by fixture") {
					return fmt.Errorf("update returned notes %q", tick.Notes)
				}
				return err
			},
		},
		{
			name: "note",
			call: func() error {
				_, err := client.Note(ctx, "a01", "noted by fixture")
				return err
			},
		},
		{
			name: "close",
			call: func() error {
				tick, err := client.Close(ctx, "a01")
				if err == nil && tick.Status != "closed" {
					return fmt.Errorf("close returned status %q", tick.Status)
				}
				return err
			},
		},
		{
			name: "reopen",
			call: func() error {
				tick, err := client.Reopen(ctx, "a01")
				if err == nil && tick.Status != "open" {
					return fmt.Errorf("reopen returned status %q", tick.Status)
				}
				return err
			},
		},
		{
			name: "merge-file",
			call: func() error {
				return client.MergeFile(ctx,
					filepath.Join(repo, "base.txt"),
					filepath.Join(repo, "ours.txt"),
					filepath.Join(repo, "theirs.txt"),
					filepath.Join(repo, "merged.txt"))
			},
		},
		{
			name: "merge-activity",
			call: func() error {
				return client.MergeActivity(ctx,
					filepath.Join(repo, "ancestor.jsonl"),
					filepath.Join(repo, "current.jsonl"),
					filepath.Join(repo, "other.jsonl"),
					filepath.Join(repo, "activity.jsonl"))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}
			if test.check != nil {
				test.check(t)
			}
		})
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.name", "ticfac fixture")
	runGit("config", "user.email", "fixture@example.com")
	runGit("remote", "add", "origin", "https://example.com/example/fixture.git")
	writeFixtureFile(t, dir, "fixture.txt", "fixture repository\n")
	runGit("add", "fixture.txt")
	runGit("commit", "-q", "-m", "fixture repository")
}

func seedFixture(t *testing.T, repo string) {
	t.Helper()
	issues := filepath.Join(repo, ".tick", "issues")
	if err := os.MkdirAll(issues, 0o755); err != nil {
		t.Fatalf("mkdir fixture issues: %v", err)
	}
	writeTick := func(id, title, kind, status string, blockedBy []string, parent string) {
		tick := map[string]any{
			"id": id, "title": title, "status": status, "priority": 1,
			"type": kind, "owner": "fixture", "created_by": "fixture",
			"created_at": "2026-09-02T00:00:00Z", "updated_at": "2026-09-02T00:00:00Z",
		}
		if len(blockedBy) != 0 {
			tick["blocked_by"] = blockedBy
		}
		if parent != "" {
			tick["parent"] = parent
		}
		data, err := json.MarshalIndent(tick, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(issues, id+".json"), append(data, '\n'), 0o644); err != nil {
			t.Fatalf("write %s: %v", id, err)
		}
	}
	writeTick("e01", "Fixture epic", "epic", "open", nil, "")
	writeTick("a01", "Fixture task", "task", "open", nil, "e01")
	writeTick("b01", "Blocked fixture task", "task", "open", []string{"a01"}, "e01")
	writeTick("d01", "Closed dependency", "task", "closed", nil, "")

	writeFixtureFile(t, repo, "base.txt", fixtureTickJSON("m01", "merge base", "base description"))
	writeFixtureFile(t, repo, "ours.txt", fixtureTickJSON("m01", "merge ours", "base description"))
	writeFixtureFile(t, repo, "theirs.txt", fixtureTickJSON("m01", "merge base", "theirs description"))
	writeFixtureFile(t, repo, "merged.txt", fixtureTickJSON("m01", "merge ours", "base description"))
	ancestor := "{\"ts\":\"2026-09-02T00:00:00Z\",\"tick\":\"a01\",\"action\":\"create\",\"actor\":\"fixture\",\"data\":{\"title\":\"Fixture task\"}}\n"
	writeFixtureFile(t, repo, "ancestor.jsonl", ancestor)
	writeFixtureFile(t, repo, "current.jsonl", ancestor+"{\"ts\":\"2026-09-02T00:01:00Z\",\"tick\":\"a01\",\"action\":\"note\",\"actor\":\"ours\",\"data\":{\"note\":\"ours\"}}\n")
	writeFixtureFile(t, repo, "other.jsonl", ancestor+"{\"ts\":\"2026-09-02T00:02:00Z\",\"tick\":\"a01\",\"action\":\"note\",\"actor\":\"theirs\",\"data\":{\"note\":\"theirs\"}}\n")
	writeFixtureFile(t, repo, "activity.jsonl", ancestor)
}

func writeFixtureFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func fixtureTickJSON(id, title, description string) string {
	tick := map[string]any{
		"id": id, "title": title, "description": description,
		"status": "open", "priority": 1, "type": "task", "owner": "fixture",
		"created_by": "fixture", "created_at": "2026-09-02T00:00:00Z", "updated_at": "2026-09-02T00:00:00Z",
	}
	data, err := json.Marshal(tick)
	if err != nil {
		panic(fmt.Sprintf("marshal fixture tick: %v", err))
	}
	return string(data) + "\n"
}
