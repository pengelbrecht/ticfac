/**
 * The order-checked reads behind the agent-faults proof's claims (tick umx).
 * proof/agent-faults-staging.ts imports these so the claims live in code
 * rather than in a hand-read of the evidence; test/agent-faults-claims.test.ts
 * pins them. The proof's log is an append-only stream, so a line's position
 * in it is its order in time: "after the resume" is a comparison of
 * positions, never a match against the whole log.
 */

/** The host's resume line — a new WorkerAgent life picked the conversation up. */
export const resumedFromStorage = /a new host life resumed the conversation from its storage/;

/** One round's `wip checkpoint <sha12> pushed` line; a failed one reads differently. */
const wipCheckpointPushed = /wip checkpoint [0-9a-f]+ pushed/g;

/**
 * The wip checkpoints that landed AFTER the resume line. The "kept landing
 * after the resume" claim is about the RESUMED host still checkpointing, so a
 * checkpoint the old host pushed before the deploy killed it does not satisfy
 * it (tick umx) — the old claim matched anywhere in the log, and before the
 * fix a pre-kill checkpoint passed it. 0 also answers for a log that shows no
 * resume at all: the claim this feeds then fails on its own, next to the
 * resume claim that fails beside it, which is the honest verdict.
 */
export function wipCheckpointsAfterResume(log: string): number {
  const resume = resumedFromStorage.exec(log);
  if (resume === null) return 0;
  const after = log.slice(resume.index + resume[0].length);
  return (after.match(wipCheckpointPushed) ?? []).length;
}
