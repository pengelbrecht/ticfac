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
//     revoke-then-signal, is REFUSED with the decided reason (see below):
//     there is no per-attempt credential on this substrate to revoke and no
//     process on this side of the boundary to signal, so a best-effort
//     request nothing acknowledges is never made.
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
// # What is deliberately not here, and where each operation LIVES
//
// Which of the four operations cross the Worker door and which the Go side
// does from git was the open decision this package refused collect, cancel
// and dispose for; it is DECIDED (tick xev, absorbing the findings against
// 8ty) and recorded where the executor's doc says the contract lives — the
// "What is deliberately NOT here" section of cloudflare/src/sandbox-dispatch.ts,
// the one place the HTTP contract lives:
//
//   - DISPATCH and INSPECT cross the door, as built: a container cannot
//     create a sibling sandbox — the binding is a Worker binding — and the
//     door is the only route to boot or re-address one.
//   - COLLECT is the GO side's, from git, and never a door route. The
//     orchestrator holds the clone; the worker's container pushed its
//     landing branch with the report its own entrypoint committed at
//     RESULT-<tick>.md; the collect reads that durable layer (collect.go)
//     exactly the way worker-collect.ts reads it through GitHub's API. The
//     door stays two routes by decision, not by omission.
//   - CANCEL is the FACTORY's, and this executor refuses it typed
//     (RefusedCancelOwnedByFactory): the credential a container holds is the
//     run's own gateway token (D17), whose revocation is the run-level kill
//     switch the door already honours — there is no per-attempt dispatch to
//     revoke — and stopping one container is the teardown the Worker-side
//     executor and the queue-expiry sweep already own.
//   - DISPOSE has nothing to act on on this side of the boundary, and is
//     refused typed (RefusedNothingLocalToDispose): no worktree, no local
//     branch, no credential; the container belongs to the factory that
//     booted it, and the branch the work landed on is retired by the close.
//
// The reconciler's teardown treats a Cancel refusal as a recorded line and
// continues, so a run through this executor stays honest without a cancel
// door that would only ever half-exist.
//
// The executor is registered in internal/cli's honoured set beside
// subprocess and herdr: a profile naming cloudflare-sandbox (the ones in
// profiles-cloudflare-sandbox/, tick njj — dispatching pi on GLM 5.3 through
// this executor) resolves, and the factory routes its dispatches here. The
// factory configuration — the factory's base URL and the run's own gateway
// token — comes from the container boot's TICKS_FACTORY_URL and
// TICKS_FACTORY_TOKEN (or the same variables on a laptop driving cloud
// workers, the local-judgement-cloud-hands shape), and the repository the
// collect reads is the orchestrator's own checkout, which is why it enters
// the options here rather than the dispatch's spec: the sandbox container
// is the worker's whole environment, but the COLLECT is this side's.
package cloudflaresandbox
