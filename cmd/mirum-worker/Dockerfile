# SPDX-FileCopyrightText: 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

FROM docker.io/library/alpine:3.23.5@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40

ARG TARGETARCH

LABEL org.opencontainers.image.source="https://github.com/dimidiumlabs/mirum" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.title="mirum-worker"

RUN apk add --no-cache bash ca-certificates git && \
    addgroup -g 10000 mirum-worker && \
    adduser -D -H -u 10000 -G mirum-worker -h /var/lib/mirum-worker \
      -s /sbin/nologin mirum-worker && \
    install -d -o mirum-worker -g mirum-worker -m 0750 /var/lib/mirum-worker

COPY --chown=root:root --chmod=0755 .container/mirum-worker-linux-${TARGETARCH} /usr/local/bin/mirum-worker
COPY LICENSE README.md /usr/share/doc/mirum/

USER 10000:10000
WORKDIR /var/lib/mirum-worker
ENTRYPOINT ["/usr/local/bin/mirum-worker"]
CMD ["--config=/etc/mirum/config.yaml"]

# syntax=docker/dockerfile:
