package runfeed

// The run's own line classes, closed here because the semantics they carry —
// which line is a run's terminal word, which line is a resume answering one —
// are the feed's to state once for every reader and every writer. The
// reconciler's stage constants alias these spellings (reconcile.go), so the
// incarnation that WRITES a resume over a standing terminal line and the
// status model that READS one answer the same question through the same
// words (tick 7l6): a resume the reader does not honour, and a reader
// honouring a resume no incarnation ever writes, are the same defect seen
// from its two sides — the live incident's restart left the run's phase
// reading cancelled exactly because the reader's rule was truer than the
// writer's.
const (
	// StageRunFinished is the terminal line a run that reached its own end
	// writes.
	StageRunFinished = "run_finished"
	// StageRunDied is the terminal line for a run that did NOT reach its own
	// run_finished: the process returned an operational error, panicked, or
	// was stopped by a signal. It is written around the reconciler, so a
	// death is never a feed that simply stops on an ordinary success line.
	StageRunDied = "run_died"
	// StageResumed is what an incarnation says about adopting the previous
	// one's work under the same run id.
	StageResumed = "resumed"
	// StageResumedAutomatically is the intervention record: a resume nobody
	// typed is still a resume, kept on its own line so it stays countable.
	StageResumedAutomatically = "resumed_automatically"
)

// StandingTerminal is the run's own last terminal word that no resume
// answered: the newest run_finished or run_died line, when no resume —
// deliberate or automatic — stands after it in the feed. Position in the file
// is the clock, and a resume standing after a terminal line makes that line
// the previous incarnation's history.
//
// The position rule lives here, once, because both sides of the feed need it:
// the status model reads it to decide how the run ended, and a restarting
// incarnation writes a StageResumed line when this answers non-nil — the
// resume that makes the previous incarnation's death that incarnation's
// history rather than the live run's ending. The feed is append-only per run
// id, so a resumed run always carries its earlier terminal line in the file;
// whether that line still speaks for the run is exactly and only this
// question.
func StandingTerminal(events []Event) *Event {
	var terminal *Event
	for i := range events {
		switch events[i].Stage {
		case StageRunFinished, StageRunDied:
			terminal = &events[i]
		case StageResumed, StageResumedAutomatically:
			terminal = nil
		}
	}
	return terminal
}
