package subprocess

import (
	"strings"
	"testing"
)

// TestThePromptAsksForNewCommitsNotRewrites (epic-2jn, rix attempt 45): a
// worker that amends a commit its supervisor already pushed leaves origin on
// a commit its branch moved off. The push follows such a rewrite now
// (ReplaceOwnEarlierHead); the prompt only makes it rarer.
func TestThePromptAsksForNewCommitsNotRewrites(t *testing.T) {
	t.Parallel()
	prompt := renderPrompt(&attemptRecord{TickID: "abc", Branch: "b", BaseSHA: "0123"},
		&JobSpec{ArtifactPrefix: "runs/r/abc"})
	if !strings.Contains(prompt, "Do not amend, rebase or reset commits you have already made") {
		t.Errorf("the worker prompt does not ask for new commits over rewrites:\n%s", prompt)
	}
}
