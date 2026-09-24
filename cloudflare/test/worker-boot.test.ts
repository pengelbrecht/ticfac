import { describe, expect, it } from "vitest";
import contract from "../../contracts/worker-boot-contract.json";
import {
  attemptLandingBranch,
  ORCHESTRATOR_COMMAND,
  WORKER_ACTOR,
  WORKER_BRANCH_PREFIX,
  WORKER_CANCEL_ARG,
  WORKER_CANCEL_COMMAND,
  WORKER_CANCEL_MARKER,
  WORKER_CANCEL_REPORT_MARKER,
  WORKER_COMMAND,
  WORKER_DEFAULT_HARNESS,
  WORKER_DEFAULT_MODEL,
  WORKER_EXIT,
  WORKER_PROBE_ARG,
  WORKER_PROBE_COMMAND,
  WORKER_PROBE_MARKER,
  WORKER_ROLE_PROMPT_ENV,
  workerBootEnv,
  workerBranch,
  workerCancelCommand,
  workerHarness,
  workerHarnessTimeoutSeconds,
  workerModel,
  workerProbeSpec,
  workerResultFile,
  workerWorkSpec,
} from "../src/worker-boot";
import { BOUNDARY_REPORT_MARKER } from "../src/worker-collect";
import { evaluateProbeOutput } from "../src/worker-dispatch";

const boot = {
  repo_url: "https://github.com/example/repo.git",
  base_sha: "930f1cf4dbcac5505cce506cbf2a8412d8248b92",
  epic: "1vn",
  tick: "tap",
  run_id: "run_abc",
  gateway_base_url: "https://factory.example.com/api/gateway",
  gateway_token: "tkr_0123456789abcdef",
};

// The whole reason this file and its Go twin exist: three readers of one
// contract (the shell, tk, the control plane), pinned to one file. A fix that
// lands in TypeScript only is the failure this repository has already paid for.
describe("the worker boot contract", () => {
  it("matches the shared fixture", () => {
    expect(ORCHESTRATOR_COMMAND).toBe(contract.orchestrator_command);
    expect(WORKER_COMMAND).toBe(contract.worker_command);
    expect(WORKER_PROBE_ARG).toBe(contract.probe_arg);
    expect(WORKER_PROBE_COMMAND).toBe(contract.probe_command);
    expect(WORKER_PROBE_MARKER).toBe(contract.probe_marker);
    // The cancellation door (tick 7zk). Same three readers, same reason: a
    // supervisor asking a container a question it does not answer looks,
    // from outside, exactly like the silent destruction this door exists to
    // end — three containers reading `no-commits` on run run_f7bd5a36.
    expect(WORKER_CANCEL_ARG).toBe(contract.cancel_arg);
    expect(WORKER_CANCEL_COMMAND).toBe(contract.cancel_command);
    expect(WORKER_CANCEL_MARKER).toBe(contract.cancel_marker);
    expect(WORKER_CANCEL_REPORT_MARKER).toBe(contract.cancel_report_marker);
    expect(WORKER_ACTOR).toBe(contract.worker_actor);
    expect(WORKER_BRANCH_PREFIX).toBe(contract.branch_prefix);
    // The boundary guard's two strings (tick dxk). The refusal is the
    // container's alone — it is asserted against the shell in
    // internal/sandbox — but the report marker is read on THIS side, so both
    // are pinned here for the same reason the probe marker is.
    expect(BOUNDARY_REPORT_MARKER).toBe(contract.boundary.report_marker);
    expect(WORKER_EXIT.push).toBe(contract.exit_codes.push);
    expect(WORKER_EXIT.no_work).toBe(contract.exit_codes.no_work);
    expect(WORKER_EXIT.agent).toBe(contract.exit_codes.agent);
  });

  it("derives the branch and report names the collector reads", () => {
    const b = contract.branch_example;
    expect(workerBranch(b.epic, b.tick)).toBe(b.branch);
    expect(workerResultFile(contract.result_file_example.tick)).toBe(
      contract.result_file_example.path,
    );
    // A worker TASK is addressed by the same two rules: the executor composes
    // it at dispatch ({tick_id, branch: attemptLandingBranch(...), base_sha}),
    // so the collector and the container read one spelling.
    expect(attemptLandingBranch(b.epic, 3, b.tick)).toBe(`tick/${b.epic}/attempt-3/${b.tick}`);
  });
});

// One ref per attempt (tick us2). A worker container derives its branch as
// `tick/${TICKS_EPIC}/${TICKS_TICK}` — inside the vendored image
// (image/worker.sh worker_branch_name), from the two env slots it is given
// and nothing else — so the control plane's only lever on WHERE an attempt's
// container lands is the epic slot. The sandbox executor uses it to make
// each attempt's landing branch its own, the rule the local run moved to for
// exactly this reason (internal/reconcile/dispatch.go attemptWriteRef: a
// shared per-tick branch made a redispatch's collect count the previous
// attempt's commits, its pushes collide, and a merge unable to tell one
// attempt's commits from another's).
describe("the per-attempt landing branch (tick us2)", () => {
  it("carries the attempt in the epic slot, so the container derives one branch per attempt", () => {
    const env = workerBootEnv({ ...boot, attempt: 3 });
    expect(env.TICKS_EPIC).toBe("1vn/attempt-3");
    // The composition the container derives from these two slots — the
    // control plane's spelling and the image's rule must agree.
    expect(attemptLandingBranch("1vn", 3, "tap")).toBe(`tick/${env.TICKS_EPIC}/${env.TICKS_TICK}`);
    expect(attemptLandingBranch("1vn", 3, "tap")).toBe("tick/1vn/attempt-3/tap");
    // One branch per attempt: a redispatch lands beside, never on top of,
    // the previous attempt's pushed work.
    expect(attemptLandingBranch("1vn", 4, "tap")).not.toBe(attemptLandingBranch("1vn", 3, "tap"));
  });
});

describe("the role prompt a dispatch carries (tick 9iz)", () => {
  // The sandbox dispatch door carries the profile's rendered prompt because
  // the container's own entrypoint renders its worker prompt from the
  // checkout's tracker and never sees the factory's otherwise. This pins the
  // unit the door's own suite proves end to end: the prompt rides the boot
  // environment the container is started with, beside the harness and the
  // model the same boot carries.
  const prompt = "# implement-tick\n\nYou are implementing ONE unit of work.\n";

  it("rides the boot environment the work command and the probe both get", () => {
    const env = workerBootEnv({ ...boot, harness: "pi", prompt });
    expect(env[WORKER_ROLE_PROMPT_ENV]).toBe(prompt);
    // The green-start probe answers in the same environment, or it proves
    // something about a container nobody will use.
    expect(workerProbeSpec({ ...boot, prompt }).env?.[WORKER_ROLE_PROMPT_ENV]).toBe(prompt);
    expect(workerWorkSpec({ ...boot, prompt }).env?.[WORKER_ROLE_PROMPT_ENV]).toBe(prompt);
  });

  it("leaves the variable absent when the dispatch carried no prompt", () => {
    expect(WORKER_ROLE_PROMPT_ENV in workerBootEnv(boot)).toBe(false);
  });
});

describe("the probe spec", () => {
  // The green-start trap is only a trap if the marker the dispatcher checks
  // for is the marker the container prints. This is that join, asserted
  // through the dispatcher's own evaluator rather than by eye.
  it("passes the dispatcher's evaluator on the entrypoint's own line", () => {
    const spec = workerProbeSpec(boot);
    const real = `ticks-worker: ${WORKER_PROBE_MARKER} tick=tap tk=0.31.0 harness=pi git version 2.43.0\n`;
    expect(evaluateProbeOutput(real, spec.expect, 0)).toEqual({ ok: true });
  });

  it("fails a container that starts cleanly and says nothing useful", () => {
    const spec = workerProbeSpec(boot);
    const green = evaluateProbeOutput("11.0.6\n", spec.expect, 0);
    expect(green.ok).toBe(false);
    if (!green.ok) expect(green.reason).toBe("wrong-output");
    const silent = evaluateProbeOutput("", spec.expect, 0);
    expect(silent.ok).toBe(false);
    if (!silent.ok) expect(silent.reason).toBe("no-output");
  });

  it("probes in the same environment the real command gets", () => {
    const spec = workerWorkSpec(boot);
    expect(spec.probe.env).toEqual(spec.env);
    expect(spec.command).toBe(WORKER_COMMAND);
  });
});

describe("the boot environment", () => {
  it("names the tick, the epic and the base the branch is cut from", () => {
    const env = workerBootEnv(boot);
    expect(env.TICKS_TICK).toBe("tap");
    expect(env.TICKS_EPIC).toBe("1vn");
    expect(env.TICKS_BASE_SHA).toBe(boot.base_sha);
    expect(env.AI_GATEWAY_TOKEN).toBe(boot.gateway_token);
  });

  // Absent is not the same as empty to a shell reading ${VAR:-default}: an
  // exported empty string defeats every default the entrypoint has.
  it("omits what the caller did not supply rather than exporting empty strings", () => {
    const env = workerBootEnv(boot);
    for (const name of ["GITHUB_TOKEN", "TICKS_WORKDIR", "TICKS_FACTORY_URL"]) {
      expect(name in env).toBe(false);
    }
    // TICKS_HARNESS/TICKS_MODEL are the exception — see the dedicated
    // describe block below — but an explicit override still wins, and an
    // explicit empty string still means "let the container decide" rather
    // than "use the worker default".
    expect(workerBootEnv({ ...boot, model: "" }).TICKS_MODEL).toBeUndefined();
    expect(workerBootEnv({ ...boot, model: "anthropic/claude-fable-5" }).TICKS_MODEL).toBe(
      "anthropic/claude-fable-5",
    );
  });

  it("runs the repository's setup unless the caller opts it out", () => {
    expect(workerBootEnv(boot).TICKS_WORKER_SETUP).toBe(contract.setup_modes.always);
    expect(workerBootEnv({ ...boot, setup: "skip" }).TICKS_WORKER_SETUP).toBe(
      contract.setup_modes.skip,
    );
  });

  // tick ys3: per-tick container fan-out failed on EVERY wave, deterministically.
  // The worker resolves `.tick/runners.toml`'s [roles.implement] cell
  // (kind="claude", model="sonnet") whenever TICKS_MODEL is unset, and the
  // factory gateway routes Workers AI only — no anthropic route, no
  // ANTHROPIC_API_KEY. `probe_model` died EXIT_MODEL before the harness ever
  // started. [roles.implement] is also what `tk herd spawn` resolves for
  // LOCAL worker CLIs, which authenticate straight to Anthropic — repointing
  // it at a workers-ai model would have fixed the container and broken every
  // local epic run in the same commit. So the worker's own default lives
  // here, in the factory, not in the repository's routing table.
  describe("the worker's own harness and model default (tick ys3)", () => {
    it("defaults an unconfigured worker to pi on GLM 5.3, never the repository's implement role", () => {
      const env = workerBootEnv(boot);
      expect(env.TICKS_HARNESS).toBe(WORKER_DEFAULT_HARNESS);
      expect(env.TICKS_MODEL).toBe(WORKER_DEFAULT_MODEL);
      // Locks the specific id, so a drift in the constant is a visible test
      // failure rather than a silent routing change. It moved flash -> pro in
      // tick 1cd on run_215b7cbff9's evidence, then omp/DeepSeek -> pi/GLM 5.3
      // in tick uqi on the operator's rule: GLM 5.3 / 5.3 Flash via pi only,
      // and nothing in the cloud runs claude.
      expect(WORKER_DEFAULT_HARNESS).toBe("pi");
      expect(WORKER_DEFAULT_MODEL).toBe("workers-ai/@cf/zai-org/glm-5.3");
    });

    it("still lets the run config or an operator override the worker default", () => {
      const env = workerBootEnv({
        ...boot,
        harness: "codex",
        model: "workers-ai/@cf/zai-org/glm-5.3-flash",
      });
      expect(env.TICKS_HARNESS).toBe("codex");
      expect(env.TICKS_MODEL).toBe("workers-ai/@cf/zai-org/glm-5.3-flash");
    });

    it("the worker default is served through workerProbeSpec/workerWorkSpec too, since the probe must run in the real command's environment", () => {
      expect(workerProbeSpec(boot).env?.TICKS_MODEL).toBe(WORKER_DEFAULT_MODEL);
      expect(workerWorkSpec(boot).env?.TICKS_MODEL).toBe(WORKER_DEFAULT_MODEL);
    });
  });

  // tick 1cd. ys3 was right to put the worker's route in the factory rather
  // than in `[roles.implement]`, but it left it a SOURCE constant: changing
  // which model every cloud worker runs meant editing this file and
  // redeploying. Run run_215b7cbff9 (2026-08-22) is why that matters — three
  // real ticks, 90-minute budgets, workers on flash: 201 exit 124 with no
  // commits, 5jo exit 0 with correct work, 5qj exit 124 with 4 paths salvaged.
  // One of three. The substrate was fine; the model was the limit.
  describe("the worker's route is deployment configuration, not a source edit (tick 1cd)", () => {
    const GLM = "workers-ai/@cf/zai-org/glm-5.3";
    const GLM_FLASH = "workers-ai/@cf/zai-org/glm-5.3-flash";

    it("falls back to the built-in default when neither the run nor the deployment names one", () => {
      expect(workerModel(null, null)).toBe(WORKER_DEFAULT_MODEL);
      expect(workerModel(null, null)).toBe(GLM);
      expect(workerHarness(null, null)).toBe(WORKER_DEFAULT_HARNESS);
      expect(workerModel(undefined, undefined)).toBe(WORKER_DEFAULT_MODEL);
      expect(workerHarness(undefined, undefined)).toBe(WORKER_DEFAULT_HARNESS);
    });

    it("takes the deployment's variable over the built-in default", () => {
      expect(workerModel(null, GLM_FLASH)).toBe(GLM_FLASH);
      expect(workerHarness(null, "codex")).toBe("codex");
    });

    // The order this tick had to preserve: an operator who submits a run with
    // an explicit model is making a decision ABOUT THAT RUN, and it outranks
    // the deployment's standing choice. run submission > deployment var >
    // built-in default.
    it("lets the run's own choice outrank the deployment variable", () => {
      expect(workerModel(GLM_FLASH, GLM)).toBe(GLM_FLASH);
      expect(workerHarness("codex", "pi")).toBe("codex");
    });

    // Same rule `textVar` applies to every other var: a var set to whitespace
    // is a var that was not set, never an empty TICKS_MODEL export.
    it("treats a blank value as unset at both levels", () => {
      expect(workerModel("", GLM_FLASH)).toBe(GLM_FLASH);
      expect(workerModel("   ", "  ")).toBe(WORKER_DEFAULT_MODEL);
      expect(workerHarness("", "")).toBe(WORKER_DEFAULT_HARNESS);
      expect(workerModel(` ${GLM_FLASH} `, null)).toBe(GLM_FLASH);
    });

    // The resolved value has to be a real string the container can export:
    // whatever `workerModel` returns goes straight into `workerBootEnv`, and
    // an empty export defeats every default the entrypoint has.
    it("resolves to something a boot environment can actually carry", () => {
      const env = workerBootEnv({
        ...boot,
        harness: workerHarness(null, "pi"),
        model: workerModel(null, GLM_FLASH),
      });
      expect(env.TICKS_HARNESS).toBe("pi");
      expect(env.TICKS_MODEL).toBe(GLM_FLASH);
    });
  });
});

describe("the harness bound", () => {
  const MINUTE = 60_000;

  // A caller that bounds the worker's harness passes the bound through to the
  // container as TICKS_WORKER_TIMEOUT, in whole seconds — the boot's one lever
  // on how long the agent inside may work.
  it("passes a caller's bound through to the container, in whole seconds", () => {
    const env = workerBootEnv({ ...boot, harness_budget_ms: 90 * MINUTE });
    expect(env.TICKS_WORKER_TIMEOUT).toBe(String(90 * 60));
  });

  // A bound of a few seconds would fail every worker rather than rescue any,
  // so a caller that bounds nothing leaves the harness unbounded.
  it("is off when the caller bounds nothing", () => {
    expect(workerHarnessTimeoutSeconds(undefined)).toBe(0);
    expect(workerHarnessTimeoutSeconds(0)).toBe(0);
    expect(workerBootEnv(boot).TICKS_WORKER_TIMEOUT).toBeUndefined();
    expect(workerBootEnv({ ...boot, harness_budget_ms: 0 }).TICKS_WORKER_TIMEOUT).toBeUndefined();
  });
});

// tick 7zk. The reason a wave stopped travels into the container as a command
// argument, so the branch it pushes says why it was cut short rather than
// looking like an agent that gave up. It is a LABEL, so anything that is not
// one is dropped rather than escaped: a reason needing escaping is not a
// reason, and this string is composed into a command line.
describe("the cancellation door", () => {
  it("carries the stop's own reason", () => {
    expect(workerCancelCommand("budget:cost")).toBe(
      "/usr/local/bin/ticks-worker --cancel budget:cost",
    );
    expect(workerCancelCommand("stopped:hard")).toBe(
      "/usr/local/bin/ticks-worker --cancel stopped:hard",
    );
  });

  it("is the bare door when there is no reason to give", () => {
    expect(workerCancelCommand()).toBe(WORKER_CANCEL_COMMAND);
    expect(workerCancelCommand("")).toBe(WORKER_CANCEL_COMMAND);
    expect(workerCancelCommand(null)).toBe(WORKER_CANCEL_COMMAND);
  });

  it("never composes anything but a label into the command line", () => {
    expect(workerCancelCommand("budget:cost; rm -rf /")).toBe(
      "/usr/local/bin/ticks-worker --cancel budget:costrm-rf",
    );
    expect(workerCancelCommand("$(curl evil)")).toBe(
      "/usr/local/bin/ticks-worker --cancel curlevil",
    );
    expect(workerCancelCommand("x".repeat(500)).length).toBeLessThan(
      WORKER_CANCEL_COMMAND.length + 70,
    );
  });

  // The container is told the door the same way it is told the probe: in the
  // spec, never invented at the teardown site.
  it("rides in the work spec beside the probe and the command", () => {
    const spec = workerWorkSpec(boot);
    expect(spec.salvage?.command).toBe(WORKER_CANCEL_COMMAND);
    expect(spec.salvage?.marker).toBe(WORKER_CANCEL_MARKER);
    // The same environment the real command gets: a door run in a different
    // environment addresses a different container's state directory.
    expect(spec.salvage?.env).toEqual(spec.env);
  });
});
