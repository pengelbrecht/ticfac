package tk

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLabelPrefersTheGloss(t *testing.T) {
	tick := Tick{ID: "kdn", Title: "Add Google OAuth login to signup and settings", Gloss: "add google oauth login"}
	if got := Ref(tick.ID, tick.Label()); got != "kdn (add google oauth login)" {
		t.Errorf("Ref = %q", got)
	}
}

// A tick with no gloss — an older tk, or one nobody glossed — is named by its
// title cut to the gloss width, never by a blank.
func TestLabelFallsBackToTheTitleCutToTheGlossWidth(t *testing.T) {
	if got := (Tick{Title: "Fix auth"}).Label(); got != "Fix auth" {
		t.Errorf("short title: %q", got)
	}
	got := (GraphTask{Title: "Running the done is the authoritative verdict on a finding"}).Label()
	if n := len([]rune(got)); n > GlossMaxRunes {
		t.Errorf("fallback is %d characters, over %d: %q", n, GlossMaxRunes, got)
	}
	if !strings.HasPrefix(got, "Running the done") || !strings.HasSuffix(got, "…") {
		t.Errorf("fallback is not the cut title: %q", got)
	}
}

func TestRefWithoutALabelIsTheBareID(t *testing.T) {
	if got := Ref("kdn", ""); got != "kdn" {
		t.Errorf("Ref with no label = %q, want the bare id", got)
	}
}

// The field decodes from tk's JSON, and a record from a tk that predates it
// decodes too, as the empty value.
func TestGlossDecodesAndIsOptional(t *testing.T) {
	var with, without Tick
	if err := json.Unmarshal([]byte(`{"id":"a","title":"T","gloss":"g"}`), &with); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"id":"a","title":"T"}`), &without); err != nil {
		t.Fatal(err)
	}
	if with.Gloss != "g" || without.Gloss != "" {
		t.Errorf("with=%q without=%q", with.Gloss, without.Gloss)
	}
}
