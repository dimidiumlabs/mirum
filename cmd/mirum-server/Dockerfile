# SPDX-FileCopyrightText: 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

FROM gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7

ARG TARGETARCH

LABEL org.opencontainers.image.source="https://github.com/dimidiumlabs/mirum" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.title="mirum-server"

COPY --chown=root:root --chmod=0755 .container/mirum-server-linux-${TARGETARCH} /usr/local/bin/mirum-server
COPY LICENSE README.md /usr/share/doc/mirum/

USER 10000:10000
EXPOSE 3000 2000
ENTRYPOINT ["/usr/local/bin/mirum-server"]
CMD ["daemon", "--config=/etc/mirum/config.yaml"]

# syntax=docker/dockerfile:
