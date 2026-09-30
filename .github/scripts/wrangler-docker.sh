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
# issues (image inspect, login, ...) goes to docker unchanged, except push.
#
# `push` is wrapped for a different reason: the registry credential. Wrangler
# logs docker in to the managed registry once, after the build, with a
# credential that expires 15 minutes later (containers-shared login.ts:
# `expirationMinutes = 15`), and then pushes. A push that outlives it — a slow
# upload of a fresh ~200 MB layer, docker's own retries — fails `unauthorized`
# however healthy the upload was. deploy-factory run 36735949343 (2026-09-30):
# every layer but one pushed, the 209 MB one retried until the credential
# lapsed and ended `unauthorized`, 35 minutes into wrangler's deploy; the next
# deploy, with nothing changed, pushed fine. So before each push attempt this
# logs in afresh with a credential that outlives any push seen
# (TICFAC_REGISTRY_CREDENTIAL_MINUTES, default 120), and a failed push is
# retried with a fresh one (TICFAC_PUSH_ATTEMPTS, default 3): layers already
# pushed are not uploaded again. The password reaches `docker login` on stdin
# and is never printed. When no credential can be minted, the push runs on
# wrangler's own login, as it always did.
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

# fresh_login <registry host>: log docker in with a newly minted push
# credential. Fails, saying why, without touching docker's stored login when
# none can be minted.
fresh_login() {
  local host=$1 minutes=${TICFAC_REGISTRY_CREDENTIAL_MINUTES:-120} creds user pass
  if ! creds=$("${TICFAC_WRANGLER_BIN:-wrangler}" containers registries credentials "$host" \
    --push --pull --expiration-minutes "$minutes" --json 2>/dev/null); then
    echo "wrangler-docker: could not mint a registry credential for $host" >&2
    return 1
  fi
  user=$(jq -r '.username // empty' <<<"$creds" 2>/dev/null || true)
  pass=$(jq -r '.password // empty' <<<"$creds" 2>/dev/null || true)
  if [[ -z $user || -z $pass ]]; then
    echo "wrangler-docker: the minted registry credential for $host named no username or password" >&2
    return 1
  fi
  if ! docker login --username "$user" --password-stdin "$host" <<<"$pass" >/dev/null; then
    echo "wrangler-docker: docker login to $host with a fresh credential failed" >&2
    return 1
  fi
  echo "wrangler-docker: logged in to $host with a ${minutes}-minute push credential" >&2
}

if [[ ${1:-} == push ]]; then
  ref=${!#}
  host=${ref%%/*}
  case $host in
    *.cloudflare.com) ;;
    *) exec docker "$@" ;;
  esac
  attempts=${TICFAC_PUSH_ATTEMPTS:-3}
  for ((attempt = 1; ; attempt++)); do
    fresh_login "$host" || true
    if docker "$@"; then
      exit 0
    fi
    if ((attempt >= attempts)); then
      echo "wrangler-docker: the push of $ref failed $attempts times" >&2
      exit 1
    fi
    echo "wrangler-docker: the push of $ref failed (attempt $attempt of $attempts); retrying with a fresh credential" >&2
    sleep "${TICFAC_PUSH_RETRY_DELAY:-10}"
  done
fi

exec docker "$@"
