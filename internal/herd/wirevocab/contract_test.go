package wirevocab

import (
	"testing"
)

// The contract's own self-consistency, checked on every test run, with or
// without a herdr binary. Load() enforces the structural rules (no dupes,
// annotations name members, dotted kinds == pane-scoped, state-label keys ==
// the named category); this test adds the SIZE pins.
//
// The sizes are deliberately hardcoded, not derived: when herdr grows a word,
// the drift test fails on the machine that has herdr, the count that changed
// shows up here as a diff, and the bump becomes a reviewed decision rather
// than a silent rebase of the vocabulary.

func TestContractLoadsAndCrossValidates(t *testing.T) {
	v, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.Metadata.SourceID != "tk-herd-paint" {
		t.Errorf("metadata.source_id = %q, want tk-herd-paint", v.Metadata.SourceID)
	}
}

func TestPinnedVocabularySizes(t *testing.T) {
	v := MustLoad()
	for _, tc := range []struct {
		name  string
		set   WordSet
		count int
	}{
		{"methods", v.Methods, 102},
		{"result_discriminators", v.ResultDiscriminators, 64},
		{"error_codes", v.ErrorCodes, 9},
		{"event_kinds", v.EventKinds, 26},
		{"subscription_event_kinds", v.SubscriptionEventKinds, 3},
		{"subscription_types", v.SubscriptionTypes, 27},
		{"agent_statuses", v.AgentStatuses, 5},
		{"pane_agent_states", v.PaneAgentStates, 4},
		{"agent_kinds", v.AgentKinds, 23},
		{"read_sources", v.ReadSources, 4},
		{"read_formats", v.ReadFormats, 2},
		{"output_match_types", v.OutputMatchTypes, 2},
		{"notification_sounds", v.NotificationSounds, 3},
		{"toast_positions", v.ToastPositions, 4},
	} {
		if got := len(tc.set.Members); got != tc.count {
			t.Errorf("%s: pinned at %d members, contract carries %d — bump the pin deliberately", tc.name, tc.count, got)
		}
	}
	if got := len(v.SubscriptionTypes.PaneScoped); got != 3 {
		t.Errorf("subscription_types.pane_scoped: pinned at 3, contract carries %d", got)
	}
}

func TestPinnedRelations(t *testing.T) {
	v := MustLoad()

	// `ok` is a real result discriminator — the report_metadata methods
	// answer with it and nothing else — not a placeholder for "no result".
	if !v.ResultDiscriminators.Has("ok") {
		t.Error("result_discriminators must contain ok")
	}

	// The client's constants must be a subset of every category that
	// carries a client_used annotation.
	for name, set := range map[string]WordSet{
		"methods":                  v.Methods,
		"result_discriminators":    v.ResultDiscriminators,
		"error_codes":              v.ErrorCodes,
		"event_kinds":              v.EventKinds,
		"subscription_event_kinds": v.SubscriptionEventKinds,
		"subscription_types":       v.SubscriptionTypes,
		"agent_statuses":           v.AgentStatuses,
		"pane_agent_states":        v.PaneAgentStates,
		"read_sources":             v.ReadSources,
		"read_formats":             v.ReadFormats,
		"output_match_types":       v.OutputMatchTypes,
		"notification_sounds":      v.NotificationSounds,
		"toast_positions":          v.ToastPositions,
	} {
		if len(set.ClientUsed) == 0 {
			t.Errorf("%s: expected a non-empty client_used annotation", name)
		}
	}

	// pane_agent_states has NO done — a pane never reports completion, only
	// the agent on it does. Pin that asymmetry against agent_statuses.
	if v.PaneAgentStates.Has("done") {
		t.Error("pane_agent_states must not contain done")
	}
	if !v.AgentStatuses.Has("done") {
		t.Error("agent_statuses must contain done")
	}

	// state_label_keys == agent_statuses (Load enforces it; assert the
	// exact rule here so a future category change is a conscious edit).
	if v.Metadata.StateLabelKeysAre != "agent_statuses" {
		t.Errorf("metadata.state_label_keys_are = %q, want agent_statuses", v.Metadata.StateLabelKeysAre)
	}

	// Both spellings of the pane-status world are pinned: the underscored
	// event kind and the dotted subscription kind for the same word.
	underscored := "pane_agent_status_changed"
	dotted := "pane.agent_status_changed"
	if !v.EventKinds.Has(underscored) {
		t.Errorf("event_kinds must contain %q", underscored)
	}
	if !v.SubscriptionEventKinds.Has(dotted) {
		t.Errorf("subscription_event_kinds must contain %q", dotted)
	}

	// The metadata pins mirror the schema's PaneReportMetadataParams.
	if v.Metadata.TokenNamePattern != "^[A-Za-z0-9_-]{1,32}$" {
		t.Errorf("metadata.token_name_pattern = %q", v.Metadata.TokenNamePattern)
	}
	if v.Metadata.MaxTokens != 16 || v.Metadata.TTLmsMin != 1 || v.Metadata.TTLmsMax != 86400000 {
		t.Errorf("metadata token/ttl pins drifted: max_tokens=%d ttl_ms_min=%d ttl_ms_max=%d",
			v.Metadata.MaxTokens, v.Metadata.TTLmsMin, v.Metadata.TTLmsMax)
	}
}

func TestProvenanceComplete(t *testing.T) {
	v := MustLoad()
	// Every observed error code must carry a note — that category cannot be
	// live-diffed, so its notes ARE the provenance.
	for _, code := range v.ErrorCodes.Members {
		if _, ok := v.ErrorCodes.Notes[code]; !ok {
			t.Errorf("error_codes: %q has no provenance note", code)
		}
	}
	// The source block must say how to re-observe the vocabulary.
	if v.Source.SchemaCommand != "herdr api schema --json" {
		t.Errorf("source.schema_command = %q", v.Source.SchemaCommand)
	}
	if v.Source.AgentKindsCommand != "herdr agent start --help" {
		t.Errorf("source.agent_kinds_command = %q", v.Source.AgentKindsCommand)
	}
}
