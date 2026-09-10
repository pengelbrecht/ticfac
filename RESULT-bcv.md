# tick bcv — a changed herdr field is loud: strict decode of required fields

Worktree `tick/bcv` from a085b31. Everything is in `internal/herd/client` (the
transport client tick 579 lifted) and `internal/herd/herdtest` (the one fake
server); no other package is touched.

## What changed

**Required fields are REQUIRED — the decision and its mechanism.** The tick
asked for a decision between `DisallowUnknownFields`, pointer fields, or a
validate step, logged. The decision is a **validate step**: presence checks in
custom `UnmarshalJSON` methods, new file `internal/herd/client/strict.go`.

- `DisallowUnknownFields` was rejected because unknown ADDED fields must stay
  tolerated — a newer herdr is expected to only add (protocol.go's
  forward-compatibility policy). `TestAddedUnknownFieldsAreTolerated` pins it.
- Pointer fields were rejected because they push presence into every consumer
  and make nil a third state to mishandle at each use site. The struct shapes
  stay exactly as tick 579 lifted them; the strictness lives in one place.

The rule enforced: a field whose zero value inverts a safety property must be
PRESENT on the wire. Presence means the key is carried; for fields whose
"none" is a real value (`name`, `agent_session`) an explicit null is present
and decodes to nil; for fields with no meaningful "none" (`agent_status`,
`interactive_ready`, an event's `pane_id`) a null is refused as loudly as an
absent key. Scope: the four load-bearing AgentInfo fields (`agent_status`,
`interactive_ready`, `name`, `agent_session`), `agent_status` on the other
three carriers (`PaneInfo`, `WorkspaceInfo`, `TabInfo`), and `pane_id` +
`agent_status` on the status-change event payload (`PaneAgentStatusChanged`)
— the event the wave fan-in waits on. Everything else stays stock-lenient.

**Unknown agent status is an explicit error.** `AgentStatus.UnmarshalJSON`
validates the closed set (idle, working, blocked, done, unknown) and returns
a typed `UnknownAgentStatusError`; because it is the field's type method,
every surface that decodes an `agent_status` gets the check at once. A
renamed status used to decode as `""` — silently non-terminal, the
30-minute-hang shape; now it is a loud refusal.

**One bad event no longer kills the stream.** `EventStream.run` (events.go)
skipped nothing before: a single unparseable line ended the subscription and
left a fan-in gap. Now it records the skip and keeps reading; new accessors
`SkippedEvents() int` and `LastSkipped() string` make the loss countable and
diagnosable. A mid-stream API error envelope still ends the stream
(that test is unchanged), and unknown non-event lines are still ignored.

**The resultType swallow is gone.** `resultType` discarded its unmarshal
error, so a result that was not decodable JSON was misfiled as protocol drift
(`UnexpectedResultError{Got:""}`). It now returns the decode error, and
`decodeTyped` wraps it with the method name.

## Typed errors

Both new errors are typed so callers can tell "the wire shape changed" from
a transport failure, and both ride inside the existing method-name wrapper
(`herd/client: decoding agent.get result: …`), so the failure names method
AND field:

- `RequiredFieldError{Type, Field, Null}` — "… is missing required field …"
- `UnknownAgentStatusError{Status}` — 'unknown agent_status "finished" —
  herdr reports one of "idle", "working", "blocked", "done", "unknown"'

## Tests (all in strict_test.go + the rewritten events_test.go case)

- `TestAgentInfoFieldRenamesAreLoud` — the four load-bearing renames, one
  subtest each (agent_status→agent_state, interactive_ready→ready,
  name→agent_name, agent_session→session) through agent.get: typed error
  naming method and field, and NO zero-valued AgentInfo returned.
- The same rename proven through `agent.list`, `agent.wait` and
  `session.snapshot` agents[] (each names its own method).
- `TestUnknownAgentStatusIsLoud` + unknown-status cases in the snapshot
  (PaneInfo) and event payload — an unknown status is an explicit
  `UnknownAgentStatusError`, never a silently non-terminal "".
- `TestEventPayloadStrictnessIsLoud` — renamed/unknown `agent_status` and
  renamed `pane_id` in the fan-in event payload.
- `TestAddedUnknownFieldsAreTolerated` — added fields at result level and in
  the agent object decode fine (forward compatibility).
- `TestNullOptionalAgentFieldsDecode` / `TestNullRequiredValuesAreLoud` —
  null semantics pinned in both directions.
- `TestMalformedResultIsALoudDecodeError` — the un-swallowed resultType
  error, asserted NOT to be an `UnexpectedResultError`.
- `TestEventStreamSkipsMalformedLineAndKeepsSubscription` (replaces
  `TestEventStreamNamesMalformedLine`, which pinned the old kill-the-stream
  behaviour): the events around the malformed line still arrive, the skip is
  counted and named, `Err()` stays nil.

## Fixture and fake edits, with provenance

The curated testdata captures are 0.8.0-era and PREDATE the four fields being
load-bearing — the captured agents carried no `name`/`interactive_ready` at
all. They were edited to the pinned 0.8.2 shape (the four keys always
carried, null where absent), noted in protocol.go's fixture comment; the
live 0.8.2 re-capture this presumes is already open work (tick yod /
RESULT-3nh.md). `herdtest` now always carries the four keys too —
`AgentJSON`, agent.start, agent.prompt and the agent.get fallback — and its
stale "key set of agent_list.json, interactive_ready included" comment
(which the fixture contradicted) is corrected.

## Boundary: the AgentGet swallow

The tick's fourth swallow — spawn.go discarding the `AgentGet` error while
back-filling `agent_session` — lives in **ticks'** `internal/herd/spawn.go`,
which is not in this repo's tree (ticfac depends on ticks only through
`tk --json` and pinned contracts; the spawn/readiness logic lands here with
tick **d9m**, the herdr executor). Nothing in ticfac swallows an AgentGet
error, so there is nothing to remove here. What this tick does make
impossible is the confusion the swallow traded on: with `agent_session`
presence-required, "the lookup failed" (an error) is now distinct from "the
agent has no session" (a nil value on a decoded agent), so d9m's back-fill
has a loud primitive to build on and must propagate the error rather than
treat it as a no-session answer.

## Verification

- `make test-short` (`go test -short -timeout 45m ./...`) — green, all
  packages (reconcile 917s, within the pinned budget): run twice, before and
  after the final strict.go refactor, which only touched `internal/herd/client`.
- `go vet ./...`, `gofmt -l` — clean.
- New tests verified to fail at the base behaviour's shape: at a085b31 a
  renamed `agent_status` decodes to `""` with no error, which is exactly
  what each rename test now refuses.

STATUS: DONE
