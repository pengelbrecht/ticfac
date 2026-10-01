package reconcile

import (
	"fmt"
	"sort"
	"strings"
)

// heldEnd is how a run that ended on its holds (window.go, holdsOnlyItsTick
// and the question hold) says so in its terminal reason.
//
// The reason used to be the stop's sentence whatever ended the run: "<tick>
// did not pass: the run stopped rather than integrating over an unproven
// change". Since #168 a refusal raised before a tick's work reached the
// integration branch holds only that tick, and the run works every tick that
// does not wait behind it to the end before it ends. Telling the reader it
// "stopped" then sends them looking for abandoned work that was in fact
// integrated and closed. So the reason names what is held, on what, and what
// waits behind it.
type heldEnd struct {
	held    []*Refusal // in the order the ticks were held
	waiting []string   // ticks not dispatched because they wait behind a hold
}

func newHeldEnd(order []string, parked map[string]*Refusal, waiting map[string]bool) *heldEnd {
	end := &heldEnd{}
	for _, tick := range order {
		if refusal := parked[tick]; refusal != nil {
			end.held = append(end.held, refusal)
		}
	}
	for tick := range waiting {
		end.waiting = append(end.waiting, tick)
	}
	sort.Strings(end.waiting)
	return end
}

// reason is the run's terminal reason up to the resumption template.
func (e *heldEnd) reason(branch string) string {
	held := make([]string, 0, len(e.held))
	for _, refusal := range e.held {
		held = append(held, fmt.Sprintf("%s is held on %s (%s)", refusal.TickID, refusal.Reason, refusal.Message))
	}
	reason := strings.Join(held, "; ") + fmt.Sprintf(
		". Nothing of a held tick reached %s, so the run did not stop at the hold: every tick that does not "+
			"wait behind it was worked to the end, and the run ends on the hold because nothing else can progress",
		branch)
	if len(e.waiting) > 0 {
		reason += fmt.Sprintf(" — %s waits behind it and was not dispatched", strings.Join(e.waiting, ", "))
	}
	return reason
}
