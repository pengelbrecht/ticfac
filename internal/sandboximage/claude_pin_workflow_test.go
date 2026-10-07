package sandboximage

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The claude CLI pin's scheduled bump (tick 6fv, acceptance [3]): the
// claude-sub rung's models are the claude CLI's VERSIONLESS aliases (sonnet,
// opus), and an alias resolves INSIDE the pinned CLI binary — CLI 2.1.227 (the
// image's pin at this tick) maps opus to claude-opus-5, current releases map it
// to 5.5. So the aliases only stay current if CLAUDE_CODE_VERSION moves, and a
// pin nobody bumps is a rung quietly frozen on last year's models. The
// scheduled workflow is the bump made routine: a job that asks npm what the
// latest release is, moves the pin when it differs, and opens a PR — a
// dependency update, reviewed like one, never a deploy that skips review.
//
// short: reads the workflow and the Dockerfile as text; no network, no npm.
func TestAScheduledWorkflowKeepsTheClaudeCLIPinCurrent(t *testing.T) {
	body, err := os.ReadFile(workflowPath(t, "claude-cli-pin.yml"))
	if err != nil {
		t.Fatalf("reading the pin-bump workflow: %v — without it the claude-sub aliases freeze on the models the pinned CLI knows", err)
	}
	workflow := string(body)

	// SCHEDULED, not only manual: the pin is a dependency nobody remembers to
	// bump, which is the whole reason it is the workflow's job.
	if !regexp.MustCompile(`(?m)^\s*-?\s*cron:`).MatchString(workflow) {
		t.Error("the pin-bump workflow declares no cron schedule — a manual-only bump is a pin nobody remembers to move")
	}
	// The pin the workflow moves is the Dockerfile's own ARG, read FROM the
	// file rather than restated, so the two cannot drift.
	if !strings.Contains(workflow, "CLAUDE_CODE_VERSION") {
		t.Error("the pin-bump workflow does not name CLAUDE_CODE_VERSION — it must move the Dockerfile's own pin, not a copy of it")
	}
	if !strings.Contains(workflow, "image/Dockerfile") {
		t.Error("the pin-bump workflow does not name image/Dockerfile — the pin it must move lives there")
	}
	// The latest release is asked of npm, the registry the pin installs from.
	if !strings.Contains(workflow, "@anthropic-ai/claude-code") {
		t.Error("the pin-bump workflow does not name @anthropic-ai/claude-code — it must ask npm what the latest release is, not guess a version")
	}
	// The bump arrives as a PR: a dependency update a person reviews and
	// merges, exactly like every other pin in this image.
	if !strings.Contains(workflow, "pull-request") && !strings.Contains(workflow, "pull_request") {
		t.Error("the pin-bump workflow opens no pull request — a pin that lands by push skips the review every other dependency change gets")
	}
	// And the permissions it needs are the narrow ones: content to push the
	// branch, PRs to open one, and nothing else.
	if !strings.Contains(workflow, "pull-requests: write") {
		t.Error("the pin-bump workflow does not declare pull-requests: write — it cannot open the PR it exists to open")
	}
}

// workflowPath locates a workflow file from this package's directory.
func workflowPath(t *testing.T, name string) string {
	t.Helper()
	p := "../../.github/workflows/" + name
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("locating %s: %v", name, err)
	}
	return p
}
