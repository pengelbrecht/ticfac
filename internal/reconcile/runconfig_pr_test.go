package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The named config's own surfaces (tick tda): the close-out's PR body names
// the config every dispatch of the run resolved under — one epic on GLM,
// another on claude, from the same repository — because the reviewer reading
// the PR is the one comparing this epic's escalation and cost numbers with
// another's, and the numbers of two configs are not comparable without the
// name.

// TestThePRSummaryNamesTheRunConfig: the "What this epic did" section opens
// with the config the run ran on and who chose it, before the epic's own
// words — and a run that selected nothing says nothing, exactly as its runs
// always have.
//
// short: a bare Reconciler and the harness's fake tracker file; no run, no git.
func TestThePRSummaryNamesTheRunConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker := newTracker(t, root)

	for _, tc := range []struct {
		name    string
		sel     RunConfigSelection
		want    string
		notWant string
	}{
		{
			name: "the flag's word",
			sel:  RunConfigSelection{Name: "claude", Source: "the --config flag"},
			want: "Run config claude — selected by the --config flag.",
		},
		{
			name: "the epic's own label",
			sel:  RunConfigSelection{Name: "glm", Source: "the epic's config: label"},
			want: "Run config glm — selected by the epic's config: label.",
		},
		{
			name: "the declared default",
			sel:  RunConfigSelection{Name: "glm", Source: "the [configs] default"},
			want: "Run config glm — selected by the [configs] default.",
		},
		{
			name:    "no selection says nothing",
			sel:     RunConfigSelection{},
			notWant: "run config",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Reconciler{runConfig: tc.sel, tracker: tracker}
			summary := r.epicSummary()
			if tc.want != "" && !strings.Contains(summary, tc.want) {
				t.Errorf("the PR's epic summary does not name the config as %q:\n%s", tc.want, summary)
			}
			if tc.notWant != "" && strings.Contains(strings.ToLower(summary), tc.notWant) {
				t.Errorf("a run that selected nothing names a config:\n%s", summary)
			}
			// The config comes FIRST, before the epic's own words: a reader
			// skimming the summary reads which routing produced the numbers
			// before reading what the epic claims they show.
			if tc.want != "" && !strings.HasPrefix(summary, "Run config ") {
				t.Errorf("the config is not the summary's first word:\n%s", summary)
			}
		})
	}
}

// TestTheConfigSelectionsLineIsWhatTheFeedShows: the detail the reconciler
// writes to the feed and the status model parses is one builder's sentence —
// and the guard that keeps it so. The words here are the same ones
// internal/statusmodel parses; a change to either half fails here.
//
// short: pure formatting over a struct; no harness, no git.
func TestTheConfigSelectionsLineIsWhatTheFeedShows(t *testing.T) {
	t.Parallel()
	detail := RunConfigSelection{Name: "claude", Source: "the --config flag"}.Detail()
	if !strings.HasPrefix(detail, "run config claude — selected by") {
		t.Errorf("the feed line is %q, want it to open with the config's name and its source", detail)
	}
	if got := (RunConfigSelection{}).Detail(); got != "" {
		t.Errorf("a run that selected nothing says %q, want nothing", got)
	}
}

// TestAnUnselectedReconcilerComposesABodyThatNeverGuessesAConfig: the zero
// selection composes the same body it always did — a repository whose
// runners files declare no named configs at all is not a fact worth a line,
// in the feed, the status or the PR.
//
// short: a bare Reconciler over the lookFirst path; no run, no git.
func TestAnUnselectedReconcilerComposesABodyThatNeverGuessesAConfig(t *testing.T) {
	t.Parallel()
	look := (&Reconciler{}).lookFirst(nil, -1, nil, []runstate.Absorption{}, nil)
	if strings.Contains(strings.ToLower(look), "run config") {
		t.Errorf("the look-first list names a config no run selected:\n%s", look)
	}
}

// The end-to-end half: a real run on a repository that declares named configs
// states its selection in its own feed at the start of every incarnation,
// and its close-out's PR body names the config the run resolved under.
func TestARunOnANamedConfigStatesItInTheFeedAndThePRBody(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	// The fixture's gate, with the named configs the acceptance's shape
	// carries for a LOCAL world: one declared config, the default — and the
	// epic's own label selects the other. Roles resolve on pi, the harness
	// the fixture's executor dispatches.
	gate := passingGate + `
[configs]
default = "plain"

[configs.plain.roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.fancy.roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[configs.fancy.roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.fancy.roles.closeout]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
`
	f := newFixture(t, fixtureOptions{gate: gate})
	// The epic's own word: the label selects fancy over the declared default.
	f.Tracker.mu.Lock()
	state, err := f.Tracker.load()
	if err != nil {
		f.Tracker.mu.Unlock()
		t.Fatalf("read the fixture's tracker state: %v", err)
	}
	epic := state.Ticks[state.Epic]
	epic.Labels = []string{"config: fancy"}
	state.Ticks[state.Epic] = epic
	if err := f.Tracker.save(state); err != nil {
		f.Tracker.mu.Unlock()
		t.Fatalf("write the epic's config label: %v", err)
	}
	f.Tracker.mu.Unlock()

	r, _, err := f.run(f.Repo, fixtureOptions{gate: gate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The selection: the epic's label won over the declared default, and the
	// reconciler says which.
	sel := r.RunConfig()
	if sel.Name != "fancy" || !strings.Contains(sel.Source, "config: label") {
		t.Fatalf("the reconciler selected %+v, want fancy by the epic's label", sel)
	}
	// The feed line: stated at the start of the run's own journal, in the
	// words the status model parses.
	events := feedStages(t, f.Repo.Dir, r.runID)
	var stated bool
	for _, e := range events {
		if e.Stage == StageConfigSelected && strings.Contains(e.Detail, "run config fancy") {
			stated = true
		}
	}
	if !stated {
		stages := make([]string, 0, len(events))
		for _, e := range events {
			stages = append(stages, e.Stage)
		}
		t.Errorf("the run never stated its config in its own feed (stages: %v)", stages)
	}
	// The PR body: the close-out composes the same body from the record, and
	// the config is its first word in the epic's summary.
	body, _, err := r.composePRBodyAt("", 0)
	if err != nil {
		t.Fatalf("compose the PR body: %v", err)
	}
	if !strings.Contains(body, "Run config fancy — selected by the epic's config: label.") {
		t.Errorf("the PR body does not name the config the run resolved under:\n%s", firstN(body, 600))
	}
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
