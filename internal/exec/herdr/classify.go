package herdr

import (
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// THE AUDIT (tick x6j): every herdr call site in this executor, and the class
// of every failure it can produce. A substrate failure is an OPERATIONAL
// error and never a verdict about the work: an error means the check could
// not be performed, never "the worker failed". Verdicts come only from
// durable evidence — git, the report, this executor's own settlement and
// cancellation records — and from herdr's POSITIVE answers, recorded durably
// at the moment they were observed.
//
// Three classes, and only three:
//
//   - OPERATIONAL: the call could not be performed, or its answer could not
//     be decoded. An operational failure rejects no tick (no error carries a
//     verdict about work nobody could look at), tears no attempt down, and
//     never deletes a branch, a workspace or a state directory. Where the
//     failure asks a question a later leg needs answered, the answer is the
//     HOLD: lost at inspect, the liveness-unknown refusal at start, adopt
//     and collect — a person decides, never a redispatch.
//   - POSITIVE: herdr ANSWERED. agent_not_found and pane_not_found are not
//     failures; they are the substrate's own evidence that nobody is there,
//     and they are recorded durably (the agent-gone marker) so a later,
//     herdr-free leg reads them as evidence rather than asking again.
//   - VERDICT: never produced by a herdr call directly. A verdict is minted
//     only from the durable layer: the report and the branch (collect), the
//     cancellation record, the agent-gone marker, or this executor's own
//     record of a launch that never happened.
//
// The call sites, one line each, with the class of every failure they can
// produce:
//
//   - New → client.New (the ping handshake): dial failure, protocol refusal
//     (ProtocolMismatchError) — OPERATIONAL. Construction fails before
//     anything is claimed, created or deleted; the run reports the error and
//     the tick is neither rejected nor redispatched. A capability refusal
//     (CapabilityError) is OPERATIONAL the same way — and this executor's
//     five operations demand no capability at all, pinned by
//     TestTheExecutorDemandsNoCapability, so the refusal can never arise
//     from them.
//   - Start → WorktreeCreate: any error, any code, a success reply with no
//     usable shape — OPERATIONAL. Nothing is cleaned up, nothing is deleted;
//     the next Start of the identity is refused by the branch-exists check
//     and held.
//   - Start → the attempt record on disk exists but cannot be read (a
//     schema this build does not know — resume across an upgrade): the
//     handle is one a later leg cannot address — OPERATIONAL, held for a
//     person (RefusedUnknown), never redispatched.
//   - startAgent → AgentStart: agent_pane_busy is the tolerated startup
//     race, retried within the caller's budget. Every other code —
//     including a RENAME of agent_pane_busy, or any code this build has
//     never heard — is OPERATIONAL: the launch is recorded unconfirmed, the
//     pane and the worktree stay as diagnostic state, nothing is cleaned
//     up, and a retry is a new attempt number, never a verdict.
//   - waitInteractiveReady → AgentGet: any error, the budget elapsing —
//     OPERATIONAL, same treatment as the launch.
//   - submit → AgentPrompt: agent_prompt_stalled is the tolerated dropped
//     prompt; every other answer — including a RENAME, or a rendering or
//     truncation change nobody has a name for yet — is OPERATIONAL and is
//     recorded as an observation. There is no content gate reading the pane
//     back in this executor, so no rendering change can ever turn into "the
//     agent cannot work".
//   - submit → AgentWait: the timeout is a REPORTED outcome; any other
//     error is OPERATIONAL. Either way the dispatch is recorded
//     unconfirmed — a fact, not a failure.
//   - Inspect → AgentGet (observe): nil → running. agent_not_found /
//     pane_not_found → POSITIVE: settled, with the answer recorded durably
//     as the agent-gone marker. Any other error — transport silence,
//     protocol drift the strict decoder refuses, an unknown code — is
//     OPERATIONAL: lost, which is not terminal and holds the attempt for a
//     person.
//   - Cancel → hasSettled's AgentGet: POSITIVE (gone) → settled, nothing is
//     spending, no cancellation record is written over a verdict the
//     attempt already settled itself into. Any other error → OPERATIONAL:
//     silence is not evidence nothing is spending, so the full refusal is
//     recorded.
//   - Cancel → AgentSendKeys: nil → interrupted. gone → POSITIVE, nothing
//     to interrupt. Any other error → OPERATIONAL: the revocation is
//     already durable, the interrupt is reported as the error it is, and no
//     teardown follows from it.
//   - Dispose → livenessForTeardown's AgentGet: gone → POSITIVE — the one
//     answer that makes removal safe, recorded durably. Working / blocked →
//     the removal is REFUSED (a live worker is never torn down). Any other
//     error — "herdr did not answer" — is OPERATIONAL and the removal
//     refuses: silence is not evidence that tearing an agent down is safe.
//   - Dispose → WorktreeRemove: workspace_not_found → POSITIVE, the state
//     the step exists to reach. Any other error → OPERATIONAL: the attempt
//     is not torn down, the branch is not deleted, nothing else is touched.
//   - Collect → no herdr call at all, by construction: the verdict is read
//     from the report, the branch, the cancellation record and the
//     agent-gone marker. An attempt with NO report and NO settlement any
//     leg recorded is not collect's to settle: the collect refuses with the
//     liveness-unknown hold rather than minting a verdict out of the
//     substrate's silence.
//
// The fixture that settles this classification is
// TestEveryHerdrCallFailingDecidesNothing: every herdr call fails, and no
// operation turns that into a verdict, a teardown or a deletion.

// gone reports whether err is herdr's POSITIVE answer that nobody is there:
// the agent does not resolve, or the pane it occupied does not. It is the
// one class of herdr error that is not a failure but evidence — the caller
// records it durably (the agent-gone marker) and treats it as settlement,
// never as an operational refusal.
func gone(err error) bool {
	return client.IsCode(err, client.CodeAgentNotFound) || client.IsCode(err, client.CodePaneNotFound)
}
