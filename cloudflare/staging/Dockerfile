# The staging image (epic umq, tick nmd): small on purpose — the rehearsal is
# about the PLATFORM (a deploy under a live container keeps it on its startup
# image), not about the factory image. STAGE_MARK is bumped between two
# deploys so the second one ships a different digest.
FROM debian:trixie-slim
ARG STAGE_MARK=1
RUN apt-get update && apt-get install -y --no-install-recommends bash coreutils procps util-linux ca-certificates \
    && rm -rf /var/lib/apt/lists/* && echo "${STAGE_MARK}" > /etc/stage-mark
# Proves a process sees the image's ENV through FactorySandbox's env file.
ENV STAGE_ENV_PROOF=from-the-image
COPY proc.sh /usr/local/bin/ticks-proc
RUN chmod 0755 /usr/local/bin/ticks-proc

# claude-sub (spike tick jvj): the claude CLI and git, so a staging container
# can run one `claude -p` review against a real diff with its subscription
# token injected by the Worker (src/claude-sub.ts). The CLI is the native
# linux-x64 build of the version the factory image pins (image/Dockerfile's
# CLAUDE_CODE_VERSION), from its npm package, checksum-pinned. No token is
# ever baked or passed in: the container only gets a placeholder.
ARG CLAUDE_CODE_VERSION=2.1.227
ARG CLAUDE_CODE_SHA256=060ad4d1ae8dd7384ba3e34cf6a97db9b3f39cf61e23569246d40f4515eadde0
RUN apt-get update && apt-get install -y --no-install-recommends curl git \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSLo /tmp/claude.tgz "https://registry.npmjs.org/@anthropic-ai/claude-code-linux-x64/-/claude-code-linux-x64-${CLAUDE_CODE_VERSION}.tgz" \
    && echo "${CLAUDE_CODE_SHA256}  /tmp/claude.tgz" | sha256sum -c - \
    && tar -xzf /tmp/claude.tgz -C /tmp package/claude \
    && install -m 0755 /tmp/package/claude /usr/local/bin/claude \
    && rm -rf /tmp/claude.tgz /tmp/package
