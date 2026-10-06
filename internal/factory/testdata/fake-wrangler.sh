#!/bin/sh
# A stand-in for the real `wrangler`, used by internal/factory's tests.
#
# It is deliberately stateful: buckets and databases it "creates" are recorded
# under $FAKE_WRANGLER_STATE, so a second `tk factory deploy` sees the same
# account a real second run would and the harness can prove no resource is
# created twice. Every invocation is appended to $FAKE_WRANGLER_LOG, one line
# per call, which is the assertion surface.
#
# Environment:
#   FAKE_WRANGLER_STATE   directory holding the simulated account (required)
#   FAKE_WRANGLER_LOG     file receiving one line per invocation (required)
#   FAKE_WRANGLER_URL     URL printed by `deploy` (default: a workers.dev URL)
#   FAKE_WRANGLER_UNAUTH  when non-empty, `whoami` fails as if not logged in
#   FAKE_WRANGLER_NO_INFO when non-empty, `r2 bucket info` is an unknown
#                         subcommand (an older wrangler), exercising the fallback
#
# Container application (the image the deploy resolves and the legacy app the
# deploy deletes):
#   FAKE_WRANGLER_PUSH_DIGEST   digest `deploy` reports pushing (default: a new one)
#   FAKE_WRANGLER_SERVING_DIGEST digest the applications report before the
#                         deploy that pushed the new one (default: an "old image" digest)
#   FAKE_WRANGLER_NO_PUSH when non-empty, `deploy` reports that the image
#                         already exists remotely and emits no image block
#   FAKE_WRANGLER_NO_CONTAINER_BLOCK when non-empty, `deploy` prints no container
#                         application block, so no digest can be read from it
#   FAKE_WRANGLER_NO_CONTAINERS when non-empty, `containers` is an unknown
#                         subcommand (an older wrangler)
#   FAKE_WRANGLER_NO_APP when non-empty, the listing has other applications but
#                         not the FactorySandbox one
#   FAKE_WRANGLER_NO_LEGACY_APP when non-empty, the account has no 0.x application
#   FAKE_WRANGLER_REGISTRY_HOST registry host `containers registries credentials`
#                         names; unset, that command is unsupported (no prune)
#                         `containers images delete` appends to
#                         $FAKE_WRANGLER_STATE/deleted-images
set -eu

: "${FAKE_WRANGLER_STATE:?fake wrangler needs FAKE_WRANGLER_STATE}"
: "${FAKE_WRANGLER_LOG:?fake wrangler needs FAKE_WRANGLER_LOG}"
mkdir -p "$FAKE_WRANGLER_STATE/buckets" "$FAKE_WRANGLER_STATE/d1"
printf '%s\n' "$*" >>"$FAKE_WRANGLER_LOG"

url=${FAKE_WRANGLER_URL:-https://ticks-factory.acme.workers.dev}

old_digest=${FAKE_WRANGLER_SERVING_DIGEST:-sha256:1111111111111111111111111111111111111111111111111111111111111111}
new_digest=${FAKE_WRANGLER_PUSH_DIGEST:-sha256:2222222222222222222222222222222222222222222222222222222222222222}
registry=registry.cloudflare.com/acct
FACTORY_APP=ticks-factory-sandbox
FACTORY_REPO=ticks-factory-factorysandbox-factory
LEGACY_APP=ticks-orchestrator

case "${1:-}" in
  --version)
    echo "4.123.0"
    ;;
  auth)
    if [ "${2:-}" != "token" ]; then
      echo "fake wrangler: unsupported auth command: $*" >&2
      exit 64
    fi
    echo '{"type":"api_token","token":"fake-api-token"}'
    ;;
  whoami)
    if [ -n "${FAKE_WRANGLER_UNAUTH:-}" ]; then
      echo "You are not authenticated. Please run \`wrangler login\`." >&2
      exit 1
    fi
    echo "Getting User settings..."
    echo "👋 You are logged in with an OAuth Token, associated with the email test@example.com."
    ;;
  r2)
    name=${4:-}
    case "${3:-}" in
      info)
        if [ -n "${FAKE_WRANGLER_NO_INFO:-}" ]; then
          echo "Unknown argument: info" >&2
          exit 1
        fi
        if [ -f "$FAKE_WRANGLER_STATE/buckets/$name" ]; then
          echo "name: $name"
        else
          echo "The specified bucket does not exist. [code: 10006]" >&2
          exit 1
        fi
        ;;
      create)
        if [ -f "$FAKE_WRANGLER_STATE/buckets/$name" ]; then
          echo "A bucket with that name already exists. [code: 10004]" >&2
          exit 1
        fi
        : >"$FAKE_WRANGLER_STATE/buckets/$name"
        echo "Created bucket '$name'."
        ;;
      list)
        echo "name:          demo-other-bucket"
        for b in "$FAKE_WRANGLER_STATE"/buckets/*; do
          [ -e "$b" ] || continue
          echo "name:          $(basename "$b")"
        done
        ;;
      *)
        echo "fake wrangler: unsupported r2 command: $*" >&2
        exit 64
        ;;
    esac
    ;;
  d1)
    case "${2:-}" in
      list)
        printf '['
        first=1
        for d in "$FAKE_WRANGLER_STATE"/d1/*; do
          [ -e "$d" ] || continue
          [ $first -eq 1 ] || printf ','
          first=0
          printf '{"uuid":"%s","name":"%s","version":"production"}' "$(cat "$d")" "$(basename "$d")"
        done
        printf ']\n'
        ;;
      create)
        name=${3:-}
        if [ -f "$FAKE_WRANGLER_STATE/d1/$name" ]; then
          echo "A database with that name already exists. [code: 7502]" >&2
          exit 1
        fi
        # Deterministic-but-distinct per name, so the log is readable.
        printf 'aaaaaaaa-bbbb-cccc-dddd-%012d' "$(ls "$FAKE_WRANGLER_STATE/d1" | wc -l | tr -d ' ')" \
          >"$FAKE_WRANGLER_STATE/d1/$name"
        echo "✅ Successfully created DB '$name'"
        echo "database_id = \"$(cat "$FAKE_WRANGLER_STATE/d1/$name")\""
        ;;
      migrations)
        echo "migrations applied" >>"$FAKE_WRANGLER_STATE/migrations.log"
        echo "🌀 No migrations to apply!"
        ;;
      execute)
        # Record the SQL so the harness can prove the version row was written.
        printf '%s\n' "$*" >>"$FAKE_WRANGLER_STATE/execute.log"
        if [ "${5:-}" = "--json" ]; then
          # The two reads the deploy makes with --json, answered from state
          # files so a test can set the account's condition:
          #   - the legacy-run count (legacyapp.go): the number of live runs
          #     still on the 0.x application, from `legacy-runs` (default 0);
          #   - the live pins (prune.go): the images live runs pin, from
          #     `pins.json` when present, none when not.
          case "$*" in
            *"COUNT(*) AS n"*)
              n=0
              [ -s "$FAKE_WRANGLER_STATE/legacy-runs" ] && n=$(cat "$FAKE_WRANGLER_STATE/legacy-runs")
              echo "[{\"results\":[{\"n\":$n}],\"success\":true,\"meta\":{}}]"
              ;;
            *"run_substrate"*)
              if [ -s "$FAKE_WRANGLER_STATE/pins.json" ]; then
                cat "$FAKE_WRANGLER_STATE/pins.json"
              else
                echo "[{\"results\":[],\"success\":true,\"meta\":{}}]"
              fi
              ;;
            *)
              echo "[{\"results\":[],\"success\":true,\"meta\":{}}]"
              ;;
          esac
        else
          echo "🚣 Executed 1 command"
        fi
        ;;
      *)
        echo "fake wrangler: unsupported d1 command: $*" >&2
        exit 64
        ;;
    esac
    ;;
  deploy)
    if [ ! -f wrangler.toml ]; then
      echo "fake wrangler: no wrangler.toml in $(pwd)" >&2
      exit 1
    fi
    if grep -q '^database_id = "00000000-0000-0000-0000-000000000000"' wrangler.toml; then
      echo "fake wrangler: refusing to deploy with the placeholder database_id" >&2
      exit 1
    fi
    # FAKE_WRANGLER_PREPARATION_TIMEOUTS=N: the first N deploys fail the way
    # wrangler 4.145 does when Cloudflare has not prepared a durable_object
    # application's image within its fixed 15 minutes.
    if [ -n "${FAKE_WRANGLER_PREPARATION_TIMEOUTS:-}" ]; then
      prep_file="$FAKE_WRANGLER_STATE/preparation-timeouts"
      prep=0
      [ -s "$prep_file" ] && prep=$(cat "$prep_file")
      if [ "$prep" -lt "$FAKE_WRANGLER_PREPARATION_TIMEOUTS" ]; then
        printf '%s' "$((prep + 1))" >"$prep_file"
        echo "Image does not exist remotely, pushing: $registry/ticks-factory-factorysandbox-factory:abc"
        echo "✘ [ERROR] Timed out while preparing the container image on Cloudflare's network."
        exit 1
      fi
    fi
    cp wrangler.toml "$FAKE_WRANGLER_STATE/deployed-wrangler.toml"
    # The docker the deploy handed wrangler (internal/factory/dockershim.go).
    printf 'WRANGLER_DOCKER_BIN=%s\nTICFAC_DOCKER_BIN=%s\nTICFAC_WRANGLER_BIN=%s\n' \
      "${WRANGLER_DOCKER_BIN:-}" "${TICFAC_DOCKER_BIN:-}" "${TICFAC_WRANGLER_BIN:-}" \
      >"$FAKE_WRANGLER_STATE/deploy-docker-env"
    # The Docker build log wrangler prints first is full of sha256 digests that
    # are NOT the application's; the parser has to ignore them.
    echo "#4 [internal] load metadata for docker.io/cloudflare/sandbox:0.12.7-python"
    echo "#4 resolve docker.io/cloudflare/sandbox@sha256:6666666666666666666666666666666666666666666666666666666666666666"
    if [ -n "${FAKE_WRANGLER_NO_PUSH:-}" ]; then
      echo "Image already exists remotely, skipping push"
    elif [ -z "${FAKE_WRANGLER_NO_CONTAINER_BLOCK:-}" ]; then
      printf '%s' "$new_digest" >"$FAKE_WRANGLER_STATE/container-target"
      echo "╭ Deploy a container application"
      echo "├ EDIT ticks-factory-sandbox"
      echo "-   image = \"$registry/$FACTORY_REPO@$old_digest\""
      echo "+   image = \"$registry/$FACTORY_REPO@$new_digest\""
      echo "╰ Applied changes"
    fi
    echo "Total Upload: 12.34 KiB / gzip: 3.21 KiB"
    echo "Uploaded ticks-factory (1.23 sec)"
    echo "Deployed ticks-factory triggers (0.45 sec)"
    echo "  $url"
    echo "Current Version ID: 00000000-1111-2222-3333-444444444444"
    ;;
  containers)
    if [ -n "${FAKE_WRANGLER_NO_CONTAINERS:-}" ]; then
      echo "Unknown argument: containers" >&2
      exit 1
    fi
    case "${2:-}" in
      list)
        # The account's applications: the FactorySandbox one always (unless a
        # test removes it), the 0.x one until the deploy deletes it. The 0.x
        # marker is created lazily so a fresh state starts where the live
        # account is — with the legacy application still there.
        legacy_marker="$FAKE_WRANGLER_STATE/legacy-app"
        gone_marker="$FAKE_WRANGLER_STATE/legacy-app-gone"
        if [ -n "${FAKE_WRANGLER_NO_LEGACY_APP:-}" ]; then
          touch "$gone_marker"
        elif [ ! -f "$legacy_marker" ] && [ ! -f "$gone_marker" ]; then
          touch "$legacy_marker"
        fi

        serving=$old_digest
        if [ -f "$FAKE_WRANGLER_STATE/container-target" ]; then
          serving=$(cat "$FAKE_WRANGLER_STATE/container-target")
        fi

        printf '['
        if [ -z "${FAKE_WRANGLER_NO_APP:-}" ]; then
          printf '{"id":"0123456789abcdef0123456789abcdef","name":"%s","state":"ready","instances":0,' "$FACTORY_APP"
          printf '"image":"%s/%s@%s","version":7,' "$registry" "$FACTORY_REPO" "$serving"
          printf '"updated_at":"2026-08-20T00:00:00Z","created_at":"2026-08-01T00:00:00Z"}'
        fi
        if [ -f "$legacy_marker" ]; then
          if [ -z "${FAKE_WRANGLER_NO_APP:-}" ]; then printf ','; fi
          printf '{"id":"fedcba9876543210fedcba9876543210","name":"%s","state":"ready","instances":0,' "$LEGACY_APP"
          printf '"image":"%s/%s@%s","version":1,' "$registry" "$LEGACY_APP" "$old_digest"
          printf '"updated_at":"2026-08-20T00:00:00Z","created_at":"2026-08-01T00:00:00Z"}'
        fi
        printf ']\n'
        ;;
      delete)
        # `wrangler containers delete <application-id>`: the deploy deletes the
        # 0.x application by its listing id. Deleting the 0.x app clears its
        # marker, so a later listing does not show it.
        if [ "${3:-}" = "fedcba9876543210fedcba9876543210" ]; then
          rm -f "$FAKE_WRANGLER_STATE/legacy-app"
          touch "$FAKE_WRANGLER_STATE/legacy-app-gone"
          printf '%s\n' "$3" >>"$FAKE_WRANGLER_STATE/deleted-apps"
          echo "Your container has been deleted"
        else
          echo "fake wrangler: refusing to delete unknown application ${3:-}" >&2
          exit 1
        fi
        ;;
      registries)
        # `containers registries credentials <domain> --pull --json`: only
        # when a test serves a registry; otherwise the prune is skipped with
        # a warning, as it is for a wrangler that cannot mint credentials.
        if [ "${3:-}" != "credentials" ] || [ -z "${FAKE_WRANGLER_REGISTRY_HOST:-}" ]; then
          echo "fake wrangler: unsupported containers command: $*" >&2
          exit 64
        fi
        printf '{"account_id":"acct","registry_host":"%s","username":"v1","password":"fake-registry-password"}\n' \
          "$FAKE_WRANGLER_REGISTRY_HOST"
        ;;
      images)
        if [ "${3:-}" != "delete" ]; then
          echo "fake wrangler: unsupported containers command: $*" >&2
          exit 64
        fi
        printf '%s\n' "${4:-}" >>"$FAKE_WRANGLER_STATE/deleted-images"
        echo "Deleted ${4:-}"
        ;;
      *)
        echo "fake wrangler: unsupported containers command: $*" >&2
        exit 64
        ;;
    esac
    ;;
  secret)
    case "${2:-}" in
      put)
        cat >"$FAKE_WRANGLER_STATE/secret-${3:-unnamed}"
        echo "✨ Success! Uploaded secret ${3:-unnamed}"
        ;;
      *)
        echo "fake wrangler: unsupported secret command: $*" >&2
        exit 64
        ;;
    esac
    ;;
  *)
    echo "fake wrangler: unsupported command: $*" >&2
    exit 64
    ;;
esac
