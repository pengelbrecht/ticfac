package refsweep

import (
	"context"
	"time"
)

// Options configure one sweep.
type Options struct {
	Git Git
	// Base is the base branch's name ("main"): a ref reachable from it is
	// merged.
	Base string
	// EpicBranch names an epic's integration branch; nil means epic/<id>.
	EpicBranch func(epic string) string
	// Epic asks the tracker about one epic; nil, or an error, is an epic
	// counted as open.
	Epic       func(ctx context.Context, id string) (Epic, error)
	Now        time.Time
	Grace      time.Duration
	LiveWindow time.Duration
	DryRun     bool
}

// Report is what one sweep found and did.
type Report struct {
	// Listed is how many ticfac-owned refs the remote held when listed.
	Listed   int               `json:"listed"`
	Verdicts []Verdict         `json:"verdicts"`
	Deleted  []Ref             `json:"deleted,omitempty"`
	Failed   map[string]string `json:"failed,omitempty"`
}

// Doomed is the verdicts that delete.
func (r Report) Doomed() []Ref {
	var refs []Ref
	for _, v := range r.Verdicts {
		if v.Delete {
			refs = append(refs, v.Ref)
		}
	}
	return refs
}

func (o Options) defaults() Options {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.EpicBranch == nil {
		o.EpicBranch = func(epic string) string { return "epic/" + epic }
	}
	return o
}

// Sweep judges every ticfac-owned ref on the remote and, unless DryRun,
// deletes the ones the plan retires.
func Sweep(ctx context.Context, o Options) (Report, error) {
	o = o.defaults()
	refs, err := o.Git.ListRemote(ctx, OwnedPrefixes)
	if err != nil {
		return Report{}, err
	}
	s := o.Git.newScratch()
	defer s.close(context.WithoutCancel(ctx))
	runs := s.checkpoints(ctx, o.Base)
	merged := s.mergeOracle(ctx, refs, o.Base, o.EpicBranch, nil)
	return o.finish(ctx, refs, runs, merged), nil
}

// RetireRun is the run end's half (a completed run): it judges only the
// run's own namespaces, and the integration heads it names — the epic
// branch as the run just left it, and the base — are what merged means.
func RetireRun(ctx context.Context, o Options, run Run, integration []string) (Report, error) {
	o = o.defaults()
	refs, err := o.Git.ListRemote(ctx, RunPrefixes(run.ID))
	if err != nil {
		return Report{}, err
	}
	if len(refs) == 0 {
		return Report{}, nil
	}
	s := o.Git.newScratch()
	defer s.close(context.WithoutCancel(ctx))
	merged := s.mergeOracle(ctx, refs, o.Base, o.EpicBranch, integration)
	return o.finish(ctx, refs, map[string]Run{run.ID: run}, merged), nil
}

func (o Options) finish(ctx context.Context, refs []Ref, runs map[string]Run, merged func(string, string) bool) Report {
	epics := map[string]Epic{}
	ask := func(id string) {
		if _, done := epics[id]; done || id == "" {
			return
		}
		e := Epic{}
		if o.Epic != nil {
			if got, err := o.Epic(ctx, id); err == nil {
				e = got
			}
		}
		epics[id] = e
	}
	for _, run := range runs {
		ask(run.Epic)
	}
	for _, ref := range refs {
		if owned, ok := Classify(ref.Name); ok {
			ask(owned.Epic)
			if epic, named := cutEpicRunID(owned.Run); named {
				ask(epic)
			}
		}
	}
	report := Report{Listed: len(refs)}
	report.Verdicts = Plan(Inputs{
		Refs: refs, Runs: runs, Epics: epics, Merged: merged,
		Now: o.Now, Grace: o.Grace, LiveWindow: o.LiveWindow,
	})
	if o.DryRun {
		return report
	}
	if doomed := report.Doomed(); len(doomed) > 0 {
		report.Deleted, report.Failed = o.Git.Delete(ctx, doomed)
	}
	return report
}

func cutEpicRunID(run string) (string, bool) {
	if len(run) > len("epic-") && run[:len("epic-")] == "epic-" {
		return run[len("epic-"):], true
	}
	return "", false
}
