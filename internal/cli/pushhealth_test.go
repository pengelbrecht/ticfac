package cli

import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// `ticfac status` says a run's pushes and its GitHub errors by class on one
// line, and nothing at all for a run that has neither (tick rlp).
//
// short: string formatting only
func TestStatusSaysPushesAndGitHubErrorsByClass(t *testing.T) {
	t.Parallel()
	if got := formatPushHealth(0, 0, statusmodel.GitHubErrors{}); got != "" {
		t.Errorf("a run with no pushes and no errors printed %q", got)
	}
	if got, want := formatPushHealth(12, 5, statusmodel.GitHubErrors{}),
		"github: 12 push(es), peak 5 in a minute; no GitHub errors"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	got := formatPushHealth(40, 6, statusmodel.GitHubErrors{RefUpdateFailed: 2, CrossRepoRefused: 1})
	if want := "github: 40 push(es), peak 6 in a minute; GitHub errors: ref_update_failed 2, cross_repo_refused 1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
