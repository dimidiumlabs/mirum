# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
# SPDX-FileCopyrightText: 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

ARG DEBIAN_VERSION=13-slim
ARG DEBIAN_DIGEST=sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132

FROM docker.io/library/debian:${DEBIAN_VERSION}@${DEBIAN_DIGEST}

ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown

LABEL org.opencontainers.image.title="Mirum" \
      org.opencontainers.image.description="Mirum web service" \
      org.opencontainers.image.source="https://git.dimidiumlabs.io/mirum" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later"

RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates=20250419 \
      gcc=4:14.2.0-1 \
      git=1:2.47.3-0+deb13u1 \
      libc6-dev=2.41-12+deb13u3 && \
    rm -rf /var/lib/apt/lists/*

COPY --chown=root:root --chmod=0755 .container/binary-${TARGETARCH}/mirum /usr/local/bin/mirum
COPY --chown=root:root deploy/mirum.toml /etc/mirum/config.toml
COPY --chown=root:root LICENSE README.md LICENSES/ /usr/share/doc/mirum/

ENV HOME=/tmp
USER 10000:10000
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/mirum"]
CMD ["--config", "/etc/mirum/config.toml"]
