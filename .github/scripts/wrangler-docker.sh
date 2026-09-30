#!/usr/bin/env bash
# The `docker` wrangler runs in the deploy-factory workflow (WRANGLER_DOCKER_BIN).
#
# Wrangler builds the orchestrator image with a plain `docker build --load ...`
# and offers no flag for a build cache. On a fresh GitHub runner that plain
# build has no cache at all, so every deploy rebuilt every one of our layers,
# produced ~0.8 GB of blobs with new digests, pushed them to the managed
# registry and made every container pull them again (2026-09-30: 22 of 46
# layers new per deploy, and a slow pull of fresh layers is what stalled that
# day's rollouts).
#
# So `build` is routed through buildx on a docker-container builder with the
# GitHub Actions cache: an unchanged layer is taken from the cache, keeps its
# digest, and the registry already has it. Every other docker command wrangler
# issues (push, image inspect, login, ...) goes to docker unchanged.
#
# Needs: a builder named by TICFAC_BUILDX_BUILDER (docker/setup-buildx-action)
# and the Actions cache runtime env (ACTIONS_RESULTS_URL, ACTIONS_RUNTIME_TOKEN;
# crazy-max/ghaction-github-runtime). Without either, it falls back to the plain
# build rather than fail a deploy over a cache.
set -euo pipefail

if [[ ${1:-} == build ]] && [[ -n ${TICFAC_BUILDX_BUILDER:-} ]] && [[ -n ${ACTIONS_RUNTIME_TOKEN:-} ]]; then
  shift
  scope=${TICFAC_BUILDX_CACHE_SCOPE:-ticks-orchestrator}
  echo "wrangler-docker: building with buildx ($TICFAC_BUILDX_BUILDER) and the GitHub Actions cache (scope $scope)" >&2
  exec docker buildx build \
    --builder "$TICFAC_BUILDX_BUILDER" \
    --cache-from "type=gha,scope=$scope" \
    --cache-to "type=gha,scope=$scope,mode=max,ignore-error=true" \
    "$@"
fi

if [[ ${1:-} == build ]]; then
  echo "wrangler-docker: no buildx builder or Actions cache runtime; building without a cache" >&2
fi
exec docker "$@"
