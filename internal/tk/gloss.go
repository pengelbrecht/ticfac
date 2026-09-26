package tk

import (
	"strings"
	"unicode/utf8"
)

// GlossMaxRunes is the length cap tk puts on a tick's gloss, and so the width
// a fallback label is cut to: a label is the same size whether a person chose
// it or it was cut from the title.
const GlossMaxRunes = 40

// Label is a tick's short human label: its gloss, or when tk carries none —
// an older tk, or a tick nobody glossed — its title cut to GlossMaxRunes.
func Label(gloss, title string) string {
	if g := strings.TrimSpace(gloss); g != "" {
		return g
	}
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) <= GlossMaxRunes {
		return title
	}
	runes := []rune(title)
	return strings.TrimSpace(string(runes[:GlossMaxRunes-1])) + "…"
}

// Label is [Label] for a tick record.
func (t Tick) Label() string { return Label(t.Gloss, t.Title) }

// Label is [Label] for a graph node.
func (t GraphTask) Label() string { return Label(t.Gloss, t.Title) }

// Ref is how a tick is named to a person: "id (label)", or the bare id when
// no label is known — a missing label never costs the id.
func Ref(id, label string) string {
	if strings.TrimSpace(label) == "" {
		return id
	}
	return id + " (" + label + ")"
}
