package runfeed

import "sort"

// Tries counts each tick's own tries from the lines of one run's feed (tick
// h58), for the surfaces that print a line for a PERSON — `ticfac watch`'s
// '<tick>#<n>' prefix and `ticfac status`'s table.
//
// The feed's `attempt` field is the RUN-WIDE dispatch number: the attempt's
// identity, the number its branch, its WIP ref, adoption and `ticfac settle`
// all key on, and it stays exactly that — the field is not repurposed and no
// field is added beside it. But it is the wrong number to LEAD a human line
// with: "w9b#5" reads as w9b's fifth try when w9b's tries were dispatches 3,
// 4 and 5. The try is derivable from the lines the feed already carries,
// because every dispatch of a tick puts its number on that tick's lines: a
// tick's try for a dispatch number is its rank among the distinct numbers the
// feed has shown for that tick. Nothing here is a new claim about the run —
// it is the same lines, counted — so it needs no contract change.
//
// It counts what the lines show and nothing else. A follower that has seen
// only part of a feed counts only that part, which is why the readers observe
// the whole standing feed before they print anything; and a dispatch made by
// an incarnation on another host, whose lines never reached this feed, is a
// try this count cannot see. The reconciler's own detail text counts tries
// from the run state on origin and is the authority when the two differ.
type Tries struct {
	seen map[string]map[int]struct{}
}

// Observe takes one line into the count. Run-level lines and lines that
// belong to no attempt yet say nothing about a try and are ignored; a line
// seen twice counts once, so replaying a feed a follower already observed is
// harmless.
func (t *Tries) Observe(e Event) {
	if e.TickID == nil || *e.TickID == "" || e.Attempt == nil {
		return
	}
	if t.seen == nil {
		t.seen = map[string]map[int]struct{}{}
	}
	numbers := t.seen[*e.TickID]
	if numbers == nil {
		numbers = map[int]struct{}{}
		t.seen[*e.TickID] = numbers
	}
	numbers[*e.Attempt] = struct{}{}
}

// Of is the tick's try for one run-wide dispatch number: 1 for the lowest
// number the feed has shown for the tick, 2 for the next, and so on. The ok
// answer is false when the feed has never shown that number for that tick —
// a try nobody counted is not guessed at.
func (t *Tries) Of(tick string, attempt int) (int, bool) {
	numbers, ok := t.seen[tick]
	if !ok {
		return 0, false
	}
	if _, ok := numbers[attempt]; !ok {
		return 0, false
	}
	sorted := make([]int, 0, len(numbers))
	for n := range numbers {
		sorted = append(sorted, n)
	}
	sort.Ints(sorted)
	return sort.SearchInts(sorted, attempt) + 1, true
}
