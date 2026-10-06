package statusmodel

import (
	"regexp"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The run's named config, as the status model derives it (tick tda).
//
// The selection is a CONSTRUCTION fact — resolved once at run start from the
// operator's --config flag, the epic's own `config:` label and the declared
// default — and its durable writer is the reconciler's config_selected feed
// line, written at the start of EVERY incarnation so a resume states the
// config it still runs on rather than inheriting a line from a feed a fresh
// clone may not hold. The model therefore reads the name off the feed exactly
// the way it reads the waits, the health counts and the wall clock firings:
// from the one record the fact's own writer keeps.
//
// The line's detail is the run's own sentence — "run config claude —
// selected by the epic's config: label" — and the model keeps only the NAME:
// the sentence rides in Recent beside it, and a field a program parses is
// not the place for prose. The parse is a prefix cut on the line the
// reconciler's own Detail builds, so the two cannot drift apart silently: a
// detail that stops parsing leaves the field null, and the anchors' test
// catches it in this repository's own goldens before it reaches a renderer.

// runConfigLine is the reconciler's stage for the selection line
// (reconcile.StageConfigSelected), spelled here so this package reads it as
// a word and not a string nobody owns.
const runConfigLine = reconcile.StageConfigSelected

// runConfigDetail reads the config's NAME out of the line's detail — the
// prefix of the sentence reconcile.RunConfigSelection.Detail builds, up to
// the em dash that starts the sentence's other half.
var runConfigDetail = regexp.MustCompile(`^run config ([a-z0-9][a-z0-9_-]*) —`)

// selectedRunConfig answers the config the run routes under, from the LAST
// config_selected line in its own feed, or "" when there is none to read.
func selectedRunConfig(feed []runfeed.Event) string {
	line := latestStage(feed, "", runConfigLine)
	if line == nil {
		return ""
	}
	if m := runConfigDetail.FindStringSubmatch(line.Detail); m != nil {
		return m[1]
	}
	// A line this reader cannot parse is a fact about the line, not licence
	// to guess: the field stays null, and Recent still carries the sentence
	// for a person to read.
	return ""
}

// runConfigSentence is the last selection line's own wording, for the
// surfaces that render the source beside the name. Empty when no line is
// there to read.
func runConfigSentence(feed []runfeed.Event) string {
	line := latestStage(feed, "", runConfigLine)
	if line == nil {
		return ""
	}
	return strings.TrimSpace(line.Detail)
}
