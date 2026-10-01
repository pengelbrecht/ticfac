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
