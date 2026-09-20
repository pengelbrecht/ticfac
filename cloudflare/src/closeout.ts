/**
 * The CI-gated close-out, ported to the Workflow host from
 * `internal/reconcile` (tick cxk): the rule a target repository declares in
 * its own `.tick/config.md`, the CI read that rule demands — including 9da's
 * ancestor walk — and the body the epic PR carries: the final review's
 * verdict and every finding the run drafted.
 *
 * The port is of the Go SHAPE, not the Go mechanics: the local reconciler
 * sleeps inside its admission loop and reads history from a git checkout;
 * this host's reconciler is a loop of PASSES over durable state (a hold
 * returns to the next pass, which re-derives), and its git is the forge's
 * REST API. The semantics — find or open the PR, carry the body, wait on
 * pending, refuse absent CI, refuse red NAMING THE JOB — are the ones
 * `.tick/config.md` declares and the Go side refuses to run without.
 */

import type { CIReport, PullRequest, PullRequests } from "./forge";
import type { ContentsStore } from "./git-contents";
import type { DecisionRecord } from "./run-state-store";

// ------------------------------------------------------------ the rule ---

/** The workflow the rule names when it names none — GitHub's own convention. */
export const DEFAULT_CI_WORKFLOW = ".github/workflows/ci.yml";

/**
 * What the target repository's own config declares about how an epic
 * integrates, read the way the Go `ReadCloseoutRule` reads it: from the
 * repository, through a reader that recognises only what it knows, rather
 * than a rule hardcoded into this bundle.
 */
export type CloseoutRule = {
  declared: boolean;
  /** The CI workflow the rule names (or the default), carried into every message. */
  ciWorkflow: string;
  /** The rule's own line, verbatim — the repository's words, not ours. */
  stated: string;
};

const UNDECLARED: CloseoutRule = { declared: false, ciWorkflow: "", stated: "" };

/**
 * The phrase that declares the rule. The config's Rules section is human
 * prose, and a reader that fuzzy-matched it would fail open on every
 * paraphrase; so "PR + CI gate" — the rule's own stable vocabulary, present
 * verbatim in every declaration of it — is the anchor, and this comment is
 * the contract a repository buys into by using it.
 */
const RULE_ANCHOR = "pr + ci gate";

/** Pulls the workflow path the rule may name in parentheses. */
const WORKFLOW_PATTERN = /\.github\/workflows\/[A-Za-z0-9._/-]+\.ya?ml/;

/**
 * Recognises the rule in the config's own Rules section. Only a `## Rules`
 * section declares it — a prompt file or a standing order mentioning a PR
 * is not a rule a run enforces, and treating it as one would be the same
 * failure this port exists to remove, pointed the other way.
 */
export function parseCloseoutRule(document: string): CloseoutRule {
  let section = "";
  for (const line of document.split("\n")) {
    const trimmed = line.trim();
    if (trimmed.startsWith("#")) {
      // A heading — any depth — starts a new section; a deeper heading
      // inside Rules ends it.
      section = trimmed.replace(/^#+/, "").trim().toLowerCase();
      continue;
    }
    if (section !== "rules") continue;
    if (!trimmed.toLowerCase().includes(RULE_ANCHOR)) continue;
    const rule: CloseoutRule = {
      declared: true,
      ciWorkflow: DEFAULT_CI_WORKFLOW,
      stated: trimmed,
    };
    const found = WORKFLOW_PATTERN.exec(trimmed);
    if (found !== null) rule.ciWorkflow = found[0];
    return rule;
  }
  return UNDECLARED;
}

/**
 * Reads the PR + CI close-out rule from a repository's `.tick/config.md`, on
 * the same ref every other durable read of the run uses.
 *
 * A missing file declares nothing, and that is not an error: most target
 * repositories carry no config.md at all, and a run against one of them is
 * exactly as correct as it was before this rule existed. A file that IS
 * there but cannot be read is an error (the store throws), for the same
 * reason as the gate reader's: the run was pointed at it.
 */
export async function readCloseoutRule(repository: ContentsStore): Promise<CloseoutRule> {
  const file = await repository.read(".tick/config.md");
  if (file === null) return UNDECLARED;
  return parseCloseoutRule(file.content);
}

// ------------------------------------------------- CI for the tree (9da) ---

/**
 * Bounds how far back a CI verdict is looked for. A run-state commit per
 * phase is a handful; a hundred would mean something else is wrong and the
 * honest answer is that CI has not run.
 */
export const CI_WALK_LIMIT = 25;

/**
 * The only path a commit may touch and still be one the gate can see past.
 * NOT `.tick/` as well: tracker files include `runners.toml`, which the
 * gate's own drift guards read, so a `.tick/` change can legitimately change
 * what CI says.
 */
export const RUN_STATE_PREFIX = ".ticfac/";

/**
 * What `ciForTree` learned: the report, the sha it is about, and whether
 * that sha is the PR's own head — callers say so in what they record,
 * because "green on the head" and "green on the last commit that changed
 * code" are different sentences and a person is owed the true one.
 */
export type CIForTree = { report: CIReport; sha: string; isHead: boolean };

/**
 * Answers what CI says about the code this PR would merge.
 *
 * The PR is RE-READ first, because the head this run holds is stale the
 * moment it checkpoints — the run's durable state lives on the integration
 * branch the PR's head IS, so the act of gating rewrites the thing being
 * gated (9da; `closeout_ci.go` documents the live deadlock). CI is asked
 * about the head — the ordinary case — and when the head has no verdict,
 * the newest ancestor that has one is used, but only after proving that
 * everything between it and the head lives under `.ticfac/`: a
 * run-state-only commit cannot change what the build compiles or the tests
 * run, so an earlier verdict is still a true statement about this tree's
 * CODE — which is what the gate is actually about. A diff that cannot be
 * read answers "changed", deliberately: this is the proof that lets an
 * older verdict stand, and an unreadable proof is not a proof.
 *
 * A PENDING ancestor is walked past, not taken: pending is the absence of a
 * verdict, and taking it ends the walk with the very answer the walk exists
 * to get past (ticks tk3 and 5ob — a cancelled ancestor reads pending
 * permanently, while a green one sits one commit further on).
 */
export async function ciForTree(forge: PullRequests, pr: PullRequest): Promise<CIForTree> {
  let current = pr;
  try {
    // The re-read is best effort, the way the Go one is: a forge that cannot
    // answer it leaves the run asking about the PR it already holds.
    const fresh = await forge.find(pr.head_ref, pr.base_ref);
    if (fresh !== null && fresh.head_sha !== "") current = fresh;
  } catch {
    // fall through on the PR the run already holds
  }
  const head = current.head_sha;
  const report = await forge.ci(head); // errors propagate: the typed refusal names them
  if (report.state !== "none") return { report, sha: head, isHead: true };

  // Nothing on the head. Look back for a commit CI did run on, and take its
  // verdict only if this tree's code is that commit's code.
  const shas = await forge.ancestors(current.head_ref, CI_WALK_LIMIT).catch(() => []);
  for (let i = 1; i < shas.length; i += 1) {
    // i === 0 is the head, already asked.
    const sha = shas[i];
    const candidate = await forge.ci(sha);
    if (candidate.state === "none" || candidate.state === "pending") continue;
    if (!(await onlyRunState(forge, sha, head))) {
      // A conclusive verdict exists but does not describe this tree: the
      // honest answer is the head's own — none.
      return { report, sha: head, isHead: true };
    }
    return { report: candidate, sha, isHead: false };
  }
  return { report, sha: head, isHead: true };
}

/** Whether every path that changed between two commits lives under run state. */
async function onlyRunState(forge: PullRequests, from: string, to: string): Promise<boolean> {
  const paths = await forge.changedPaths(from, to).catch(() => null);
  if (paths === null) return false;
  return paths.every((path) => path === "" || path.startsWith(RUN_STATE_PREFIX));
}

/** How the feed and a refusal name what a CI verdict is about. */
export function ciSubject(sha: string, isHead: boolean, pr: PullRequest): string {
  if (isHead) return `the epic PR #${pr.number}'s head ${short(sha)}`;
  return (
    `${short(sha)}, the newest commit of the epic PR #${pr.number} that CI ran on ` +
    `(every commit since changes only ${RUN_STATE_PREFIX}, so the verdict is about this tree's code)`
  );
}

/** The short sha a message names a commit by — the shape the local feed uses. */
function short(sha: string): string {
  return sha.slice(0, 7);
}

// ------------------------------------------ the record the PR is a view of ---

/** One findings draft, the fields the PR body carries. */
export type DraftedFinding = {
  kind: string;
  title: string;
  body: string;
  severity: string;
  target: string;
  status: string;
};

/**
 * Every findings draft the run holds on the branch, at
 * `.ticfac/runs/<run-id>/findings/*.json` — the same records the Go
 * reconciler's close-out reads (tick 4sb). A draft that cannot be read is a
 * refusal rather than a skip: the body exists so a person MERGING reads
 * every finding, and a finding dropped from it is the failure the channel
 * exists to remove.
 */
export async function readFindings(
  repository: ContentsStore,
  runID: string,
): Promise<DraftedFinding[]> {
  const paths = await repository.list(`${RUN_STATE_PREFIX}runs/${runID}/findings`);
  const findings: DraftedFinding[] = [];
  for (const path of paths) {
    const file = await repository.read(path);
    if (file === null) continue;
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(file.content) as Record<string, unknown>;
    } catch (error) {
      throw new Error(`the findings draft ${path} is not readable JSON: ${String(error)}`);
    }
    const field = (name: string): string | null =>
      typeof parsed[name] === "string" ? (parsed[name] as string) : null;
    const kind = field("kind");
    const title = field("title");
    const body = field("body");
    const severity = field("severity");
    const target = field("target");
    if (kind === null || title === null || body === null || severity === null || target === null) {
      throw new Error(
        `the findings draft ${path} does not carry the finding's typed fields (kind, title, body, severity, target)`,
      );
    }
    const status = field("status");
    findings.push({ kind, title, body, severity, target, status: status ?? "proposed" });
  }
  return findings;
}

/**
 * The final review the run recorded, read as the body's verdict fields.
 *
 * The recorded response's shape is the role-result contract's (the
 * run-state schema pins the envelope, not the answer), so the reader
 * prefers the fields a typed verdict spells (`status`, `summary`) and falls
 * back to the collect vocabulary's (`outcome`, `detail`) — which is what
 * this host records today — rather than assuming either. `null` when the run
 * recorded no review at all.
 */
export function finalReviewOf(decisions: DecisionRecord[]): {
  decision: number;
  status: string;
  summary: string;
} | null {
  let final: DecisionRecord | null = null;
  for (const decision of decisions) {
    if (decision.role === "review-epic") final = decision;
  }
  if (final === null) return null;
  const as = (name: string): string | null =>
    typeof final!.response[name] === "string" ? (final!.response[name] as string) : null;
  return {
    decision: final.decision,
    status: as("status") ?? as("outcome") ?? "",
    summary: as("summary") ?? as("detail") ?? "",
  };
}

/**
 * Composes the body the epic PR carries (the `closeoutPRBody` port): the
 * rule the repository declares — kept as the opening, so the PR still says
 * why it exists — then the final review's verdict, then every finding the
 * run drafted, each with its own text and triage state.
 *
 * It reads only the durable record it is handed, so a resumed close-out
 * composes the same body from the same record: that is the whole
 * idempotence argument, and it is why the composition takes the record, not
 * a repository — there is nothing to read that the caller does not hold.
 */
export function composePRBody(input: {
  runID: string;
  branch: string;
  rule: CloseoutRule;
  review: { decision: number; status: string; summary: string } | null;
  findings: DraftedFinding[];
}): string {
  const { runID, branch, rule, review, findings } = input;
  const lines: string[] = [];
  lines.push(
    `ticfac run ${runID} opened this PR because the repository declares the PR + CI close-out ` +
      `rule in .tick/config.md: the epic close-out may not complete until CI (${rule.ciWorkflow}) is green on this PR.`,
  );
  lines.push("", "## The review's verdict", "");
  if (review === null) {
    // The body states the absence rather than staying silent about it: a
    // PR that says nothing about the review reads as "no review found
    // anything", which is a verdict nobody gave.
    lines.push(
      "No review decision is recorded for this run: the epic reached its close-out without a " +
        "review whose validated answer landed in the run's state.",
    );
  } else {
    lines.push(
      `The final review (decision ${review.decision}) answered ${review.status}: ${review.summary}`,
    );
  }
  lines.push("", "## Findings this run drafted", "");
  if (findings.length === 0) lines.push("This run drafted no findings.");
  findings.forEach((finding, index) => {
    const target = finding.target === "" ? "this repository" : finding.target;
    lines.push(
      `${index + 1}. ${finding.kind} — ${finding.title} (${finding.severity}, for ${target}), ` +
        `triaged ${finding.status}`,
    );
    if (finding.body !== "") {
      lines.push("");
      for (const bodyLine of finding.body.split("\n")) lines.push(`   ${bodyLine}`);
    }
  });
  lines.push(
    "",
    `The durable record is the run's own state under ${RUN_STATE_PREFIX}runs/${runID}/ on ${branch}; ` +
      "this body is the view of it, recomposed from the record and overwritten — never appended " +
      "to — so a resumed close-out carries these facts once, however many times it writes.",
  );
  return lines.join("\n");
}
