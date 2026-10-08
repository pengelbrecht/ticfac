# lrd — the harness half of the local gateway metering join

Tick lrd (absorbed into epic ex6 as the final review's blocking finding):
m1w delivered the local gateway metering join on the Go side only —
`workerconfig.go` wrote it into `worker.json`, and no harness code read it,
so a local pi-durable worker still called Workers AI through
`localWorkersAIProvider()` on ambient credentials, unmetered. The local
half of the epic's [A3] ("pi-durable workers are metered locally and in the
cloud") failed while the gate stayed green, because the only tests
exercised the writer.

## What changed

The harness now reads the join and composes the same provider override the
pi CLI's generated extension composes for a herdr pane (`WriteExtension`):

- **`harness/src/local/gateway-metering.ts`** (new — the very file the Go
  writer's comment cited, "a file that exists on no branch" when the
  finding was filed): the join as `worker.json` carries it
  (`gatewayUrl`, `runId`, `metadata`, `credentialCommand`), the applies
  rule (`GatewayMetering.Applies`' own: only a Workers AI spelling, never
  the tests' faux rung), the route (`<gateway>/workers-ai/v1`, validated
  at boot the way the writer validates it before writing), and
  `meteredWorkersAIProvider` — the local rung's own catalog (GLM
  corrections included), only the address and the credential changed. The
  credential pipeline executes in the harness's own shell at request time
  (the token is never written anywhere; a rotated `~/.ticfacrc` is what
  the next request presents); an empty answer is a refusal naming the
  provider, never an anonymous call — the same answer the cloud rung's
  gateway provider gives an empty token.
- **`harness/src/local/worker-host.ts`**: `LocalWorkerConfig.metering` and
  the wiring — a Workers AI model whose config carries a join runs the
  metered provider, everything else runs exactly as before.
- **`harness/test/node/local-metering.test.ts`** (new): the acceptance
  suite. THE acceptance test drives the real entry
  (`src/local/main.ts`) as a real child process, over a real git worktree,
  with the real credential pipeline (a sealed `~/.ticfacrc` — the
  operator's real token never reaches a wire these assertions print on),
  against a recording fake AI Gateway: **a metered local launch tags its
  calls** — both requests at `<gateway>/workers-ai/v1/chat/completions`
  with `Authorization` and `cf-aig-authorization` both carrying the
  credential the pipeline printed (a stored key and an ambient key are in
  place to catch a missing displacement), `cf-aig-metadata` verbatim from
  worker.json, `x-session-affinity` the run id, GLM's corrected
  max-output on the wire, and each stream drained to its tail (the read
  the gateway needs before it writes the row the metering IS, tick 648).
  The siblings: the faux rung runs unmetered with a join in its config
  (no request reaches the gateway), a gateway URL that composes no route
  refuses the boot (exit 2), a missing credential settles unanswered with
  the provider named and nothing sent. I reproduced the acceptance test
  against the base harness behaviour first: it fails there (`expected 1
  to be 0` — the join read by no one, the run settles unanswered).
- **`internal/exec/subprocess/workerconfig_parity_test.go`** (new): the
  seam guard for the direction only a Go test can see — every field the
  writer marshals is a field the harness declares, so a renamed tag can
  never write a join the harness silently drops (the finding's exact
  shape). Verified it fails on a simulated rename.
- **`harness/README.md`**: the new module, documented beside its siblings.

One deliberate scope line: the Go writer needed no change (it was already
correct); the [A3] gap was purely the missing reader, and this is it.

## What I ran

- `cd harness && pnpm exec vitest run -c vitest.node.config.ts
  test/node/local-metering.test.ts` — **10 passed** (the acceptance suite).
- `make harness-gate` (the harness package's whole gate: install + Biome
  + both tsc projects + BOTH vitest suites, the Makefile twin of the
  pending `harness` testing command) — **exit 0**: workerd 10 files, node
  11 files, 66 node tests, all green.
- `go test ./internal/exec/subprocess/ -short -run Metering -count=1` and
  the new parity test — pass; the parity test also verified failing on a
  simulated drift.
- `make gate` (gofmt + go vet + `go test -short` over the whole
  repository) — **exit 0**.

## What the next tick has to know

- The harness-side metering is behaviour the node suite pins
  (`local-metering.test.ts`); a pi-ai or pi-durable upgrade that changes
  the auth seam (`Provider.auth.apiKey.resolve` returning
  `{ headers, baseUrl }`) or the OpenAI SDK's header precedence will fail
  these tests loudly — that is their job.
- The wire tests run with the suite process's `HOME` sealed (restored in
  afterEach); the launch tests seal the child's `HOME`, `TICFAC_PI_AUTH_FILE`
  and ambient Cloudflare keys in the fixture. Keep that if you touch them:
  the pipeline reads `$HOME/.ticfacrc`, and an unsealed run would put the
  operator's real token on a recorded wire.
- The `harness` `[testing.commands]` cell is still the pending protected
  change (tick 2pn) — until it lands, `make harness-gate` is the gate a
  harness-touching tick runs; I ran it.
- The timeout-discipline guard classifies the two wire tests as
  worker/process-driving through its fixture-vocabulary approximation
  (the shared `gateway`/`f` variable marking); they state the demanded
  300s bound. The guard's own doc accepts over-binding as the cheap side
  of the trade.
- Epic ex6 [A3]'s local half is now delivered on test evidence; the
  cloud half is the factory's own gateway stamping (D17), untouched here.

```findings v2
[]
```

STATUS: DONE
