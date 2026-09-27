package acceptance

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// attemptedMark matches a LINE-LEADING bracketed token that could be an item
// mark: a literal `[A`, then the captured inside of the brackets, then the
// closing bracket. Whether the captured inside is a stable id, a malformed
// one, or prose wearing brackets is decided after the match — the shape is
// cheap to recognise and the class is not. A bracketed [A<n>] mid-line is a
// REFERENCE to an item, never a definition of one, and never matches: only
// a mark leading its line introduces an item.
var attemptedMark = regexp.MustCompile(`^[ \t]*\[A([^\]]*)\]`)

// Item is one enumerable acceptance item of a container's definition of done:
// a stable id and the fact the [A<n>] mark annotates. The fact is everything
// from its mark to the next mark — a line-wrapped fact is joined, never
// silently truncated.
type Item struct {
	// ID is the stable item id, matching runconfig.AcceptanceItemPattern —
	// the same pattern [evidence.acceptance] keys on.
	ID string
	// Text is the fact the mark annotates: the marked item's own words, not
	// the goal paragraph and not any other item's.
	Text string
}

// State is what an item is against the evidence table. It is deliberately not
// a boolean: "no proof ran" is UNVERIFIED, a third state, and collapsing it
// into false would make every unrunnable item silently non-gating — the false
// negative this epic exists to prevent. Runnable and Unverified are the two
// halves of the two-tier decision gvc designs: the oracle runs what is
// Runnable, the classifier predicts over what is Unverified.
type State string

const (
	// Runnable: the item's id is bound in [evidence.acceptance] to the id of
	// the one command that proves it, and running that command is the
	// authoritative verdict on the item.
	Runnable State = "runnable"
	// Unverified: no command is bound to the item. Not false, not true —
	// unproven, visibly, and the state that decides the item belongs to the
	// classifier rather than the oracle.
	Unverified State = "unverified"
)

// Resolved is an item with its state against the evidence table, and, when
// runnable, the command id that proves it — the id a close-out runs, never
// the command text: the table in .tick/runners.toml is what authorises shell,
// and this package never sees it.
type Resolved struct {
	Item
	// State is the item's state: Runnable or Unverified.
	State State
	// Command is the id of the command bound to the item, empty exactly when
	// the state is Unverified.
	Command string
}

// Done is a container's definition of done as a THING: the enumerated items,
// each resolved against the repository's [evidence.acceptance] table. A Done
// with no runnable items is legitimate mid-epic — it means every item is the
// classifier's to predict, and none is the oracle's to run yet.
type Done struct {
	// Items are the resolved items, in document order.
	Items []Resolved
}

// Refusal is the decision that an acceptance cannot decide absorption: the
// container's done carries no items, so nothing is runnable, nothing can be
// pointed at, and whether a finding gates the done cannot be answered. A
// refusal is a named outcome, not an error — prose acceptance is a state a
// person wrote, and the answer to it is to mark the items, not to "fix" a
// value.
type Refusal struct {
	// Reason names the refusal and what to do about it.
	Reason string
}

// String is the refusal's reason, so the decision records read as the refusal
// reads.
func (r *Refusal) String() string { return r.Reason }

// refusalReason is the one reason a no-item acceptance refuses with: it names
// what is missing, what cannot be decided because of it, and the fix.
const refusalReason = "the acceptance carries no [A<n>]-marked items, so its definition of done is prose: " +
	"nothing is runnable, nothing can be pointed at, and whether a finding gates it cannot be decided — " +
	"refusing to absorb rather than guessing. Mark the acceptance into stable [A<n>] items first " +
	"(ticks goal-design.md, fact sheets), then decide absorption against them."

// Parse extracts the [A<n>]-marked items from one container's acceptance
// criteria, in document order. It never invents marks: prose before the first
// mark is a goal statement, not an item, and an acceptance with no marks is
// zero items and no error — the refusal, not the parser, answers for that
// state.
//
// A line-leading bracket that looks like an item mark but is not a stable id
// (item zero, beyond the range, leading zeros, junk inside the brackets) is a
// HARD ERROR, not a silent skip: an epic must not be able to claim structure
// it does not have. The same id marked twice is an error too. Both name the
// offending mark and the line it is on, so the fix is a one-line edit.
func Parse(criteria string) ([]Item, error) {
	// CRLF normalised before splitting: a \r left on a line end would ride
	// along inside the item text and match nothing a reader looks for.
	criteria = strings.ReplaceAll(criteria, "\r\n", "\n")

	var items []Item
	seen := make(map[string]int)
	for line, text := range strings.Split(criteria, "\n") {
		loc := attemptedMark.FindStringSubmatchIndex(text)
		if loc == nil {
			joinContinuation(text, &items)
			continue
		}
		id, isMark, err := stableID(text[loc[2]:loc[3]], line+1)
		if err != nil {
			return nil, err
		}
		if !isMark {
			// A word in brackets, not a mark: prose, and a continuation of
			// the current item if there is one.
			joinContinuation(text, &items)
			continue
		}
		if first, ok := seen[id]; ok {
			return nil, fmt.Errorf("line %d: acceptance item %s is marked twice (first at line %d) — ids are stable within a container and unique across every container that carries an acceptance list, because [evidence.acceptance] is a single namespace",
				line+1, id, first)
		}
		seen[id] = line + 1
		items = append(items, Item{ID: id, Text: strings.TrimSpace(text[loc[1]:])})
	}
	for i := range items {
		items[i].Text = strings.TrimSpace(items[i].Text)
	}
	return items, nil
}

// joinContinuation appends a prose line to the current item's fact, if there
// is one — prose after a mark belongs to that mark, up to the next mark.
func joinContinuation(text string, out *[]Item) {
	if len(*out) == 0 {
		return
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	(*out)[len(*out)-1].Text += "\n" + trimmed
}

// stableID classifies the inside of a line-leading bracket. A mark's inside
// starts with a digit; then it must be all digits and match
// runconfig.AcceptanceItemPattern — the same pattern [evidence.acceptance]
// keys on, so a mark and a binding cannot disagree about what an id is.
// Anything else is a word in brackets ([Alpha]), which is prose and no id.
func stableID(inside string, line int) (id string, isMark bool, err error) {
	if inside == "" || inside[0] < '0' || inside[0] > '9' {
		return "", false, nil
	}
	for _, r := range inside {
		if r < '0' || r > '9' {
			return "", false, fmt.Errorf("line %d: [A%s] is not a stable acceptance item id (%s) — an item mark is [A<n> with n in 1..999 and no leading zeros, as [evidence.acceptance] keys on",
				line, inside, runconfig.AcceptanceItemPattern)
		}
	}
	id = "A" + inside
	if !runconfig.AcceptanceItemPattern.MatchString(id) {
		return "", false, fmt.Errorf("line %d: [%s] is not a stable acceptance item id (%s) — an item mark is [A<n> with n in 1..999 and no leading zeros, as [evidence.acceptance] keys on",
			line, id, runconfig.AcceptanceItemPattern)
	}
	return id, true, nil
}

// Resolve decides each item's state against [evidence.acceptance] — the map
// from item id to command id that internal/runconfig loads and validates.
// Bindings for ids that are not among the items are ignored on purpose: the
// table is one namespace across every container the repository carries.
//
// The map's values are command IDS, already validated by the config reader
// that loaded them; this package never resolves an id to shell. A nil or empty
// table leaves every item Unverified, which is a legitimate state — it means
// no item is the oracle's to run, and every item is the classifier's to
// predict.
func Resolve(items []Item, evidence map[string]string) []Resolved {
	resolved := make([]Resolved, 0, len(items))
	for _, item := range items {
		r := Resolved{Item: item, State: Unverified}
		if command := evidence[item.ID]; command != "" {
			r.State = Runnable
			r.Command = command
		}
		resolved = append(resolved, r)
	}
	return resolved
}

// Decide is the entry point for every absorption decision: it parses the
// container's acceptance, refuses when it carries no items, and resolves the
// items against the evidence table otherwise.
//
// The three outcomes are distinct on purpose, and every caller of the
// absorption machinery has to take all three:
//
//   - (Done, nil, nil): the done enumerated and resolved — absorb against it.
//   - (Done{}, *Refusal, nil): the done is prose — refuse to absorb, record
//     the refusal's reason, and do not guess.
//   - (Done{}, nil, error): the acceptance is malformed — a person must fix
//     the container's own text; neither absorbing nor refusing on it would
//     be a decision.
func Decide(criteria string, evidence map[string]string) (Done, *Refusal, error) {
	items, err := Parse(criteria)
	if err != nil {
		return Done{}, nil, err
	}
	if len(items) == 0 {
		return Done{}, &Refusal{Reason: refusalReason}, nil
	}
	return Done{Items: Resolve(items, evidence)}, nil, nil
}
