// Package escalation measures how often a run's implementation ticks needed
// the tier ladder: how many started, how many had to try again at a higher
// tier, what made the lower tier fail (the model, or the infrastructure
// around it), and how many succeeded on the tier they started at.
//
// WHY IT EXISTS. On 2026-10-04 the classifier-driven start was switched off
// (tick r3y's evaluation, docs/classifier-eval-2026-10-04-jev-clef.md: dear
// mass predicts nothing, AUC 0.37-0.42). Every implementation tick now starts
// at [tier_policy].default and the ladder alone carries the hard ones. The
// cloud's ladder starts on GLM 5.3 Flash, which Phase 2 saw finish 1 of 3
// real ticks, and its config says to revisit the start "if the recorded
// escalation rate is high". This package is that rate, read off the record.
//
// IT READS ONLY THE FEED the run already writes, never a new record:
//
//   - tier_derived: one per dispatch, naming the tick, its try, its run
//     dispatch number and the tier it runs at. A later try at a higher tier
//     than an earlier one of the same tick in the same run is an escalation.
//   - collected: names the job's role ("the implement-tick job answered"),
//     so role jobs that share a tick id (resolve-conflict, repair, review)
//     are not counted as implementation work.
//   - rejected, infrastructure_redispatched, gate_failed, wall_clock_fired:
//     what happened to the try an escalation climbed from, which says whose
//     failure it was.
//   - closed / closed_carrying: the tick closed.
//
// What the feed cannot say is not guessed: a tick this run picked up past
// its first try started in an earlier run, so its first tier is not this
// run's to judge, and a failure with no record in this run is counted as
// "other", never assigned to the model.
package escalation

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The feed stages this package reads, spelled as the reconciler writes them
// (internal/reconcile's Stage* constants; a test pins the two together).
const (
	stageTierDerived     = "tier_derived"
	stageCollected       = "collected"
	stageRejected        = "rejected"
	stageInfraRedispatch = "infrastructure_redispatched"
	stageGateFailed      = "gate_failed"
	stageWallClock       = "wall_clock_fired"
	stageClosed          = "closed"
	stageClosedCarrying  = "closed_carrying"
)

// implementRole is the job role of an implementation tick, as the collected
// line names it.
const implementRole = "implement-tick"

// Cause is whose failure made a tick climb.
type Cause string

const (
	// CauseModel: the worker had its chance and did not pass — it answered
	// BLOCKED, committed nothing, left an unreadable report, failed the gate,
	// or ran into its wall clock.
	CauseModel Cause = "model"
	// CauseInfrastructure: the worker never had its chance — its boot
	// stopped, the model gateway or origin did not answer (exit 14/15), the
	// sandbox door refused, or its push never landed.
	CauseInfrastructure Cause = "infrastructure"
	// CauseOther: the records do not say (a merge conflict, or a failure
	// this run's feed holds no line for).
	CauseOther Cause = "other"
)

// Summary is one run's escalation record.
type Summary struct {
	// Dispatched is the implementation ticks this run dispatched at all.
	Dispatched int `json:"dispatched"`
	// Started is the implementation ticks whose FIRST try this run
	// dispatched: the denominator of the first-tier share.
	Started int `json:"started"`
	// Escalated is the ticks that ran a later try at a higher tier than an
	// earlier try of theirs in this run.
	Escalated int `json:"escalated"`
	// ByCause splits Escalated by what failed the try each tick climbed
	// from (the first climb of each tick).
	Model          int `json:"model_caused"`
	Infrastructure int `json:"infrastructure_caused"`
	Other          int `json:"other_caused"`
	// FirstTier is the started ticks that closed without climbing: they
	// succeeded on the tier they started at (retries at that same tier —
	// an infrastructure redispatch — do not disqualify them).
	FirstTier int `json:"first_tier_succeeded"`
	// Open is the started ticks that have not closed (still in flight,
	// held, or left for a later run).
	Open int `json:"open"`
}

// FirstTierShare is FirstTier / Started, or -1 when nothing started.
func (s Summary) FirstTierShare() float64 {
	if s.Started == 0 {
		return -1
	}
	return float64(s.FirstTier) / float64(s.Started)
}

// Line renders the summary as one line of `ticfac status`.
func (s Summary) Line() string {
	if s.Dispatched == 0 {
		return "escalation: no implementation tick dispatched in this run yet"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "escalation: %d implementation tick(s) started here", s.Started)
	if resumed := s.Dispatched - s.Started; resumed > 0 {
		fmt.Fprintf(&b, " (+%d picked up past their first try)", resumed)
	}
	fmt.Fprintf(&b, "; %d needed a higher tier", s.Escalated)
	if s.Escalated > 0 {
		fmt.Fprintf(&b, " (%d model, %d infrastructure", s.Model, s.Infrastructure)
		if s.Other > 0 {
			fmt.Fprintf(&b, ", %d other", s.Other)
		}
		b.WriteString(")")
	}
	if share := s.FirstTierShare(); share >= 0 {
		fmt.Fprintf(&b, "; %d of %d succeeded on their first tier (%.0f%%)", s.FirstTier, s.Started, share*100)
	}
	if s.Open > 0 {
		fmt.Fprintf(&b, "; %d not closed", s.Open)
	}
	return b.String()
}

// tierDerivedPattern reads a tier_derived line's head: the tick, its try
// (absent when the run could not count it), its run dispatch number and the
// tier — reconcile.AttemptLabel followed by ` runs at tier "<tier>"`.
var tierDerivedPattern = regexp.MustCompile(`^(\S+)(?: try (\d+))? \(run dispatch #(\d+)\) runs at tier "([^"]*)"`)

// collectedRolePattern reads the job role a collected line names.
var collectedRolePattern = regexp.MustCompile(`^the (\S+) job answered`)

// infrastructurePattern recognises a failure verdict the worker's harness
// never got to: a boot that stopped, a service that did not answer, a door
// that refused, a push that never landed. It is matched against the run's
// own verdict text only, never against a worker's prose.
var infrastructurePattern = regexp.MustCompile(`(?i)never reached its harness|push never landed|no report and no commit|door_fault|door refused|could not be started|\bexit(?:ed)? 1[45]\b|its boot|boot stopped|gateway did not answer|ContainerUnavailable|sandbox died`)

type dispatch struct {
	number int
	try    int
	tier   int // index in runconfig.TierNames, -1 when unknown
}

type tickState struct {
	dispatches []dispatch
	closed     bool
}

// FromFeed computes one run's summary from its feed, in feed order.
func FromFeed(events []runfeed.Event) Summary {
	ticks := map[string]*tickState{}
	var order []string
	rolesOf := map[int]map[string]bool{}     // dispatch number -> job roles its collected lines name
	failures := map[string][]runfeed.Event{} // tick\x00dispatch -> failure lines

	key := func(tick string, n int) string { return tick + "\x00" + strconv.Itoa(n) }
	for _, e := range events {
		if e.TickID == nil {
			continue
		}
		tick := *e.TickID
		switch e.Stage {
		case stageTierDerived:
			m := tierDerivedPattern.FindStringSubmatch(e.Detail)
			if m == nil || m[1] != tick || m[4] == "" {
				continue // a role job at its base values, or a line this reader cannot place
			}
			n, _ := strconv.Atoi(m[3])
			try, _ := strconv.Atoi(m[2])
			st := ticks[tick]
			if st == nil {
				st = &tickState{}
				ticks[tick] = st
				order = append(order, tick)
			}
			st.dispatches = append(st.dispatches, dispatch{number: n, try: try, tier: tierIndex(m[4])})
		case stageCollected:
			if e.Attempt != nil {
				if m := collectedRolePattern.FindStringSubmatch(e.Detail); m != nil {
					if rolesOf[*e.Attempt] == nil {
						rolesOf[*e.Attempt] = map[string]bool{}
					}
					rolesOf[*e.Attempt][m[1]] = true
				}
			}
		case stageRejected, stageInfraRedispatch, stageGateFailed, stageWallClock:
			if e.Attempt != nil {
				failures[key(tick, *e.Attempt)] = append(failures[key(tick, *e.Attempt)], e)
			}
		case stageClosed, stageClosedCarrying:
			if st := ticks[tick]; st != nil {
				st.closed = true
			}
		}
	}

	var s Summary
	for _, tick := range order {
		st := ticks[tick]
		// Only implementation work: a dispatch whose collected lines name
		// only another role's job is not one. A resolve-conflict job is
		// collected under the number of the implementation attempt it
		// resolves, so one implement-tick line is enough to keep it.
		var work []dispatch
		for _, d := range st.dispatches {
			if roles := rolesOf[d.number]; len(roles) > 0 && !roles[implementRole] {
				continue
			}
			work = append(work, d)
		}
		if len(work) == 0 {
			continue
		}
		s.Dispatched++
		started := work[0].try == 1
		if started {
			s.Started++
		}
		escalated := false
		for i := 1; i < len(work); i++ {
			before, now := work[i-1], work[i]
			if before.tier < 0 || now.tier <= before.tier {
				continue
			}
			escalated = true
			switch causeOf(failures[key(tick, before.number)]) {
			case CauseModel:
				s.Model++
			case CauseInfrastructure:
				s.Infrastructure++
			default:
				s.Other++
			}
			break
		}
		if escalated {
			s.Escalated++
		}
		if started {
			switch {
			case st.closed && !escalated:
				s.FirstTier++
			case !st.closed:
				s.Open++
			}
		}
	}
	return s
}

// causeOf reads what failed one try from the lines the run wrote about it.
func causeOf(lines []runfeed.Event) Cause {
	if len(lines) == 0 {
		return CauseOther
	}
	model := false
	for _, e := range lines {
		switch e.Stage {
		case stageInfraRedispatch:
			return CauseInfrastructure
		case stageGateFailed, stageWallClock:
			model = true
		case stageRejected:
			detail := e.Detail
			switch {
			case strings.HasPrefix(detail, "the worker answered"):
				// The worker's own answer: its prose may mention anything,
				// and it is the model's either way.
				model = true
			case strings.HasPrefix(detail, "merge_failed"):
				// A conflict with the work beside it: neither the model's
				// nor the infrastructure's.
			case infrastructurePattern.MatchString(detail):
				return CauseInfrastructure
			default:
				model = true
			}
		}
	}
	if model {
		return CauseModel
	}
	return CauseOther
}

func tierIndex(name string) int {
	for i, t := range runconfig.TierNames {
		if string(t) == name {
			return i
		}
	}
	return -1
}
