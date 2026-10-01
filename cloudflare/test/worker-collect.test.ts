import { env } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import WORKER_SH from "../../image/worker.sh?raw";
import EXITCLASS_GO from "../../internal/exec/cloudflaresandbox/exitclass.go?raw";
import SANDBOXIMAGE_GO from "../../internal/sandboximage/sandboximage.go?raw";
import {
  WORKER_EXIT_CLASSES,
  workerBootStoppedBranch,
  workerBootStoppedFile,
  workerExitClass,
} from "../src/worker-boot";
import {
  BOUNDARY_REPORT_MARKER,
  collectFromGithub,
  githubWorkerCollector,
  needsHuman,
  parseBootStopped,
  parseStatus,
  resultFile,
  STATUS_BLOCKED,
  STATUS_DONE,
  STATUS_DONE_WITH_CONCERNS,
  STATUS_NEEDS_CONTEXT,
} from "../src/worker-collect";

/**
 * Per-tick worker collect (tick 0ds): the durable-layer-only verdict, ported
 * from `internal/herd/collect/collect.go` onto GitHub's compare and
 * contents APIs — a worker sandbox pushes and exits, so there is no local
 * worktree left to read the way `tk herd collect` reads one.
 */

const saved: Record<string, unknown> = {};
function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  (env as unknown as Record<string, unknown>)[name] = value;
}
afterEach(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
    delete saved[name];
  }
});

const API = "https://github.example.test";
const PROJECT = "acme/project";
const BASE = "a".repeat(40);
const BRANCH = "tick/1vn/0ds";

// -------------------------------------------------------------- the stub ---

type StubRoute = {
  compare?: { status: number; body: unknown };
  contents?: { status: number; body: unknown };
  /**
   * Per-file answers keyed `<path>@<ref>`, consulted before `contents`: the
   * boot-stopped marker sits on a branch of its own, and the report on the
   * worker branch must not answer for it.
   */
  files?: Record<string, { status: number; body: unknown }>;
};

/** Stands in for GitHub's compare and contents endpoints. */
function stubGithub(route: StubRoute): { calls: string[]; restore: () => void } {
  const calls: string[] = [];
  const original = globalThis.fetch;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    if (!url.startsWith(API)) return original(input as RequestInfo, init);
    calls.push(url);
    if (url.includes("/compare/") && route.compare !== undefined) {
      return Response.json(route.compare.body, { status: route.compare.status });
    }
    if (url.includes("/contents/") && route.files !== undefined) {
      const parsed = new URL(url);
      const path = decodeURIComponent(parsed.pathname.split("/contents/")[1] ?? "");
      const answer = route.files[`${path}@${parsed.searchParams.get("ref") ?? ""}`];
      if (answer !== undefined) return Response.json(answer.body, { status: answer.status });
      if (path.startsWith("BOOT-STOPPED-")) {
        return Response.json({ message: "Not Found" }, { status: 404 });
      }
    }
    if (url.includes("/contents/") && route.contents !== undefined) {
      return Response.json(route.contents.body, { status: route.contents.status });
    }
    return new Response("unexpected request in test", { status: 500 });
  }) as typeof fetch;
  return { calls, restore: () => void (globalThis.fetch = original) };
}

/** UTF-8-safe base64, matching how GitHub actually encodes file contents. */
function b64(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function compareOK(ahead_by: number, files: string[] = []): { status: number; body: unknown } {
  return {
    status: 200,
    body: { ahead_by, files: files.map((filename) => ({ filename })) },
  };
}

const RESULT_PATH = resultFile("0ds");

beforeEach(() => {
  set("GITHUB_API_BASE_URL", API);
  set("GITHUB_TOKEN", "ghp_repo_scoped");
});

// ---------------------------------------------------------- status parse ---

describe("parseStatus", () => {
  it("finds a plain status line", () => {
    const { status, detail } = parseStatus("Some notes\n\nSTATUS: DONE\n");
    expect(status).toBe(STATUS_DONE);
    expect(detail).toBe("");
  });

  it("never truncates DONE_WITH_CONCERNS to DONE", () => {
    const { status, detail } = parseStatus("STATUS: DONE_WITH_CONCERNS — double-check the seam");
    expect(status).toBe(STATUS_DONE_WITH_CONCERNS);
    expect(detail).toBe("double-check the seam");
  });

  it("strips list/quote/heading decoration around the line", () => {
    const { status } = parseStatus("- **STATUS: BLOCKED**");
    expect(status).toBe(STATUS_BLOCKED);
  });

  it("keeps only the FINAL status line when a report quotes the template's options", () => {
    const body = [
      "A final line, exactly one of:",
      "STATUS: DONE",
      "STATUS: DONE_WITH_CONCERNS — ...",
      "STATUS: NEEDS_CONTEXT — ...",
      "STATUS: BLOCKED — ...",
      "",
      "STATUS: NEEDS_CONTEXT — missing the base sha",
    ].join("\n");
    const { status, detail } = parseStatus(body);
    expect(status).toBe(STATUS_NEEDS_CONTEXT);
    expect(detail).toBe("missing the base sha");
  });

  it("reports nothing for a report with no recognisable status line", () => {
    expect(parseStatus("Implemented the thing.\n")).toEqual({ status: "", detail: "", line: "" });
  });
});

describe("needsHuman", () => {
  it("flags BLOCKED and NEEDS_CONTEXT, not DONE or DONE_WITH_CONCERNS", () => {
    const base = {
      tick_id: "0ds",
      branch: BRANCH,
      base_sha: BASE,
      verdict: "ready-to-merge" as const,
      branch_exists: true,
      commits: 1,
      result_path: RESULT_PATH,
      result_exists: true,
      status_detail: "",
      status_line: "",
      boundary_files: [],
      report_only: false,
      detail: "",
    };
    expect(needsHuman({ ...base, status: STATUS_BLOCKED })).toBe(true);
    expect(needsHuman({ ...base, status: STATUS_NEEDS_CONTEXT })).toBe(true);
    expect(needsHuman({ ...base, status: STATUS_DONE })).toBe(false);
    expect(needsHuman({ ...base, status: STATUS_DONE_WITH_CONCERNS })).toBe(false);
  });
});

// --------------------------------------------------------------- collect ---

describe("collectFromGithub", () => {
  it("is ready-to-merge: commits beyond base, a DONE report, no boundary files", async () => {
    const github = stubGithub({
      compare: compareOK(3),
      contents: {
        status: 200,
        body: { content: b64("work\n\nSTATUS: DONE\n"), encoding: "base64" },
      },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("ready-to-merge");
      expect(report.branch_exists).toBe(true);
      expect(report.commits).toBe(3);
      expect(report.status).toBe(STATUS_DONE);
      expect(report.boundary_files).toEqual([]);
      expect(github.calls[0]).toContain(
        `/repos/${PROJECT}/compare/${BASE}...${encodeURIComponent(BRANCH)}`,
      );
      expect(github.calls[1]).toContain(`/repos/${PROJECT}/contents/${RESULT_PATH}?ref=`);
    } finally {
      github.restore();
    }
  });

  // Tick 94u, absorbing the finding the Go executor's own collect already
  // refused (dyo, 73ba193d): the container's entrypoint commits the report
  // itself, in its own commit (image/worker.sh), so a worker that did
  // NOTHING still leaves one commit beyond the base — and a collect that
  // counts commits alone reads it as ready-to-merge. The refusal is the
  // same verdict the Go side uses, no-commits, with its own sentence,
  // because "the branch is empty" is a lie about this one.
  it("is no-commits, never ready-to-merge, when the report is the branch's only change", async () => {
    const github = stubGithub({
      compare: compareOK(1, [RESULT_PATH]),
      contents: {
        status: 200,
        body: { content: b64("STATUS: DONE"), encoding: "base64" },
      },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("no-commits");
      expect(report.report_only).toBe(true);
      // The commit is a stated fact, not a denial: the refusal is about the
      // WORK, and one commit is exactly what this shape leaves.
      expect(report.commits).toBe(1);
      // The report is still read: the refusal says what the worker CLAIMED.
      expect(report.status).toBe(STATUS_DONE);
      expect(report.detail).toContain(RESULT_PATH);
      expect(report.detail).toContain("not a deliverable");
    } finally {
      github.restore();
    }
  });

  // The refusal must not swallow real work: the entrypoint's report commit
  // rides beside the worker's own commits on every honest branch, and the
  // check reads the whole diff, not the commit count.
  it("stays ready-to-merge when the report rides beside real work", async () => {
    const github = stubGithub({
      compare: compareOK(2, ["src/thing.ts", RESULT_PATH]),
      contents: {
        status: 200,
        body: { content: b64("work\n\nSTATUS: DONE\n"), encoding: "base64" },
      },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("ready-to-merge");
      expect(report.report_only).toBe(false);
    } finally {
      github.restore();
    }
  });

  // Ordering, pinned the way the Go side's classify pins it: a report that
  // carries no STATUS line is missing-result even on a report-only branch,
  // because an answer nobody can read is the more urgent fact.
  it("is missing-result, not the report-only refusal, when the report carries no STATUS: line", async () => {
    const github = stubGithub({
      compare: compareOK(1, [RESULT_PATH]),
      contents: {
        status: 200,
        body: { content: b64("Implemented the thing.\n"), encoding: "base64" },
      },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("missing-result");
    } finally {
      github.restore();
    }
  });

  it("is no-commits when the branch does not exist on origin", async () => {
    const github = stubGithub({ compare: { status: 404, body: { message: "Not Found" } } });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("no-commits");
      expect(report.branch_exists).toBe(false);
      expect(report.detail).toContain(BRANCH);
    } finally {
      github.restore();
    }
  });

  it("is no-commits — never done-with-no-changes — when the branch exists but has nothing beyond base", async () => {
    const github = stubGithub({
      compare: compareOK(0),
      contents: { status: 404, body: { message: "Not Found" } },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("no-commits");
      expect(report.branch_exists).toBe(true);
      expect(report.commits).toBe(0);
    } finally {
      github.restore();
    }
  });

  it("still reads the result file when there are no commits, so a BLOCKED report without a push is visible", async () => {
    const github = stubGithub({
      compare: compareOK(0),
      contents: {
        status: 200,
        body: { content: b64("STATUS: BLOCKED — no gateway credential"), encoding: "base64" },
      },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("no-commits");
      expect(report.status).toBe(STATUS_BLOCKED);
      expect(needsHuman(report)).toBe(true);
    } finally {
      github.restore();
    }
  });

  it("is missing-result when the report file is absent from the branch", async () => {
    const github = stubGithub({
      compare: compareOK(2),
      contents: { status: 404, body: { message: "Not Found" } },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("missing-result");
      expect(report.result_exists).toBe(false);
    } finally {
      github.restore();
    }
  });

  it("is missing-result when the report exists but carries no recognisable STATUS: line", async () => {
    const github = stubGithub({
      compare: compareOK(2),
      contents: {
        status: 200,
        body: { content: b64("Implemented the thing.\n"), encoding: "base64" },
      },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("missing-result");
      expect(report.result_exists).toBe(true);
      expect(report.status).toBe("");
    } finally {
      github.restore();
    }
  });

  it("is boundary-violation when the branch touches .tick/, whatever the report says", async () => {
    const github = stubGithub({
      compare: compareOK(1, ["src/thing.go", ".tick/issues/0ds.json"]),
      contents: { status: 200, body: { content: b64("STATUS: DONE"), encoding: "base64" } },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("boundary-violation");
      expect(report.boundary_files).toEqual([".tick/issues/0ds.json"]);
    } finally {
      github.restore();
    }
  });

  // Tick dxk. The container now REFUSES the violation instead of asking the
  // agent not to commit it, which inverts what this collector sees: the branch
  // comes back clean and ready-to-merge for exactly the runs where a model
  // ignored an explicit instruction. Without this, the guard's whole point —
  // that the attempt reaches a human — would end at the report file nobody
  // greps.
  it("surfaces a prevented boundary violation the report declares, on a branch that is otherwise clean", async () => {
    const body = [
      "_ticks-worker: branch `tick/1vn/0ds`, base `aaaa`, harness `omp` exited 0._",
      "",
      `> **${BOUNDARY_REPORT_MARKER}.** This agent tried to write tracker state.`,
      "> - the agent ran `tk close 0ds`",
      "",
      "STATUS: DONE",
    ].join("\n");
    const github = stubGithub({
      compare: compareOK(2, ["src/thing.go"]),
      contents: { status: 200, body: { content: b64(body), encoding: "base64" } },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      // The guard worked, so the branch is mergeable and the verdict says so.
      expect(report.verdict).toBe("ready-to-merge");
      expect(report.boundary_files).toEqual([]);
      // And the attempt is still visible.
      expect(report.boundary_attempted).toBe(true);
    } finally {
      github.restore();
    }
  });

  it("does not claim a boundary attempt on a report that never mentions one", async () => {
    const github = stubGithub({
      compare: compareOK(2),
      contents: {
        status: 200,
        body: { content: b64("Implemented it.\n\nSTATUS: DONE"), encoding: "base64" },
      },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.boundary_attempted).toBe(false);
    } finally {
      github.restore();
    }
  });

  it("reports unknown, not a failing verdict, when the compare API cannot be read", async () => {
    const github = stubGithub({
      compare: { status: 503, body: { message: "service unavailable" } },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("unknown");
      expect(report.detail).toContain("503");
    } finally {
      github.restore();
    }
  });

  it("reports unknown, not missing-result, when the contents API cannot be read", async () => {
    const github = stubGithub({
      compare: compareOK(1),
      contents: { status: 500, body: { message: "internal error" } },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
      });
      expect(report.verdict).toBe("unknown");
      expect(report.detail).toContain("500");
    } finally {
      github.restore();
    }
  });
});

describe("githubWorkerCollector", () => {
  it("reads the same verdict collectFromGithub would, through the WorkerCollector seam", async () => {
    const github = stubGithub({
      compare: compareOK(1),
      contents: { status: 200, body: { content: b64("STATUS: DONE"), encoding: "base64" } },
    });
    try {
      const collector = githubWorkerCollector(env, PROJECT);
      const report = await collector.collect({ tick_id: "0ds", branch: BRANCH, base_sha: BASE });
      expect(report.verdict).toBe("ready-to-merge");
    } finally {
      github.restore();
    }
  });
});

// ------------------------------------------------- the boot-stopped marker ---

/**
 * A worker that stopped in its boot, before its harness, pushes its exit code
 * and the boot's own reason BESIDE its worker branch (image/worker.sh
 * boot_stopped, #176), never on it. The Go collect reads it (internal/exec/
 * cloudflaresandbox/boot_stopped.go); this one must say the same thing about
 * the same branch, or a cloud-side reader is left with "has no commits beyond
 * the base" for a container that told origin exactly why it stopped (hn6
 * run_ee8e: 378's resolve job exited 7 at its gateway probe).
 */
describe("the boot-stopped marker", () => {
  const LANDING = "tick/1vn/attempt-2/0ds";
  const RUN = "run_8ty_boot";
  const marker = (exit: number, reason: string) => ({
    status: 200,
    body: {
      content: b64(
        `# 0ds: the boot stopped before the harness started\n\nexit: ${exit}\nreason: ${reason}\n\n` +
          `run: ${RUN}\nworker branch: ${LANDING}\n`,
      ),
      encoding: "base64",
    },
  });

  it("names the exit code, its class and the reason when the worker branch is empty", async () => {
    const github = stubGithub({
      compare: compareOK(0),
      contents: { status: 404, body: { message: "Not Found" } },
      files: {
        [`${workerBootStoppedFile("0ds")}@${workerBootStoppedBranch(LANDING)}`]: marker(
          7,
          "the gateway did not answer a one-token request within 30s.",
        ),
      },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
        landing_branch: LANDING,
        run_id: RUN,
      });
      // The verdict is the branch's; the marker only says why.
      expect(report.verdict).toBe("no-commits");
      expect(report.boot_stopped).toEqual({
        exit_code: 7,
        reason: "the gateway did not answer a one-token request within 30s.",
      });
      expect(report.detail).toBe(
        "the container's boot stopped before its harness started (exit 7: the container could not call its " +
          "model (the gateway probe failed)): the gateway did not answer a one-token request within 30s.. " +
          `Nothing reached ${LANDING}; the reason is on ${LANDING}-boot-stopped`,
      );
    } finally {
      github.restore();
    }
  });

  it("finds the marker beside the per-run landing branch too, as the Go collect does", async () => {
    const fallback = `${LANDING}-${RUN}`;
    const github = stubGithub({
      compare: { status: 404, body: { message: "Not Found" } },
      files: {
        [`${workerBootStoppedFile("0ds")}@${workerBootStoppedBranch(fallback)}`]: marker(
          15,
          "origin did not answer the fetch",
        ),
      },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
        landing_branch: LANDING,
        run_id: RUN,
      });
      expect(report.verdict).toBe("no-commits");
      expect(report.boot_stopped?.exit_code).toBe(15);
      expect(report.detail).toContain("(exit 15: origin did not answer the fetch through");
      expect(report.detail).toContain("): origin did not answer the fetch.");
    } finally {
      github.restore();
    }
  });

  it("says what it always said when there is no marker", async () => {
    const github = stubGithub({
      compare: compareOK(0),
      contents: { status: 404, body: { message: "Not Found" } },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
        landing_branch: LANDING,
        run_id: RUN,
      });
      expect(report.verdict).toBe("no-commits");
      expect(report.boot_stopped).toBeUndefined();
      expect(report.detail).toBe(`${BRANCH} has no commits beyond ${BASE.slice(0, 8)}`);
    } finally {
      github.restore();
    }
  });

  it("is not read for a worker that reached its harness", async () => {
    const github = stubGithub({
      compare: compareOK(1, ["work.go", RESULT_PATH]),
      contents: { status: 200, body: { content: b64("STATUS: DONE"), encoding: "base64" } },
      files: {
        [`${workerBootStoppedFile("0ds")}@${workerBootStoppedBranch(LANDING)}`]: marker(7, "stale"),
      },
    });
    try {
      const report = await collectFromGithub(env, PROJECT, {
        tick_id: "0ds",
        branch: BRANCH,
        base_sha: BASE,
        landing_branch: LANDING,
        run_id: RUN,
      });
      expect(report.verdict).toBe("ready-to-merge");
      expect(report.boot_stopped).toBeUndefined();
      expect(github.calls.some((url) => url.includes("BOOT-STOPPED-"))).toBe(false);
    } finally {
      github.restore();
    }
  });

  it("refuses a marker without both lines, like the Go parseBootStopped", () => {
    expect(parseBootStopped("exit: 7\nreason: the probe failed\n")).toEqual({
      exit_code: 7,
      reason: "the probe failed",
    });
    expect(parseBootStopped("exit: 7\n")).toBeNull();
    expect(parseBootStopped("reason: x\n")).toBeNull();
    expect(parseBootStopped("exit: seven\nreason: x\n")).toBeNull();
  });
});

/**
 * The spellings and the exit classes have three homes — the image that writes
 * the marker, the Go collect and this one — and nothing compiles across them.
 * The image's own text and the Go sources are read here, so a rename on any
 * side fails this suite rather than silently leaving one collect blind.
 */
describe("the boot-stopped marker agrees with the image and the Go collect", () => {
  it("is spelled as image/worker.sh pushes it and sandboximage names it", () => {
    // The shell's own spelling, `${…}` and all, assembled so it reads as
    // the literal text of image/worker.sh rather than a template.
    const dollar = "$";
    expect(WORKER_SH).toContain(
      `local marker_branch="${dollar}{worker_branch}-boot-stopped" file="BOOT-STOPPED-${dollar}{tick_id}.md"`,
    );
    expect(workerBootStoppedBranch("tick/e/attempt-1/t")).toBe("tick/e/attempt-1/t-boot-stopped");
    expect(workerBootStoppedFile("t")).toBe("BOOT-STOPPED-t.md");
    expect(SANDBOXIMAGE_GO).toContain('return workerBranch + "-boot-stopped"');
    expect(SANDBOXIMAGE_GO).toContain('return "BOOT-STOPPED-" + tick + ".md"');
  });

  it("names every exit code with the Go collect's own sentence", () => {
    const values = new Map<string, number>();
    for (const m of SANDBOXIMAGE_GO.matchAll(/^\s*(Exit[A-Za-z]+)\s*=\s*(\d+)/gm)) {
      values.set(m[1], Number(m[2]));
    }
    const classes = new Map<number, string>();
    for (const m of EXITCLASS_GO.matchAll(
      /case (?:sandboximage\.(Exit[A-Za-z]+)|(\d+)):\s*\n\s*return "([^"]+)"/g,
    )) {
      const code = m[1] !== undefined ? values.get(m[1]) : Number(m[2]);
      expect(code, m[0]).toBeDefined();
      classes.set(code as number, m[3]);
    }
    expect(classes.size).toBeGreaterThan(10);
    for (const [code, sentence] of classes) {
      expect(workerExitClass(code), `exit ${code}`).toBe(sentence);
    }
    expect(Object.keys(WORKER_EXIT_CLASSES).length).toBe(classes.size);
    expect(workerExitClass(99)).toBe("");
  });
});
