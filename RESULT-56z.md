<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ilz/attempt-1/56z`, base `b99b5b0868aa64aacd9ae0cfbf369438fb2e0eb5`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

> **BOUNDARY VIOLATION ATTEMPTED.** This agent tried to write tracker state, which the
> orchestrator owns. The container refused it, so nothing under `.tick/`
> should have reached this branch — but the attempt is reported rather than
> silently cleaned, because a model that ignored an explicit instruction is
> something a human has to see. What it did:
>
> - the agent ran `tk tree ilz`

# Tick 56z: write claude-sub guide

## What changed

- Added `docs/claude-sub-operator.md`: the operator guide for the
  claude-sub rung — making a token (`claude setup-token`), adding
  /rotating/removing a subscription (`cd cloudflare && pnpm exec wrangler
  secret put|delete|list CLAUDE_SUB_TOKEN_<LABEL>`, against the production
  wrangler config, no `-c`; the `LABEL` regex from `claude-sub.ts`),
  failover behaviour (sticky lease per job id — a running job never
  switches subscription; bench on a quota 429 until the unified reset or a
  brief throttle bench; 24h bench on 401/403; the next lease routes around
  a benched subscription; Workers AI step-down when none is free — never a
  queue), selecting the config per epic (`--config` flag >
  `config:<name>` epic label > `[configs]` default, from
  `internal/reconcile/runconfig_select.go` and this repo's
  `.tick/runners.cloud.toml`), and observing the pool (`GET
  /api/claude-sub`, `POST /api/claude-sub/unbench/<LABEL>`, and the
  `claude_sub_labels` field on `GET /api/deployment` that `ticfac factory
  status`/`doctor` read) — all without any token, account id or factory
  URL.
- Added one link to it from `README.md`'s "Deploying the factory" section.

Every statement in the doc is checked against
`cloudflare/src/claude-sub.ts` (`isSubscriptionRung`, `classifyAnswer`,
`ClaudeSubPoolCore.lease`, `subscriptionLabels`, the `LABEL` regex, the
secret never reaching a container), `cloudflare/src/sandbox-executor.ts`
(`claudeSubLeaseForBoot`'s step-down to Workers AI on a failed lease) and
`internal/reconcile/runconfig_select.go` (`selectRunConfig`'s precedence
and the resume-keeps-its-config rule), plus `cloudflare/src/index.ts`'s
`/api/claude-sub` and `/api/deployment` routes and
`internal/cli/config_preflight.go`/`doctor.go`/`internal/factory/status.go`
for how `ticfac doctor`/`factory status` surface the labels.

## What I ran

- `GOTEST_PARALLEL=4 GOFLAGS=-p=2 make gate` — green (gofmt, go vet,
  `go test -short -timeout 45m -parallel 4 ./...` across every package,
  including `internal/cli`'s README-pins-the-command-table test).

No Go or TypeScript source changed, only docs and the README, so no other
test run applies.

## Next tick's note

Nothing left for this tick: acceptance criteria are all satisfied
(doc exists and covers all five points named in the description, README
links it, `make gate` passes). No findings — I found nothing outside this
tick's scope while reading the three cited source files.

```findings v2
[]
```

STATUS: DONE
