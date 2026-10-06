package statusmodel

import (
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The named run config as the model derives it (tick tda): from the LAST
// config_selected line of the run's own feed — the durable record the
// reconciler writes at the start of every incarnation — and null when the run
// selected nothing, never a guess.
//
// short: pure derivation over feed events in memory; no harness, no git.

func configEvent(at time.Time, detail string) runfeed.Event {
	n := at.Format(time.RFC3339)
	return runfeed.Event{At: n, Stage: "config_selected", Detail: detail}
}

func TestTheModelNamesTheRunConfigFromItsOwnFeed(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		feed []runfeed.Event
		want *string
	}{
		{
			name: "the selection line",
			feed: []runfeed.Event{
				configEvent(at, "run config claude — selected by the epic's config: label"),
				{At: at.Format(time.RFC3339), Stage: "dispatched", Detail: "46x try 1 dispatched"},
			},
			want: strptrForTest("claude"),
		},
		{
			name: "a resume restates the config it still runs on",
			feed: []runfeed.Event{
				configEvent(at, "run config glm — selected by the [configs] default"),
				configEvent(at.Add(time.Hour), "run config claude — selected by the --config flag"),
			},
			want: strptrForTest("claude"),
		},
		{
			name: "a repository that declares no named configs",
			feed: []runfeed.Event{{At: at.Format(time.RFC3339), Stage: "dispatched", Detail: "46x try 1 dispatched"}},
			want: nil,
		},
		{
			name: "a feed that could not be read",
			feed: nil,
			want: nil,
		},
		{
			name: "a line this reader cannot parse is null, never a guess",
			feed: []runfeed.Event{
				configEvent(at, "run configuration claude (an older spelling this reader does not build)"),
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Build(Sources{Now: at, RunID: "epic-v5t", EpicID: "v5t", Feed: tc.feed})
			if tc.want == nil {
				if m.RunConfig != nil {
					t.Fatalf("the model names run config %q, want null", *m.RunConfig)
				}
				return
			}
			if m.RunConfig == nil {
				t.Fatalf("the model names no run config, want %q", *tc.want)
			}
			if *m.RunConfig != *tc.want {
				t.Fatalf("the model names run config %q, want %q", *m.RunConfig, *tc.want)
			}
		})
	}
}

// TestTheRunConfigSentenceIsTheLineTheReconcilerWrites: the derivation and
// the reconciler's own sentence agree — a feed line built by the reconciler's
// RunConfigSelection.Detail parses, so the writer and the reader cannot drift
// apart silently: a detail that stops parsing leaves the model's field null
// and this test red.
func TestTheRunConfigSentenceIsTheLineTheReconcilerWrites(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, sel := range []reconcile.RunConfigSelection{
		{Name: "claude", Source: "the --config flag"},
		{Name: "glm", Source: "the [configs] default"},
		{Name: "claude", Source: "the epic's config: label"},
	} {
		feed := []runfeed.Event{configEvent(at, sel.Detail())}
		if got := selectedRunConfig(feed); got != sel.Name {
			t.Errorf("the reconciler's own detail %q did not parse (got %q)", sel.Detail(), got)
		}
	}
}

func strptrForTest(s string) *string { return &s }
