// Package wirevocab pins the herdr wire vocabulary — method names, result
// discriminators, error codes, agent status words, both event-kind spellings,
// subscription types and their pane scoping, agent kinds, and the metadata
// source id and state-label keys — as one JSON contract, herd-vocabulary.json.
//
// The contract is diffed against three audiences:
//
//   - the LIVE server: drift_test.go runs `herdr api schema --json` and
//     compares every schema-enumerated category for set equality, both
//     directions, whenever a herdr binary is on PATH;
//   - the CLIENT: internal/herd/client/vocabulary_contract_test.go pins the
//     client's Go constants member-for-member against the client_used
//     annotations, so a one-sided edit fails the build;
//   - the FAKE: internal/herd/herdtest/vocabulary_test.go pins the fake's
//     constants and dials every builtin, asserting what it serves is contract
//     vocabulary and nothing else.
//
// The contract file follows the shape of contracts/collect-vocabulary.json
// but deliberately lives here: contracts/ is a digest-pinned vendored bundle
// from pengelbrecht/ticks that CONTRACTS.md freezes — this repository's own
// vocabulary must not edit or extend it.
package wirevocab

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed herd-vocabulary.json
var raw []byte

// WordSet is one category of wire words. Members is the full server-side
// set; ClientUsed annotates the members this repo's client spells as Go
// constants (pinned bidirectionally by the client's contract test); Notes
// carries per-member provenance for categories the live schema does not
// enumerate; PaneScoped (subscription types only) names the members whose
// request requires a pane_id.
type WordSet struct {
	Why        []string          `json:"why"`
	Members    []string          `json:"members"`
	ClientUsed []string          `json:"client_used,omitempty"`
	PaneScoped []string          `json:"pane_scoped,omitempty"`
	Notes      map[string]string `json:"notes,omitempty"`
}

// Has reports whether member is in the set.
func (w WordSet) Has(member string) bool {
	for _, m := range w.Members {
		if m == member {
			return true
		}
	}
	return false
}

// validate checks the structural invariants every category shares: no
// duplicate members, and every annotation (client_used, pane_scoped, notes)
// naming a member the set does not carry.
func (w WordSet) validate(name string) error {
	if err := checkNoDupes(name, w.Members); err != nil {
		return err
	}
	for _, label := range []struct {
		set  []string
		tag  string
		full bool
	}{
		{w.ClientUsed, "client_used", false},
		{w.PaneScoped, "pane_scoped", false},
	} {
		if err := checkNoDupes(name+"."+label.tag, label.set); err != nil {
			return err
		}
		for _, m := range label.set {
			if !w.Has(m) {
				return fmt.Errorf("wirevocab: %s.%s lists %q, which is not a member", name, label.tag, m)
			}
		}
	}
	for m := range w.Notes {
		if !w.Has(m) {
			return fmt.Errorf("wirevocab: %s.notes documents %q, which is not a member", name, m)
		}
	}
	return nil
}

// Source records how the live vocabulary was observed: the commands the drift
// test re-runs, plus dated findings about what the schema can and cannot pin.
type Source struct {
	Why               []string `json:"why"`
	SchemaCommand     string   `json:"schema_command"`
	AgentKindsCommand string   `json:"agent_kinds_command"`
	Observations      []string `json:"observations"`
}

// Metadata is the metadata channel's own vocabulary. SourceID is OUR source
// id (not the server's); StateLabelKeysAre names the category whose members
// StateLabelKeys must equal — herdr renders one label per agent status, so
// the key set IS the status set. The token and ttl pins mirror the schema's
// PaneReportMetadataParams.
type Metadata struct {
	Why               []string `json:"why"`
	SourceID          string   `json:"source_id"`
	StateLabelKeysAre string   `json:"state_label_keys_are"`
	StateLabelKeys    []string `json:"state_label_keys"`
	TokenNamePattern  string   `json:"token_name_pattern"`
	MaxTokens         int      `json:"max_tokens"`
	TTLmsMin          int      `json:"ttl_ms_min"`
	TTLmsMax          int      `json:"ttl_ms_max"`
}

// Vocabulary is the parsed contract.
type Vocabulary struct {
	Why    []string `json:"why"`
	Source Source   `json:"source"`

	Methods                WordSet  `json:"methods"`
	ResultDiscriminators   WordSet  `json:"result_discriminators"`
	ErrorCodes             WordSet  `json:"error_codes"`
	EventKinds             WordSet  `json:"event_kinds"`
	SubscriptionEventKinds WordSet  `json:"subscription_event_kinds"`
	SubscriptionTypes      WordSet  `json:"subscription_types"`
	AgentStatuses          WordSet  `json:"agent_statuses"`
	PaneAgentStates        WordSet  `json:"pane_agent_states"`
	AgentKinds             WordSet  `json:"agent_kinds"`
	ReadSources            WordSet  `json:"read_sources"`
	ReadFormats            WordSet  `json:"read_formats"`
	OutputMatchTypes       WordSet  `json:"output_match_types"`
	NotificationSounds     WordSet  `json:"notification_sounds"`
	ToastPositions         WordSet  `json:"toast_positions"`
	Metadata               Metadata `json:"metadata"`
}

// Load parses the embedded contract and enforces its cross-category rules.
// It cannot fail for a well-formed embedded file, so the exported MustLoad is
// the ergonomic face; tests that want the error use Load.
func Load() (*Vocabulary, error) {
	var v Vocabulary
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("wirevocab: parsing embedded herd-vocabulary.json: %w", err)
	}
	for name, set := range map[string]WordSet{
		"methods":                  v.Methods,
		"result_discriminators":    v.ResultDiscriminators,
		"error_codes":              v.ErrorCodes,
		"event_kinds":              v.EventKinds,
		"subscription_event_kinds": v.SubscriptionEventKinds,
		"subscription_types":       v.SubscriptionTypes,
		"agent_statuses":           v.AgentStatuses,
		"pane_agent_states":        v.PaneAgentStates,
		"agent_kinds":              v.AgentKinds,
		"read_sources":             v.ReadSources,
		"read_formats":             v.ReadFormats,
		"output_match_types":       v.OutputMatchTypes,
		"notification_sounds":      v.NotificationSounds,
		"toast_positions":          v.ToastPositions,
	} {
		if len(set.Members) == 0 {
			return nil, fmt.Errorf("wirevocab: %s has no members", name)
		}
		if err := set.validate(name); err != nil {
			return nil, err
		}
	}

	// herdr reuses the DOTTED subscription name verbatim as the
	// subscription_event kind, and only the three pane-scoped subscriptions
	// produce those envelopes — so the two sets are the same set.
	if err := checkSameSet(
		"subscription_event_kinds",
		v.SubscriptionEventKinds.Members,
		"subscription_types.pane_scoped",
		v.SubscriptionTypes.PaneScoped,
	); err != nil {
		return nil, err
	}

	// state_label_keys_are is a category reference: herdr renders one
	// label per agent status, so the pinned key set must equal the named
	// category's members.
	var stateLabelCategory []string
	switch v.Metadata.StateLabelKeysAre {
	case "agent_statuses":
		stateLabelCategory = v.AgentStatuses.Members
	default:
		return nil, fmt.Errorf(
			"wirevocab: metadata.state_label_keys_are names unknown category %q",
			v.Metadata.StateLabelKeysAre)
	}
	if err := checkNoDupes("metadata.state_label_keys", v.Metadata.StateLabelKeys); err != nil {
		return nil, err
	}
	if err := checkSameSet(
		"metadata.state_label_keys",
		v.Metadata.StateLabelKeys,
		v.Metadata.StateLabelKeysAre,
		stateLabelCategory,
	); err != nil {
		return nil, err
	}
	if v.Metadata.SourceID == "" {
		return nil, fmt.Errorf("wirevocab: metadata.source_id is empty")
	}
	return &v, nil
}

// MustLoad parses the embedded contract and panics on any violation. The file
// is embedded, so a violation means the contract or this package is broken,
// not the environment.
func MustLoad() *Vocabulary {
	v, err := Load()
	if err != nil {
		panic(err)
	}
	return v
}

// checkNoDupes fails when set repeats a member — a duplicate would make every
// count-based assertion lie about the vocabulary's size.
func checkNoDupes(name string, set []string) error {
	seen := make(map[string]bool, len(set))
	for _, m := range set {
		if seen[m] {
			return fmt.Errorf("wirevocab: %s repeats member %q", name, m)
		}
		seen[m] = true
	}
	return nil
}

// checkSameSet reports the members present on only one side, so a failure
// reads as a vocabulary diff rather than a bare inequality.
func checkSameSet(leftName string, left []string, rightName string, right []string) error {
	lset := make(map[string]bool, len(left))
	for _, m := range left {
		lset[m] = true
	}
	rset := make(map[string]bool, len(right))
	for _, m := range right {
		rset[m] = true
	}
	var onlyLeft, onlyRight []string
	for _, m := range left {
		if !rset[m] {
			onlyLeft = append(onlyLeft, m)
		}
	}
	for _, m := range right {
		if !lset[m] {
			onlyRight = append(onlyRight, m)
		}
	}
	if len(onlyLeft) == 0 && len(onlyRight) == 0 {
		return nil
	}
	sort.Strings(onlyLeft)
	sort.Strings(onlyRight)
	return fmt.Errorf(
		"wirevocab: %s and %s differ: only in %s: %v; only in %s: %v",
		leftName, rightName, leftName, onlyLeft, rightName, onlyRight)
}
