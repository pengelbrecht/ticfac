// Package cloudflaresandbox is ticfac's Cloudflare sandbox executor: the
// same five operations internal/reconcile already knows — Start, Inspect,
// CollectDetail, Cancel, Dispose — over the factory's per-tick sandbox
// dispatch door (cloudflare/src/sandbox-dispatch.ts, tick 8ty), where the
// work runs inside a sandbox container the factory boots and owns.
//
// It is the third implementation of the seam Phase 1 defined. The protocol
// records (JobSpec, JobHandle, JobStatus, CancelAck, JobResult) and the
// refusal vocabulary are the ones internal/exec/subprocess owns, because
// they are the CONTRACT's, not that executor's; this package imports them
// rather than re-spelling them, so three executors cannot disagree about a
// record with nothing failing. Everything substrate-specific — the
// container's name, the work process id, the run, the epic, the base ref —
// lives inside JobHandle.Handle, the one open object in the contract, and
// inside this package.
//
// # The shape that matters: a handle, not a result
//
// The door is built around NOT waiting, and so is this executor. Start asks
// the factory to boot one attempt's container and comes back with the
// handle the factory minted, while the tick's work goes on running inside
// the container. Inspect re-asks the door BY IDENTITY — run from the
// credential, tick and attempt from the path — and maps the answer onto the
// JobStatus every other executor answers in. An executor that blocked until
// the attempt finished would put a wall clock inside a dispatch, which is
// exactly the thing the "nothing waits" discipline exists to forbid.
//
// # Where each local assumption was reinterpreted
//
// The seam was defined over a LOCAL process, and a sandbox container over
// HTTP is not one. Each reinterpretation is named here rather than papered
// over, because each is a place a future reader could otherwise assume the
// old semantics still hold:
//
//   - SIGNALS BECOME HTTP VERDICTS. There is no process in this container
//     to signal and no exit code to read directly: "it exited 0" arrives
//     as the door's `succeeded`/`failed` state with the exit code riding an
//     `exited` observation's detail text. Cancel, whose whole contract is
//     revoke-then-signal, has no door to go through yet and is REFUSED
//     (see below) rather than half-implemented as a best-effort request
//     nothing acknowledges.
//   - STDIO BECOMES NOTHING AT ALL. The container's terminal output is not
//     reachable through this door and is deliberately not missed: the
//     completion contract is the branch and the RESULT report in git, the
//     same durable layer every executor's collect reads from, and terminal
//     output is diagnostic material the door's `detail` text carries in
//     bounded form.
//   - THE EXIT CODE IS THE DOOR'S OBSERVATION, NOT A RE-READ OF THE PROCESS.
//     A settled container is inspected by name, exactly as a live one is:
//     the door re-addresses the sandbox and reports what its own records
//     hold, so "finished with a result" stays the factory's positive
//     answer and never this client's inference from silence.
//   - LIVENESS IS THE OBSERVER'S, AND UNREACHABLE IS NOT ABSENT. The door
//     answers `lost` when no container under this identity holds a work
//     process — a statement about the observer, not a verdict. A door this
//     client cannot REACH is a transport error and never a `lost`: reading
//     an outage as absence is how an attempt gets written off while its
//     container is still running (tick avx's rule, held in the client's
//     error handling where the route's own header says it lives).
//
// # What is deliberately not here yet
//
// Collect, cancel and dispose are not part of the door, and this executor
// refuses all three with a typed refusal rather than inventing a second
// mechanism beside the one the door's own header says is the only place the
// contract may grow ("when the Go executor needs either over this boundary,
// they land here beside the two routes above"). Collect reads the durable
// layer — git, which the orchestrator's own container holds a clone of —
// and cancel is a salvage door plus a teardown the Worker-side executor
// already owns; where each of those operations lives is an open decision
// this tick was told not to make for the whole seam (the finding triaged
// against tick 8ty), so this package implements the two operations the door
// carries and fails CLOSED on the other three, naming the decision, so a
// run that reaches one of them stops honestly instead of half-acting.
//
// The executor is not yet wired into internal/cli's honoured set either:
// profiles-cloudflare-sandbox/ names it (tick njj — the dispatch profile
// pairing pi on GLM 5.3 with this executor), but no run can SELECT that
// profile until the collect-and-cancel decision lands and a follow-on tick
// registers this executor beside subprocess and herdr, so nothing in
// production can dispatch through an executor whose settle path refuses.
package cloudflaresandbox
