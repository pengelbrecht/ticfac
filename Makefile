# internal/reconcile's suite measured 604-1063s serial — past go test's default
# 10-minute PER-PACKAGE timeout ("panic: test timed out after 10m0s") — and
# now runs parallelised (GOTEST_PARALLEL below) in ~3 minutes. The explicit
# timeout stays regardless: a loaded or slower host must fail on its own
# merits, never on the default. CI and the docs point here rather than at a
# bare `go test ./...` so the timeout can't be dropped by drifting out of sync.
GOTEST_TIMEOUT := 45m

# internal/reconcile's 80 tests each fork git and real worker processes; the
# suite's cost is process spawning, not sleeps, so the lever is parallelism
# (the package's parallel_test.go keeps the annotations from rotting).
# Measured on this machine (10-core darwin/arm64), the package alone, -short:
#   -parallel 1 = 730s | 4 = 260s | 8 = 210s | 10 = 223s | 12 = 185s, 177s
#   16 = 192s | 24 = 171s, 184s — returns stop at 12 (the 16/24 spread is
# run-to-run noise) while host load, and so the risk on the one
# wall-clock-sensitive test, only grows past it. `?=` so another host can
# re-measure and override without editing this file.
GOTEST_PARALLEL ?= 12

.PHONY: build vet test-short test gate

build:
	go build ./...

vet:
	go vet ./...

test-short:
	go test -short -timeout $(GOTEST_TIMEOUT) -parallel $(GOTEST_PARALLEL) ./...

test:
	go test -timeout $(GOTEST_TIMEOUT) -parallel $(GOTEST_PARALLEL) ./...

# What the integrated gate runs, kept here so a human and CI run exactly what
# gates a tick. The authoritative copy is .tick/runners.toml — that file is what
# says what the gate runs, and a reader must see the command there rather than
# an indirection — so TestTheGateTargetMatchesTheDeclaredGate fails if this
# recipe and that command ever disagree.
#
# The gate's copy had drifted to a bare `go test -short -count=1 ./...`, losing
# both numbers above: the gate, the one runner that can block every tick, was
# running internal/reconcile serially against go's 10-minute per-package limit.
# -count=1 was the gate's own addition, on the reasoning that a verdict served
# from go's test cache is not evidence that this tree passes. That reasoning is
# half right, and on 2026-09-18 the operator traded the other half for speed:
# for a package whose sources, dependencies and consulted files are unchanged, a
# cached pass IS a true statement about this tree, and re-running it 49 times an
# epic is what made the gate cost more than the work.
#
# `make suite` below keeps -count=1 for when you want the paranoid answer —
# notably for the drift guards that read .tick/runners.toml by absolute path,
# which are the one place caching is known to be able to serve a stale pass.
gate:
	go test -short -timeout $(GOTEST_TIMEOUT) -parallel $(GOTEST_PARALLEL) ./...

# The TypeScript half of the gate (tick odc). Kept as its own target, and its
# own [testing.commands] entry, so each check records its own evidence and a
# reader can see which half refused a tick.
#
# vitest is deliberately NOT here: it is 82s against these two at 3s, gate
# commands run serially, and CI runs it as its own job beside the Go one for no
# wall clock at all. This target covers CONTRACTS AND TYPES, not behaviour.
ts-gate:
	cd ticfac/cloudflare && pnpm install --frozen-lockfile --prefer-offline && pnpm contracts:check && pnpm exec tsc --noEmit

# The gate, with the cache refused. Slower and unconditional.
suite:
	go test -short -count=1 -timeout $(GOTEST_TIMEOUT) -parallel $(GOTEST_PARALLEL) ./...
