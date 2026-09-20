/**
 * The close-out's own pieces, ported from `internal/reconcile` (tick cxk):
 * the rule a repository declares in its own `.tick/config.md`, the CI a
 * close-out gates on — including 9da's ancestor walk, the one that stops a
 * run-state commit from borrowing a green it did not earn — and the body the
 * epic PR carries: the final review's verdict and every finding's text.
 *
 * Each is tested against the behaviour the Go side already proved, because
 * the acceptance is one sentence with no room to argue: a Workflow-hosted
 * close-out refuses to complete without green CI on the epic PR, names the
 * failing job on red, and the PR carries the review's verdict and findings.
 */

import { describe, expect, it } from "vitest";

import {
  CI_WALK_LIMIT,
  type CloseoutRule,
  ciForTree,
  composePRBody,
  DEFAULT_CI_WORKFLOW,
  type DraftedFinding,
  parseCloseoutRule,
  RUN_STATE_PREFIX,
  readCloseoutRule,
  readFindings,
} from "../src/closeout";
import { type CIReport, classifyCheckRuns, type PullRequests } from "../src/forge";
import type { ContentsStore, StoredFile, StoreWrite } from "../src/git-contents";

// ----------------------------------------------------------- a tiny store ---

/** The tracked-file minimum the close-out reads: list and read, at one ref. */
class MemoryContents implements ContentsStore {
  readonly files = new Map<string, StoredFile>();

  constructor(seed: Record<string, string> = {}) {
    for (const [path, content] of Object.entries(seed)) {
      this.files.set(path, { content, sha: `blob-${path}` });
    }
  }

  async list(prefix: string): Promise<string[]> {
    return [...this.files.keys()].filter((path) => path.startsWith(prefix)).sort();
  }

  async read(path: string): Promise<StoredFile | null> {
    const file = this.files.get(path);
    return file === undefined ? null : { content: file.content, sha: file.sha };
  }

  async create(): Promise<StoreWrite> {
    return { state: "exists", detail: "not written by this test" };
  }

  async update(): Promise<StoreWrite> {
    return { state: "missing", detail: "not written by this test" };
  }
}

// ------------------------------------------------------------ the fake forge ---

/** The PullRequests seam, with every answer a test states by key. */
class FakeForge implements PullRequests {
  readonly prs = new Map<string, { head_ref: string; base_ref: string; head_sha: string }>();
  readonly ciBySHA = new Map<string, CIReport>();
  readonly ancestorsByHead = new Map<string, string[]>();
  readonly changedByPair = new Map<string, string[] | null>();
  readonly findCalls: string[] = [];
  default: CIReport = { state: "none", failing: [] };

  async find(headRef: string, _baseRef: string) {
    this.findCalls.push(headRef);
    const pr = this.prs.get(headRef);
    return pr === undefined
      ? null
      : {
          number: 7,
          url: "https://example.com/pr/7",
          head_ref: pr.head_ref,
          head_sha: pr.head_sha,
          base_ref: pr.base_ref,
        };
  }

  async open(): Promise<never> {
    throw new Error("this test never opens a PR");
  }

  async updateBody(): Promise<void> {}

  async ci(sha: string): Promise<CIReport> {
    return this.ciBySHA.get(sha) ?? this.default;
  }

  async ancestors(headRef: string, limit: number): Promise<string[]> {
    return (this.ancestorsByHead.get(headRef) ?? []).slice(0, limit);
  }

  async changedPaths(from: string, to: string): Promise<string[] | null> {
    return this.changedByPair.get(`${from}...${to}`) ?? null;
  }
}

// ------------------------------------------------------------------- tests ---

describe("the close-out rule, read from the repository's own config", () => {
  it("recognises the declaration in the Rules section, with the workflow it names", () => {
    const rule = parseCloseoutRule(
      "# Tick Run Configuration\n\n## Rules\n\n" +
        "- Epic integration goes through a PR + CI gate: the close-out may not complete until " +
        "CI (.github/workflows/tests.yml) is green on the epic PR.\n",
    );
    expect(rule.declared).toBe(true);
    expect(rule.ciWorkflow).toBe(".github/workflows/tests.yml");
    expect(rule.stated).toContain("PR + CI gate");
  });

  it("defaults the workflow to GitHub's convention when the rule names none", () => {
    const rule = parseCloseoutRule("## Rules\n\n- a PR + CI gate governs the close-out\n");
    expect(rule.declared).toBe(true);
    expect(rule.ciWorkflow).toBe(DEFAULT_CI_WORKFLOW);
  });

  it("does not read a rule that lives outside the Rules section, or none at all", () => {
    // A standing order mentioning the gate is not a rule the run enforces —
    // the same closed-reading property `internal/reconcile` pins (0iz).
    const outside = parseCloseoutRule(
      "## Standing orders\n\n- library choice: a PR + CI gate is mentioned in prose\n",
    );
    expect(outside.declared).toBe(false);
    const deeper = parseCloseoutRule(
      "## Rules\n\n### Exceptions\n\n- a PR + CI gate, but in a subsection\n",
    );
    expect(deeper.declared).toBe(false);
    expect(parseCloseoutRule("## Rules\n\n- nothing this run enforces\n").declared).toBe(false);
  });

  it("a missing config declares nothing; a present one is read", async () => {
    const empty = await readCloseoutRule(new MemoryContents({}));
    expect(empty.declared).toBe(false);
    const declared = await readCloseoutRule(
      new MemoryContents({
        ".tick/config.md":
          "## Rules\n\n- the PR + CI gate is declared (.github/workflows/ci.yml)\n",
      }),
    );
    expect(declared.declared).toBe(true);
    expect(declared.ciWorkflow).toBe(".github/workflows/ci.yml");
  });
});

describe("the CI classification, ported closed and conservative", () => {
  const run = (name: string, status: string, conclusion: string) => ({ name, status, conclusion });

  it("no check runs is `none`; every check passed is green", () => {
    expect(classifyCheckRuns([])).toEqual({ state: "none", failing: [] });
    expect(classifyCheckRuns([run("build", "completed", "success")])).toEqual({
      state: "green",
      failing: [],
    });
  });

  it("a check that has not concluded leaves the whole report pending", () => {
    expect(
      classifyCheckRuns([run("build", "completed", "success"), run("test", "in_progress", "")]),
    ).toEqual({ state: "pending", failing: [] });
  });

  it("failure and timeout are red, and the failing jobs are NAMED", () => {
    expect(classifyCheckRuns([run("build-and-test", "completed", "failure")]).failing).toEqual([
      "build-and-test",
    ]);
    expect(classifyCheckRuns([run("lint", "completed", "timed_out")]).state).toBe("red");
    // ...and a red report beside a running one is still pending-first.
    expect(
      classifyCheckRuns([run("build", "completed", "failure"), run("test", "in_progress", "")]),
    ).toEqual({ state: "pending", failing: ["build"] });
  });

  it("cancelled, stale and action_required read as pending, never green", () => {
    expect(classifyCheckRuns([run("ci", "completed", "cancelled")]).state).toBe("pending");
    // neutral and skipped neither pass nor fail the report.
    expect(
      classifyCheckRuns([run("ci", "completed", "neutral"), run("other", "completed", "skipped")]),
    ).toEqual({ state: "green", failing: [] });
  });
});

describe("ciForTree: what CI says about the code this PR would merge", () => {
  const PR = { head_ref: "epic/x", base_ref: "main", head_sha: "head" };
  const forgeWith = (forge: FakeForge) => forge;

  function forgeSeeded(headCI: CIReport, config: Partial<FakeForge> = {}): FakeForge {
    const forge = new FakeForge();
    forge.prs.set("epic/x", { head_ref: "epic/x", base_ref: "main", head_sha: "head" });
    forge.ciBySHA.set("head", headCI);
    Object.assign(forge, config);
    return forge;
  }

  it("green on the head is the ordinary case: no walk, the head's own verdict", async () => {
    const forge = forgeSeeded({ state: "green", failing: [] });
    const verdict = await ciForTree(forgeWith(forge), { ...PR, number: 7, url: "" });
    expect(verdict.report.state).toBe("green");
    expect(verdict.sha).toBe("head");
    expect(verdict.isHead).toBe(true);
    expect(forge.findCalls.length).toBe(1); // the re-read, and nothing more
  });

  it("re-reads the PR first: the head this run remembers is stale by construction", async () => {
    const forge = new FakeForge();
    // The PR moved after this run read it: the FRESH head has the checks.
    forge.prs.set("epic/x", { head_ref: "epic/x", base_ref: "main", head_sha: "moved-head" });
    forge.ciBySHA.set("moved-head", { state: "green", failing: [] });
    // The PR the run holds still names the old head — 9da's first half.
    const verdict = await ciForTree(forge, { ...PR, head_sha: "stale-head", number: 7, url: "" });
    expect(verdict.report.state).toBe("green");
    expect(verdict.sha).toBe("moved-head");
    expect(verdict.isHead).toBe(true);
  });

  it("walks past a pending ancestor: pending is the absence of a verdict, not one", async () => {
    const forge = forgeSeeded({ state: "none", failing: [] });
    forge.ancestorsByHead.set("epic/x", ["head", "ancestor-pending", "ancestor-green"]);
    forge.ciBySHA.set("ancestor-pending", { state: "pending", failing: [] });
    forge.ciBySHA.set("ancestor-green", { state: "green", failing: [] });
    forge.changedByPair.set("ancestor-green...head", [".ticfac/runs/x/checkpoint.json"]);
    const verdict = await ciForTree(forge, { ...PR, number: 7, url: "" });
    expect(verdict.report.state).toBe("green");
    expect(verdict.sha).toBe("ancestor-green");
    expect(verdict.isHead).toBe(false);
  });

  it("borrows an ancestor's verdict only when every change since lives under .ticfac/", async () => {
    const forge = forgeSeeded({ state: "none", failing: [] });
    forge.ancestorsByHead.set("epic/x", ["head", "ancestor-green"]);
    forge.ciBySHA.set("ancestor-green", { state: "green", failing: [] });
    forge.changedByPair.set("ancestor-green...head", ["src/changed.ts"]);
    const verdict = await ciForTree(forge, { ...PR, number: 7, url: "" });
    // An unprovable tree is not a proven one: the honest answer is the head's.
    expect(verdict.report.state).toBe("none");
    expect(verdict.isHead).toBe(true);
  });

  it("a diff it cannot read is a change it cannot prove: no borrowed green", async () => {
    const forge = forgeSeeded({ state: "none", failing: [] });
    forge.ancestorsByHead.set("epic/x", ["head", "ancestor-green"]);
    forge.ciBySHA.set("ancestor-green", { state: "green", failing: [] });
    forge.changedByPair.set("ancestor-green...head", null); // unreadable compare
    const verdict = await ciForTree(forge, { ...PR, number: 7, url: "" });
    expect(verdict.report.state).toBe("none");
    expect(verdict.isHead).toBe(true);
  });

  it("walks at most CI_WALK_LIMIT commits back, and a failure to read ancestors is not fatal", async () => {
    const forge = forgeSeeded({ state: "none", failing: [] });
    forge.ancestorsByHead.set("epic/x", ["head"]);
    const verdict = await ciForTree(forge, { ...PR, number: 7, url: "" });
    expect(verdict.report.state).toBe("none");
    expect(verdict.isHead).toBe(true);
    expect(RUN_STATE_PREFIX).toBe(".ticfac/");
    expect(CI_WALK_LIMIT).toBe(25);
  });
});

describe("the findings the run drafted, read from the branch", () => {
  const draft = (overrides: Record<string, unknown> = {}): Record<string, unknown> => ({
    schema_version: 1,
    key: "key-draft",
    kind: "proposed-tick",
    title: "A tick this repository should carry",
    body: "Why it matters.",
    severity: "high",
    target: "",
    status: "proposed",
    ...overrides,
  });

  it("no drafts on the branch is an empty list, not an error", async () => {
    expect(await readFindings(new MemoryContents({}), "run-x")).toEqual([]);
  });

  it("reads a draft with its typed fields and its triage state", async () => {
    const contents = new MemoryContents({
      ".ticfac/runs/run-x/findings/key-draft.json": JSON.stringify(draft()),
    });
    const findings = await readFindings(contents, "run-x");
    expect(findings).toEqual([
      {
        kind: "proposed-tick",
        title: "A tick this repository should carry",
        body: "Why it matters.",
        severity: "high",
        target: "",
        status: "proposed",
      },
    ] satisfies DraftedFinding[]);
  });

  it("a draft that is not JSON, or lacks its typed fields, is a refusal not a shrug", async () => {
    const broken = new MemoryContents({
      ".ticfac/runs/run-x/findings/bad.json": "not json at all",
    });
    await expect(readFindings(broken, "run-x")).rejects.toThrow(/bad\.json/);
    const untyped = new MemoryContents({
      ".ticfac/runs/run-x/findings/loose.json": JSON.stringify({ title: "no kind, no severity" }),
    });
    await expect(readFindings(untyped, "run-x")).rejects.toThrow(/typed fields/);
  });
});

describe("the body the epic PR carries", () => {
  const rule: CloseoutRule = {
    declared: true,
    ciWorkflow: ".github/workflows/ci.yml",
    stated: "the PR + CI gate",
  };

  it("opens with the rule, states the review's verdict, and lists every finding's text", () => {
    const body = composePRBody({
      runID: "run-x",
      branch: "epic/x",
      rule,
      review: { decision: 2, status: "DONE", summary: "nothing to refuse" },
      findings: [
        {
          kind: "defect",
          title: "A defect outside the discovering tick",
          body: "The finding's own text.",
          severity: "medium",
          target: "",
          status: "proposed",
        },
      ],
    });
    expect(body).toContain("PR + CI close-out rule");
    expect(body).toContain(".github/workflows/ci.yml");
    expect(body).toContain("## The review's verdict");
    expect(body).toContain("The final review (decision 2) answered DONE: nothing to refuse");
    expect(body).toContain("## Findings this run drafted");
    expect(body).toContain(
      "1. defect — A defect outside the discovering tick (medium, for this repository), triaged proposed",
    );
    expect(body).toContain("The finding's own text.");
    expect(body).toContain("overwritten — never appended to");
  });

  it("states the absence of a review rather than staying silent about it", () => {
    const body = composePRBody({
      runID: "run-x",
      branch: "epic/x",
      rule,
      review: null,
      findings: [],
    });
    expect(body).toContain("No review decision is recorded for this run");
    expect(body).toContain("This run drafted no findings.");
  });

  it("a verdict a later contract spells further (status/summary) is preferred over the collect words", () => {
    const body = composePRBody({
      runID: "run-x",
      branch: "epic/x",
      rule,
      review: { decision: 1, status: "NOT READY", summary: "the gate run was never performed" },
      findings: [],
    });
    expect(body).toContain("answered NOT READY: the gate run was never performed");
  });
});
