package client

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/herd/wirevocab"
)

// The client's half of the wire vocabulary contract: every constant this
// package spells is pinned member-for-member against
// internal/herd/wirevocab/herd-vocabulary.json, in both directions. A
// one-sided edit — a constant renamed here, a word re-pinned there — fails
// the build instead of surfacing as a cryptic protocol error in a run.
//
// The contract's client_used annotations are the exact client surface; the
// full member sets are pinned against the live server by the wirevocab
// drift test, and against the herdtest fake by its own vocabulary test.

func diff(t *testing.T, name string, contract, client []string) {
	t.Helper()
	inContract := make(map[string]bool, len(contract))
	for _, m := range contract {
		inContract[m] = true
	}
	inClient := make(map[string]bool, len(client))
	for _, m := range client {
		inClient[m] = true
	}
	for _, m := range contract {
		if !inClient[m] {
			t.Errorf("vocabulary %s: contract pins %q but the client has no constant for it", name, m)
		}
	}
	for _, m := range client {
		if !inContract[m] {
			t.Errorf("vocabulary %s: client constant %q is not pinned in the contract", name, m)
		}
	}
}

func TestMethodConstantsMatchVocabulary(t *testing.T) {
	v := wirevocab.MustLoad()
	diff(t, "methods", v.Methods.ClientUsed, []string{
		MethodPing, MethodSessionSnapshot,
		MethodWorktreeCreate, MethodWorktreeList, MethodWorktreeRemove,
		MethodWorkspaceFocus,
		MethodAgentStart, MethodAgentPrompt, MethodAgentWait,
		MethodAgentList, MethodAgentGet,
		MethodPaneRead, MethodPaneWaitForOutput,
		MethodPaneReportMetadata, MethodWorkspaceReportMetadata,
		MethodEventsSubscribe, MethodEventsWait,
		MethodNotificationShow,
	})
}

func TestResultDiscriminatorConstantsMatchVocabulary(t *testing.T) {
	v := wirevocab.MustLoad()
	diff(t, "result_discriminators", v.ResultDiscriminators.ClientUsed, []string{
		resultPong, resultSessionSnapshot,
		resultWorktreeCreated, resultWorktreeList, resultWorktreeRemoved,
		resultWorkspaceInfo,
		resultAgentStarted, resultAgentPrompted, resultAgentInfo,
		resultAgentList,
		resultPaneRead, resultOutputMatched,
		resultSubscriptionStarted, resultWaitMatched,
		resultNotificationShow, resultOK,
	})
}

func TestErrorCodeConstantsMatchVocabulary(t *testing.T) {
	v := wirevocab.MustLoad()
	diff(t, "error_codes", v.ErrorCodes.ClientUsed, []string{
		CodeTimeout, CodeInvalidRequest,
		CodeAgentNotFound, CodePaneNotFound, CodeWorkspaceNotFound,
		CodeAgentPaneBusy, CodeAgentPromptStalled,
	})
}

func TestEventKindConstantsMatchVocabulary(t *testing.T) {
	v := wirevocab.MustLoad()
	// The underscored `event` kinds — one constant each.
	diff(t, "event_kinds", v.EventKinds.ClientUsed, []string{
		string(EventWorkspaceCreated), string(EventWorkspaceUpdated),
		string(EventWorkspaceMetadataUpdated), string(EventWorkspaceClosed),
		string(EventWorkspaceRenamed), string(EventWorkspaceMoved),
		string(EventWorkspaceReordered), string(EventWorkspaceFocused),
		string(EventWorktreeCreated), string(EventWorktreeOpened),
		string(EventWorktreeRemoved),
		string(EventTabCreated), string(EventTabClosed),
		string(EventTabRenamed), string(EventTabMoved), string(EventTabFocused),
		string(EventPaneCreated), string(EventPaneClosed),
		string(EventPaneUpdated), string(EventPaneFocused),
		string(EventPaneMoved), string(EventPaneOutputChanged),
		string(EventPaneExited), string(EventPaneAgentDetected),
		string(EventPaneAgentStatusChanged),
		string(EventLayoutUpdated),
	})
	// The dotted `subscription_event` kinds — exactly the three pane-scoped
	// subscriptions, spelled like the subscription that asked for them.
	diff(t, "subscription_event_kinds", v.SubscriptionEventKinds.ClientUsed, []string{
		string(EventScopedPaneAgentStatusChanged),
		string(EventScopedPaneOutputMatched),
		string(EventScopedPaneScrollChanged),
	})
}

func TestSubscriptionTypeConstantsMatchVocabulary(t *testing.T) {
	v := wirevocab.MustLoad()
	diff(t, "subscription_types", v.SubscriptionTypes.ClientUsed, []string{
		string(SubWorkspaceCreated), string(SubWorkspaceUpdated),
		string(SubWorkspaceMetadataUpdated), string(SubWorkspaceRenamed),
		string(SubWorkspaceMoved), string(SubWorkspaceReordered),
		string(SubWorkspaceClosed), string(SubWorkspaceFocused),
		string(SubWorktreeCreated), string(SubWorktreeOpened),
		string(SubWorktreeRemoved),
		string(SubTabCreated), string(SubTabClosed), string(SubTabFocused),
		string(SubTabRenamed), string(SubTabMoved),
		string(SubPaneCreated), string(SubPaneClosed), string(SubPaneUpdated),
		string(SubPaneFocused), string(SubPaneMoved), string(SubPaneExited),
		string(SubPaneAgentDetected), string(SubPaneOutputMatched),
		string(SubPaneAgentStatusChanged), string(SubPaneScrollChanged),
		string(SubLayoutUpdated),
	})

	// Pane scoping: IsPaneScoped must agree with the contract for every
	// subscription type, not just the three true ones — a subscription
	// newly scoped upstream should flip its answer and this test together.
	for _, sub := range v.SubscriptionTypes.Members {
		var typ SubscriptionType = SubscriptionType(sub)
		want := false
		for _, scoped := range v.SubscriptionTypes.PaneScoped {
			if scoped == sub {
				want = true
				break
			}
		}
		if got := typ.IsPaneScoped(); got != want {
			t.Errorf("subscription %s: IsPaneScoped() = %v, contract says %v", sub, got, want)
		}
	}
}

func TestStatusAndEnumConstantsMatchVocabulary(t *testing.T) {
	v := wirevocab.MustLoad()
	diff(t, "agent_statuses", v.AgentStatuses.ClientUsed, []string{
		string(StatusIdle), string(StatusWorking), string(StatusBlocked),
		string(StatusDone), string(StatusUnknown),
	})
	// TerminalStatuses is the default `until` set for a wait — a subset of
	// the vocabulary, not a fresh list.
	for _, s := range TerminalStatuses {
		if !v.AgentStatuses.Has(string(s)) {
			t.Errorf("TerminalStatuses: %q is not an agent_statuses member", s)
		}
	}

	diff(t, "pane_agent_states", v.PaneAgentStates.ClientUsed, []string{
		// PaneAgentState is the same shape the accessors expose for
		// pane-scoped status events; the client pins the words through the
		// AgentStatus constants and accepts the four-state subset.
		"blocked", "idle", "unknown", "working",
	})

	diff(t, "read_sources", v.ReadSources.ClientUsed, []string{
		string(SourceVisible), string(SourceRecent),
		string(SourceRecentUnwrapped), string(SourceDetection),
	})
	diff(t, "read_formats", v.ReadFormats.ClientUsed, []string{
		string(FormatText), string(FormatANSI),
	})
	diff(t, "output_match_types", v.OutputMatchTypes.ClientUsed, []string{
		string(MatchSubstring), string(MatchRegex),
	})
	diff(t, "notification_sounds", v.NotificationSounds.ClientUsed, []string{
		string(SoundNone), string(SoundDone), string(SoundRequest),
	})
	diff(t, "toast_positions", v.ToastPositions.ClientUsed, []string{
		string(PositionTopLeft), string(PositionTopRight),
		string(PositionBottomLeft), string(PositionBottomRight),
	})
}

func TestMetadataPinsMatchVocabulary(t *testing.T) {
	v := wirevocab.MustLoad()
	// SourceHerdPaint is OUR source id, not the server's; herdr keys
	// expiry and overwrite on it, so it is load-bearing.
	if SourceHerdPaint != v.Metadata.SourceID {
		t.Errorf("SourceHerdPaint = %q, contract metadata.source_id = %q", SourceHerdPaint, v.Metadata.SourceID)
	}
	// The TTL bounds the report methods validate must be the schema's:
	// one millisecond to one day, and zero is not a value.
	if MinMetadataTTL != time.Duration(v.Metadata.TTLmsMin)*time.Millisecond {
		t.Errorf("MinMetadataTTL = %v, contract pins %dms", MinMetadataTTL, v.Metadata.TTLmsMin)
	}
	if MaxMetadataTTL != time.Duration(v.Metadata.TTLmsMax)*time.Millisecond {
		t.Errorf("MaxMetadataTTL = %v, contract pins %dms", MaxMetadataTTL, v.Metadata.TTLmsMax)
	}
	// StateLabels keys are the agent statuses herdr renders labels for —
	// pin the words the client's own state machine names them by.
	want := []string{string(StatusIdle), string(StatusWorking), string(StatusBlocked), string(StatusDone), string(StatusUnknown)}
	if !reflect.DeepEqual(v.Metadata.StateLabelKeys, sortedCopy(want)) {
		t.Errorf("metadata.state_label_keys = %v, want the agent status words %v", v.Metadata.StateLabelKeys, want)
	}
}

// sortedCopy returns a sorted copy, for order-insensitive set comparison.
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// outputMatchedForwardCompat is the one string this package switches on
// that is deliberately NOT a contract member: herdr emits only the dotted
// pane.output_matched today, and IsOutputMatched accepts the underscored
// form as forward compatibility. The contract pins what EXISTS, and the
// live drift test enforces set equality against herdr, so this spelling
// can never enter it while herdr stays silent.
//
// If herdr ever speaks the underscored form, the drift test fires first
// (server-only member of event_kinds); this pin fires with it, forcing the
// conscious rewiring: add the contract member, give the client an EventKind
// constant in its place, and drop the raw comparison in IsOutputMatched.
const outputMatchedForwardCompat = "pane_output_matched"

func TestOutputMatchedForwardCompatSpellingIsPinnedAbsent(t *testing.T) {
	v := wirevocab.MustLoad()
	if v.EventKinds.Has(outputMatchedForwardCompat) {
		t.Fatalf("herdr now speaks the underscored %q event kind — pin it as a real EventKind constant, add it to the contract, and drop the forward-compat comparison in IsOutputMatched", outputMatchedForwardCompat)
	}
	// The forward-compat behaviour itself is pinned: the raw spelling
	// still decodes as an output match, exactly as documented in events.go.
	if !(Event{Kind: outputMatchedForwardCompat}).IsOutputMatched() {
		t.Errorf("IsOutputMatched must accept the forward-compat spelling %q, per its contract in events.go", outputMatchedForwardCompat)
	}
}
