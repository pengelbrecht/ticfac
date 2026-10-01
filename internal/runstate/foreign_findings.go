package runstate

import (
	"fmt"
	"sort"
	"strings"
)

// Another run's findings, read so a run that takes over a dead run's claim
// can adopt them (the hn6 follow-up): a dead run never reaches its close-out,
// so its untriaged drafts would otherwise sit under a run that will never
// decide them. Read only: this store still writes nowhere outside its own
// run's directory.

// ForeignFindings returns another run's findings drafts as this writer last
// fetched them, ordered by key.
func (s *Store) ForeignFindings(runID string) ([]Finding, error) {
	if err := checkSegment("run id", runID); err != nil {
		return nil, fmt.Errorf("runstate: %w", err)
	}
	if err := s.ensureFetched(); err != nil {
		return nil, err
	}
	prefix := RunDir(runID) + "/findings/"
	keys := []string{}
	for path := range s.view {
		name, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		if key, ok := strings.CutSuffix(name, ".json"); ok && !strings.Contains(key, "/") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := []Finding{}
	for _, key := range keys {
		var f Finding
		ok, err := s.load(FindingPath(runID, key), &f)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// ForeignFindingsByKey returns every OTHER run's draft under one finding key,
// ordered by run id: the same finding as an earlier run of this epic drafted
// it. Findings are content-hashed, so a key is the finding's identity across
// runs as well as within one, and a run that meets a key an earlier run
// already decided links to that decision rather than proposing it again
// (hn6: run_6d88's oro and yjq, absorbed again by run_ee8e as log and qrl).
func (s *Store) ForeignFindingsByKey(key string) ([]Finding, error) {
	if err := checkSegment("finding key", key); err != nil {
		return nil, fmt.Errorf("runstate: %w", err)
	}
	if err := s.ensureFetched(); err != nil {
		return nil, err
	}
	prefix := Root + "/runs/"
	suffix := "/findings/" + key + ".json"
	runs := []string{}
	for path := range s.view {
		rest, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		run, ok := strings.CutSuffix(rest, suffix)
		if !ok || run == s.runID || strings.Contains(run, "/") {
			continue
		}
		runs = append(runs, run)
	}
	sort.Strings(runs)
	out := []Finding{}
	for _, run := range runs {
		var f Finding
		ok, err := s.load(FindingPath(run, key), &f)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// EpicPromotions is every promotion of a finding to a tick that ANY run on
// this integration branch recorded — this run's and every other's: each
// run's absorption decisions, and each promoted draft's promoted_as. A key
// that appears with two tick ids is one finding promoted twice (hn6: oro and
// log, yjq and qrl), which the run closes down to its earliest tick.
// Ordered by run id, then key; a draft and a decision naming the same tick
// are both returned, and the caller collapses them.
func (s *Store) EpicPromotions() ([]Promotion, error) {
	if err := s.ensureFetched(); err != nil {
		return nil, err
	}
	prefix := Root + "/runs/"
	type file struct{ run, kind, key string }
	files := []file{}
	for path := range s.view {
		rest, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		parts := strings.Split(rest, "/")
		if len(parts) != 3 || (parts[1] != "absorptions" && parts[1] != "findings") {
			continue
		}
		key, ok := strings.CutSuffix(parts[2], ".json")
		if !ok || checkSegment("run id", parts[0]) != nil || checkSegment("finding key", key) != nil {
			continue
		}
		files = append(files, file{run: parts[0], kind: parts[1], key: key})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].run != files[j].run {
			return files[i].run < files[j].run
		}
		if files[i].key != files[j].key {
			return files[i].key < files[j].key
		}
		return files[i].kind < files[j].kind
	})
	out := []Promotion{}
	for _, one := range files {
		if one.kind == "absorptions" {
			var a Absorption
			ok, err := s.load(AbsorptionPath(one.run, one.key), &a)
			if err != nil {
				return nil, err
			}
			if ok && a.TickID != "" {
				out = append(out, Promotion{Key: one.key, TickID: a.TickID, RunID: one.run, At: a.DecidedAt})
			}
			continue
		}
		var f Finding
		ok, err := s.load(FindingPath(one.run, one.key), &f)
		if err != nil {
			return nil, err
		}
		if ok && f.Status == FindingPromoted && f.PromotedAs != "" {
			out = append(out, Promotion{Key: one.key, TickID: f.PromotedAs, RunID: one.run, At: f.TriagedAt})
		}
	}
	return out, nil
}

// Promotion is one recorded promotion of a finding to a tick: the finding's
// key, the tick (`<tick-id>`, or `<owner/name>:<tick-id>` for a routed one),
// the run that recorded it, and when (RFC3339).
type Promotion struct {
	Key    string
	TickID string
	RunID  string
	At     string
}

// ForeignAbsorption returns another run's absorption decision for one
// finding, when it recorded one: the decision a run adopting that finding
// adopts with it, so a decision made once is never made again and its tick
// is never created twice.
func (s *Store) ForeignAbsorption(runID, key string) (*Absorption, bool, error) {
	if err := checkSegment("run id", runID); err != nil {
		return nil, false, fmt.Errorf("runstate: %w", err)
	}
	if err := checkSegment("absorption key", key); err != nil {
		return nil, false, fmt.Errorf("runstate: %w", err)
	}
	var a Absorption
	ok, err := s.load(AbsorptionPath(runID, key), &a)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &a, true, nil
}
