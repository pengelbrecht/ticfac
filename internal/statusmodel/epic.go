package statusmodel

// The epic's records across runs (hn6, tick gmo): the dashboard answers for
// the EPIC, and an epic is worked by many runs. Each run of one epic keeps
// its own durable records under its own run id on the integration branch,
// with its own per-run attempt numbering — so the same (tick, attempt) key
// names different dispatches in different runs, and the records cannot be
// merged into one flat list. This file is the merge: which run's record a
// row reads, and what the newest run's fresh "ready" rows must never erase.
//
// The rules, stated once:
//
//   - A tick's STATE is the newest non-"ready" row any run checkpointed for
//     it — except a row from a run that has ENDED, which is that run's last
//     word, and a last word can be stale: it does not override a tick the
//     tracker closed (tick cno). A fresh run seeds its plan "ready" before
//     it settles the tracker's answer, so a "ready" row is not evidence of
//     openness — the tracker's own status is what a "ready" row falls back
//     to. A tick no run ever touched is the tracker's closed status, else
//     ready.
//   - A tick's ROW (its dispatch markers, gate evidence, provenance, tries)
//     is the chronologically LAST run that has records for it (tick c9n):
//     the newest run with dispatch markers for the tick, else the run
//     whose row the state came from. "Closed ticks show done with their
//     last run's pipeline/time/attempts."
//   - The layers are ordered by the checkpoints' own updated_at, oldest
//     first (tick c9n) — never by which run the surface named. A read from
//     a checkout without the local feed can name an OLDER run as the
//     subject while a chronologically later sibling closed the work, and
//     the later run's word is the newer truth: row, owner and marker
//     precedence follow the clock, with a defined tie-break (the given
//     order, the subject last among equals).
//   - Gate evidence accumulates across runs: the gates array is the EPIC's
//     evidence, per tick (tick ihw) — a closed tick's drill-in reads the
//     gate rows of the run that closed it, and the newest run's own records
//     may carry none of them.
//   - Absorptions and findings accumulate: they are the epic's own history,
//     keyed by content, and no run's copy is newer than another's.
//   - Everything else — the run section — stays the subject run's alone; the
//     merge is never read for workers, cost, waits or the feed.

import (
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// mergedRuns is every run's records for one epic, ordered oldest first by
// each checkpoint's own updated_at (tick c9n), with the per-tick views the
// model reads built once. The SUBJECT run — the one the surface was opened
// on, whose records and feed the Sources carry — is not necessarily the
// last layer: a checkout without the local feed can name an older run while
// a later sibling closed the work. The run section — workers, cost, waits,
// the feed — is the subject's alone either way, read from the Sources
// themselves and never from this merge.
type mergedRuns struct {
	// runs is oldest first by the checkpoints' own updated_at; the
	// subject's own index is carried beside it.
	runs []Records

	// subject is the index of the SUBJECT run — the run the surface was
	// opened on, whose records the Sources carry as Records. Ordering the
	// layers by their own clocks means it is not necessarily len-1 (tick
	// c9n): the machinery this model watches — the feed, the census, the
	// liveness — is the subject's whatever the chronology says.
	subject int

	// rows is each tick's newest non-ready row and the run that wrote it.
	rows map[string]rowState
	// owner is, per tick, the last run that has records for it — newest by
	// the checkpoints' own clock (tick c9n): the newest run with dispatch
	// markers for the tick, else the newest with a non-ready row, else
	// none (-1).
	owner map[string]int
	// markers and evidence are the OWNER run's records for that tick —
	// never a cross-run union, because attempt numbers are per run and the
	// same key names different dispatches in different runs.
	markers  map[string][]runstate.Attempt
	evidence map[string][]runstate.Evidence
	// allEvidence is EVERY run's evidence records, oldest run first — the
	// per-tick gate rows the drill-in reads (tick ihw). Evidence is keyed by
	// content in the record store (the evidence path names its run), so a
	// union across runs cannot make one dispatch's record pose as another
	// run's same-numbered one; the drill-in reads the rows of whichever run
	// worked the tick.
	allEvidence []runstate.Evidence
	// absorbed is the union of every run's gated absorptions, by tick id.
	absorbed map[string]bool
	// absorptions and findings accumulate across runs, deduplicated by
	// their content key with the earliest record kept — a finding is one
	// record, and a later run's re-promotion of it is a duplicate, not a
	// newer truth.
	absorptionByKey map[string]runstate.Absorption
	findingByKey    map[string]runstate.Finding
}

// rowState is one tick's merged row: the state, the attempt it names, and
// the run (index into runs) whose row it is.
type rowState struct {
	state   string
	attempt *int
	run     int
}

// newMergedRuns builds the merged view. prior is the sibling runs'
// records, ordered oldest first by the reader that selected them; current
// is the SUBJECT run's records — the run the surface was opened on. The
// layers are (re)ordered by the checkpoints' own updated_at, oldest first
// (tick c9n): the subject is not always the newest run — a checkout
// without the local feed can name an older run while a chronologically
// later sibling closed the work — and the later run's word is the newer
// truth for every precedence the merge decides (rows, owners, markers).
// The tie-break is the given order, held by a stable sort: the priors stay
// oldest-first as the reader selected them, the subject stands after every
// prior it ties with, and a run with no checkpoint — only a subject that
// wrote no durable record — carries no updated_at and sorts as the oldest
// layer.
func newMergedRuns(current Records, prior []Records) *mergedRuns {
	type layer struct {
		rec     Records
		subject bool
	}
	layers := make([]layer, 0, len(prior)+1)
	for _, p := range prior {
		layers = append(layers, layer{rec: p})
	}
	layers = append(layers, layer{rec: current, subject: true})
	sort.SliceStable(layers, func(i, j int) bool {
		return layerStamp(layers[i].rec) < layerStamp(layers[j].rec)
	})
	runs := make([]Records, len(layers))
	subject := -1
	for i, l := range layers {
		runs[i] = l.rec
		if l.subject {
			subject = i
		}
	}
	m := &mergedRuns{
		runs:            runs,
		subject:         subject,
		rows:            map[string]rowState{},
		owner:           map[string]int{},
		markers:         map[string][]runstate.Attempt{},
		evidence:        map[string][]runstate.Evidence{},
		absorbed:        map[string]bool{},
		absorptionByKey: map[string]runstate.Absorption{},
		findingByKey:    map[string]runstate.Finding{},
	}
	// Rows, oldest layer to newest by the checkpoints' own clock: a newer
	// run's non-ready row replaces an older one's, and a newer "ready" row
	// never does.
	for i, r := range m.runs {
		if r.Checkpoint == nil {
			continue
		}
		for _, ts := range r.Checkpoint.Ticks {
			if ts.State == tickReady {
				continue
			}
			row := rowState{state: ts.State, run: i}
			if ts.Attempt > 0 {
				attempt := ts.Attempt
				row.attempt = &attempt
			}
			m.rows[ts.TickID] = row
		}
	}
	// Owners and per-tick records, per run, the chronologically newest
	// run's answer surviving (tick c9n).
	for i, r := range m.runs {
		m.allEvidence = append(m.allEvidence, r.Evidence...)
		perTick := map[string][]runstate.Attempt{}
		for _, a := range r.Attempts {
			perTick[a.TickID] = append(perTick[a.TickID], a)
		}
		for tickID, marks := range perTick {
			m.markers[tickID] = marks
			m.owner[tickID] = i
		}
		for _, e := range r.Evidence {
			if e.Provenance.TickID == nil || *e.Provenance.TickID == "" {
				continue
			}
			tickID := *e.Provenance.TickID
			m.evidence[tickID] = append(m.evidence[tickID], e)
			if _, ok := m.owner[tickID]; !ok {
				m.owner[tickID] = i
			}
		}
		for _, a := range r.Absorptions {
			if a.TickID == "" {
				continue
			}
			if a.Gating {
				m.absorbed[a.TickID] = true
			}
			if prior, ok := m.absorptionByKey[a.Key]; !ok || a.DecidedAt < prior.DecidedAt {
				m.absorptionByKey[a.Key] = a
			}
		}
		for _, f := range r.Findings {
			if prior, ok := m.findingByKey[f.Key]; !ok || f.ProposedAt < prior.ProposedAt {
				m.findingByKey[f.Key] = f
			}
		}
	}
	// A tick whose only record is a row (no markers, no evidence) is owned
	// by the run whose row it reads.
	for tickID, row := range m.rows {
		if _, ok := m.owner[tickID]; !ok {
			m.owner[tickID] = row.run
		}
	}
	// Markers sort by attempt number within their run, the same order the
	// try history reads them in.
	for _, marks := range m.markers {
		sort.Slice(marks, func(i, j int) bool { return marks[i].Attempt < marks[j].Attempt })
	}
	return m
}

// stateOf is a tick's merged state: the newest non-ready row any run wrote,
// else the tracker's own closed status, else ready. One row cannot win:
// a row from a run that has ENDED does not override a tracker-closed tick
// (tick cno) — an ended run's last row can be stale (the run stopped
// mid-flight and the tick was closed after it, by a later run or by the
// person), and a stale "dispatched" read as live work over a close nobody
// can dispute. The row from a LIVE run is that run's present tense and
// still wins, and a tick the tracker has not closed keeps an ended run's
// row too: that row is the history the holds and the resume read. The
// second return names the attempt the winning row carries; the third is
// the run whose row won (-1 when no run did, i.e. the answer came from
// the tracker or from nothing).
func (m *mergedRuns) stateOf(task tk.GraphTask) (string, *int, int) {
	if row, ok := m.rows[task.ID]; ok {
		if task.Status != "closed" || !m.runEnded(row.run) {
			return row.state, row.attempt, row.run
		}
		return tickClosed, row.attempt, row.run
	}
	if task.Status == "closed" {
		return tickClosed, nil, -1
	}
	return tickReady, nil, -1
}

// runEnded says whether the run whose records sit at index i ended by its
// own word: a terminal checkpoint state — the same word runEndedAt and the
// lifecycle read. A run with no checkpoint never answered, and no row of a
// run without a checkpoint exists to demote.
func (m *mergedRuns) runEnded(i int) bool {
	if i < 0 || i >= len(m.runs) {
		return false
	}
	return m.runs[i].Checkpoint != nil && m.runs[i].Checkpoint.State.Terminal()
}

// layerStamp is one run layer's own clock: its checkpoint's updated_at,
// the same field the cli reader orders the prior runs by — one ordering
// vocabulary, never two that could disagree. A run with no checkpoint
// carries no clock; it sorts as the oldest layer.
func layerStamp(r Records) string {
	if r.Checkpoint == nil {
		return ""
	}
	return r.Checkpoint.UpdatedAt
}

// subjectRun is the index of the subject run's records — the run the
// surface was opened on, the one whose feed, census and liveness the
// Sources carry. It is the last layer in the normal case (the subject is
// the newest run), but ordering the layers by their own clocks means an
// older subject can read behind a chronologically later sibling (tick
// c9n) — and the machinery this model watches is the subject's either way.
func (m *mergedRuns) subjectRun() int { return m.subject }

// ownerIndex is every tick's owning run, by id.
func (m *mergedRuns) ownerIndex() map[string]int { return m.owner }

// markersOf is the owner run's dispatch markers for one tick.
func (m *mergedRuns) markersOf(tickID string) []runstate.Attempt { return m.markers[tickID] }

// allEvidence is every run's evidence records, oldest run first — the
// gates array is built from it, so a closed tick's drill-in carries the
// gate rows of the run that closed it (tick ihw).
func (m *mergedRuns) evidenceAll() []runstate.Evidence { return m.allEvidence }

// ownerRunID is the run id of the run whose row one tick reads — the run
// whose attempt numbers the tick's row carries, and the only run its
// attempt branches and archived reports are ever filed under. The SUBJECT
// run answers `newest` (the id the surfaces were opened on): its records
// are the caller's own. Any other run — earlier or later than the subject
// (tick c9n) — answers its checkpoint's own id, and "" when its records
// carried none, which the report readers answer as not read: a run id
// nobody recorded is not the subject's by default, which was exactly the
// collision the (run, tick, attempt) key ends.
func (m *mergedRuns) ownerRunID(tickID, newest string) string {
	i, ok := m.owner[tickID]
	if !ok || i < 0 || i >= len(m.runs) {
		return ""
	}
	if i == m.subject {
		return newest
	}
	if cp := m.runs[i].Checkpoint; cp != nil {
		return cp.RunID
	}
	return ""
}

// duplicateOf reads a tick's own tracker record for a closure as a
// duplicate: the dedup writer's note ("closed as a duplicate of <id>") or a
// closed_reason that names the word with the duplicated tick. Nil when the
// tick is not a closed duplicate — including every open tick, since a
// duplicate that is still open is work the epic has not deduped yet.
func duplicateOf(task tk.GraphTask) *string {
	if task.Status != "closed" {
		return nil
	}
	// The dedup writer's own wording, in the note it leaves when it closes a
	// later promotion: "closed as a duplicate of <id>. Both were promoted…".
	if id, ok := cutDuplicateOf(task.Notes); ok {
		return &id
	}
	// A hand-closed duplicate, where the person named the reason:
	// "duplicate of ky5, fixed in #46" / "duplicate: tracked in …".
	if id, ok := cutDuplicateOf(task.ClosedReason); ok {
		return &id
	}
	if containsDuplicateWord(task.ClosedReason) {
		unknown := ""
		return &unknown
	}
	return nil
}

// cutDuplicateOf finds "duplicate of <id>" in a record's free text and
// returns the id. The id is the next word, bounded to the tracker's own
// alphabet (lowercase alphanumerics); anything else is prose, not a pointer.
func cutDuplicateOf(text string) (string, bool) {
	for _, marker := range []string{"duplicate of ", "duplicate: "} {
		at := strings.Index(text, marker)
		if at < 0 {
			continue
		}
		rest := text[at+len(marker):]
		id := rest
		if end := strings.IndexAny(rest, " \t\n.,;:)"); end >= 0 {
			id = rest[:end]
		}
		id = strings.Trim(id, "*`_")
		if isTickID(id) {
			return id, true
		}
		// "duplicate: tracked in pengelbrecht/ticks:jlm" names a repo, not
		// a tick here — the word alone is the fact.
		return "", false
	}
	return "", false
}

// containsDuplicateWord says whether a closed reason names a duplicate
// without naming the tick it duplicates — the fact alone, with no pointer.
func containsDuplicateWord(text string) bool {
	return strings.Contains(strings.ToLower(text), "duplicate")
}

// isTickID is the tracker's own id, as contracts/tracker-layout.json pins
// it: lowercase alphanumerics, three or four characters. A word outside the
// alphabet or the length is prose ("tracked in pengelbrecht/ticks:jlm"),
// not a pointer — the fact of a duplicate is still stated by the word, and
// duplicateOf answers it without a pointer.
func isTickID(id string) bool {
	if len(id) < 3 || len(id) > 4 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
