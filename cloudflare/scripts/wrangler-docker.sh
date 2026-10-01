#!/usr/bin/env bash
# The `docker` wrangler runs during `ticfac factory deploy` (WRANGLER_DOCKER_BIN).
#
# The deploy stages this script with the bundle and points wrangler at it on
# every deploy, from CI and from a laptop alike (internal/factory/dockershim.go).
# The docker it wraps is TICFAC_DOCKER_BIN: whatever WRANGLER_DOCKER_BIN named
# before the deploy set it, else `docker`.
#
# `push` to the managed registry: the registry credential. Wrangler logs docker
# in to the managed registry once, after the build, with a credential that
# expires 15 minutes later (containers-shared login.ts: `expirationMinutes =
# 15`), and then pushes. A push that outlives it — a slow upload of a fresh
# ~200 MB layer, docker's own retries — fails `unauthorized` however healthy
# the upload was. deploy-factory run 36735949343 (2026-09-30): every layer but
# one pushed, the 209 MB one retried until the credential lapsed and ended
# `unauthorized`, 35 minutes into wrangler's deploy; the next deploy, with
# nothing changed, pushed fine. So before each push attempt this logs in afresh
# with a credential that outlives any push seen
# (TICFAC_REGISTRY_CREDENTIAL_MINUTES, default 120), and a failed push is
# retried with a fresh one (TICFAC_PUSH_ATTEMPTS, default 3): layers already
# pushed are not uploaded again. The password reaches `docker login` on stdin
# and is never printed. When no credential can be minted, the push runs on
# wrangler's own login, as it always did. The wrangler that mints it is
# TICFAC_WRANGLER_BIN with the words of TICFAC_WRANGLER_PREFIX before its
# arguments (`npx --no wrangler`), else `wrangler`.
#
# `build` in CI: the build cache. Wrangler builds the orchestrator image with a
# plain `docker build --load ...` and offers no flag for a build cache. On a
# fresh GitHub runner that plain build has no cache at all, so every deploy
# rebuilt every one of our layers, produced ~0.8 GB of blobs with new digests,
# pushed them to the managed registry and made every container pull them again
# (2026-09-30: 22 of 46 layers new per deploy, and a slow pull of fresh layers
# is what stalled that day's rollouts). So when a buildx builder is named by
# TICFAC_BUILDX_BUILDER (docker/setup-buildx-action) and the Actions cache
# runtime env is present (ACTIONS_RESULTS_URL, ACTIONS_RUNTIME_TOKEN;
# crazy-max/ghaction-github-runtime), `build` goes through buildx with the
# GitHub Actions cache: an unchanged layer keeps its digest, and the registry
# already has it. Without either — a laptop — it is the plain build.
#
# Every other docker command wrangler issues goes to docker unchanged.
set -euo pipefail

docker_bin=${TICFAC_DOCKER_BIN:-docker}
# A TICFAC_DOCKER_BIN that is this script would recurse forever.
if [[ $docker_bin -ef ${BASH_SOURCE[0]} ]]; then
  docker_bin=docker
fi

if [[ ${1:-} == build ]] && [[ -n ${TICFAC_BUILDX_BUILDER:-} ]] && [[ -n ${ACTIONS_RUNTIME_TOKEN:-} ]]; then
  shift
  scope=${TICFAC_BUILDX_CACHE_SCOPE:-ticks-orchestrator}
  echo "wrangler-docker: building with buildx ($TICFAC_BUILDX_BUILDER) and the GitHub Actions cache (scope $scope)" >&2
  exec "$docker_bin" buildx build \
    --builder "$TICFAC_BUILDX_BUILDER" \
    --cache-from "type=gha,scope=$scope" \
    --cache-to "type=gha,scope=$scope,mode=max,ignore-error=true" \
    "$@"
fi

# json_field <name>: the string value of a top-level field of the JSON object
# on stdin. Wrangler's credential JSON is flat and its values (a username, a
# JWT) carry no quotes or escapes, so this needs no jq.
json_field() {
  tr -d '\n' | sed -nE "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"([^\"]*)\".*/\\1/p"
}

# fresh_login <registry host>: log docker in with a newly minted push
# credential. Fails, saying why, without touching docker's stored login when
# none can be minted.
fresh_login() {
  local host=$1 minutes=${TICFAC_REGISTRY_CREDENTIAL_MINUTES:-120} creds user pass
  local -a wrangler_cmd
  # shellcheck disable=SC2206 # the prefix is words, by contract
  wrangler_cmd=("${TICFAC_WRANGLER_BIN:-wrangler}" ${TICFAC_WRANGLER_PREFIX:-})
  if ! creds=$("${wrangler_cmd[@]}" containers registries credentials "$host" \
    --push --pull --expiration-minutes "$minutes" --json 2>/dev/null); then
    echo "wrangler-docker: could not mint a registry credential for $host" >&2
    return 1
  fi
  user=$(json_field username <<<"$creds")
  pass=$(json_field password <<<"$creds")
  if [[ -z $user || -z $pass ]]; then
    echo "wrangler-docker: the minted registry credential for $host named no username or password" >&2
    return 1
  fi
  if ! "$docker_bin" login --username "$user" --password-stdin "$host" <<<"$pass" >/dev/null; then
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
    *) exec "$docker_bin" "$@" ;;
  esac
  attempts=${TICFAC_PUSH_ATTEMPTS:-3}
  for ((attempt = 1; ; attempt++)); do
    fresh_login "$host" || true
    if "$docker_bin" "$@"; then
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

exec "$docker_bin" "$@"
